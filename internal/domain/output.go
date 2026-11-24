package domain

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Where the full text of filtered command outputs is kept, inside an item's
// session folder (.barnraise-session/outputs) or the data folder.
const (
	OutputsDir  = "outputs"
	OutputIndex = "index.jsonl"
)

// Environment of an agent session that carries the outfilter settings to
// `barnraise filter` and the hook.
const (
	EnvOutfilterDelegate = "BARNRAISE_OUTFILTER_DELEGATE"
	EnvOutputKeep        = "BARNRAISE_OUTPUT_KEEP"       // Go duration
	EnvOutputKeepBytes   = "BARNRAISE_OUTPUT_KEEP_BYTES" // bytes
	EnvRun               = "BARNRAISE_RUN"               // the session's run id
)

// OutfilterEnv is the session environment for these settings.
func OutfilterEnv(o OutfilterDef, run string) map[string]string {
	if o.Delegate == "" {
		o = DefaultOutfilter()
	}
	return map[string]string{
		EnvOutfilterDelegate: o.Delegate,
		EnvOutputKeep:        o.Keep.String(),
		EnvOutputKeepBytes:   strconv.FormatInt(o.KeepBytes, 10),
		EnvRun:               run,
	}
}

// Delegation modes of the output filter (outfilter.delegate).
const (
	DelegateAuto = "auto" // rtk, when installed, for commands without a native filter
	DelegateRTK  = "rtk"  // rtk, when installed, for every command it handles
	DelegateOff  = "off"  // native filters only
)

// OutfilterDef configures the output filter and the store of full outputs.
type OutfilterDef struct {
	Delegate  string        `json:"delegate"`   // auto, rtk, off
	Keep      time.Duration `json:"keep"`       // full outputs older than this are removed
	KeepBytes int64         `json:"keep_bytes"` // most bytes of full outputs per folder
}

// DefaultOutfilter is used when barnraise.yaml says nothing.
func DefaultOutfilter() OutfilterDef {
	return OutfilterDef{Delegate: DelegateAuto, Keep: 7 * 24 * time.Hour, KeepBytes: 64 << 20}
}

// ValidDelegate reports whether m is a known delegation mode.
func ValidDelegate(m string) bool {
	return m == DelegateAuto || m == DelegateRTK || m == DelegateOff
}

// OutputRecord is one command output that was shown filtered and kept in full.
type OutputRecord struct {
	ID          string    `json:"id"`
	Run         string    `json:"run,omitempty"` // the agent session that ran it
	At          time.Time `json:"at"`
	Tool        string    `json:"tool"`
	Command     string    `json:"command"`
	Exit        int       `json:"exit"`
	Bytes       int64     `json:"bytes"`        // size of the kept file
	RawTokens   int       `json:"raw_tokens"`   // estimate of the whole output
	ShownTokens int       `json:"shown_tokens"` // estimate of what the agent saw
	Delegated   string    `json:"delegated,omitempty"`
	Truncated   bool      `json:"truncated,omitempty"` // the middle was dropped to stay bounded
}

// Saved is how many tokens the filter kept out of the agent's context.
func (r OutputRecord) Saved() int {
	if r.RawTokens > r.ShownTokens {
		return r.RawTokens - r.ShownTokens
	}
	return 0
}

// ParseOutputIndex reads index.jsonl, skipping lines it cannot parse.
func ParseOutputIndex(data []byte) []OutputRecord {
	var out []OutputRecord
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var r OutputRecord
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.ID != "" {
			out = append(out, r)
		}
	}
	return out
}

// FilterSavings sums the records of one run: how many outputs were filtered
// and the tokens saved.
func FilterSavings(recs []OutputRecord, run string) (runs int, saved int64) {
	for _, r := range recs {
		if run != "" && r.Run != run {
			continue
		}
		runs++
		saved += int64(r.Saved())
	}
	return runs, saved
}

// ParseBytes reads a size such as 64MB, 512KB, 1GB or a plain number of bytes.
func ParseBytes(s string) (int64, error) {
	v := strings.ToUpper(strings.TrimSpace(s))
	mult := int64(1)
	for _, u := range []struct {
		suffix string
		mult   int64
	}{{"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}, {"B", 1}} {
		if strings.HasSuffix(v, u.suffix) {
			v, mult = strings.TrimSpace(strings.TrimSuffix(v, u.suffix)), u.mult
			break
		}
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size %q: use a number with KB, MB or GB", s)
	}
	return n * mult, nil
}
