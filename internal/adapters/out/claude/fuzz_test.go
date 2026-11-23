package claude

import (
	"bufio"
	"os"
	"path/filepath"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

func addJSONLSeeds(f *testing.F) {
	files, _ := filepath.Glob(filepath.Join("testdata", "*.jsonl"))
	for _, name := range files {
		fh, err := os.Open(name)
		if err != nil {
			f.Fatal(err)
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			f.Add(sc.Text())
		}
		_ = fh.Close()
	}
	for _, s := range []string{"", "x", "{}", "[]", "null", `{"type":"result"}`, `{"type":"assistant","message":null}`,
		`{"type":"assistant","message":{"content":"text"}}`, `{"type":"rate_limit_event","rate_limit_info":7}`} {
		f.Add(s)
	}
}

func FuzzParseLine(f *testing.F) {
	addJSONLSeeds(f)
	f.Fuzz(func(t *testing.T, line string) {
		p := NewParser(domain.TokenUsage{Provider: "claude"})
		for _, ev := range p.ParseLine(line) {
			_, _ = RateLimit(ev)
		}
		// A second line on the same parser exercises accumulated state.
		_ = p.ParseLine(line)
		_ = p.Finish(1, line)
		_ = p.Result()
	})
}
