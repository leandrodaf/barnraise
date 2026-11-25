package config

import (
	"fmt"
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// staleReadsRaw is the `stale_reads:` block.
type staleReadsRaw struct {
	Enabled  *bool `yaml:"enabled"`
	Rebase   bool  `yaml:"rebase"`
	MaxFiles int   `yaml:"max_files"`
}

// lessonsRaw is the `lessons:` block.
type lessonsRaw struct {
	Enabled *bool `yaml:"enabled"`
	Max     *int  `yaml:"max"`
}

// documentsRaw is the `documents:` block.
type documentsRaw struct {
	Diagrams string `yaml:"diagrams"`
}

// compileOrchestration fills the stale-read warning and the lessons from
// rework. Both are on by default; an absent block keeps the defaults.
func compileOrchestration(r *rawConfig, c *domain.ProjectConfig) error {
	c.StaleReads = domain.DefaultStaleReads()
	if r.StaleReads.Enabled != nil {
		c.StaleReads.Enabled = *r.StaleReads.Enabled
	}
	c.StaleReads.Rebase = r.StaleReads.Rebase
	if r.StaleReads.MaxFiles < 0 {
		return fmt.Errorf("stale_reads.max_files must not be negative")
	}
	if r.StaleReads.MaxFiles > 0 {
		c.StaleReads.MaxFiles = r.StaleReads.MaxFiles
	}
	c.Lessons = domain.DefaultLessons()
	if r.Lessons.Enabled != nil {
		c.Lessons.Enabled = *r.Lessons.Enabled
	}
	if r.Lessons.Max != nil {
		if *r.Lessons.Max < 0 || *r.Lessons.Max > 20 {
			return fmt.Errorf("lessons.max must be between 0 and 20")
		}
		c.Lessons.Max = *r.Lessons.Max
	}
	c.Documents = domain.DefaultDocuments()
	if d := strings.ToLower(strings.TrimSpace(r.Documents.Diagrams)); d != "" {
		if !domain.ValidDiagrams(d) {
			return fmt.Errorf("documents.diagrams must be auto, always or off, not %q", r.Documents.Diagrams)
		}
		c.Documents.Diagrams = d
	}
	return nil
}
