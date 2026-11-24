package config

import (
	"fmt"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// rawKnowledge is the knowledge: block of barnraise.yaml.
type rawKnowledge struct {
	Symbols string `yaml:"symbols"`
	Map     struct {
		Enabled      *bool `yaml:"enabled"`
		MaxFiles     int   `yaml:"max_files"`
		PromptTokens *int  `yaml:"prompt_tokens"`
	} `yaml:"map"`
}

// Defaults of the knowledge: block.
const (
	defaultMapMaxFiles     = 5000
	defaultMapPromptTokens = 1200
)

func compileKnowledge(r rawKnowledge) (domain.KnowledgeDef, error) {
	k := domain.KnowledgeDef{Symbols: def(r.Symbols, "auto"), Map: domain.RepoMapDef{Enabled: true, MaxFiles: defaultMapMaxFiles, PromptTokens: defaultMapPromptTokens}}
	if k.Symbols != "auto" && k.Symbols != "native" {
		return k, fmt.Errorf("knowledge.symbols: %q is not auto or native", r.Symbols)
	}
	if r.Map.Enabled != nil {
		k.Map.Enabled = *r.Map.Enabled
	}
	if r.Map.MaxFiles < 0 {
		return k, fmt.Errorf("knowledge.map.max_files: %d is negative", r.Map.MaxFiles)
	}
	if r.Map.MaxFiles > 0 {
		k.Map.MaxFiles = r.Map.MaxFiles
	}
	if p := r.Map.PromptTokens; p != nil {
		if *p < 0 {
			return k, fmt.Errorf("knowledge.map.prompt_tokens: %d is negative", *p)
		}
		k.Map.PromptTokens = *p
	}
	return k, nil
}
