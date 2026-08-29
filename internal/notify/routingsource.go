package notify

import (
	"errors"
	"fmt"
)

// RoutingSource builds the router's table from the catalogue and the user's
// per-(kind, channel) choices, and swaps each rebuild into the LIVE router.
//
// It is the join between two things that already exist and must not grow
// second copies of each other (AD-8): the catalogue names what can be routed
// where, and the settings registry owns the choices, their validation, their
// persistence and their change notification. This type holds neither. It reads
// the catalogue, asks a lookup whether a cell is on, and hands the result to
// the router — which is still the only holder of "where" (ADR-0047 §2.3).
//
// It owns the router's CONSTRUCTION because the two cannot be built
// independently: a router needs a table and a source needs a router to swap
// into. Building the router here makes the initial table the same code path as
// every later one, so there is no first table that skipped a check.
type RoutingSource struct {
	catalogue *Catalogue
	sinks     map[string]Sink
	enabled   func(kindID, channelID string) bool
	onRefused func(error)
	router    *Router
}

// RoutingConfig is everything a RoutingSource needs. Every field except
// OnRefused is required; NewRoutingSource refuses an incomplete one rather
// than filling in a default, because each missing field would silently mean
// "route nothing" or "route somewhere nobody chose".
type RoutingConfig struct {
	// Catalogue names the routable kinds and channels.
	Catalogue *Catalogue

	// Sinks binds one Sink per catalogue channel, keyed by channel id. It
	// must cover the catalogue exactly: a channel with no sink is a toggle
	// that could never work, and a sink for a channel nobody declared is a
	// destination outside the catalogue.
	Sinks map[string]Sink

	// Enabled answers whether one (kind, channel) cell is on. The
	// composition root satisfies it from the settings registry; nothing here
	// caches the answer, so a rebuild always reads the current value.
	Enabled func(kindID, channelID string) bool

	// Limits are the router's global admission bounds.
	Limits Limits

	// OnRefused is called when a rebuild produced a table the router refused.
	// It exists because a refusal has to be visible in the PRODUCT and not
	// only in a log (AGENTS.md): the previous routing stays live, so the
	// user's change appears to have been accepted while nothing about
	// delivery changed. Optional; a nil one only means the refusal is
	// returned to the caller of Rebuild and logged by whoever wired this.
	OnRefused func(error)
}

// NewRoutingSource validates the binding, builds the initial table and the
// router that carries it.
func NewRoutingSource(cfg RoutingConfig) (*RoutingSource, error) {
	if cfg.Catalogue == nil {
		return nil, errors.New("notify: routing source needs a catalogue")
	}
	if cfg.Enabled == nil {
		return nil, errors.New("notify: routing source needs an enabled lookup")
	}
	// The binding must cover the catalogue exactly, and each sink must AGREE
	// with its channel about leaving the machine. The catalogue declares that
	// fact so it can withhold a forbidden pair before any sink exists; the
	// sink declares it because the router enforces the bound with it. Two
	// declarations of one fact is what AD-8 is about, so disagreement is a
	// construction error rather than something to discover at delivery.
	sinks := make(map[string]Sink, len(cfg.Sinks))
	for _, ch := range cfg.Catalogue.Channels() {
		sink, bound := cfg.Sinks[ch.ID]
		if !bound || sink == nil {
			return nil, fmt.Errorf("notify: channel %q has no sink bound", ch.ID)
		}
		if sink.LeavesMachine() != ch.LeavesMachine {
			return nil, fmt.Errorf("notify: channel %q declares leavesMachine=%t and its sink declares %t",
				ch.ID, ch.LeavesMachine, sink.LeavesMachine())
		}
		sinks[ch.ID] = sink
	}
	if len(cfg.Sinks) != len(sinks) {
		for id := range cfg.Sinks {
			if _, declared := sinks[id]; !declared {
				return nil, fmt.Errorf("notify: a sink is bound to %q, which the catalogue does not declare", id)
			}
		}
	}

	s := &RoutingSource{
		catalogue: cfg.Catalogue,
		sinks:     sinks,
		enabled:   cfg.Enabled,
		onRefused: cfg.OnRefused,
	}
	router, err := NewRouter(s.build(), cfg.Limits)
	if err != nil {
		return nil, err
	}
	s.router = router
	return s, nil
}

// Router is the live router this source swaps tables into.
func (s *RoutingSource) Router() *Router { return s.router }

// Rebuild reads the choices again, builds a table and swaps it in. The router
// re-runs the trust-capability validation on it; a table that fails is refused
// whole, the previous one stays live, and the failure is both returned and
// handed to OnRefused.
func (s *RoutingSource) Rebuild() error {
	if err := s.router.SetTable(s.build()); err != nil {
		if s.onRefused != nil {
			s.onRefused(err)
		}
		return err
	}
	return nil
}

// build turns the catalogue and the current choices into a table.
//
// Default-deny is mechanical here rather than remembered: the table is built
// from the cells that are ON, so a kind with every cell off contributes no
// key at all, and a kind the catalogue does not list has no cell to read. A
// cell the trust bound forbids was never offered, so it cannot be read either
// (D2, D3).
//
// One cell writes one row per trust class the kind carries, because a cell is
// (kind, channel) and a table key is (kind, trust).
func (s *RoutingSource) build() Table {
	table := Table{}
	for _, p := range s.catalogue.Pairs() {
		if !s.enabled(p.Kind.ID, p.Channel.ID) {
			continue
		}
		route := Route{
			Sink: s.sinks[p.Channel.ID],
			// The channel id IS the resolved target: one word for one
			// surface, the one the router carries into every outcome and a
			// failed delivery repeats to say which channel failed.
			Destination: Destination{Target: p.Channel.ID},
		}
		for _, trust := range p.Kind.Trusts {
			key := Key{Kind: p.Kind.Kind, Trust: trust}
			table[key] = append(table[key], route)
		}
	}
	return table
}
