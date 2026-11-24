package depscan

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// DefaultAPI is the public OSV service.
const DefaultAPI = "https://api.osv.dev"

// batchSize is the most queries OSV takes in one querybatch call.
const batchSize = 1000

// Vuln is the part of an OSV record the check uses.
type Vuln struct {
	ID       string   `json:"id"`
	Summary  string   `json:"summary"`
	Aliases  []string `json:"aliases"`
	Severity []struct {
		Type  string `json:"type"`
		Score string `json:"score"`
	} `json:"severity"`
	Affected []struct {
		Package struct {
			Ecosystem string `json:"ecosystem"`
			Name      string `json:"name"`
		} `json:"package"`
		Ranges []struct {
			Events []map[string]string `json:"events"`
		} `json:"ranges"`
		EcosystemSpecific map[string]any `json:"ecosystem_specific"`
		DatabaseSpecific  map[string]any `json:"database_specific"`
	} `json:"affected"`
	DatabaseSpecific map[string]any `json:"database_specific"`
}

// rating is a vulnerability's severity: a qualitative level and the CVSS
// base score when one was given (0 otherwise).
func (v Vuln) rating(p Package) (string, float64) {
	best, score := "", 0.0
	for _, s := range v.Severity {
		if strings.HasPrefix(s.Type, "CVSS_V3") || strings.HasPrefix(s.Score, "CVSS:3") {
			if x, ok := CVSS3Score(s.Score); ok && x > score {
				score = x
			}
		}
	}
	if score > 0 {
		best = domain.SeverityOfScore(score)
	}
	consider := func(m map[string]any) {
		if s, ok := m["severity"].(string); ok && domain.SeverityRank(s) > domain.SeverityRank(best) {
			best = strings.ToLower(s)
			if best == "moderate" {
				best = "medium"
			}
		}
	}
	if best == "" {
		consider(v.DatabaseSpecific)
		for _, a := range v.Affected {
			if a.Package.Name == p.Name {
				consider(a.EcosystemSpecific)
				consider(a.DatabaseSpecific)
			}
		}
	}
	return best, score
}

// fixedIn lists the versions that fix it for p.
func (v Vuln) fixedIn(p Package) []string {
	var out []string
	for _, a := range v.Affected {
		if a.Package.Name != p.Name || !strings.EqualFold(a.Package.Ecosystem, p.Ecosystem) {
			continue
		}
		for _, r := range a.Ranges {
			for _, e := range r.Events {
				if f := e["fixed"]; f != "" && !domain.Contains(out, f) {
					out = append(out, f)
				}
			}
		}
	}
	return out
}

// osvClient asks an OSV-compatible API.
type osvClient struct {
	base  string
	http  *http.Client
	cache map[string]Vuln
}

type osvQuery struct {
	Package struct {
		Name      string `json:"name"`
		Ecosystem string `json:"ecosystem"`
	} `json:"package"`
	Version   string `json:"version"`
	PageToken string `json:"page_token,omitempty"`
}

func queryOf(p Package) osvQuery {
	var q osvQuery
	q.Package.Name, q.Package.Ecosystem, q.Version = p.Name, p.Ecosystem, p.Version
	return q
}

type idList struct {
	Vulns []struct {
		ID string `json:"id"`
	} `json:"vulns"`
	NextPageToken string `json:"next_page_token"`
}

// affecting returns, per package, the ids of the vulnerabilities that
// affect its version.
func (c *osvClient) affecting(ctx context.Context, pkgs []Package) (map[string][]string, error) {
	out := map[string][]string{}
	for start := 0; start < len(pkgs); start += batchSize {
		chunk := pkgs[start:min(start+batchSize, len(pkgs))]
		qs := make([]osvQuery, len(chunk))
		for i, p := range chunk {
			qs[i] = queryOf(p)
		}
		var res struct {
			Results []idList `json:"results"`
		}
		if err := c.post(ctx, "/v1/querybatch", map[string]any{"queries": qs}, &res); err != nil {
			return nil, err
		}
		for i, r := range res.Results {
			if i >= len(chunk) {
				break
			}
			p := chunk[i]
			for _, v := range r.Vulns {
				out[p.key()] = append(out[p.key()], v.ID)
			}
			for tok := r.NextPageToken; tok != ""; {
				q := queryOf(p)
				q.PageToken = tok
				var more idList
				if err := c.post(ctx, "/v1/query", q, &more); err != nil {
					return nil, err
				}
				for _, v := range more.Vulns {
					out[p.key()] = append(out[p.key()], v.ID)
				}
				tok = more.NextPageToken
			}
		}
	}
	return out, nil
}

