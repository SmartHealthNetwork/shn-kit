// multi.go — Multi fans one run's stamp out to several relays and drains them
// all. The Kit runs its existing gateway child and, under the Java trio, a
// second provider-data gateway child; each process has its own observer hub,
// so each gets its own Relay — but the scenario runner brackets a row ONCE,
// regardless of how many children emitted into the timeline. Multi is that
// single bracket.
package relay

import (
	"context"
	"errors"
	"sort"
)

// Stamper is the seam the scenario runner brackets a row with: stamp the run
// identity on, drain every observer event emitted so far, clear the stamp. A
// single Relay satisfies it; Multi fans it out to several.
type Stamper interface {
	SetStamp(Stamp)
	ClearStamp()
	Drain(context.Context) error
}

// WindowStamper adds atomic lifecycle boundaries without breaking Stamper callers.
type WindowStamper interface {
	Stamper
	Begin(Stamp, func(error))
	End(bool, func(error))
}

var (
	_ WindowStamper = (*Relay)(nil)
	_ WindowStamper = (*Multi)(nil)
	_ Stamper       = (*Relay)(nil)
	_ Stamper       = (*Multi)(nil)
)

// Multi applies every Stamper call to each member relay. Drain drains every
// member and joins their errors: a run is caught up only when every child's
// hub is, so a lagging member fails the drain loudly rather than letting the
// runner close its window early.
type Multi struct{ members []*Relay }

// NewMulti builds a Multi over rs. nil members are skipped, so a caller can
// pass an optional relay unconditionally.
func NewMulti(rs ...*Relay) *Multi {
	m := &Multi{}
	seen := make(map[*Relay]bool)
	for _, r := range rs {
		if r != nil && !seen[r] {
			seen[r] = true
			m.members = append(m.members, r)
		}
	}
	sort.Slice(m.members, func(i, j int) bool { return m.members[i].order < m.members[j].order })
	return m
}

// SetStamp sets the identity on every member.
func (m *Multi) SetStamp(s Stamp) {
	for _, r := range m.members {
		r.SetStamp(s)
	}
}

// ClearStamp clears the identity on every member.
func (m *Multi) ClearStamp() {
	for _, r := range m.members {
		r.ClearStamp()
	}
}

// Drain drains every member; the joined error names each member that did not
// catch up within ctx.
func (m *Multi) Drain(ctx context.Context) error {
	var errs []error
	for _, r := range m.members {
		if err := r.Drain(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *Multi) lock() {
	for _, r := range m.members {
		r.mu.Lock()
	}
}
func (m *Multi) unlock() {
	for i := len(m.members) - 1; i >= 0; i-- {
		m.members[i].mu.Unlock()
	}
}
func (m *Multi) Begin(s Stamp, publish func(error)) {
	m.lock()
	defer m.unlock()
	var errs []error
	for _, r := range m.members {
		if err := r.beginLocked(s); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		for _, r := range m.members {
			r.stamp = Stamp{}
		}
	}
	publish(errors.Join(errs...))
}
func (m *Multi) End(uncertain bool, publish func(error)) {
	m.lock()
	defer m.unlock()
	var errs []error
	for _, r := range m.members {
		errs = append(errs, r.endLocked(uncertain))
	}
	publish(errors.Join(errs...))
}
