package capability

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/operation/catalog"
	"github.com/shady2k/nocx/internal/transport/control"
)

// THE QUARANTINE IS CHECKED ON BOTH SIDES OF THE WAIT (nocx-3an1s.2.16).
//
// The window the quarantine exists for is the wait itself: a restore holds the
// store set exclusively, its rollback fails, and it quarantines the stores it
// was rewriting before it releases the permit — the permit is what keeps
// readers out while the set holds two generations. A caller that queued behind
// that barrier passed the first check while the quarantine did not exist yet,
// and the permit it is finally granted has to be checked again, or the save is
// admitted to, and acknowledged into, the generation the quarantine hides.
//
// The order is forced, not timed. admission under test wraps a gate the test
// drives by hand: the test knows the caller is inside the wait because the
// gate says so, quarantines while it is parked there, and only then lets the
// grant through.
func TestStoreGuard_QuarantineDuringTheWaitRefusesTheGrantedCaller(t *testing.T) {
	guard := NewStoreGuard(catalog.Declarations(), 3*time.Second)
	gate := newDrivenGate()
	admission := &guardedAdmission{
		guard:  guard,
		stores: []string{StoreConfig},
		inner:  gate,
		name:   "store:" + StoreConfig + "+shared",
	}

	type answer struct {
		permit control.Permit
		rej    *control.Rejection
	}
	answered := make(chan answer, 1)
	go func() {
		permit, rej := admission.TryAcquire(context.Background())
		answered <- answer{permit: permit, rej: rej}
	}()

	// The caller is past the first check and inside the wait.
	<-gate.entered

	// The restore's last two acts, in their real order: the rollback failed, so
	// the stores are quarantined while the exclusive permit is still held, and
	// only then is the permit released — which is this gate opening.
	guard.Quarantine([]string{StoreConfig}, "the rollback failed")
	close(gate.open)

	got := <-answered
	if got.permit != nil {
		got.permit.Release()
	}
	if got.rej == nil {
		t.Fatal("a caller granted after the quarantine was admitted to the store")
	}
	if got.rej.Kind != control.RejectionUnavailable {
		t.Fatalf("rejection = %q, want the store's unavailable answer", got.rej.Kind)
	}
	if !strings.Contains(got.rej.Reason, "Restart nocx") {
		t.Fatalf("the refusal does not say what ends it: %s", got.rej.Reason)
	}
	if !strings.Contains(got.rej.Reason, "settings and connection") {
		t.Fatalf("the refusal does not name the store: %s", got.rej.Reason)
	}
	if released := gate.released(); released != 1 {
		t.Fatalf("the permit granted mid-wait was released %d time(s), want once: a held shared "+
			"permit keeps a later restore from ever draining the set it must rewrite", released)
	}

	// And a caller arriving after the quarantine is refused before it takes
	// capacity at all, so the gate is never entered a second time.
	if _, rej := admission.TryAcquire(context.Background()); rej == nil ||
		rej.Kind != control.RejectionUnavailable {
		t.Fatalf("a caller arriving after the quarantine answered %+v, want unavailable", rej)
	}
	if entered := gate.entries(); entered != 1 {
		t.Fatalf("the gate was entered %d time(s), want once: a quarantined store is refused "+
			"before it waits", entered)
	}
}

// drivenGate is an admission a test opens by hand: TryAcquire announces that
// it is waiting and parks until the test lets the grant through.
type drivenGate struct {
	entered chan struct{}
	open    chan struct{}

	mu        sync.Mutex
	taken     int
	givenBack int
}

func newDrivenGate() *drivenGate {
	return &drivenGate{entered: make(chan struct{}, 1), open: make(chan struct{})}
}

func (g *drivenGate) Name() string { return "test:driven-gate" }

func (g *drivenGate) TryAcquire(context.Context) (control.Permit, *control.Rejection) {
	g.mu.Lock()
	g.taken++
	g.mu.Unlock()
	g.entered <- struct{}{}
	<-g.open
	return &drivenPermit{gate: g}, nil
}

func (g *drivenGate) released() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.givenBack
}

// entries is how many callers have passed the first check into the wait.
func (g *drivenGate) entries() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.taken
}

type drivenPermit struct {
	gate *drivenGate
	once sync.Once
}

func (p *drivenPermit) Release() {
	p.once.Do(func() {
		p.gate.mu.Lock()
		p.gate.givenBack++
		p.gate.mu.Unlock()
	})
}
