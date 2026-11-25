package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// Existing tests, references and check scripts are locked for agents: a
// change to one could hide a regression. When the change is the right one
// (the owner decided it), the owner, or the autopilot for the owner, lets
// that item change those files; nobody edits them by hand.

// UnlockFiles lets an item change locked files.
func (s *Service) UnlockFiles(ctx context.Context, project string, a Actor, itemID string, files []string, why string) (*domain.Item, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeApprove, p.Name); err != nil {
		return nil, err
	}
	id, err := domain.NormalizeItemID(itemID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	var clean []string
	for _, f := range files {
		f = strings.TrimSpace(strings.Trim(strings.TrimSpace(f), "`'\""))
		if f == "" {
			continue
		}
		c := path.Clean(strings.TrimPrefix(f, "./"))
		if path.IsAbs(c) || c == "." || strings.HasPrefix(c, "../") || c == ".." {
			return nil, fmt.Errorf("%w: %q is not a path in the repository", ErrInvalid, f)
		}
		locked := false
		for _, g := range p.Cfg().Policy.LockExisting {
			if domain.MatchGlob(g, c) {
				locked = true
				break
			}
		}
		if !locked {
			return nil, fmt.Errorf("%w: %s is not a locked file; the agent may change it already", ErrInvalid, c)
		}
		clean = append(clean, c)
	}
	if len(clean) == 0 {
		return nil, fmt.Errorf("%w: name the locked files to unlock", ErrInvalid)
	}
	return s.mutateItem(ctx, p, id, domain.EvItemUpdated, actorName(a), func(it *domain.Item) error {
		for _, f := range clean {
			if !domain.Contains(it.Unlocked, f) {
				it.Unlocked = append(it.Unlocked, f)
			}
		}
		return nil
	}, domain.Payload{Note: "unlocked " + strings.Join(clean, ", ") + ": " + strings.TrimSpace(why)})
}

// unlockOption reads the advisor's "UNLOCK path, path" suggestion.
func unlockOption(option string) ([]string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(option), "UNLOCK")
	if !ok || (rest != "" && rest[0] != ' ' && rest[0] != ':') {
		return nil, false
	}
	var out []string
	for _, f := range strings.FieldsFunc(strings.TrimLeft(rest, ": "), func(r rune) bool { return r == ',' || r == ' ' }) {
		if f != "" {
			out = append(out, f)
		}
	}
	return out, len(out) > 0
}

// retryOption is the option of a question that goes on with the step: the
// recommended one, or the first.
func retryOption(q *domain.Question) string {
	for _, o := range q.Options {
		if o.Recommended {
			return o.Label
		}
	}
	if len(q.Options) > 0 {
		return q.Options[0].Label
	}
	return ""
}

// editOption reads "EDIT {json}": the item fields the advisor records
// (criteria, deps, area, size, priority, title, clarifications cleared).
func editOption(option string) (ItemPatch, bool, error) {
	rest, ok := strings.CutPrefix(option, "EDIT")
	if !ok {
		if rest, ok = strings.CutPrefix(option, "edit"); !ok {
			return ItemPatch{}, false, nil
		}
	}
	var in struct {
		ItemPatch
		Clear bool `json:"clear_clarifications"`
	}
	dec := json.NewDecoder(strings.NewReader(strings.TrimSpace(rest)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return ItemPatch{}, true, fmt.Errorf("%w: option EDIT needs a JSON object of item fields: %v", ErrInvalid, err)
	}
	patch := in.ItemPatch
	if in.Clear {
		none := []string{}
		patch.Clarifications = &none
	}
	if patch.Branch != nil || patch.Kind != nil || patch.Class != nil {
		return ItemPatch{}, true, fmt.Errorf("%w: option EDIT records criteria, dependencies, area, size, priority and title only", ErrInvalid)
	}
	return patch, true, nil
}

// moveOnOption is the option of a question that lets the item go on once
// what it waited for is recorded: one that does not stop, abandon or hand
// the decision to the owner, the recommended first.
func moveOnOption(q *domain.Question) string {
	stops := func(label string) bool {
		l := strings.ToLower(label)
		for _, w := range []string{"owner", "dono", "abandon", "desistir", "close", "fechar", "parado", "stays", "fica parado", "contorn", "workaround"} {
			if strings.Contains(l, w) {
				return true
			}
		}
		return false
	}
	for _, o := range q.Options {
		if o.Recommended && !stops(o.Label) {
			return o.Label
		}
	}
	for _, o := range q.Options {
		if !stops(o.Label) {
			return o.Label
		}
	}
	return retryOption(q)
}

// applyAdvice applies the answer the advisor suggests on a decision: a
// question's option, a proposal's verdict, an approval, an unlock (then the
// step goes on), or leaves it to the owner (OWNER). left reports the last.
func (s *Service) applyAdvice(ctx context.Context, p *Project, a Actor, thread, awaits string, last *domain.DiscussionMessage, reason string) (left bool, err error) {
	option := strings.TrimSpace(last.SuggestOption)
	if option == "" || strings.EqualFold(option, "owner") {
		why := strings.TrimSpace(last.SuggestNote)
		if why == "" {
			why = last.Text
		}
		return true, s.leaveToOwner(ctx, p, last, why)
	}
	if files, ok := unlockOption(option); ok {
		snap := p.Snapshot()
		itemID := last.ItemID
		if q := snap.Questions[thread]; q != nil && q.ItemID != "" {
			itemID = q.ItemID
		}
		if itemID == "" {
			return false, fmt.Errorf("%w: %s names no item to unlock files for", ErrInvalid, thread)
		}
		if _, err := s.UnlockFiles(ctx, p.Name, a, itemID, files, reason); err != nil {
			return false, err
		}
		if q := snap.Questions[thread]; q != nil && q.Open() {
			_, err = s.Answer(ctx, p.Name, a, thread, retryOption(q), reason)
		}
		return false, err
	}
	if patch, ok, perr := editOption(option); ok {
		if perr != nil {
			return false, perr
		}
		snap := p.Snapshot()
		itemID := last.ItemID
		if q := snap.Questions[thread]; q != nil && q.ItemID != "" {
			itemID = q.ItemID
		}
		if itemID == "" && !strings.HasPrefix(thread, "Q-") && !strings.HasPrefix(thread, "P-") {
			itemID = thread
		}
		if itemID == "" {
			return false, fmt.Errorf("%w: %s names no item to edit", ErrInvalid, thread)
		}
		patch.Reason = reason
		if _, err := s.UpdateItem(ctx, p.Name, a, itemID, patch); err != nil {
			return false, err
		}
		if q := snap.Questions[thread]; q != nil && q.Open() {
			_, err = s.Answer(ctx, p.Name, a, thread, moveOnOption(q), reason)
		}
		return false, err
	}
	switch {
	case strings.HasPrefix(thread, "Q-"):
		_, err = s.Answer(ctx, p.Name, a, thread, option, reason)
	case strings.HasPrefix(thread, "P-"):
		_, err = s.DecideProposal(ctx, p.Name, a, thread, strings.ToLower(option), reason)
	case awaits != "":
		err = s.Approve(ctx, p.Name, a, thread, awaits, strings.HasPrefix(strings.ToLower(option), "approve"), reason)
	default:
		err = fmt.Errorf("%w: only a question, a proposal or an approval can be decided for you", ErrInvalid)
	}
	return false, err
}
