package app

import (
	"errors"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

func TestItemImages(t *testing.T) {
	e := newEnv(t, withGit())
	e.repo.PushToMain(t, "the old icon", map[string]string{"art/icon.png": "OLD-PNG", "art/keep.png": "SAME"})
	it := e.toQueue("New art", map[string]string{"art/icon.png": "NEW-PNG", "art/hero.svg": "<svg/>", "src/main.go": "package main\n"})

	check := func(when string) {
		t.Helper()
		v, err := e.svc.ItemImages(e.ctx, "demo", Owner("owner"), it.ID)
		if err != nil {
			t.Fatal(when, err)
		}
		got := map[string]bool{}
		for _, im := range v.Images {
			got[im.Path] = im.Added
		}
		if len(got) != 2 || got["art/hero.svg"] != true || got["art/icon.png"] != false {
			t.Fatalf("%s: %+v", when, v)
		}
		after, err := e.svc.ItemImageBytes(e.ctx, "demo", Owner("owner"), it.ID, "art/icon.png", false)
		if err != nil || string(after) != "NEW-PNG" {
			t.Fatalf("%s after %q %v", when, after, err)
		}
		before, err := e.svc.ItemImageBytes(e.ctx, "demo", Owner("owner"), it.ID, "art/icon.png", true)
		if err != nil || string(before) != "OLD-PNG" {
			t.Fatalf("%s before %q %v", when, before, err)
		}
		// Only what the item changed, and only pictures.
		for _, f := range []string{"art/keep.png", "src/main.go", "../../etc/passwd.png"} {
			if _, err := e.svc.ItemImageBytes(e.ctx, "demo", Owner("owner"), it.ID, f, false); err == nil {
				t.Fatalf("%s: %s must not be readable", when, f)
			}
		}
	}
	check("on its branch")

	e.checks.Script("main", testkit.Pass)
	if n, err := e.svc.ProcessQueue(e.ctx, "demo"); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	e.wantStatus(it.ID, domain.StatusIntegrated, "")
	check("integrated")

	if _, err := e.svc.ItemImages(e.ctx, "demo", Owner("owner"), "I-999"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
