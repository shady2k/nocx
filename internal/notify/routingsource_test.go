package notify_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/shady2k/nocx/internal/notify"
)

// ── the router's table swap ────────────────────────────────────────────

func targets(routes []notify.Route) []string {
	out := make([]string, 0, len(routes))
	for _, r := range routes {
		out = append(out, r.Destination.Target)
	}
	return out
}

// A raise resolves against exactly one table, the one live when it began; a
// swap takes effect for raises that begin AFTER it. Both ends of the interval,
// asserted one at a time.
func TestTableSwapTakesEffectForRaisesThatBeginAfterIt(t *testing.T) {
	before := &recordingSink{notified: make(chan struct{}, 1)}
	after := &recordingSink{notified: make(chan struct{}, 1)}
	r, err := notify.NewRouter(notify.Table{
		{Kind: kindA, Trust: notify.TrustProgramRequest}: {
			{Sink: before, Destination: notify.Destination{Target: "before"}},
		},
	}, testLimits())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	r.Raise(context.Background(), event(kindA))
	if before.count() != 1 || after.count() != 0 {
		t.Fatalf("before the swap: before=%d after=%d, want 1/0", before.count(), after.count())
	}

	if swapErr := r.SetTable(notify.Table{
		{Kind: kindA, Trust: notify.TrustProgramRequest}: {
			{Sink: after, Destination: notify.Destination{Target: "after"}},
		},
	}); swapErr != nil {
		t.Fatalf("SetTable: %v", swapErr)
	}

	r.Raise(context.Background(), event(kindA))
	if before.count() != 1 || after.count() != 1 {
		t.Fatalf("after the swap: before=%d after=%d, want 1/1", before.count(), after.count())
	}
}

// The trust-capability bound is a security control, so it runs on EVERY table
// the router is handed and not only on the one it was built with. A table that
// fails is refused WHOLE: nothing of it is applied, and the previous table
// stays live — a partially applied routing table silently grants a route
// nobody chose (D3).
func TestTableRefusedWholeKeepsThePreviousTableLive(t *testing.T) {
	local := &recordingSink{notified: make(chan struct{}, 1)}
	network := &recordingSink{leaves: true, notified: make(chan struct{}, 1)}
	good := notify.Table{
		{Kind: kindA, Trust: notify.TrustHeuristic}: {
			{Sink: local, Destination: notify.Destination{Target: "local"}},
		},
	}
	r, err := notify.NewRouter(good, testLimits())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	bad := notify.Table{
		// The one row that is fine, beside the one that is not: if the swap
		// applied what it could, kindB would start resolving.
		{Kind: kindB, Trust: notify.TrustAttested}: {
			{Sink: local, Destination: notify.Destination{Target: "local"}},
		},
		{Kind: kindA, Trust: notify.TrustHeuristic}: {
			{Sink: network, Destination: notify.Destination{Target: "network"}},
		},
	}
	swapErr := r.SetTable(bad)
	if !errors.Is(swapErr, notify.ErrTrustCapability) {
		t.Fatalf("SetTable(bad) = %v, want ErrTrustCapability", swapErr)
	}
	if got := targets(r.Resolve(kindA, notify.TrustHeuristic, notify.RouteRaise)); len(got) != 1 || got[0] != "local" {
		t.Errorf("after a refused swap kindA resolves to %v, want the previous table's [local]", got)
	}
	if got := r.Resolve(kindB, notify.TrustAttested, notify.RouteRaise); len(got) != 0 {
		t.Errorf("the refused table's acceptable row was applied anyway: kindB resolves to %v", targets(got))
	}
	r.Raise(context.Background(), eventFor(kindA, notify.TrustHeuristic))
	if network.count() != 0 {
		t.Errorf("a refused table's network sink was invoked %d times", network.count())
	}
	if local.count() != 1 {
		t.Errorf("the previous table's sink was invoked %d times, want 1", local.count())
	}
}

