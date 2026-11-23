package github

import (
	"context"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// An issue a person wrote is adopted, not rewritten: Barnraise keeps its
// state and labels current and leaves the title and the text alone.
func TestUpdateOfAnAdoptedIssueKeepsItsTitleAndText(t *testing.T) {
	f := projectFake(t, "gql_issue_items_empty.json")
	c := f.client(t, nil)
	it := sampleItem()
	it.TrackerRef = "#1608"
	it.Adopted = true
	it.Status = domain.StatusInProgress
	if err := c.Tracker().Update(context.Background(), it, "en"); err != nil {
		t.Fatal(err)
	}
	patch := f.callsTo("PATCH", "/repos/acme/app/issues/1608")[0].Body
	if _, ok := patch["title"]; ok {
		t.Errorf("the title of an adopted issue must not be sent: %v", patch)
	}
	if _, ok := patch["body"]; ok {
		t.Errorf("the text of an adopted issue must not be sent: %v", patch)
	}
	if patch["state"] != "open" || len(toStrings(patch["labels"])) == 0 {
		t.Errorf("state and labels are still kept current: %v", patch)
	}
}
