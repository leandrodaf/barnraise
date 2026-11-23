package gemini

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
	for _, s := range []string{"x", "{}", "[]", `{"type":"message"}`, `{"type":"message","role":"assistant","content":[{"text":1}]}`,
		`{"type":"tool_result","output":null}`, `{"type":"tool_use","parameters":null}`, `{"type":"result","status":"error"}`,
		`{"type":"error","error":"plain"}`, `{"type":"error","error":{"message":3}}`, `{"type":"result","stats":{"cached":-1}}`, `{"type":"new.kind"}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		p := NewParser(domain.TokenUsage{Provider: "gemini"})
		_ = p.Parse(b)
		_ = p.Parse(b)
		_ = p.Finish(1, string(b))
		_ = p.Result()
	})
}