// No raise may ever see half of two tables. Raises run in flight across
// repeated swaps and every resolved set must come wholly from one table —
// two routes per table so a mixture is expressible at all.
func TestTableSwapIsAtomicWithRespectToARaise(t *testing.T) {
	sink := &recordingSink{notified: make(chan struct{}, 1)}
	tableFor := func(prefix string) notify.Table {
		return notify.Table{
			{Kind: kindA, Trust: notify.TrustProgramRequest}: {
				{Sink: sink, Destination: notify.Destination{Target: prefix + "-1"}},
				{Sink: sink, Destination: notify.Destination{Target: prefix + "-2"}},
			},
		}
	}
	r, err := notify.NewRouter(tableFor("a"), testLimits())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	var mixed atomic.Bool
	stop := make(chan struct{})
	var swapper, raisers sync.WaitGroup

	swapper.Add(1)
	go func() {
		defer swapper.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			name := "a"
			if i%2 == 1 {
				name = "b"
			}
			if setErr := r.SetTable(tableFor(name)); setErr != nil {
				mixed.Store(true)
				return
			}
		}
	}()

	for w := 0; w < 4; w++ {
		raisers.Add(1)
		go func() {
			defer raisers.Done()
			for i := 0; i < 500; i++ {
				out := r.Raise(context.Background(), event(kindA))
				got := targets(out.Resolved)
				if len(got) != 2 || got[0][0] != got[1][0] {
					mixed.Store(true)
					return
				}
			}
		}()
	}

	// The raisers are the bounded half; the swapper runs until they are done,
	// so the swap is live for every one of their raises.
	raisers.Wait()
	close(stop)
	swapper.Wait()
	if mixed.Load() {
		t.Fatal("a raise resolved against a mixture of two tables")
	}
}

// ── the routing source ─────────────────────────────────────────────────

// allOn / allOff are the two ends of the matrix, as a lookup the source reads.
func allOn(string, string) bool  { return true }
func allOff(string, string) bool { return false }

// shippedDefaults answers from the shipped catalogue's own DefaultOn flags —
// what a user who has ticked nothing gets.
func shippedDefaults(cat *notify.Catalogue) func(string, string) bool {
	on := map[string]bool{}
	for _, p := range cat.Pairs() {
		on[p.SettingKey()] = p.DefaultOn
	}
	return func(kindID, channelID string) bool { return on[notify.RouteSettingKey(kindID, channelID)] }
}

func localSinks() map[string]notify.Sink {
	return map[string]notify.Sink{
		notify.ChannelBanner: &recordingSink{notified: make(chan struct{}, 1)},
		notify.ChannelToast:  &recordingSink{notified: make(chan struct{}, 1)},
	}
}

func newSource(t *testing.T, cfg notify.RoutingConfig) *notify.RoutingSource {
	t.Helper()
	if cfg.Catalogue == nil {
		cfg.Catalogue = notify.DefaultCatalogue()
	}
	if cfg.Sinks == nil {
		cfg.Sinks = localSinks()
	}
	if cfg.Limits == (notify.Limits{}) {
		cfg.Limits = testLimits()
	}
	s, err := notify.NewRoutingSource(cfg)
	if err != nil {
		t.Fatalf("NewRoutingSource: %v", err)
	}
	return s
}

// The default table is exactly the four rows the composition root carried by
// hand before this task: an existing user's notifications do not change.
func TestRoutingSourceBuildsTodaysTableFromTheShippedDefaults(t *testing.T) {
	cat := notify.DefaultCatalogue()
	s := newSource(t, notify.RoutingConfig{Enabled: shippedDefaults(cat)})
	r := s.Router()

	for _, tc := range []struct {
		kind  notify.Kind
		trust notify.Trust
		want  []string
	}{
		{notify.KindProgramNotify, notify.TrustProgramRequest, []string{notify.ChannelBanner, notify.ChannelToast}},
		{notify.KindSessionEnded, notify.TrustAttested, []string{notify.ChannelBanner, notify.ChannelToast}},
		{notify.KindBlockFinished, notify.TrustAttested, nil},
		{notify.KindBell, notify.TrustProgramRequest, nil},
		{notify.KindPaneWorkFinished, notify.TrustHeuristic, nil},
	} {
		got := targets(r.Resolve(tc.kind, tc.trust, notify.RouteRaise))
		if len(got) != len(tc.want) {
			t.Errorf("%s resolves to %v, want %v", tc.kind, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s resolves to %v, want %v", tc.kind, got, tc.want)
				break
			}
		}
	}
}

