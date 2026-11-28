package github

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/leandrodaf/barnraise/internal/adapters/out/httpx"
)

// GitHub compares label names ignoring case and accents: a repository with
// "área:mobile" refuses to create "area:mobile" (422 already_exists), and
// no retry helps. Barnraise's labels take the spelling the repository
// already has.

// labelKey is a label name as GitHub compares it.
func labelKey(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(strings.TrimSpace(s))) {
		if unicode.Is(unicode.Mn, r) {
			continue // a combining accent
		}
		b.WriteRune(r)
	}
	return b.String()
}

// repoLabels are the repository's label names, read at most every ten
// minutes.
func (c *Client) repoLabels(ctx context.Context) []string {
	c.mu.Lock()
	if c.labels != nil && time.Since(c.labelsAt) < 10*time.Minute {
		out := c.labels
		c.mu.Unlock()
		return out
	}
	c.mu.Unlock()
	next := c.repoPath("/labels") + "?per_page=100"
	out := []string{}
	for page := 0; next != "" && page < 20; page++ {
		var ls []ghLabel
		resp, err := c.http.Do(ctx, http.MethodGet, next, nil, &ls)
		if err != nil {
			return nil
		}
		for _, l := range ls {
			out = append(out, l.Name)
		}
		next = httpx.NextLink(resp.Header)
	}
	c.mu.Lock()
	c.labels, c.labelsAt = out, time.Now()
	c.mu.Unlock()
	return out
}

// spellLabels gives each wanted label the spelling the repository already
// has for it, when it has one under another case or accents.
func spellLabels(want, have []string) []string {
	byKey := map[string]string{}
	for _, h := range have {
		byKey[labelKey(h)] = h
	}
	out := make([]string, 0, len(want))
	for _, w := range want {
		if h, ok := byKey[labelKey(w)]; ok {
			w = h
		}
		out = append(out, w)
	}
	return out
}

// withLabelsSpelled is in, with its "labels" spelled as the repository has
// them, when in is a request body that names labels.
func (c *Client) withLabelsSpelled(ctx context.Context, in any) any {
	body, ok := in.(map[string]any)
	if !ok {
		return in
	}
	ls, ok := body["labels"].([]string)
	if !ok || len(ls) == 0 {
		return in
	}
	have := c.repoLabels(ctx)
	if len(have) == 0 {
		return in
	}
	out := make(map[string]any, len(body))
	for k, v := range body {
		out[k] = v
	}
	out["labels"] = spellLabels(ls, have)
	return out
}
