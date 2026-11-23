package domain

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Capacity splits agent time between lanes. Shares are percentages.
type Capacity struct {
	Goal         int `json:"goal"`
	Bugs         int `json:"bugs"`
	Improvements int `json:"improvements"`
	// SelfPerWeek is the self-improvement budget in items per week.
	SelfPerWeek int `json:"self_per_week"`
}

// DefaultCapacity is 80/15/5 and one self-improvement item a week.
var DefaultCapacity = Capacity{Goal: 80, Bugs: 15, Improvements: 5, SelfPerWeek: 1}

// Validate checks shares add up to 100.
func (c Capacity) Validate() error {
	if c.Goal < 0 || c.Bugs < 0 || c.Improvements < 0 {
		return fmt.Errorf("capacity shares cannot be negative")
	}
	if s := c.Goal + c.Bugs + c.Improvements; s != 100 {
		return fmt.Errorf("capacity shares add up to %d%%, they must add up to 100%%", s)
	}
	return nil
}

// Share returns the share for a lane.
func (c Capacity) Share(l Lane) int {
	switch l {
	case LaneGoal:
		return c.Goal
	case LaneBugs:
		return c.Bugs
	case LaneImprovement:
		return c.Improvements
	}
	return 0
}

// ParsePercent parses "80%" or "80".
func ParsePercent(s string) (int, error) {
	s = strings.TrimSuffix(strings.TrimSpace(s), "%")
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 100 {
		return 0, fmt.Errorf("invalid percentage %q", s)
	}
	return n, nil
}

// LaneUsage counts recent claims per lane (the scheduler's window).
type LaneUsage map[Lane]int

// PickLane chooses the lane with the largest deficit against its target
// share among lanes that have candidates. P0 bugs always go first. The self
// improvement lane is used only when its weekly budget is not spent and
// nothing else is available, or when its item is approved and due.
func PickLane(c Capacity, usage LaneUsage, available map[Lane]bool, selfUsedThisWeek int, hasP0Bug bool) (Lane, bool) {
	if hasP0Bug && available[LaneBugs] {
		return LaneBugs, true
	}
	total := 0
	for _, l := range []Lane{LaneGoal, LaneBugs, LaneImprovement} {
		total += usage[l]
	}
	best := Lane("")
	bestDeficit := -1e18
	for _, l := range []Lane{LaneGoal, LaneBugs, LaneImprovement} {
		if !available[l] || c.Share(l) == 0 {
			continue
		}
		actual := 0.0
		if total > 0 {
			actual = float64(usage[l]) / float64(total) * 100
		}
		deficit := float64(c.Share(l)) - actual
		if deficit > bestDeficit {
			best, bestDeficit = l, deficit
		}
	}
	if best == "" {
		// Lanes with a zero share still run when nothing else can.
		for _, l := range []Lane{LaneGoal, LaneBugs, LaneImprovement} {
			if available[l] {
				return l, true
			}
		}
	}
	if best != "" {
		return best, true
	}
	if available[LaneSelf] && selfUsedThisWeek < c.SelfPerWeek {
		return LaneSelf, true
	}
	return "", false
}

