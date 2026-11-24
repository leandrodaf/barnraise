package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/internal/adapters/out/fakeprovider"
	"github.com/leandrodaf/barnraise/internal/client"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// fakeAgent is a scripted agent for demos and end-to-end tests: it talks to
// the daemon through the same API and tokens as a real agent.
//
// Item bodies can steer it:
//
//	fake: write <path> <content>   write a file in the implement step
//	fake: rework-once              the first review asks for rework
//	fake: block                    deliver BLOCKED in the implement step
type fakeAgent struct {
	provider *fakeprovider.Provider
}

func newFakeAgent() *fakeAgent {
	f := &fakeAgent{provider: fakeprovider.New("fake", "fake")}
	f.provider.Handle(f.handle)
	return f
}

// FakeModels routes every class to the fake provider.
func fakeModels(r domain.ModelRouter) domain.ModelRouter {
	for class := range r.Classes {
		r.Classes[class] = []domain.ModelChoice{{Provider: "fake", Model: "fake-large"}, {Provider: "fake", Model: "fake-small"}}
	}
	r.Classes["cheap"] = []domain.ModelChoice{{Provider: "fake", Model: "fake-small"}, {Provider: "fake", Model: "fake-large"}}
	return r
}

func (f *fakeAgent) handle(ctx context.Context, spec ports.SessionSpec, emit func(ports.ProviderEvent)) (ports.SessionResult, error) {
	start := time.Now()
	cl := client.New(client.Options{Socket: spec.Env["BARNRAISE_SOCKET"], Token: spec.Env["BARNRAISE_TOKEN"]})
	project := spec.Env["BARNRAISE_PROJECT"]
	emit(ports.ProviderEvent{Kind: ports.PEInit, SessionID: "fake"})
	asg, err := cl.NextItem(ctx, connect.NewRequest(&pb.NextItemRequest{Project: project}))
	if err != nil {
		return ports.SessionResult{}, err
	}
	if asg.Msg.None {
		return ports.SessionResult{Text: "no work"}, nil
	}
	it := asg.Msg.Item
	if spec.Env["BARNRAISE_STEP"] == "refine" {
		if strings.Contains(it.Body, "fake: needs-owner") {
			_, err := cl.AskOwner(ctx, connect.NewRequest(&pb.AskOwnerRequest{Project: project, Question: &pb.Question{ItemId: it.Id,
				Context: "The draft does not say which data to show.", Decision: "What should the screen show?",
				Options: []*pb.Option{{Label: "The balance", Recommended: true}, {Label: "The last transactions"}}, IfNoAnswer: "The item stays a draft."}}))
			return ports.SessionResult{Text: "asked the owner"}, err
		}
		patch, _ := structpb.NewStruct(map[string]any{
			"criteria":       []any{map[string]any{"text": "WHEN the user opens " + it.Title + ", THE SYSTEM SHALL show it", "check": "main"}},
			"clarifications": []any{},
			"body":           strings.ReplaceAll(it.Body, "[NEEDS CLARIFICATION", "[ANSWERED"),
			"reason":         "refined by the analyst",
		})
		_, err := cl.UpdateItem(ctx, connect.NewRequest(&pb.UpdateItemRequest{Project: project, Id: it.Id, Patch: patch}))
		return ports.SessionResult{Text: "refined " + it.Id}, err
	}
	st := asg.Msg.Step.GetFields()
	stepName := st["name"].GetStringValue()
	stepType := st["type"].GetStringValue()
	output := st["output"].GetStringValue()
	review := st["review"].GetBoolValue() || stepType == string(domain.StepApproval)
	emit(ports.ProviderEvent{Kind: ports.PEToolUse, Tool: "Read"})
	usage := domain.TokenUsage{Input: 1200, Output: 300, CacheRead: 4000}
	emit(ports.ProviderEvent{Kind: ports.PEUsage, Usage: &usage})
	text := fmt.Sprintf("fake agent %s on %s step %s", spec.Agent, it.Id, stepName)
	switch {
	case review:
		r := &pb.Review{ItemId: it.Id, Step: stepName, CriteriaVerdict: "pass", CodeVerdict: "pass", Decision: "APPROVE", Summary: "Looks right: criteria verified by the checks."}
		if strings.Contains(it.Body, "fake: rework-once") && len(it.Rework) == 0 && stepType != string(domain.StepApproval) {
			r = &pb.Review{ItemId: it.Id, Step: stepName, CriteriaVerdict: "fail", CodeVerdict: "pass", Decision: "REWORK", Summary: "One criterion is not shown.",
				Findings: []*pb.Finding{{File: "README.md", Line: 1, Severity: "major", Text: "add the evidence for C1", Area: it.Area}}}
		}
		if _, err := cl.SubmitReview(ctx, connect.NewRequest(&pb.ReviewRequest{Project: project, Review: r})); err != nil {
			return ports.SessionResult{Text: text, Usage: usage}, err
		}
	case output == "plan.md":
		var plan strings.Builder
		fmt.Fprintf(&plan, "# Plan for %s\n\n## Files\n- FAKE_%s.md\n\n## Steps\n1. Write the change.\n\n## Verification\n", it.Id, it.Id)
		for _, c := range it.Criteria {
			fmt.Fprintf(&plan, "- %s: check %s\n", c.Id, c.Check)
		}
		if err := os.WriteFile(filepath.Join(spec.Workdir, "plan.md"), []byte(plan.String()), 0o644); err != nil {
			return ports.SessionResult{}, err
		}
		if _, err := cl.Deliver(ctx, connect.NewRequest(&pb.DeliverRequest{Project: project, Delivery: &pb.Delivery{ItemId: it.Id, Step: stepName, Status: "DONE", Summary: "plan.md written", Files: []string{"plan.md"}}})); err != nil {
			return ports.SessionResult{Text: text, Usage: usage}, err
		}
	case output == "failing_test":
		name := filepath.Join("tests", "repro_"+strings.ToLower(it.Id)+".sh")
		_ = os.MkdirAll(filepath.Join(spec.Workdir, "tests"), 0o755)
		if err := os.WriteFile(filepath.Join(spec.Workdir, name), []byte("#!/bin/sh\ntest -f FAKE_"+it.Id+".md\n"), 0o755); err != nil {
			return ports.SessionResult{}, err
		}
		if _, err := cl.Deliver(ctx, connect.NewRequest(&pb.DeliverRequest{Project: project, Delivery: &pb.Delivery{ItemId: it.Id, Step: stepName, Status: "DONE", Summary: "reproduction test written", Files: []string{name}}})); err != nil {
			return ports.SessionResult{Text: text, Usage: usage}, err
		}
	default:
		// "fake: hang-on <machine>" holds the implement step on that machine (failover tests).
		for line := range strings.SplitSeq(it.Body, "\n") {
			if m, ok := strings.CutPrefix(strings.TrimSpace(line), "fake: hang-on "); ok && strings.TrimSpace(m) == os.Getenv("BARNRAISE_FAKE_MACHINE") {
				_ = os.WriteFile(filepath.Join(spec.Workdir, "partial.txt"), []byte("work in progress\n"), 0o644)
				<-ctx.Done()
				return ports.SessionResult{}, ctx.Err()
			}
		}
		if strings.Contains(it.Body, "fake: block") {
			_, err := cl.Deliver(ctx, connect.NewRequest(&pb.DeliverRequest{Project: project, Delivery: &pb.Delivery{ItemId: it.Id, Step: stepName, Status: "BLOCKED", Summary: "cannot continue", Reason: "the fake was told to block"}}))
			return ports.SessionResult{Text: text, Usage: usage}, err
		}
		files := []string{}
		wrote := false
		for line := range strings.SplitSeq(it.Body, "\n") {
			if path, ok := strings.CutPrefix(strings.TrimSpace(line), "fake: env-file "); ok {
				// The session's own environment, as a file of the change (the
				// per-item isolation an end-to-end run checks).
				var env strings.Builder
				for _, k := range []string{"PORT", "BARNRAISE_PORTS", "COMPOSE_PROJECT_NAME", "BARNRAISE_ITEM_SLUG"} {
					fmt.Fprintf(&env, "%s=%s\n", k, spec.Env[k])
				}
				full := filepath.Join(spec.Workdir, filepath.Clean("/" + path)[1:])
				_ = os.MkdirAll(filepath.Dir(full), 0o755)
				if err := os.WriteFile(full, []byte(env.String()), 0o644); err != nil {
					return ports.SessionResult{}, err
				}
				files = append(files, path)
				wrote = true
				continue
			}
			rest, ok := strings.CutPrefix(strings.TrimSpace(line), "fake: write ")
			if !ok {
				continue
			}
			path, content, _ := strings.Cut(rest, " ")
			full := filepath.Join(spec.Workdir, filepath.Clean("/" + path)[1:])
			_ = os.MkdirAll(filepath.Dir(full), 0o755)
			if err := os.WriteFile(full, []byte(content+"\n"), 0o644); err != nil {
				return ports.SessionResult{}, err
			}
			files = append(files, path)
			wrote = true
		}
		if !wrote {
			name := "FAKE_" + it.Id + ".md"
			body := fmt.Sprintf("# %s\n\n%s\n(rounds: %d)\n", it.Title, it.Id, len(it.Rework))
			if err := os.WriteFile(filepath.Join(spec.Workdir, name), []byte(body), 0o644); err != nil {
				return ports.SessionResult{}, err
			}
			files = append(files, name)
		}
		var marks []*pb.CriterionMark
		for _, c := range it.Criteria {
			marks = append(marks, &pb.CriterionMark{Id: c.Id, Met: true, Evidence: "verified by " + c.Check})
		}
		if _, err := cl.Deliver(ctx, connect.NewRequest(&pb.DeliverRequest{Project: project, Delivery: &pb.Delivery{ItemId: it.Id, Step: stepName, Status: "DONE", Summary: "change written", Files: files, Criteria: marks}})); err != nil {
			return ports.SessionResult{Text: text, Usage: usage}, err
		}
	}
	return ports.SessionResult{Text: text, Usage: usage, Duration: time.Since(start)}, nil
}
