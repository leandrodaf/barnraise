package app

import (
	"strconv"
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// The board's columns: where an item belongs on the owner's queue.
const (
	ColBacklog = "backlog" // ready, draft, parked: nobody works on it yet
	ColWorking = "working" // a step that is not a review runs or waits for a machine
	ColNeeds   = "needs"   // the owner has to answer or approve
	ColReview  = "review"  // at a review step
	ColQueue   = "queue"   // in the merge queue
	ColBlocked = "blocked" // waits for another item, or its machine is out
	ColDone    = "done"    // integrated or closed
)

// FlowStep is one step of the item's flow and where the item is on it.
type FlowStep struct {
	Name string `json:"name"`
	// Kind: plan, work, approval, review, check or merge.
	Kind string `json:"kind"`
	// State: done, current or next.
	State string `json:"state"`
}

// Signal is the one fact a person should read about an item now, as a code
// and its argument; the panel words it in the reader's language.
type Signal struct {
	// Kind: approve (arg plan|delivery), question (arg question id),
	// check_failed (arg check), changes_requested (arg findings),
	// waits_for (arg items), stalled (arg machine), working (arg agent),
	// running (arg processes), queued, paused, parked, draft (arg open
	// clarifications), ready (arg items it comes after), idle (arg machine),
	// integrated, closed (arg reason).
	Kind string `json:"kind"`
	Arg  string `json:"arg,omitempty"`
}

func stepKind(st domain.StepDef) string {
	switch st.Type {
	case domain.StepAgent:
		switch {
		case st.Review:
			return "review"
		case st.Output == "plan.md":
			return "plan"
		}
		return "work"
	case domain.StepApproval:
		return "approval"
	case domain.StepMergeQueue:
		return "merge"
	}
	return "check"
}

// boardFacts fills what the owner's board needs from the item's flow: the
// steps, what the owner approves now, and the column.
func boardFacts(p *Project, it *domain.Item, io *ItemOverview) {
	var cur domain.StepDef
	hasCur := false
	if f, err := p.Cfg().FlowOf(it.Kind, it.Area); err == nil {
		at := f.Index(it.Step)
		for i, st := range f.Steps {
			state := "next"
			switch {
			case it.Status == domain.StatusIntegrated, at >= 0 && i < at:
				state = "done"
			case i == at && !it.Status.Terminal():
				state = "current"
			}
			io.Flow = append(io.Flow, FlowStep{Name: st.Name, Kind: stepKind(st), State: state})
		}
		cur, hasCur = f.Step(it.Step)
	}
	if hasCur && !it.Status.Terminal() && cur.Type == domain.StepApproval && (cur.Approver == "" || cur.Approver == "owner") {
		io.Awaits = approvalWhat(p, it, cur)
	}
	io.NeedsOwner = it.Status == domain.StatusWaitingOwner || io.Awaits != ""
	switch {
	case it.Status.Terminal():
		io.Column = ColDone
	case io.NeedsOwner:
		io.Column = ColNeeds
	case it.Status == domain.StatusQueued:
		io.Column = ColQueue
	case it.Status == domain.StatusInProgress && io.Activity == ActBlocked:
		// Its step waits for other items: planned, and it starts by itself.
		io.Column = ColBacklog
	case it.Status == domain.StatusBlocked, io.Activity == ActBlocked, io.Activity == ActStalled:
		io.Column = ColBlocked
	case it.Status == domain.StatusInProgress && hasCur && cur.Type == domain.StepAgent && cur.Review:
		io.Column = ColReview
	case it.Status == domain.StatusInProgress, it.Status == domain.StatusPaused:
		io.Column = ColWorking
	default:
		io.Column = ColBacklog
	}
}

// itemSignal picks the fact to show first. il is the item's ledger, nil when
// nothing was spent on it yet; it is read even for a brief answer.
func itemSignal(it *domain.Item, io *ItemOverview, il *domain.ItemLedger) Signal {
	switch {
	case it.Status == domain.StatusIntegrated:
		return Signal{Kind: "integrated"}
	case it.Status == domain.StatusClosed:
		return Signal{Kind: "closed", Arg: it.ClosedReason}
	case io.Awaits != "":
		return Signal{Kind: "approve", Arg: io.Awaits}
	case io.NeedsOwner:
		return Signal{Kind: "question", Arg: it.WaitingQuestion}
	}
	if il != nil {
		for _, name := range sortedKeys(il.Checks) {
			if c := il.Checks[name]; !c.Passed && !c.Flaky {
				return Signal{Kind: "check_failed", Arg: c.Check}
			}
		}
		if r := il.LastReview; r != nil && !strings.EqualFold(r.Decision, "approve") && r.Decision != "" && r.Step != it.Step {
			return Signal{Kind: "changes_requested", Arg: strconv.Itoa(r.Findings)}
		}
	}
	switch io.Activity {
	case ActBlocked:
		return Signal{Kind: "waits_for", Arg: strings.Join(io.BlockedBy, ", ")}
	case ActStalled:
		return Signal{Kind: "stalled", Arg: io.Machine}
	case ActWorking, ActRefining:
		if len(io.Sessions) > 0 {
			return Signal{Kind: "working", Arg: io.Sessions[0].Agent}
		}
		return Signal{Kind: "working"}
	case ActRunning:
		if io.Procs != nil {
			return Signal{Kind: "running", Arg: strconv.Itoa(io.Procs.Count)}
		}
		return Signal{Kind: "running"}
	case ActQueued:
		return Signal{Kind: "queued"}
	case ActPaused:
		return Signal{Kind: "paused"}
	case ActParked:
		return Signal{Kind: "parked"}
	case ActDraft:
		if n := len(it.Clarifications); n > 0 {
			return Signal{Kind: "draft", Arg: strconv.Itoa(n)}
		}
		return Signal{Kind: "draft"}
	case ActReady:
		return Signal{Kind: "ready", Arg: strings.Join(io.BlockedBy, ", ")}
	}
	return Signal{Kind: "idle", Arg: io.Machine}
}