// vuln fetches one full record (cached for the run).
func (c *osvClient) vuln(ctx context.Context, id string) (Vuln, error) {
	if v, ok := c.cache[id]; ok {
		return v, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/v1/vulns/"+url.PathEscape(id), nil)
	if err != nil {
		return Vuln{}, err
	}
	var v Vuln
	if err := c.do(req, &v); err != nil {
		return Vuln{}, err
	}
	if v.ID == "" {
		v.ID = id
	}
	c.cache[id] = v
	return v, nil
}

func (c *osvClient) post(ctx context.Context, path string, body, out any) error {
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *osvClient) do(req *http.Request, out any) error {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "barnraise-dependencies-check")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("OSV at %s: %w", c.base, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return fmt.Errorf("OSV at %s: %w", c.base, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("OSV at %s answered %d to %s: %s", c.base, resp.StatusCode, req.URL.Path, strings.TrimSpace(string(body[:min(len(body), 200)])))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("OSV at %s: unreadable answer to %s: %w", c.base, req.URL.Path, err)
	}
	return nil
}

// CVSS3Score computes the base score of a CVSS v3.0 or v3.1 vector
// (CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H), as the specification
// defines it.
func CVSS3Score(vector string) (float64, bool) {
	m := map[string]string{}
	for i, part := range strings.Split(vector, "/") {
		k, v, ok := strings.Cut(part, ":")
		if !ok {
			return 0, false
		}
		if i == 0 {
			if k != "CVSS" || !strings.HasPrefix(v, "3") {
				return 0, false
			}
			continue
		}
		m[k] = v
	}
	weights := map[string]map[string]float64{
		"AV": {"N": 0.85, "A": 0.62, "L": 0.55, "P": 0.2},
		"AC": {"L": 0.77, "H": 0.44},
		"UI": {"N": 0.85, "R": 0.62},
		"C":  {"H": 0.56, "L": 0.22, "N": 0},
		"I":  {"H": 0.56, "L": 0.22, "N": 0},
		"A":  {"H": 0.56, "L": 0.22, "N": 0},
	}
	val := map[string]float64{}
	for k, w := range weights {
		x, ok := w[m[k]]
		if !ok {
			return 0, false
		}
		val[k] = x
	}
	changed := m["S"] == "C"
	if m["S"] != "U" && !changed {
		return 0, false
	}
	pr := map[string]float64{"N": 0.85, "L": 0.62, "H": 0.27}
	if changed {
		pr["L"], pr["H"] = 0.68, 0.5
	}
	prv, ok := pr[m["PR"]]
	if !ok {
		return 0, false
	}
	iss := 1 - (1-val["C"])*(1-val["I"])*(1-val["A"])
	impact := 6.42 * iss
	if changed {
		impact = 7.52*(iss-0.029) - 3.25*math.Pow(iss-0.02, 15)
	}
	if impact <= 0 {
		return 0, true
	}
	expl := 8.22 * val["AV"] * val["AC"] * prv * val["UI"]
	if changed {
		return roundUp(math.Min(1.08*(impact+expl), 10)), true
	}
	return roundUp(math.Min(impact+expl, 10)), true
}

// roundUp is CVSS 3.1's Roundup: the smallest one-decimal number not below x.
func roundUp(x float64) float64 {
	i := int64(math.Round(x * 100000))
	if i%10000 == 0 {
		return float64(i) / 100000
	}
	return (math.Floor(float64(i)/10000) + 1) / 10
}

// parseScore reads a CVSS score written as a number ("7.5").
func parseScore(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return f
}
