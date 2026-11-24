package app

import (
	"context"
	"maps"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// checkBase is the commit a check measures the change against: where the
// item's branch forked from main ("" when unknown). The dependencies check
// compares the manifests there with the item's own.
func (s *Service) checkBase(ctx context.Context, p *Project, main, head string) string {
	if p.Ports().Git == nil || main == "" || head == "" {
		return ""
	}
	base, err := p.Ports().Git.MergeBase(ctx, main, head)
	if err != nil {
		return main
	}
	return base
}

// withStepOutcome names the finished step and its outcome in the event, so
// hooks see which step ended (the item it carries has already moved on).
func withStepOutcome(pl domain.Payload, step string, outcome domain.Outcome) domain.Payload {
	extra := maps.Clone(pl.Extra)
	if extra == nil {
		extra = map[string]any{}
	}
	if _, ok := extra["step"]; !ok {
		extra["step"] = step
	}
	if _, ok := extra["outcome"]; !ok && outcome != "" {
		extra["outcome"] = string(outcome)
	}
	pl.Extra = extra
	return pl
}
