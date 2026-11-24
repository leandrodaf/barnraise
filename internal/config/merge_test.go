package config

import "testing"

func TestMergeRerereAndStructural(t *testing.T) {
	cfg, err := Compile(t.TempDir(), []byte("goal: x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Merge.Rerere || cfg.Merge.Structural != "auto" {
		t.Fatalf("defaults %+v", cfg.Merge)
	}
	cfg, err = Compile(t.TempDir(), []byte("merge: { rerere: false, structural: off }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Merge.Rerere || cfg.Merge.Structural != "off" {
		t.Fatalf("set %+v", cfg.Merge)
	}
	if _, err := Compile(t.TempDir(), []byte("merge: { structural: always }\n")); err == nil {
		t.Fatal("unknown structural value")
	}
}
