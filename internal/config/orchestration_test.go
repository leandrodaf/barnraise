package config

import (
	"strings"
	"testing"
)

func TestStaleReadsAndLessonsConfig(t *testing.T) {
	base := "project: demo\nchecks:\n  main: \"true\"\n"
	cfg, err := Compile(t.TempDir(), []byte(base))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.StaleReads.Enabled || cfg.StaleReads.Rebase || cfg.StaleReads.MaxFiles != 10 || !cfg.Lessons.Enabled || cfg.Lessons.Max != 5 {
		t.Fatalf("defaults %+v %+v", cfg.StaleReads, cfg.Lessons)
	}
	cfg, err = Compile(t.TempDir(), []byte(base+"stale_reads: { enabled: false, rebase: true, max_files: 3 }\nlessons: { enabled: false, max: 0 }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StaleReads.Enabled || !cfg.StaleReads.Rebase || cfg.StaleReads.MaxFiles != 3 || cfg.Lessons.Enabled || cfg.Lessons.Max != 0 {
		t.Fatalf("set %+v %+v", cfg.StaleReads, cfg.Lessons)
	}
	out, err := Explain(cfg)
	if err != nil || !strings.Contains(out, "stale_reads:") || !strings.Contains(out, "lessons:") {
		t.Fatalf("explain %v\n%s", err, out)
	}
	for _, bad := range []string{"stale_reads: { max_files: -1 }\n", "lessons: { max: 21 }\n", "lessons: { max: -1 }\n"} {
		if _, err := Compile(t.TempDir(), []byte(base+bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
