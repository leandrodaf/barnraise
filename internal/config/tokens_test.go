package config

import (
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

func TestOutfilterAndTerseDefaults(t *testing.T) {
	cfg, err := Compile(t.TempDir(), []byte("goal: x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Outfilter != domain.DefaultOutfilter() {
		t.Fatalf("%+v", cfg.Outfilter)
	}
	if cfg.Terse.Mode != domain.TerseAuto || len(cfg.Terse.Steps) != 0 {
		t.Fatalf("%+v", cfg.Terse)
	}
}

func TestOutfilterAndTerseFromYAML(t *testing.T) {
	yml := `goal: x
outfilter: { delegate: off, keep: 2d, keep_size: 8MB }
terse: { mode: all, steps: [quick-look] }
`
	cfg, err := Compile(t.TempDir(), []byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Outfilter.Delegate != domain.DelegateOff || cfg.Outfilter.Keep != 48*time.Hour || cfg.Outfilter.KeepBytes != 8<<20 {
		t.Fatalf("%+v", cfg.Outfilter)
	}
	if cfg.Terse.Mode != domain.TerseAll || cfg.Terse.Steps[0] != "quick-look" {
		t.Fatalf("%+v", cfg.Terse)
	}
	for _, bad := range []string{
		"outfilter: { delegate: maybe }",
		"outfilter: { keep: soon }",
		"outfilter: { keep_size: big }",
		"terse: { mode: loud }",
	} {
		_, err := Compile(t.TempDir(), []byte("goal: x\n"+bad+"\n"))
		if err == nil || !strings.Contains(err.Error(), strings.Fields(bad)[0][:len(strings.Fields(bad)[0])-1]) {
			t.Errorf("%s: %v", bad, err)
		}
	}
}