// Turning every toggle of a kind off makes that kind reach nothing — the
// mechanical half of default-deny (D2).
func TestRoutingSourceWithEveryToggleOffReachesNothing(t *testing.T) {
	s := newSource(t, notify.RoutingConfig{Enabled: allOff})
	r := s.Router()
	for _, p := range notify.DefaultCatalogue().Pairs() {
		for _, tr := range p.Kind.Trusts {
			if got := r.Resolve(p.Kind.Kind, tr, notify.RouteRaise); len(got) != 0 {
				t.Errorf("with every toggle off, %s/%s still resolves to %v", p.Kind.Kind, tr, targets(got))
			}
		}
	}
}

// A change reaches the LIVE router: no restart, no second router, and the
// table the raise before the rebuild resolved against is gone afterwards.
func TestRoutingSourceRebuildReachesTheLiveRouter(t *testing.T) {
	var on atomic.Bool
	s := newSource(t, notify.RoutingConfig{
		Enabled: func(kindID, channelID string) bool {
			return on.Load() && kindID == "bell" && channelID == notify.ChannelToast
		},
	})
	r := s.Router()

	if got := r.Resolve(notify.KindBell, notify.TrustProgramRequest, notify.RouteRaise); len(got) != 0 {
		t.Fatalf("bell resolves to %v before the toggle went on", targets(got))
	}
	on.Store(true)
	if err := s.Rebuild(); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	got := targets(r.Resolve(notify.KindBell, notify.TrustProgramRequest, notify.RouteRaise))
	if len(got) != 1 || got[0] != notify.ChannelToast {
		t.Fatalf("after the rebuild bell resolves to %v, want [toast]", got)
	}
}

// One toggle writes one row per trust class the kind carries: the toggle is a
// (kind, channel) cell and the table is keyed by (kind, trust).
func TestRoutingSourceWritesOneRowPerTrustClass(t *testing.T) {
	cat, err := notify.NewCatalogue(
		[]notify.RoutableKind{{
			Kind: "test.mixed", ID: "mixed", Label: "mixed", Description: "two classes.",
			Trusts:          []notify.Trust{notify.TrustAttested, notify.TrustProgramRequest},
			DefaultChannels: []string{"local"},
		}},
		[]notify.RoutableChannel{{ID: "local", Label: "local", Description: "a local surface"}},
	)
	if err != nil {
		t.Fatalf("NewCatalogue: %v", err)
	}
	s := newSource(t, notify.RoutingConfig{
		Catalogue: cat,
		Sinks:     map[string]notify.Sink{"local": &recordingSink{notified: make(chan struct{}, 1)}},
		Enabled:   allOn,
	})
	r := s.Router()
	for _, tr := range []notify.Trust{notify.TrustAttested, notify.TrustProgramRequest} {
		if got := targets(r.Resolve("test.mixed", tr, notify.RouteRaise)); len(got) != 1 {
			t.Errorf("test.mixed/%s resolves to %v, want one row", tr, got)
		}
	}
}

// ── the trust bound on every rebuild ───────────────────────────────────

// flippingSink answers LeavesMachine from a flag the test moves. It is the
// only way to make a rebuild produce a forbidden row at all — the catalogue
// cannot express one — and that is exactly what makes it the proof that the
// bound is re-checked on EVERY rebuild rather than once at construction.
type flippingSink struct {
	leaves atomic.Bool
	calls  atomic.Int64
}

func (s *flippingSink) Deliver(context.Context, notify.Delivery) error {
	s.calls.Add(1)
	return nil
}

func (s *flippingSink) LeavesMachine() bool { return s.leaves.Load() }

func heuristicCatalogue(t *testing.T) *notify.Catalogue {
	t.Helper()
	cat, err := notify.NewCatalogue(
		[]notify.RoutableKind{{
			Kind: "test.guessed", ID: "guessed", Label: "guessed", Description: "an inference.",
			Trusts:          []notify.Trust{notify.TrustHeuristic},
			DefaultChannels: []string{"local"},
		}},
		[]notify.RoutableChannel{{ID: "local", Label: "local", Description: "a local surface"}},
	)
	if err != nil {
		t.Fatalf("NewCatalogue: %v", err)
	}
	return cat
}

