package knowledge

import (
	"context"
	"fmt"
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
)

const itemsOrigin = "items"

// IndexItems upserts items and proposals as documents with their links:
// item → deps (depends_on), item → anchors (files as file:<path>, symbols,
// decisions, areas, contracts, specs, screens), item → goal, proposal → item.
func (ix *Index) IndexItems(ctx context.Context, project string, items []*domain.Item, proposals []*domain.Proposal) error {
	if len(items) == 0 && len(proposals) == 0 {
		return nil
	}
	return ix.write(ctx, func(tx *txw) error {
		for _, it := range items {
			if it == nil || it.ID == "" {
				continue
			}
			if err := putDoc(ctx, tx, project, itemDoc(it)); err != nil {
				return fmt.Errorf("index %s: %w", it.ID, err)
			}
		}
		for _, p := range proposals {
			if p == nil || p.ID == "" {
				continue
			}
			if err := putDoc(ctx, tx, project, proposalDoc(p)); err != nil {
				return fmt.Errorf("index %s: %w", p.ID, err)
			}
		}
		return nil
	})
}

func firstLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(ln), "#>-* ")); t != "" {
			return t
		}
	}
	return ""
}

func itemDoc(it *domain.Item) *docRow {
	var body strings.Builder
	body.WriteString(it.Title)
	if it.Body != "" {
		body.WriteString("\n\n" + it.Body)
	}
	if len(it.Criteria) > 0 {
		body.WriteString("\n\nCriteria:\n")
		for _, c := range it.Criteria {
			fmt.Fprintf(&body, "- %s %s\n", c.ID, c.Text)
		}
	}
	if len(it.Preserve) > 0 {
		body.WriteString("\nPreserve:\n- " + strings.Join(it.Preserve, "\n- ") + "\n")
	}
	summary := firstLine(it.Body)
	if summary == "" {
		summary = it.Title
	}
	updated := it.UpdatedAt
	if updated.IsZero() {
		updated = it.CreatedAt
	}
	areas := append([]string{it.Area}, it.Anchors.Areas...)
	d := &docRow{
		ID: it.ID, Kind: "item", Title: oneLine(it.Title, 200), Summary: oneLine(summary, 300), Body: body.String(),
		Status: string(it.Status), Areas: areas, Goal: it.GoalID, Updated: updated, Commit: it.Anchors.Base, Origin: itemsOrigin,
		Meta: map[string]string{"priority": it.Priority.String(), "lane": string(it.Lane), "flow": it.Kind},
	}
	for k, v := range map[string]string{"step": it.Step, "branch": it.Branch, "pr": it.PR, "tracker": it.TrackerRef, "merge_commit": it.MergeCommit} {
		if v != "" {
			d.Meta[k] = v
		}
	}
	add := func(kind string, dsts ...string) {
		for _, v := range dsts {
			if v = strings.TrimSpace(v); v != "" {
				d.Links = append(d.Links, linkRow{v, kind})
			}
		}
	}
	for _, dep := range it.Deps {
		add("depends_on", canonicalID(dep))
	}
	for _, f := range it.Anchors.Files {
		add("anchors", "file:"+strings.TrimPrefix(f, "./"))
	}
	add("anchors", it.Anchors.Symbols...)
	for _, dc := range it.Anchors.Decisions {
		add("anchors", canonicalID(dc))
	}
	for _, a := range normAreas(areas) {
		add("anchors", domain.AreaID(a))
	}
	add("anchors", it.Anchors.Contracts...)
	add("anchors", it.Anchors.Specs...)
	add("anchors", it.Anchors.Screens...)
	if it.GoalID != "" {
		add("goal", canonicalID(it.GoalID))
	}
	add("tweak", it.Tweaks...)
	return d
}

func proposalDoc(p *domain.Proposal) *docRow {
	var body strings.Builder
	body.WriteString(p.Title)
	for _, part := range []struct{ label, text string }{
		{"Problem", p.Problem}, {"Evidence", p.Evidence}, {"Impact", p.Impact}, {"Goal", p.GoalLink}, {"Reason", p.Reason},
	} {
		if strings.TrimSpace(part.text) != "" {
			fmt.Fprintf(&body, "\n\n%s: %s", part.label, part.text)
		}
	}
	for _, c := range p.Comments {
		body.WriteString("\n\nComment: " + c)
	}
	summary := firstLine(p.Problem)
	if summary == "" {
		summary = p.Title
	}
	updated := p.CreatedAt
	if p.DecidedAt.After(updated) {
		updated = p.DecidedAt
	}
	d := &docRow{
		ID: p.ID, Kind: "proposal", Title: oneLine(p.Title, 200), Summary: oneLine(summary, 300), Body: body.String(),
		Status: string(p.Status), Areas: []string{p.Area}, Updated: updated, Origin: itemsOrigin,
		Meta: map[string]string{"priority": p.Priority.String(), "by": p.By, "type": p.Kind, "size": p.Size},
	}
	if p.ItemID != "" {
		d.Links = append(d.Links, linkRow{canonicalID(p.ItemID), "item"})
	}
	if a := domain.AreaName(p.Area); a != "" {
		d.Links = append(d.Links, linkRow{domain.AreaID(a), "area"})
	}
	return d
}
