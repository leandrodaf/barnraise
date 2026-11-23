package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"connectrpc.com/connect"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/domain"
)

// ownerOnly are the tools only the owner's session sees.
var ownerOnly = []string{"answer", "create_item"}

// isReviewer reports whether a role returns reviews instead of deliveries.
func isReviewer(role string) bool {
	r := strings.ToLower(strings.TrimSpace(role))
	return oneOf(r, "reviewer", "critic", "designer", "editor", "analyst") || strings.HasSuffix(r, "reviewer")
}

// ToolNames returns the names of the tools a role sees, in order. An empty
// role is the owner, who sees every tool.
func ToolNames(role string) []string {
	out := []string{}
	for _, t := range visibleTools(role) {
		out = append(out, t.Name)
	}
	return out
}

func visibleTools(role string) []*tool {
	all := allTools()
	byName := map[string]*tool{}
	for _, t := range all {
		byName[t.Name] = t
	}
	var names []string
	if strings.TrimSpace(role) == "" {
		names = append(app.MCPTools([]string{app.ScopeAll}), ownerOnly...)
	} else {
		names = app.MCPTools(app.RoleScopes(strings.ToLower(strings.TrimSpace(role)), isReviewer(role)))
	}
	out := make([]*tool, 0, len(names))
	for _, n := range names {
		if t := byName[n]; t != nil {
			out = append(out, t)
		}
	}
	return out
}

func allTools() []*tool {
	return []*tool{
		nextItemTool(), deliverTool(), reviewTool(), proposeTool(), askOwnerTool(), messageTool(),
		statusTool(), contextTool(), findTool(), approveTool(), focusTool(), pauseTool(),
		answerTool(), createItemTool(),
	}
}

// itemID resolves an optional item_id argument against the session's item.
func (s *server) itemID(arg, toolName string) (string, error) {
	if id := strings.TrimSpace(arg); id != "" {
		return id, nil
	}
	if id := s.currentItem(); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("%s needs item_id: this session has no current item (call next_item first or pass item_id)", toolName)
}

// ---------- next_item ----------

const noWork = "No authorized work: record your state and end your turn; do not create work."

func nextItemTool() *tool {
	t := &tool{
		Name:        "next_item",
		Description: "Get your next assignment: atomically reserves an item and returns it with the step to do, its instructions and its context. When it reports no authorized work, record your state and end your turn; never create work for yourself.",
		Schema:      object(map[string]any{}),
	}
	t.run = func(ctx context.Context, s *server, args json.RawMessage) *toolResult {
		var a struct{}
		if err := decodeArgs(t, args, &a); err != nil {
			return errorResult(err.Error())
		}
		resp, err := s.api.NextItem(ctx, connect.NewRequest(&pb.NextItemRequest{Project: s.opts.Project}))
		if err != nil {
			return daemonError(err)
		}
		as := resp.Msg
		if as.GetNone() || as.GetItem() == nil || as.GetItem().GetId() == "" {
			note := as.GetNote()
			if blank(note) {
				note = noWork
			}
			return textResult(note, map[string]any{"assignment": pbJSON(as)})
		}
		it := as.GetItem()
		s.setItem(it.GetId())
		var b strings.Builder
		fmt.Fprintf(&b, "Assignment: %s %q", it.GetId(), it.GetTitle())
		if meta := itemMeta(it); meta != "" {
			b.WriteString(" (" + meta + ")")
		}
		step := stepField(as, "name")
		if step == "" {
			step = it.GetStep()
		}
		fmt.Fprintf(&b, ", step %s.\n", step)
		writeCriteria(&b, it)
		if len(it.GetPreserve()) > 0 {
			b.WriteString("Must not change:\n")
			for _, p := range it.GetPreserve() {
				b.WriteString("- " + p + "\n")
			}
		}
		b.WriteString("\nInstructions: " + stepGuide(as) + "\n")
		structured := map[string]any{"assignment": pbJSON(as)}
		cr, cerr := s.api.Context(ctx, connect.NewRequest(&pb.ContextRequest{Project: s.opts.Project, Id: it.GetId()}))
		if cerr == nil {
			if txt := renderContext(cr.Msg); txt != "" {
				b.WriteString("\n## Context\n" + txt + "\n")
			}
			structured["context"] = pbJSON(cr.Msg)
		} else {
			if !blank(it.GetBody()) {
				b.WriteString("\n## Item\n" + it.GetBody() + "\n")
			}
			b.WriteString("\n(The context could not be loaded: " + daemonError(cerr).Content[0].Text + " Call context to retry.)\n")
		}
		return textResult(b.String(), structured)
	}
	return t
}

func itemMeta(it *pb.Item) string {
	var parts []string
	if it.GetKind() != "" {
		parts = append(parts, it.GetKind())
	}
	if it.GetArea() != "" {
		parts = append(parts, "area "+it.GetArea())
	}
	parts = append(parts, fmt.Sprintf("P%d", it.GetPriority()))
	if it.GetSize() != "" {
		parts = append(parts, "size "+it.GetSize())
	}
	return strings.Join(parts, ", ")
}

func writeCriteria(b *strings.Builder, it *pb.Item) {
	if len(it.GetCriteria()) == 0 {
		return
	}
	b.WriteString("Criteria (mark each by id; never rewrite them):\n")
	for _, c := range it.GetCriteria() {
		fmt.Fprintf(b, "- %s: %s", c.GetId(), c.GetText())
		if c.GetCheck() != "" {
			fmt.Fprintf(b, " [check: %s]", c.GetCheck())
		}
		b.WriteByte('\n')
	}
}

func stepField(as *pb.Assignment, name string) string {
	if as.GetStep() == nil {
		return ""
	}
	return as.GetStep().GetFields()[name].GetStringValue()
}