func TestTrustBoundIsRecheckedOnEveryRebuildAndTheRefusalIsReported(t *testing.T) {
	sink := &flippingSink{}
	var refused []error
	s := newSource(t, notify.RoutingConfig{
		Catalogue: heuristicCatalogue(t),
		Sinks:     map[string]notify.Sink{"local": sink},
		Enabled:   allOn,
		OnRefused: func(err error) { refused = append(refused, err) },
	})
	r := s.Router()
	if got := targets(r.Resolve("test.guessed", notify.TrustHeuristic, notify.RouteRaise)); len(got) != 1 {
		t.Fatalf("the initial table resolves to %v, want one local row", got)
	}

	// The surface behind the channel starts leaving the machine. Nothing in
	// the catalogue changed, so only a rebuild that re-validates can notice.
	sink.leaves.Store(true)
	err := s.Rebuild()
	if !errors.Is(err, notify.ErrTrustCapability) {
		t.Fatalf("Rebuild after the sink began leaving the machine = %v, want ErrTrustCapability", err)
	}
	if len(refused) != 1 || !errors.Is(refused[0], notify.ErrTrustCapability) {
		t.Fatalf("the refusal reached OnRefused as %v, want exactly one ErrTrustCapability", refused)
	}
	if got := targets(r.Resolve("test.guessed", notify.TrustHeuristic, notify.RouteRaise)); len(got) != 1 {
		t.Fatalf("after the refusal the kind resolves to %v, want the previous table's one row", got)
	}
}

// ── construction refusals ──────────────────────────────────────────────

func TestRoutingSourceRefusesAnIncompleteOrDisagreeingBinding(t *testing.T) {
	cat := notify.DefaultCatalogue()
	network := &flippingSink{}
	network.leaves.Store(true)

	cases := map[string]notify.RoutingConfig{
		"no catalogue": {Catalogue: nil, Sinks: localSinks(), Enabled: allOn, Limits: testLimits()},
		"no enabled lookup": {
			Catalogue: cat, Sinks: localSinks(), Limits: testLimits(),
		},
		"a channel with no sink": {
			Catalogue: cat, Enabled: allOn, Limits: testLimits(),
			Sinks: map[string]notify.Sink{notify.ChannelBanner: &recordingSink{notified: make(chan struct{}, 1)}},
		},
		"a sink for a channel nobody declared": {
			Catalogue: cat, Enabled: allOn, Limits: testLimits(),
			Sinks: map[string]notify.Sink{
				notify.ChannelBanner: &recordingSink{notified: make(chan struct{}, 1)},
				notify.ChannelToast:  &recordingSink{notified: make(chan struct{}, 1)},
				"smoke-signal":       &recordingSink{notified: make(chan struct{}, 1)},
			},
		},
		"a sink that disagrees with its channel about leaving the machine": {
			Catalogue: cat, Enabled: allOn, Limits: testLimits(),
			Sinks: map[string]notify.Sink{
				notify.ChannelBanner: &recordingSink{notified: make(chan struct{}, 1)},
				notify.ChannelToast:  network,
			},
		},
		"invalid limits": {
			Catalogue: cat, Sinks: localSinks(), Enabled: allOn,
			Limits: notify.Limits{MaxInFlight: 0},
		},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := notify.NewRoutingSource(cfg); err == nil {
				t.Fatalf("NewRoutingSource accepted %s", name)
			}
		})
	}
}

// The sinks map is copied at construction: a caller that keeps the map cannot
// redirect a channel afterwards, which would put "where" outside the router.
func TestRoutingSourceCopiesItsBindings(t *testing.T) {
	sinks := localSinks()
	s := newSource(t, notify.RoutingConfig{Sinks: sinks, Enabled: allOn})
	intruder := &recordingSink{notified: make(chan struct{}, 1)}
	sinks[notify.ChannelBanner] = intruder
	if err := s.Rebuild(); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	s.Router().Raise(context.Background(), event(notify.KindProgramNotify))
	if intruder.count() != 0 {
		t.Error("a sink swapped into the caller's map after construction was invoked")
	}
}
