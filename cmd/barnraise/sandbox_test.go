package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/leandrodaf/barnraise/internal/daemoncfg"
)

func TestDaemonIsolation(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := &daemon{log: log, df: &daemoncfg.File{}}
	iso := d.isolation("/bin/barnraise")
	if iso.Landlock == nil || iso.MicroVM != nil || iso.MicroVMFor != nil {
		t.Fatalf("Landlock always, the microVM only when daemon.yaml asks: %+v", iso)
	}
	for _, l := range d.sandboxLabels() {
		if l != "landlock" {
			t.Fatalf("label %s", l)
		}
	}
	d = &daemon{log: log, df: &daemoncfg.File{Isolation: daemoncfg.Isolation{Backend: "microvm",
		MicroVM: daemoncfg.MicroVM{Runner: "no-such-runner-xyz", Image: "img", For: []string{"checks"}}}}}
	iso = d.isolation("/bin/barnraise")
	if iso.MicroVM == nil || !iso.MicroVMFor("checks") || iso.MicroVMFor("sessions") {
		t.Fatalf("microvm: %+v", iso)
	}
	for _, l := range d.sandboxLabels() {
		if l == "microvm" {
			t.Fatal("a missing runner gives no microvm label")
		}
	}
	bad := &daemon{log: log, df: &daemoncfg.File{Isolation: daemoncfg.Isolation{Backend: "jail"}}}
	if iso := bad.isolation("/bin/barnraise"); iso.MicroVM != nil {
		t.Fatal("an invalid isolation block is ignored")
	}
	if o := MicroVMOptions(daemoncfg.MicroVM{Runner: "r", CPUs: 2, MemoryMB: 512}); o.Runner != "r" || o.CPUs != 2 || o.MemoryMB != 512 {
		t.Fatalf("%+v", o)
	}
}