// Goal is the long-term result with a measurable end.
type Goal struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	DoneWhen   string    `json:"done_when"`
	Inventory  string    `json:"inventory,omitempty"` // "screens docs/prototipo-mobile"
	Active     bool      `json:"active"`
	CreatedAt  time.Time `json:"created_at,omitzero"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
	BudgetPct  int       `json:"budget_pct,omitempty"` // max share of the weekly account window
}

// GoalProgress is counted, not felt.
type GoalProgress struct {
	Total      int       `json:"total"`
	Done       int       `json:"done"`
	PerDay     float64   `json:"per_day"`
	Forecast   time.Time `json:"forecast,omitzero"`
	Percentage int       `json:"percentage"`
}

// ComputeProgress counts integrated items of a goal and projects the end
// from the pace over the last window.
func ComputeProgress(items []*Item, goalID string, now time.Time, window time.Duration) GoalProgress {
	var p GoalProgress
	recent := 0
	for _, it := range items {
		if it.GoalID != goalID {
			continue
		}
		if it.Status == StatusClosed && it.ClosedReason != "done_by" {
			continue // obsolete or duplicate items leave the inventory
		}
		p.Total++
		if it.Status == StatusIntegrated || (it.Status == StatusClosed && it.ClosedReason == "done_by") {
			p.Done++
			if !it.IntegratedAt.IsZero() && now.Sub(it.IntegratedAt) <= window {
				recent++
			}
		}
	}
	if p.Total > 0 {
		p.Percentage = p.Done * 100 / p.Total
	}
	days := window.Hours() / 24
	if days > 0 {
		p.PerDay = float64(recent) / days
	}
	if left := p.Total - p.Done; left > 0 && p.PerDay > 0 {
		p.Forecast = now.Add(time.Duration(float64(left)/p.PerDay*24) * time.Hour)
	}
	return p
}

// Focus is a temporary priority inside the goal.
type Focus struct {
	Name      string    `json:"name"`
	Area      string    `json:"area,omitempty"`
	Items     []string  `json:"items,omitempty"`
	Until     time.Time `json:"until,omitzero"` // zero means until done
	Share     int       `json:"share"`          // share of capacity, default 80
	SetBy     string    `json:"set_by"`
	SetAt     time.Time `json:"set_at,omitzero"`
	Cancelled bool      `json:"cancelled,omitempty"`
}

// Includes reports whether an item is part of the focus.
func (f *Focus) Includes(it *Item) bool {
	if f == nil {
		return false
	}
	for _, id := range f.Items {
		if id == it.ID {
			return true
		}
	}
	return f.Area != "" && AreaName(f.Area) == AreaName(it.Area)
}

// Active reports whether the focus still applies.
func (f *Focus) Active(items []*Item, now time.Time) bool {
	if f == nil || f.Cancelled {
		return false
	}
	if !f.Until.IsZero() {
		return now.Before(f.Until)
	}
	// "until done": active while any focus item is not finished.
	for _, it := range items {
		if f.Includes(it) && !it.Status.Terminal() {
			return true
		}
	}
	return false
}

// FocusEffect describes what setting a focus does, shown before applying.
type FocusEffect struct {
	FocusItems      int      `json:"focus_items"`
	ParkedElsewhere []string `json:"parked_elsewhere"` // in progress elsewhere: finish current step and wait
	Share           int      `json:"share"`
}

// PreviewFocus computes the effect of a new focus.
func PreviewFocus(f *Focus, items []*Item) FocusEffect {
	e := FocusEffect{Share: f.Share}
	for _, it := range items {
		if it.Status.Terminal() {
			continue
		}
		if f.Includes(it) {
			e.FocusItems++
		} else if it.Status == StatusInProgress && it.Lane == LaneGoal {
			e.ParkedElsewhere = append(e.ParkedElsewhere, it.ID)
		}
	}
	sort.Strings(e.ParkedElsewhere)
	return e
}

// ParseUntil parses "until done", a date "2026-12-01" or a duration "3d".
func ParseUntil(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "", "done", "until done", "até terminar", "ate terminar":
		return time.Time{}, nil
	}
	if d, err := ParseDuration(s); err == nil {
		return now.Add(d), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid focus deadline %q: use a date (2026-12-01), a duration (3d) or \"done\"", s)
}

// ParseDuration extends time.ParseDuration with days ("14d") and weeks ("2w").
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	mult := time.Duration(0)
	switch s[len(s)-1] {
	case 'd':
		mult = 24 * time.Hour
	case 'w':
		mult = 7 * 24 * time.Hour
	}
	if mult > 0 {
		n, err := strconv.ParseFloat(s[:len(s)-1], 64)
		if err != nil || !(n >= 0) || n*float64(mult) >= math.MaxInt64 { // also rejects NaN, Inf and overflow
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(n * float64(mult)), nil
	}
	return time.ParseDuration(s)
}

// ParseSizeRange parses "2h-6h" into hours.
func ParseSizeRange(s string) (min, max float64, err error) {
	a, b, ok := strings.Cut(strings.TrimSpace(s), "-")
	if !ok {
		return 0, 0, fmt.Errorf("invalid size range %q: use 2h-6h", s)
	}
	da, err1 := time.ParseDuration(strings.TrimSpace(a))
	db, err2 := time.ParseDuration(strings.TrimSpace(b))
	if err1 != nil || err2 != nil || da <= 0 || db < da {
		return 0, 0, fmt.Errorf("invalid size range %q: use 2h-6h", s)
	}
	return da.Hours(), db.Hours(), nil
}

// SizeVerdict tells the planner what to do with an estimate.
type SizeVerdict string

const (
	SizeOK      SizeVerdict = "ok"
	SizeTweak   SizeVerdict = "tweak" // smaller than the target: becomes a tweak
	SizeSplit   SizeVerdict = "split" // larger: split it
	SizeUnknown SizeVerdict = "unknown"
)

// JudgeSize compares an estimate in hours with the target range.
func JudgeSize(hours, min, max float64) SizeVerdict {
	switch {
	case hours <= 0:
		return SizeUnknown
	case hours < min/2:
		return SizeTweak
	case hours > max*1.5:
		return SizeSplit
	}
	return SizeOK
}
