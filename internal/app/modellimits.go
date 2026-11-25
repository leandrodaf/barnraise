package app

import (
	"context"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// Some limits belong to a model, not to the account ("You've hit your Fable
// limit"): the account still runs the other models. They are kept in this
// machine's memory until they reset, and the work goes to the next model of
// its class meanwhile.

// modelLimitDefault is how long a model waits when its limit says no reset time.
const modelLimitDefault = 5 * time.Hour

func modelKey(account, model string) string { return account + "/" + model }

// markModelLimited records that a model of an account is at its own limit.
func (s *Service) markModelLimited(account, model string, until time.Time) {
	if until.IsZero() {
		until = s.d.Clock.Now().Add(modelLimitDefault)
	}
	s.modelLimits.Store(modelKey(account, model), until)
	s.d.Log.Info("model at its own limit; the next model of the class takes the work", "account", account, "model", model, "until", until)
}

// modelLimited reports whether a model of an account is at its own limit now.
func (s *Service) modelLimited(account, model string, now time.Time) bool {
	v, ok := s.modelLimits.Load(modelKey(account, model))
	if !ok {
		return false
	}
	if now.After(v.(time.Time)) {
		s.modelLimits.Delete(modelKey(account, model))
		return false
	}
	return true
}

// withModel keeps the accounts that may run the model now.
func (s *Service) withModel(accounts []*domain.Account, model string, now time.Time) []*domain.Account {
	var out []*domain.Account
	for _, a := range accounts {
		if !s.modelLimited(a.ID, model, now) {
			out = append(out, a)
		}
	}
	return out
}

// pickChoice is the first model of the class an account here may run now,
// with that account: the strongest one available.
func (s *Service) pickChoice(choices []domain.ModelChoice, accounts []*domain.Account, project string, now time.Time) (domain.ModelChoice, *domain.Account) {
	for _, c := range choices {
		if acc := domain.PickAccount(s.withModel(accounts, c.Model, now), c.Provider, s.d.Machine, project, now); acc != nil {
			return c, acc
		}
	}
	return domain.ModelChoice{}, nil
}

// AdvisorAccounts are the accounts of a project's provider that may run a
// model on this machine now, the least loaded first, as the machine runs them.
func (s *Service) AdvisorAccounts(project, provider, model string) []domain.Account {
	p, err := s.Project(project)
	if err != nil {
		return nil
	}
	now := s.d.Clock.Now()
	var out []domain.Account
	rest := s.withModel(p.Snapshot().AccountList(), model, now)
	for {
		acc := domain.PickAccount(rest, provider, s.d.Machine, p.Name, now)
		if acc == nil {
			return out
		}
		out = append(out, *acc.On(s.d.Machine, s.d.DataDir))
		var next []*domain.Account
		for _, a := range rest {
			if a.ID != acc.ID {
				next = append(next, a)
			}
		}
		rest = next
	}
}

// AdvisorLimit records a limit the advisor met: the model alone, or the
// account's window.
func (s *Service) AdvisorLimit(project, account, model string, resets time.Time, modelOnly bool) {
	if modelOnly {
		s.markModelLimited(account, model, resets)
		return
	}
	if p, err := s.Project(project); err == nil {
		s.markAccountLimited(context.Background(), p, account, resets)
	}
}

// advisorReady is an advisor that can say whether it can answer now.
type advisorReady interface{ Ready() (bool, string) }

// AdvisorStatus says why the advisor of a project cannot answer now, and
// until when; "" when it can.
func (s *Service) AdvisorStatus(project string) string {
	p, err := s.Project(project)
	if err != nil {
		return ""
	}
	adv := p.Ports().Advisor
	if adv == nil {
		return ot(p, "advisor.none")
	}
	r, ok := adv.(advisorReady)
	if !ok {
		return ""
	}
	if ready, _ := r.Ready(); ready {
		return ""
	}
	// The earliest reset of the project's accounts.
	var next time.Time
	for _, a := range p.Snapshot().AccountList() {
		if t := a.Usage.FiveHourResets; t.After(s.d.Clock.Now()) && (next.IsZero() || t.Before(next)) {
			next = t
		}
	}
	if next.IsZero() {
		return ot(p, "advisor.limited")
	}
	return ot(p, "advisor.until", next.Local().Format("15:04"))
}
