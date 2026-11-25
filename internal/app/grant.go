package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// Scoped session tokens (ADR-0029). The token the daemon hands an agent
// session is bound to that session: its project, its item, its role's
// scopes and its lifetime. Every call made with it is checked here, before
// the handler runs, against an allow-list of the API procedures a session
// may call at all; anything not on the list is refused. A token copied out
// of a session therefore cannot act on another item, call a tool its role
// was not given, reach another project, or outlive the session.

// sessionTokenGrace is how long a session token outlives the session's
// timeout, for the last calls of a session being stopped.
const sessionTokenGrace = 5 * time.Minute

// ProcStreamEvents is the name the HTTP adapter uses for the event stream
// (GET /v1/events), which is not a Connect procedure.
const ProcStreamEvents = "StreamEvents"

// procRule is what a session needs to call a procedure.
type procRule struct {
	// scope the session must hold ("" = every session).
	scope string
	// bound: every item the request names must be the session's item.
	bound bool
	// analystFree: the analyst's work spans the backlog (it refines one item
	// but asks, approves and messages about others), so the binding is
	// lifted for it on this procedure.
	analystFree bool
	// idIsItem: the request's top-level id (or target) names an item.
	idIsItem bool
}

// sessionProcedures is the allow-list. A procedure missing here is never
// callable with a session token (owner and API tokens are not affected).
var sessionProcedures = map[string]procRule{
	// The work of the session.
	"NextItem":        {scope: ScopeClaim},
	"Deliver":         {scope: ScopeDeliver, bound: true},
	"SubmitReview":    {scope: ScopeReview, bound: true},
	"UpdateItem":      {scope: ScopeItemsWrite, bound: true, idIsItem: true},
	"CloseItem":       {scope: ScopeItemsWrite, bound: true, idIsItem: true}, // a draft only, for an agent
	"AskOwner":        {scope: ScopeAsk, bound: true, analystFree: true},
	"SendMessage":     {scope: ScopeMessage, bound: true, analystFree: true},
	"Propose":         {scope: ScopePropose},
	"AddTweak":        {scope: ScopeTweak},
	"Journal":         {scope: ScopeJournal},
	"Inbox":           {bound: true},
	"ReportViolation": {bound: true},
	"ReportUsage":     {}, // the service accepts only the session's own account
	// Reading the project.
	"Status": {scope: ScopeItemsRead}, "GetItem": {scope: ScopeItemsRead}, "ListItems": {scope: ScopeItemsRead},
	"Context": {scope: ScopeItemsRead}, "Find": {scope: ScopeItemsRead}, "Get": {scope: ScopeItemsRead},
	"Related": {scope: ScopeItemsRead}, "Why": {scope: ScopeItemsRead}, "History": {scope: ScopeItemsRead},
	"Rules": {scope: ScopeItemsRead}, "Timeline": {scope: ScopeItemsRead}, "ListProposals": {scope: ScopeItemsRead},
	"ListEvents": {scope: ScopeEvents}, ProcStreamEvents: {scope: ScopeEvents},
	// The analyst's.
	"CreateItem":     {scope: ScopeItemsWrite},
	"Approve":        {scope: ScopeApprove, bound: true, analystFree: true, idIsItem: true},
	"DecideProposal": {scope: ScopeProposalDecide},
	"SetFocus":       {scope: ScopeFocus},
	"Pause":          {scope: ScopePause},
	"Resume":         {scope: ScopePause},
}

// SessionProcedures lists the procedures a session with these scopes may
// call, sorted: for the docs, `barnraise token` output and tests.
func SessionProcedures(scopes []string) []string {
	a := Actor{Kind: ActorAgent, Scopes: scopes}
	var out []string
	for name, r := range sessionProcedures {
		if r.scope == "" || a.Can(r.scope, "") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// CallRefs are the references of a request, read by the inbound adapter.
type CallRefs struct {
	Project string   // the request's project field
	ItemIDs []string // every item_id field, at any depth
	ID      string   // the top-level id or target field
}

// AuthorizeCall decides whether the actor may call a procedure with a
// request naming refs. Only session tokens (agents) are restricted here;
// the handlers still apply every actor's scopes.
func AuthorizeCall(a Actor, procedure string, refs CallRefs) error {
	if a.Kind != ActorAgent {
		return nil
	}
	r, ok := sessionProcedures[procedure]
	if !ok {
		return fmt.Errorf("%w: a session token cannot call %s", ErrForbidden, procedure)
	}
	if refs.Project != "" && a.Project != "" && refs.Project != a.Project {
		return fmt.Errorf("%w: this session belongs to project %s, not %s", ErrForbidden, a.Project, refs.Project)
	}
	if r.scope != "" && !a.Can(r.scope, "") {
		return fmt.Errorf("%w: role %s was not given %s (%s)", ErrForbidden, def(a.Role, "agent"), procedure, r.scope)
	}
	if !r.bound || a.ItemID == "" || (r.analystFree && a.Role == "analyst") {
		return nil
	}
	ids := refs.ItemIDs
	if r.idIsItem && refs.ID != "" {
		ids = append(append([]string(nil), ids...), refs.ID)
	}
	for _, id := range ids {
		if id != "" && !sameItem(id, a.ItemID) {
			return fmt.Errorf("%w: this session works on %s, not %s", ErrForbidden, a.ItemID, id)
		}
	}
	return nil
}

func sameItem(a, b string) bool {
	if na, err := domain.NormalizeItemID(a); err == nil {
		a = na
	}
	if nb, err := domain.NormalizeItemID(b); err == nil {
		b = nb
	}
	return strings.EqualFold(a, b)
}

// maxDenialEvents bounds the denial events one session can write.
const maxDenialEvents = 5

// TokenDenied records a refused session-token call: an event in the
// session's project (the first few per session), and one notice to the
// owner per session. A copied token in use is exactly what this shows.
func (s *Service) TokenDenied(ctx context.Context, a Actor, procedure string, why error) {
	n := s.tokens.noteDenied(a.Session)
	if n > maxDenialEvents || a.Project == "" {
		return
	}
	p, err := s.Project(a.Project)
	if err != nil {
		return
	}
	reason := strings.TrimPrefix(why.Error(), ErrForbidden.Error()+": ")
	_, _ = s.emit(ctx, p, domain.EvTokenDenied, a.ItemID, actorName(a), domain.Payload{Note: reason,
		Extra: map[string]any{"procedure": procedure, "session": a.Session, "role": a.Role, "step": a.Step}})
	if n == 1 {
		go s.notifyOwner(context.WithoutCancel(ctx), p, "policy_violation", "A session token was used outside its session",
			actorName(a)+" ("+a.ItemID+") called "+procedure+": "+reason)
	}
}