func stepGuide(as *pb.Assignment) string {
	var parts []string
	review := as.GetStep() != nil && as.GetStep().GetFields()["review"].GetBoolValue()
	if out := stepField(as, "output"); out != "" {
		parts = append(parts, "produce "+out+".")
	}
	if req := stepField(as, "require"); req != "" {
		parts = append(parts, "Required: "+req+".")
	}
	if review {
		parts = append(parts, "End the step with review: criteria_verdict and code_verdict separately (pass or fail), a decision (APPROVE, REWORK or REJECT), a summary and findings with file and line.")
	} else {
		parts = append(parts, "End the step with deliver: a status, a summary, every criterion marked by id with evidence, the files you touched and any concerns. Use NEEDS_CONTEXT or BLOCKED with a reason when you cannot finish.")
	}
	parts = append(parts, "Ideas outside this item go to propose, not into the change.")
	s := strings.Join(parts, " ")
	return strings.ToUpper(s[:1]) + s[1:]
}

func renderContext(c *pb.ContextResponse) string {
	if !blank(c.GetText()) {
		return strings.TrimSpace(c.GetText())
	}
	var b strings.Builder
	for _, p := range c.GetParts() {
		if p.GetTitle() != "" {
			b.WriteString("### " + p.GetTitle() + "\n")
		}
		b.WriteString(strings.TrimSpace(p.GetBody()) + "\n\n")
	}
	return strings.TrimSpace(b.String())
}

// ---------- deliver ----------

type criterionMark struct {
	ID       string `json:"id"`
	Met      *bool  `json:"met"`
	Evidence string `json:"evidence"`
}

func deliverTool() *tool {
	t := &tool{
		Name:        "deliver",
		Description: "End your step on the item with a structured delivery. Loose text does not move an item. Mark each criterion by id with evidence (a check run, a file, a screenshot); never rewrite criteria.",
		Schema: object(map[string]any{
			"item_id": str("Item id such as I-42. Defaults to the item of this session."),
			"status": enum("DONE: finished. DONE_WITH_CONCERNS: finished but list the concerns. NEEDS_CONTEXT: missing information, say what in reason. BLOCKED: cannot go on, say why in reason.",
				string(domain.DeliveryDone), string(domain.DeliveryConcerns), string(domain.DeliveryNeedsContext), string(domain.DeliveryBlocked)),
			"summary": str("What changed, in one or two sentences, facts first."),
			"criteria": array("Each acceptance criterion of the item, marked by id.", object(map[string]any{
				"id":       str("Criterion id from the item, e.g. C1."),
				"met":      boolean("Whether the criterion is met."),
				"evidence": str("How you know: the check that passed, a file:line, a screenshot path. Required when met is true."),
			}, "id", "met")),
			"files":    strList("Files you touched, relative to the repository root."),
			"concerns": strList("Concerns the reviewer should know about. Required for DONE_WITH_CONCERNS."),
			"reason":   str("Why you need context or are blocked. Required for NEEDS_CONTEXT and BLOCKED."),
		}, "status", "summary"),
	}
	t.run = func(ctx context.Context, s *server, args json.RawMessage) *toolResult {
		var a struct {
			ItemID   string          `json:"item_id"`
			Status   string          `json:"status"`
			Summary  string          `json:"summary"`
			Criteria []criterionMark `json:"criteria"`
			Files    []string        `json:"files"`
			Concerns []string        `json:"concerns"`
			Reason   string          `json:"reason"`
		}
		if err := decodeArgs(t, args, &a); err != nil {
			return errorResult(err.Error())
		}
		a.Status = strings.ToUpper(strings.TrimSpace(a.Status))
		id, err := s.itemID(a.ItemID, t.Name)
		if err != nil {
			return errorResult(err.Error())
		}
		d := &pb.Delivery{ItemId: id, Status: a.Status, Summary: strings.TrimSpace(a.Summary), Files: a.Files,
			Concerns: nonBlank(a.Concerns), Reason: strings.TrimSpace(a.Reason)}
		for _, c := range a.Criteria {
			if blank(c.ID) {
				return errorResult("every criterion needs its id")
			}
			if c.Met == nil {
				return errorResult(fmt.Sprintf("criterion %s needs met: true or false", c.ID))
			}
			if *c.Met && blank(c.Evidence) {
				return errorResult(fmt.Sprintf("criterion %s is marked met without evidence: say how you know (a check, a file, a screenshot)", c.ID))
			}
			d.Criteria = append(d.Criteria, &pb.CriterionMark{Id: c.ID, Met: *c.Met, Evidence: strings.TrimSpace(c.Evidence)})
		}
		if err := validateDelivery(d); err != nil {
			return errorResult(err.Error())
		}
		resp, err := s.api.Deliver(ctx, connect.NewRequest(&pb.DeliverRequest{Project: s.opts.Project, Delivery: d}))
		if err != nil {
			return daemonError(err)
		}
		text := fmt.Sprintf("Delivered %s as %s.", id, d.Status)
		if it := resp.Msg.GetItem(); it != nil {
			text += " " + itemState(it)
		}
		return textResult(text+" Your step is over: record your state and end your turn unless next_item gives you more work.", pbJSON(resp.Msg))
	}
	return t
}

func validateDelivery(d *pb.Delivery) error {
	if !oneOf(d.Status, string(domain.DeliveryDone), string(domain.DeliveryConcerns), string(domain.DeliveryNeedsContext), string(domain.DeliveryBlocked)) {
		return fmt.Errorf("status must be DONE, DONE_WITH_CONCERNS, NEEDS_CONTEXT or BLOCKED, not %q", d.Status)
	}
	if blank(d.Summary) {
		return errors.New("a delivery needs a summary of what changed")
	}
	switch d.Status {
	case string(domain.DeliveryNeedsContext), string(domain.DeliveryBlocked):
		if blank(d.Reason) {
			return fmt.Errorf("a %s delivery needs a reason: say what is missing or what blocks you", d.Status)
		}
	case string(domain.DeliveryConcerns):
		if len(d.Concerns) == 0 {
			return errors.New("a DONE_WITH_CONCERNS delivery needs at least one concern")
		}
	}
	return nil
}

