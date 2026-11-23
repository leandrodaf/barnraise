// Package testkit holds in-memory fakes of the ports the application uses,
// and helpers that build real temporary git repositories. Fakes follow the
// same semantics as the real adapters; the contract suites in
// testkit/contract run against both so they cannot drift apart.
package testkit

import (
	"fmt"
	"sync"
	"time"
)

// Epoch is the default start of a manual clock: a Monday morning, UTC.
var Epoch = time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)

// Clock is a manual clock (ports.Clock). It only moves with Advance or Set.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock returns a clock at Epoch, or at start when given.
func NewClock(start ...time.Time) *Clock {
	c := &Clock{now: Epoch}
	if len(start) > 0 {
		c.now = start[0]
	}
	return c
}

// Now implements ports.Clock.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// Set moves the clock to t.
func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

// IDs is a sequential id generator (ports.IDGen): prefix plus a zero-padded
// counter, so ids sort in creation order.
type IDs struct {
	mu     sync.Mutex
	prefix string
	n      int
}

// NewIDs returns a generator; machines in the same test use different prefixes.
func NewIDs(prefix string) *IDs { return &IDs{prefix: prefix} }

// NewID implements ports.IDGen.
func (g *IDs) NewID() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	return fmt.Sprintf("%s%08d", g.prefix, g.n)
}

// Count returns how many ids were handed out.
func (g *IDs) Count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.n
}
