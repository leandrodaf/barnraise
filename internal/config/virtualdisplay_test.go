package config

import (
	"testing"
)

func TestVirtualDisplayPolicy(t *testing.T) {
	cfg, err := Compile(t.TempDir(), []byte("template: blank\npolicy:\n  virtual_display: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Policy.VirtualDisplay {
		t.Fatal("policy.virtual_display was not read")
	}
	cfg, err = Compile(t.TempDir(), []byte("template: blank\n"))
	if err != nil || cfg.Policy.VirtualDisplay {
		t.Fatalf("off by default: %v %v", cfg.Policy.VirtualDisplay, err)
	}
}