func nonBlank(in []string) []string {
	var out []string
	for _, s := range in {
		if !blank(s) {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

func itemState(it *pb.Item) string {
	s := fmt.Sprintf("%s is now %s", it.GetId(), strings.ReplaceAll(it.GetStatus(), "_", " "))
	if it.GetStep() != "" {
		s += " at step " + it.GetStep()
	}
	return s + "."
}

// ---------- review ----------

func reviewTool() *tool {
	t := &tool{
		Name:        "review",
		Description: "End a review step with two separate verdicts (does it meet the criteria? is the code good?), a decision and findings. A review without both verdicts is refused. Findings in other areas become proposals.",
		Schema: object(map[string]any{
			"item_id":          str("Item id such as I-42. Defaults to the item of this session."),
			"criteria_verdict": enum("Does the change meet every acceptance criterion?", "pass", "fail"),
			"code_verdict":     enum("Is the code good: correct, readable, tested, within the standards?", "pass", "fail"),
			"decision":         enum("APPROVE needs both verdicts to pass. REWORK sends it back with findings. REJECT drops the approach; needs findings.", "APPROVE", "REWORK", "REJECT"),
			"summary":          str("The result in one or two sentences."),
			"findings": array("Concrete problems. Required for REWORK and REJECT.", object(map[string]any{
				"file":     str("File path relative to the repository root."),
				"line":     integer("Line number, 0 if not applicable.", 0, 0),
				"severity": enum("blocker: must fix; major: should fix now; minor: nice to fix.", "blocker", "major", "minor"),
				"text":     str("What is wrong and what to do instead."),
				"area":     str("Area of the finding when it is not the item's area."),
			}, "text", "severity")),
		}, "criteria_verdict", "code_verdict", "decision", "summary"),
	}
	t.run = func(ctx context.Context, s *server, args json.RawMessage) *toolResult {
		var a struct {
			ItemID   string `json:"item_id"`
			Criteria string `json:"criteria_verdict"`
			Code     string `json:"code_verdict"`
			Decision string `json:"decision"`
			Summary  string `json:"summary"`
			Findings []struct {
				File     string `json:"file"`
				Line     int32  `json:"line"`
				Severity string `json:"severity"`
				Text     string `json:"text"`
				Area     string `json:"area"`
			} `json:"findings"`
		}
		if err := decodeArgs(t, args, &a); err != nil {
			return errorResult(err.Error())
		}
		r := &pb.Review{CriteriaVerdict: strings.ToLower(strings.TrimSpace(a.Criteria)), CodeVerdict: strings.ToLower(strings.TrimSpace(a.Code)),
			Decision: strings.ToUpper(strings.TrimSpace(a.Decision)), Summary: strings.TrimSpace(a.Summary)}
		for i, f := range a.Findings {
			sev := strings.ToLower(strings.TrimSpace(f.Severity))
			if blank(f.Text) {
				return errorResult(fmt.Sprintf("finding %d needs a text saying what is wrong", i+1))
			}
			if sev != "" && !oneOf(sev, "blocker", "major", "minor") {
				return errorResult(fmt.Sprintf("finding %d has severity %q: use blocker, major or minor", i+1, f.Severity))
			}
			if f.Line < 0 {
				return errorResult(fmt.Sprintf("finding %d has a negative line", i+1))
			}
			r.Findings = append(r.Findings, &pb.Finding{File: strings.TrimSpace(f.File), Line: f.Line, Severity: sev, Text: strings.TrimSpace(f.Text), Area: strings.TrimSpace(f.Area)})
		}
		if err := validateReview(r); err != nil {
			return errorResult(err.Error())
		}
		id, err := s.itemID(a.ItemID, t.Name)
		if err != nil {
			return errorResult(err.Error())
		}
		r.ItemId = id
		resp, err := s.api.SubmitReview(ctx, connect.NewRequest(&pb.ReviewRequest{Project: s.opts.Project, Review: r}))
		if err != nil {
			return daemonError(err)
		}
		text := fmt.Sprintf("Review of %s recorded: %s (criteria %s, code %s, %d findings).", id, r.Decision, r.CriteriaVerdict, r.CodeVerdict, len(r.Findings))
		if it := resp.Msg.GetItem(); it != nil {
			text += " " + itemState(it)
		}
		return textResult(text, pbJSON(resp.Msg))
	}
	return t
}

func validateReview(r *pb.Review) error {
	if blank(r.CriteriaVerdict) || blank(r.CodeVerdict) {
		return errors.New("a review needs two verdicts: criteria_verdict (does it meet the criteria?) and code_verdict (is the code good?), each pass or fail")
	}
	if !oneOf(r.CriteriaVerdict, "pass", "fail") {
		return fmt.Errorf("criteria_verdict must be pass or fail, not %q", r.CriteriaVerdict)
	}
	if !oneOf(r.CodeVerdict, "pass", "fail") {
		return fmt.Errorf("code_verdict must be pass or fail, not %q", r.CodeVerdict)
	}
	switch r.Decision {
	case "APPROVE":
		if r.CriteriaVerdict != "pass" || r.CodeVerdict != "pass" {
			return errors.New("APPROVE needs both verdicts to pass: use REWORK or REJECT with findings")
		}
	case "REWORK", "REJECT":
		if len(r.Findings) == 0 {
			return fmt.Errorf("%s needs at least one finding saying what to fix", r.Decision)
		}
	default:
		return fmt.Errorf("decision must be APPROVE, REWORK or REJECT, not %q", r.Decision)
	}
	if blank(r.Summary) {
		return errors.New("a review needs a summary")
	}
	return nil
}

// ---------- propose ----------

func proposeTool() *tool {
	t := &tool{
		Name:        "propose",
		Description: "Propose work outside your item in the fixed format. The owner decides; nothing is built until approved. There is a daily limit per agent, and equal proposals become a comment on the existing one.",
		Schema: object(map[string]any{
			"title":     str("Short title stating the change."),
			"problem":   str("The problem, in one or two sentences."),
			"evidence":  str("Facts that show the problem: file:line, numbers, a failing check, an event."),
			"impact":    str("What gets better, and for whom, if it is done."),
			"size":      str("Estimate such as 1h, 2h-6h or 2d."),
			"area":      str("Area of the project it touches."),
			"goal_link": str("How it serves the current goal, or 'none' with why it still matters."),
			"kind":      enum("Kind of change. self_improvement changes how the agents work.", "feature", "improvement", "bug", "self_improvement"),
			"priority":  integer("0 (highest) to 3. Defaults to 2.", 0, 3),
		}, "title", "problem", "evidence", "impact", "size", "area", "goal_link"),
	}
	t.run = func(ctx context.Context, s *server, args json.RawMessage) *toolResult {
		var a struct {
			Title    string `json:"title"`
			Problem  string `json:"problem"`
			Evidence string `json:"evidence"`
			Impact   string `json:"impact"`
			Size     string `json:"size"`
			Area     string `json:"area"`
			GoalLink string `json:"goal_link"`
			Kind     string `json:"kind"`
			Priority *int32 `json:"priority"`
		}
		if err := decodeArgs(t, args, &a); err != nil {
			return errorResult(err.Error())
		}
		if err := missing("a proposal", [2]string{"title", a.Title}, [2]string{"problem", a.Problem}, [2]string{"evidence", a.Evidence},
			[2]string{"impact", a.Impact}, [2]string{"size", a.Size}, [2]string{"area", a.Area}, [2]string{"goal_link", a.GoalLink}); err != nil {
			return errorResult(err.Error())
		}
		kind := strings.ToLower(strings.TrimSpace(a.Kind))
		if kind != "" && !oneOf(kind, "feature", "improvement", "bug", "self_improvement") {
			return errorResult(fmt.Sprintf("kind must be feature, improvement, bug or self_improvement, not %q", a.Kind))
		}
		prio := int32(domain.P2)
		if a.Priority != nil {
			if *a.Priority < 0 || *a.Priority > 3 {
				return errorResult("priority must be 0 (highest) to 3")
			}
			prio = *a.Priority
		}
		p := &pb.Proposal{Title: strings.TrimSpace(a.Title), Problem: strings.TrimSpace(a.Problem), Evidence: strings.TrimSpace(a.Evidence),
			Impact: strings.TrimSpace(a.Impact), Size: strings.TrimSpace(a.Size), Area: strings.TrimSpace(a.Area),
			GoalLink: strings.TrimSpace(a.GoalLink), Kind: kind, Priority: prio}
		resp, err := s.api.Propose(ctx, connect.NewRequest(&pb.ProposeRequest{Project: s.opts.Project, Proposal: p}))
		if err != nil {
			return daemonError(err)
		}
		var text string
		switch {
		case resp.Msg.GetDuplicate() != "":
			text = fmt.Sprintf("Not a new proposal: %s already covers it, so yours was added there as a comment.", resp.Msg.GetDuplicate())
		case resp.Msg.GetProposal() != nil:
			text = fmt.Sprintf("Proposal %s recorded (%s). The owner decides; do not build it now.", resp.Msg.GetProposal().GetId(), resp.Msg.GetProposal().GetStatus())
		default:
			text = "Proposal recorded. The owner decides; do not build it now."
		}
		return textResult(text, pbJSON(resp.Msg))
	}
	return t
}

// ---------- ask_owner ----------

func askOwnerTool() *tool {
	t := &tool{
		Name:        "ask_owner",
		Description: "Ask the owner a decision only a person can take, in the fixed format: context in one sentence, the decision, 2 to 4 options with exactly one recommended, and what happens if nobody answers. Do not wait for the answer: the item waits for the owner and you take other work.",
		Schema: object(map[string]any{
			"item_id":  str("Item the question is about. Defaults to the item of this session."),
			"context":  str("The situation, in one sentence."),
			"decision": str("The decision asked, as a question."),
			"options": minMax(array("2 to 4 options, exactly one recommended.", object(map[string]any{
				"label":       str("The option, short and concrete."),
				"recommended": boolean("True for your recommendation."),
			}, "label")), 2, 4),
			"if_no_answer": str("What Barnraise does if nobody answers in time, usually the recommended option."),
		}, "context", "decision", "options", "if_no_answer"),
	}
	t.run = func(ctx context.Context, s *server, args json.RawMessage) *toolResult {
		var a struct {
			ItemID   string `json:"item_id"`
			Context  string `json:"context"`
			Decision string `json:"decision"`
			Options  []struct {
				Label       string `json:"label"`
				Recommended bool   `json:"recommended"`
			} `json:"options"`
			IfNoAnswer string `json:"if_no_answer"`
		}
		if err := decodeArgs(t, args, &a); err != nil {
			return errorResult(err.Error())
		}
		if err := missing("a question to the owner", [2]string{"context", a.Context}, [2]string{"decision", a.Decision}, [2]string{"if_no_answer", a.IfNoAnswer}); err != nil {
			return errorResult(err.Error())
		}
		if strings.Contains(strings.TrimSpace(a.Context), "\n") {
			return errorResult("context must be one sentence, without line breaks")
		}
		if len(a.Options) < 2 || len(a.Options) > 4 {
			return errorResult(fmt.Sprintf("a question needs 2 to 4 options, not %d", len(a.Options)))
		}
		q := &pb.Question{Context: strings.TrimSpace(a.Context), Decision: strings.TrimSpace(a.Decision), IfNoAnswer: strings.TrimSpace(a.IfNoAnswer)}
		rec := 0
		for i, o := range a.Options {
			if blank(o.Label) {
				return errorResult(fmt.Sprintf("option %d needs a label", i+1))
			}
			if o.Recommended {
				rec++
			}
			q.Options = append(q.Options, &pb.Option{Label: strings.TrimSpace(o.Label), Recommended: o.Recommended})
		}
		if rec != 1 {
			return errorResult(fmt.Sprintf("exactly one option must be recommended, not %d", rec))
		}
		q.ItemId = strings.TrimSpace(a.ItemID)
		if q.ItemId == "" {
			q.ItemId = s.currentItem()
		}
		resp, err := s.api.AskOwner(ctx, connect.NewRequest(&pb.AskOwnerRequest{Project: s.opts.Project, Question: q}))
		if err != nil {
			return daemonError(err)
		}
		got := resp.Msg.GetQuestion()
		text := fmt.Sprintf("Question %s sent to the owner.", got.GetId())
		if q.ItemId != "" {
			text += fmt.Sprintf(" %s waits for the owner;", q.ItemId)
		}
		text += " do not wait: record your state and take other work."
		if got.GetDeadline() != "" {
			text += fmt.Sprintf(" If nobody answers by %s: %s", got.GetDeadline(), q.IfNoAnswer)
		}
		return textResult(text, pbJSON(resp.Msg))
	}
	return t
}

// ---------- message ----------

func messageTool() *tool {
	t := &tool{
		Name:        "message",
		Description: "Send a short message to another agent, to every agent of a role or to whoever works on an item. Use it for facts others need now, not for discussion.",
		Schema: object(map[string]any{
			"to":      str("An agent name, role:<role> (e.g. role:reviewer) or item:<id> (e.g. item:I-42)."),
			"text":    str(fmt.Sprintf("The message, at most %d characters.", domain.MaxMessageLen)),
			"item_id": str("Item the message is about. Defaults to the item of this session."),
		}, "to", "text"),
	}
	t.run = func(ctx context.Context, s *server, args json.RawMessage) *toolResult {
		var a struct {
			To     string `json:"to"`
			Text   string `json:"text"`
			ItemID string `json:"item_id"`
		}
		if err := decodeArgs(t, args, &a); err != nil {
			return errorResult(err.Error())
		}
		if err := missing("a message", [2]string{"to", a.To}, [2]string{"text", a.Text}); err != nil {
			return errorResult(err.Error())
		}
		to := strings.TrimSpace(a.To)
		for _, prefix := range []string{"role:", "item:"} {
			if strings.HasPrefix(to, prefix) && blank(strings.TrimPrefix(to, prefix)) {
				return errorResult(fmt.Sprintf("to %q needs a value after %s", to, prefix))
			}
		}
		if n := len([]rune(strings.TrimSpace(a.Text))); n > domain.MaxMessageLen {
			return errorResult(fmt.Sprintf("the message has %d characters; the limit is %d: shorten it or link to a file", n, domain.MaxMessageLen))
		}
		item := strings.TrimSpace(a.ItemID)
		if item == "" && strings.HasPrefix(to, "item:") {
			item = strings.TrimSpace(strings.TrimPrefix(to, "item:"))
		}
		if item == "" {
			item = s.currentItem()
		}
		m := &pb.Message{To: to, Text: strings.TrimSpace(a.Text), ItemId: item}
		resp, err := s.api.SendMessage(ctx, connect.NewRequest(&pb.SendMessageRequest{Project: s.opts.Project, Message: m}))
		if err != nil {
			return daemonError(err)
		}
		return textResult(fmt.Sprintf("Message %s sent to %s.", resp.Msg.GetMessage().GetId(), to), pbJSON(resp.Msg))
	}
	return t
}

// ---------- status ----------

func statusTool() *tool {
	t := &tool{
		Name:        "status",
		Description: "The project at a glance: goal and progress, focus, item counts, the queue, questions waiting for the owner, agents, accounts and alerts.",
		Schema:      object(map[string]any{}),
	}
	t.run = func(ctx context.Context, s *server, args json.RawMessage) *toolResult {
		var a struct{}
		if err := decodeArgs(t, args, &a); err != nil {
			return errorResult(err.Error())
		}
		resp, err := s.api.Status(ctx, connect.NewRequest(&pb.ProjectRef{Project: s.opts.Project}))
		if err != nil {
			return daemonError(err)
		}
		return textResult(renderStatus(resp.Msg), pbJSON(resp.Msg))
	}
	return t
}

func renderStatus(st *pb.StatusResponse) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Project %s", st.GetProject())
	if st.GetMachine() != "" {
		fmt.Fprintf(&b, " on %s", st.GetMachine())
	}
	b.WriteString(".\n")
	if st.GetPaused() {
		b.WriteString("PAUSED")
		if st.GetPausedWhy() != "" {
			b.WriteString(": " + st.GetPausedWhy())
		}
		b.WriteString("\n")
	}
	if g := st.GetGoal(); g != nil && g.GetName() != "" {
		fmt.Fprintf(&b, "Goal: %s", g.GetName())
		if p := st.GetProgress(); p != nil {
			fmt.Fprintf(&b, " (%d/%d done, %d%%", p.GetDone(), p.GetTotal(), p.GetPercentage())
			if p.GetForecast() != "" {
				fmt.Fprintf(&b, ", forecast %s", p.GetForecast())
			}
			b.WriteString(")")
		}
		b.WriteString("\n")
	}
	if f := st.GetFocus(); f != nil && !f.GetCancelled() && (f.GetArea() != "" || len(f.GetItems()) > 0) {
		target := f.GetArea()
		if target == "" {
			target = strings.Join(f.GetItems(), ", ")
		}
		fmt.Fprintf(&b, "Focus: %s (%d/%d done", target, st.GetFocusDone(), st.GetFocusAll())
		if f.GetUntil() != "" {
			fmt.Fprintf(&b, ", until %s", f.GetUntil())
		}
		b.WriteString(")\n")
	}
	if len(st.GetCounts()) > 0 {
		keys := make([]string, 0, len(st.GetCounts()))
		for k := range st.GetCounts() {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s %d", strings.ReplaceAll(k, "_", " "), st.GetCounts()[k]))
		}
		b.WriteString("Items: " + strings.Join(parts, ", ") + "\n")
	}
	if q := st.GetQueue(); len(q) > 0 {
		ids := make([]string, 0, len(q))
		for _, it := range q {
			ids = append(ids, it.GetId())
		}
		b.WriteString("Queue: " + strings.Join(ids, ", ") + "\n")
	}
	if w := st.GetWaitingOwner(); len(w) > 0 {
		b.WriteString("Waiting for the owner:\n")
		for _, q := range w {
			fmt.Fprintf(&b, "- %s", q.GetId())
			if q.GetItemId() != "" {
				fmt.Fprintf(&b, " (%s)", q.GetItemId())
			}
			fmt.Fprintf(&b, ": %s\n", q.GetDecision())
		}
	}
	if ag := st.GetAgents(); len(ag) > 0 {
		b.WriteString("Agents:\n")
		for _, x := range ag {
			fmt.Fprintf(&b, "- %s (%s) %s", x.GetAgent(), x.GetRole(), x.GetState())
			if x.GetItemId() != "" {
				fmt.Fprintf(&b, " on %s", x.GetItemId())
				if x.GetStep() != "" {
					fmt.Fprintf(&b, " step %s", x.GetStep())
				}
			}
			b.WriteString("\n")
		}
	}
	if acc := st.GetAccounts(); len(acc) > 0 {
		b.WriteString("Accounts:\n")
		for _, x := range acc {
			fmt.Fprintf(&b, "- %s %s", x.GetId(), x.GetStatus())
			if u := x.GetUsage(); u != nil {
				fmt.Fprintf(&b, " (5h %.0f%%, week %.0f%%)", u.GetFiveHourPct(), u.GetWeeklyPct())
			}
			b.WriteString("\n")
		}
	}
	if st.GetOpenProposals() > 0 {
		fmt.Fprintf(&b, "Open proposals: %d\n", st.GetOpenProposals())
	}
	for _, al := range st.GetAlerts() {
		b.WriteString("Alert: " + al + "\n")
	}
	return b.String()
}

// ---------- context ----------

func contextTool() *tool {
	t := &tool{
		Name:        "context",
		Description: "The rendered context of an item: role core, constitution, standards of its areas, the item, decisions and learnings, cut to a token budget. Use it to resume after a context reset instead of guessing.",
		Schema: object(map[string]any{
			"item_id": str("Item id such as I-42. Defaults to the item of this session."),
			"budget":  integer("Token budget; 0 uses the project default.", 0, 0),
		}),
	}
	t.run = func(ctx context.Context, s *server, args json.RawMessage) *toolResult {
		var a struct {
			ItemID string `json:"item_id"`
			Budget int32  `json:"budget"`
		}
		if err := decodeArgs(t, args, &a); err != nil {
			return errorResult(err.Error())
		}
		if a.Budget < 0 {
			return errorResult("budget must be 0 or more tokens")
		}
		id, err := s.itemID(a.ItemID, t.Name)
		if err != nil {
			return errorResult(err.Error())
		}
		resp, err := s.api.Context(ctx, connect.NewRequest(&pb.ContextRequest{Project: s.opts.Project, Id: id, Budget: a.Budget}))
		if err != nil {
			return daemonError(err)
		}
		c := resp.Msg
		text := renderContext(c)
		if text == "" {
			text = "No context recorded for " + id + "."
		}
		if c.GetTokens() > 0 || len(c.GetCut()) > 0 {
			text += fmt.Sprintf("\n\n(%d of %d tokens", c.GetTokens(), c.GetBudget())
			if len(c.GetCut()) > 0 {
				text += "; left out: " + strings.Join(c.GetCut(), ", ") + "; use find to read them"
			}
			text += ")"
		}
		return textResult(text, pbJSON(c))
	}
	return t
}

// ---------- find ----------

func findTool() *tool {
	t := &tool{
		Name:        "find",
		Description: "Search the project's knowledge: items, decisions, standards, specs, learnings and code anchors. Returns short hits; open what you need with context.",
		Schema: object(map[string]any{
			"query": str("Words to search for."),
			"kind":  str("Only hits of this kind, e.g. item, decision, standard, spec, learning."),
			"area":  str("Only hits of this area."),
			"limit": integer("Maximum hits, 1 to 50. Defaults to 10.", 0, 50),
		}, "query"),
	}
	t.run = func(ctx context.Context, s *server, args json.RawMessage) *toolResult {
		var a struct {
			Query string `json:"query"`
			Kind  string `json:"kind"`
			Area  string `json:"area"`
			Limit int32  `json:"limit"`
		}
		if err := decodeArgs(t, args, &a); err != nil {
			return errorResult(err.Error())
		}
		if blank(a.Query) {
			return errorResult("find needs a query")
		}
		if a.Limit < 0 || a.Limit > 50 {
			return errorResult("limit must be 1 to 50")
		}
		if a.Limit == 0 {
			a.Limit = 10
		}
		resp, err := s.api.Find(ctx, connect.NewRequest(&pb.FindRequest{Project: s.opts.Project, Query: strings.TrimSpace(a.Query),
			Kind: strings.TrimSpace(a.Kind), Area: strings.TrimSpace(a.Area), Limit: a.Limit}))
		if err != nil {
			return daemonError(err)
		}
		hits := resp.Msg.GetHits()
		if len(hits) == 0 {
			return textResult(fmt.Sprintf("Nothing found for %q.", a.Query), pbJSON(resp.Msg))
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d hits for %q:\n", len(hits), a.Query)
		for _, h := range hits {
			fmt.Fprintf(&b, "- %s [%s] %s", h.GetId(), h.GetKind(), h.GetTitle())
			if h.GetSummary() != "" {
				b.WriteString(": " + h.GetSummary())
			}
			if h.GetPath() != "" {
				b.WriteString(" (" + h.GetPath() + ")")
			}
			b.WriteString("\n")
		}
		return textResult(b.String(), pbJSON(resp.Msg))
	}
	return t
}

// ---------- approve ----------

func approveTool() *tool {
	t := &tool{
		Name:        "approve",
		Description: "Approve or refuse a plan, a delivery or a proposal. Always say what is approved: approving a plan never approves the delivery.",
		Schema: object(map[string]any{
			"target":  str("I-42 for a plan or a delivery, P-7 for a proposal."),
			"what":    enum("What is approved.", "plan", "delivery", "proposal"),
			"approve": boolean("True to approve, false to refuse."),
			"note":    str("Why, or what to change. Recommended when refusing."),
		}, "target", "what", "approve"),
	}
	t.run = func(ctx context.Context, s *server, args json.RawMessage) *toolResult {
		var a struct {
			Target  string `json:"target"`
			What    string `json:"what"`
			Approve *bool  `json:"approve"`
			Note    string `json:"note"`
		}
		if err := decodeArgs(t, args, &a); err != nil {
			return errorResult(err.Error())
		}
		target := strings.ToUpper(strings.TrimSpace(a.Target))
		what := strings.ToLower(strings.TrimSpace(a.What))
		if target == "" {
			return errorResult("approve needs a target such as I-42 or P-7")
		}
		if !oneOf(what, "plan", "delivery", "proposal") {
			return errorResult("approve needs what: plan, delivery or proposal; never approve without saying what")
		}
		if a.Approve == nil {
			return errorResult("approve needs approve: true to approve, false to refuse")
		}
		if what == "proposal" && !strings.HasPrefix(target, "P-") {
			return errorResult(fmt.Sprintf("a proposal target looks like P-7, not %s", target))
		}
		if what != "proposal" && !strings.HasPrefix(target, "I-") {
			return errorResult(fmt.Sprintf("a %s target is an item such as I-42, not %s", what, target))
		}
		_, err := s.api.Approve(ctx, connect.NewRequest(&pb.ApproveRequest{Project: s.opts.Project, Target: target, What: what,
			Approve: *a.Approve, Note: strings.TrimSpace(a.Note)}))
		if err != nil {
			return daemonError(err)
		}
		verb := "Approved"
		if !*a.Approve {
			verb = "Refused"
		}
		return textResult(fmt.Sprintf("%s the %s of %s.", verb, what, target),
			map[string]any{"target": target, "what": what, "approve": *a.Approve})
	}
	return t
}

// ---------- focus ----------

func focusTool() *tool {
	t := &tool{
		Name:        "focus",
		Description: "Point the agents at one area or a list of items for a while. Use preview first to see what it would park elsewhere.",
		Schema: object(map[string]any{
			"area":    str("Area to focus on."),
			"items":   strList("Items to focus on, e.g. [\"I-42\", \"I-43\"]."),
			"until":   str("When the focus ends: a date, a duration such as 3d, or 'done' when its items are integrated."),
			"share":   integer("Percent of capacity the focus takes, 1 to 100. Defaults to the project setting.", 0, 100),
			"preview": boolean("True to only show the effect without applying it."),
		}),
	}
	t.run = func(ctx context.Context, s *server, args json.RawMessage) *toolResult {
		var a struct {
			Area    string   `json:"area"`
			Items   []string `json:"items"`
			Until   string   `json:"until"`
			Share   int32    `json:"share"`
			Preview bool     `json:"preview"`
		}
		if err := decodeArgs(t, args, &a); err != nil {
			return errorResult(err.Error())
		}
		items := nonBlank(a.Items)
		if blank(a.Area) && len(items) == 0 {
			return errorResult("focus needs an area or a list of items")
		}
		if a.Share < 0 || a.Share > 100 {
			return errorResult("share must be 1 to 100 percent")
		}
		f := &pb.Focus{Area: strings.TrimSpace(a.Area), Items: items, Until: strings.TrimSpace(a.Until), Share: a.Share}
		resp, err := s.api.SetFocus(ctx, connect.NewRequest(&pb.FocusRequest{Project: s.opts.Project, Focus: f, Until: f.Until, Preview: a.Preview}))
		if err != nil {
			return daemonError(err)
		}
		target := f.Area
		if target == "" {
			target = strings.Join(items, ", ")
		}
		e := resp.Msg.GetEffect()
		lead := "Focus on " + target + " applied"
		if a.Preview || !resp.Msg.GetApplied() {
			lead = "Preview of a focus on " + target + " (not applied)"
		}
		text := fmt.Sprintf("%s: %d items in focus, %d%% of capacity.", lead, e.GetFocusItems(), e.GetShare())
		if len(e.GetParkedElsewhere()) > 0 {
			text += " Parked elsewhere: " + strings.Join(e.GetParkedElsewhere(), ", ") + "."
		}
		return textResult(text, pbJSON(resp.Msg))
	}
	return t
}

// ---------- pause ----------

func pauseTool() *tool {
	t := &tool{
		Name:        "pause",
		Description: "Stop all agents of the project now (they record their state and end), or resume with resume true.",
		Schema: object(map[string]any{
			"why":    str("Why everything stops. Required to pause."),
			"resume": boolean("True to resume work instead of pausing."),
		}),
	}
	t.run = func(ctx context.Context, s *server, args json.RawMessage) *toolResult {
		var a struct {
			Why    string `json:"why"`
			Resume bool   `json:"resume"`
		}
		if err := decodeArgs(t, args, &a); err != nil {
			return errorResult(err.Error())
		}
		if a.Resume {
			if _, err := s.api.Resume(ctx, connect.NewRequest(&pb.ProjectRef{Project: s.opts.Project})); err != nil {
				return daemonError(err)
			}
			return textResult("Resumed: agents take work again.", map[string]any{"paused": false})
		}
		if blank(a.Why) {
			return errorResult("pause needs why: say why everything stops")
		}
		if _, err := s.api.Pause(ctx, connect.NewRequest(&pb.PauseRequest{Project: s.opts.Project, Why: strings.TrimSpace(a.Why)})); err != nil {
			return daemonError(err)
		}
		return textResult("Paused: agents record their state and stop. Resume with pause and resume true.",
			map[string]any{"paused": true, "why": strings.TrimSpace(a.Why)})
	}
	return t
}

// ---------- answer (owner) ----------

func answerTool() *tool {
	t := &tool{
		Name:        "answer",
		Description: "Answer a question an agent asked the owner. The answer is one of the offered options (its label or its number); add a note for anything else.",
		Schema: object(map[string]any{
			"id":     str("Question id, as shown by status."),
			"answer": str("The chosen option: its label or its number (1 to 4)."),
			"note":   str("Extra guidance for the agent."),
		}, "id", "answer"),
	}
	t.run = func(ctx context.Context, s *server, args json.RawMessage) *toolResult {
		var a struct {
			ID     string `json:"id"`
			Answer string `json:"answer"`
			Note   string `json:"note"`
		}
		if err := decodeArgs(t, args, &a); err != nil {
			return errorResult(err.Error())
		}
		if err := missing("an answer", [2]string{"id", a.ID}, [2]string{"answer", a.Answer}); err != nil {
			return errorResult(err.Error())
		}
		resp, err := s.api.Answer(ctx, connect.NewRequest(&pb.AnswerRequest{Project: s.opts.Project, Id: strings.TrimSpace(a.ID),
			Answer: strings.TrimSpace(a.Answer), Note: strings.TrimSpace(a.Note)}))
		if err != nil {
			return daemonError(err)
		}
		q := resp.Msg.GetQuestion()
		text := fmt.Sprintf("Answered %s: %s.", q.GetId(), q.GetAnswer())
		if q.GetItemId() != "" {
			text += " " + q.GetItemId() + " goes back to work."
		}
		return textResult(text, pbJSON(resp.Msg))
	}
	return t
}

// ---------- create_item (owner) ----------

func createItemTool() *tool {
	t := &tool{
		Name:        "create_item",
		Description: "Create an item: a title, the goal it belongs to, EARS acceptance criteria, area and size; for a bug, what must not change. Without criteria it stays a draft for the analyst.",
		Schema: object(map[string]any{
			"title":    str("Short title stating the result."),
			"body":     str("What and why, facts first."),
			"kind":     str("Flow name, e.g. feature, bug, improvement."),
			"priority": enum("P0 (highest) to P3. Defaults to P2.", "P0", "P1", "P2", "P3"),
			"area":     str("Area of the project it touches."),
			"goal_id":  str("Goal it belongs to; empty uses the active goal."),
			"size":     str("Estimate such as 2h-6h."),
			"criteria": array("Acceptance criteria in EARS form.", object(map[string]any{
				"id":    str("Criterion id, e.g. C1."),
				"text":  str("When <trigger>, the <system> shall <response>."),
				"check": str("The check that verifies it, if any."),
			}, "id", "text")),
			"preserve": strList("For bugs: behavior that must not change."),
			"deps":     strList("Items that must be integrated first."),
			"risky":    boolean("True when a person must approve the plan."),
		}, "title"),
	}
	t.run = func(ctx context.Context, s *server, args json.RawMessage) *toolResult {
		var a struct {
			Title    string `json:"title"`
			Body     string `json:"body"`
			Kind     string `json:"kind"`
			Priority string `json:"priority"`
			Area     string `json:"area"`
			GoalID   string `json:"goal_id"`
			Size     string `json:"size"`
			Criteria []struct {
				ID    string `json:"id"`
				Text  string `json:"text"`
				Check string `json:"check"`
			} `json:"criteria"`
			Preserve []string `json:"preserve"`
			Deps     []string `json:"deps"`
			Risky    bool     `json:"risky"`
		}
		if err := decodeArgs(t, args, &a); err != nil {
			return errorResult(err.Error())
		}
		if blank(a.Title) {
			return errorResult("create_item needs a title")
		}
		if a.Priority != "" {
			if _, err := domain.ParsePriority(a.Priority); err != nil {
				return errorResult("priority must be P0, P1, P2 or P3")
			}
		}
		req := &pb.CreateItemRequest{Project: s.opts.Project, Title: strings.TrimSpace(a.Title), Body: strings.TrimSpace(a.Body),
			Kind: strings.TrimSpace(a.Kind), Priority: strings.ToUpper(strings.TrimSpace(a.Priority)), Area: strings.TrimSpace(a.Area),
			GoalId: strings.TrimSpace(a.GoalID), Size: strings.TrimSpace(a.Size), Preserve: nonBlank(a.Preserve), Deps: nonBlank(a.Deps), Risky: a.Risky}
		for i, c := range a.Criteria {
			if blank(c.ID) || blank(c.Text) {
				return errorResult(fmt.Sprintf("criterion %d needs an id and a text", i+1))
			}
			req.Criteria = append(req.Criteria, &pb.Criterion{Id: strings.TrimSpace(c.ID), Text: strings.TrimSpace(c.Text), Check: strings.TrimSpace(c.Check)})
		}
		resp, err := s.api.CreateItem(ctx, connect.NewRequest(req))
		if err != nil {
			return daemonError(err)
		}
		var text string
		switch {
		case resp.Msg.GetItem() != nil:
			it := resp.Msg.GetItem()
			text = fmt.Sprintf("Created %s %q (%s).", it.GetId(), it.GetTitle(), strings.ReplaceAll(it.GetStatus(), "_", " "))
		case resp.Msg.GetProposal() != nil:
			text = fmt.Sprintf("Recorded as proposal %s.", resp.Msg.GetProposal().GetId())
		default:
			text = "Recorded."
		}
		if n := resp.Msg.GetNote(); n != "" {
			text += " " + n
		}
		return textResult(text, pbJSON(resp.Msg))
	}
	return t
}
