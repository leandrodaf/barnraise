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

func TestReviewAndQuestionsOptions(t *testing.T) {
	cfg, err := Compile(t.TempDir(), []byte("template: blank\nreview:\n  send_back: blockers\nquestions:\n  decide_after: 30m\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Review.SendBack != "blockers" || cfg.Questions.DecideAfter.Minutes() != 30 {
		t.Fatalf("%+v %+v", cfg.Review, cfg.Questions)
	}
	cfg, err = Compile(t.TempDir(), []byte("template: blank\n"))
	if err != nil || cfg.Review.SendBack != "all" || cfg.Questions.DecideAfter != 0 {
		t.Fatalf("defaults: %+v %+v %v", cfg.Review, cfg.Questions, err)
	}
	for _, bad := range []string{"review:\n  send_back: some\n", "questions:\n  decide_after: soon\n", "questions:\n  decide_after: 10s\n"} {
		if _, err := Compile(t.TempDir(), []byte("template: blank\n"+bad)); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}
