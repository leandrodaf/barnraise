package domain

import "testing"

// An item's own memory files and the repository map are no shared work:
// two items that both have a plan.md do not wait for each other.
func TestPredictedFilesLeaveTheItemsOwnFiles(t *testing.T) {
	a := &Item{FilesTouched: []string{"plan.md", "criteria.json", RepoMapFile, "pixel_keeper.gd"}}
	b := &Item{FilesTouched: []string{"plan.md", "expedition_report.gd"}}
	if FilesOverlap(PredictedFiles(a), PredictedFiles(b)) {
		t.Fatalf("%v and %v overlap", PredictedFiles(a), PredictedFiles(b))
	}
	if got := PredictedFiles(a); len(got) != 1 || got[0] != "pixel_keeper.gd" {
		t.Fatalf("%v", got)
	}
}
