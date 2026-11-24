package domain

import (
	"testing"
)

func TestParseBytes(t *testing.T) {
	cases := map[string]int64{"64MB": 64 << 20, "512kb": 512 << 10, "1G": 1 << 30, "100": 100, "7 B": 7, "2m": 2 << 20}
	for in, want := range cases {
		got, err := ParseBytes(in)
		if err != nil || got != want {
			t.Errorf("ParseBytes(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "MB", "-1", "lots"} {
		if _, err := ParseBytes(bad); err == nil {
			t.Errorf("ParseBytes(%q) accepted", bad)
		}
	}
}

func TestOutputIndexAndSavings(t *testing.T) {
	data := []byte(`{"id":"o1","run":"r1","tool":"go","raw_tokens":1000,"shown_tokens":100}
not json
{"id":"o2","run":"r2","raw_tokens":50,"shown_tokens":80}
{"id":"o3","run":"r1","raw_tokens":300,"shown_tokens":100}
{"run":"r1"}
`)
	recs := ParseOutputIndex(data)
	if len(recs) != 3 {
		t.Fatalf("%+v", recs)
	}
	if recs[1].Saved() != 0 {
		t.Fatal("a longer summary saves nothing")
	}
	if n, saved := FilterSavings(recs, "r1"); n != 2 || saved != 1100 {
		t.Fatalf("%d %d", n, saved)
	}
	if n, saved := FilterSavings(recs, ""); n != 3 || saved != 1100 {
		t.Fatalf("%d %d", n, saved)
	}
	if !ValidDelegate(DelegateRTK) || ValidDelegate("") || DefaultOutfilter().Delegate != DelegateAuto {
		t.Fatal("delegate modes")
	}
}

func TestTerseApplies(t *testing.T) {
	ladders := DefaultModelRouter().Ladders
	critic := StepDef{Name: "quick-look", Role: "critic", Model: "haiku"}
	impl := StepDef{Name: "implement", Role: "implementer"}
	check := StepDef{Name: "lint", Type: StepCheck}
	auto := TerseDef{Mode: TerseAuto}
	if !auto.Applies(critic, "normal", ladders) {
		t.Error("the cheapest pinned model is mechanical")
	}
	if auto.Applies(StepDef{Name: "review", Model: "opus"}, "cheap", ladders) {
		t.Error("a pinned strong model is not mechanical")
	}
	if auto.Applies(impl, "normal", ladders) || !auto.Applies(impl, "simple", ladders) || !auto.Applies(impl, "cheap", ladders) {
		t.Error("class decides when no model is pinned")
	}
	if auto.Applies(check, "cheap", ladders) {
		t.Error("checks have no agent")
	}
	if !(TerseDef{Mode: TerseAuto, Steps: []string{"implement"}}).Applies(impl, "complex", ladders) {
		t.Error("listed steps are terse")
	}
	if (TerseDef{Mode: TerseOff, Steps: []string{"implement"}}).Applies(impl, "cheap", ladders) {
		t.Error("off wins")
	}
	if !(TerseDef{Mode: TerseAll}).Applies(impl, "complex", ladders) {
		t.Error("all")
	}
	if !ValidTerse("") || ValidTerse("loud") {
		t.Error("ValidTerse")
	}
}
