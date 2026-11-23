package codex

import (
	"bufio"
	"os"
	"path/filepath"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

func FuzzParse(f *testing.F) {
	files, _ := filepath.Glob(filepath.Join("testdata", "*.jsonl"))
	for _, name := range files {
		fh, err := os.Open(name)
		if err != nil {
			f.Fatal(err)
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			f.Add(append([]byte(nil), sc.Bytes()...))
		}
		_ = fh.Close()
	}
	for _, s := range []string{"x", "{}", "[]", `{"type":"item.completed"}`, `{"type":"item.completed","item":{"type":"mcp_tool_call"}}`,
		`{"type":"turn.failed"}`, `{"type":"error","error":"plain"}`, `{"type":"error","error":{"message":3}}`, `{"type":"turn.completed"}`, `{"type":"new.kind"}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		p := NewParser(domain.TokenUsage{Provider: "codex"})
		_ = p.Parse(b)
		_ = p.Parse(b)
		_ = p.Finish(1, string(b))
		_ = p.Result()
	})
}
