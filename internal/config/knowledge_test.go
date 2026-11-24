package config

import (
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

func TestKnowledgeDefaults(t *testing.T) {
	cfg, err := Compile(t.TempDir(), []byte("project: x\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := domain.KnowledgeDef{Symbols: "auto", Map: domain.RepoMapDef{Enabled: true, MaxFiles: 5000, PromptTokens: 1200}}
	if cfg.Knowledge != want {
		t.Errorf("defaults: %+v", cfg.Knowledge)
	}
}

func TestKnowledgeBlock(t *testing.T) {
	cfg, err := Compile(t.TempDir(), []byte("project: x\nknowledge:\n  symbols: native\n  map:\n    enabled: false\n    max_files: 300\n    prompt_tokens: 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := domain.KnowledgeDef{Symbols: "native", Map: domain.RepoMapDef{Enabled: false, MaxFiles: 300, PromptTokens: 0}}
	if cfg.Knowledge != want {
		t.Errorf("block: %+v", cfg.Knowledge)
	}
	for yml, msg := range map[string]string{
		"knowledge: {symbols: tree-sitter}":     "knowledge.symbols",
		"knowledge: {map: {max_files: -1}}":     "knowledge.map.max_files",
		"knowledge: {map: {prompt_tokens: -5}}": "knowledge.map.prompt_tokens",
	} {
		if _, err := Compile(t.TempDir(), []byte("project: x\n"+yml+"\n")); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%s: %v", yml, err)
		}
	}
}
