package transport

// The presence count, over the real socket.
//
// The vault's side of D9 is asserted in internal/vault against a count
// somebody hands it. This is the half that has to be true for that to mean
// anything: a real client attaching and a real client going away produce
// exactly those numbers, on the real WebSocket.

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/waittest"
)

// recordingPresence records every count it is told, in order.
type recordingPresence struct {
	mu     sync.Mutex
	counts []int
}

func (r *recordingPresence) ClientsAttached(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counts = append(r.counts, n)
}

func (r *recordingPresence) last() (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.counts) == 0 {
		return 0, false
	}
	return r.counts[len(r.counts)-1], true
}

func (r *recordingPresence) seen() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.counts...)
}

func presenceServer(t *testing.T) (*WSServer, *recordingPresence) {
	t.Helper()
	rec := &recordingPresence{}
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithVaultLifecycle(newFakeVaultLifecycle()),
		WithClientPresence(rec))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	return ws, rec
}

func waitForCount(t *testing.T, rec *recordingPresence, want int) {
	t.Helper()
	waittest.WaitForDetail(t, fmt.Sprintf("the presence observer to be told %d", want),
		func() string {
			n, ok := rec.last()
			if !ok {
				return "the observer was never called at all"
			}
			return fmt.Sprintf("last count %d, want %d; sequence %v", n, want, rec.seen())
		},
		func() bool {
			n, ok := rec.last()
			return ok && n == want
		})
}

// The whole seam in one pass: one, two, one, nobody.
func TestClientPresence_CountsAttachAndDetach(t *testing.T) {
	ws, rec := presenceServer(t)

	first := connectWS(t, ws)
	waitForCount(t, rec, 1)

	second := connectWS(t, ws)
	waitForCount(t, rec, 2)

	_ = second.Close()
	waitForCount(t, rec, 1)

	_ = first.Close()
	waitForCount(t, rec, 0)
}

// The count reported on a detach is taken AFTER the connection has left the
// set. Reporting it before would say 1 for the last client's departure, the
// vault would never seal, and every other check here would still pass.
func TestClientPresence_TheLastDetachReportsZeroNotOne(t *testing.T) {
	ws, rec := presenceServer(t)

	conn := connectWS(t, ws)
	waitForCount(t, rec, 1)
	_ = conn.Close()

	waitForCount(t, rec, 0)
	for _, n := range rec.seen() {
		if n < 0 {
			t.Fatalf("a negative count reached the observer: %v", rec.seen())
		}
	}
}

// A connection that dies without a close handshake is a detach like any
// other — a window that crashed, a machine that slept, a socket a proxy
// dropped. If only the polite path reported, the vault would stay unsealed
// after exactly the departures nobody chose.
func TestClientPresence_AnAbruptDeathIsADetach(t *testing.T) {
	ws, rec := presenceServer(t)

	conn := connectWS(t, ws)
	waitForCount(t, rec, 1)

	// No close frame: rip the TCP connection out from under the server.
	_ = conn.UnderlyingConn().Close()

	waitForCount(t, rec, 0)
}

// The final delivery is the truth under concurrency. Two goroutines that
// both changed the set could otherwise deliver in the opposite order and
// leave the observer believing a stale value — which for the last detach
// would mean a vault that never seals.
func TestClientPresence_TheFinalCountIsTheTruthUnderConcurrency(t *testing.T) {
	ws, rec := presenceServer(t)

	const clients = 6
	var wg sync.WaitGroup
	conns := make([]*websocket.Conn, clients)
	for i := range clients {
		conns[i] = connectWS(t, ws)
	}
	waitForCount(t, rec, clients)

	for i := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = conns[i].Close()
		}()
	}
	wg.Wait()

	waitForCount(t, rec, 0)
}

// A transport with no observer must not panic on either edge — that is what
// every test in this package that is not about presence relies on.
func TestClientPresence_NoObserverIsHarmless(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithVaultLifecycle(newFakeVaultLifecycle()))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "vault.status", nil, 1)
	if resp.Error != nil {
		t.Fatalf("vault.status: %s", resp.Error.Message)
	}
	_ = conn.Close()
}

// A RECONNECT LOOKS EXACTLY LIKE A DEPARTURE FROM HERE, and it must: the
// transport reports what it can see, and it cannot see intent. A socket that
// drops and comes back produces 1, 0, 1 — the same three numbers as a person
// leaving and a different person arriving.
//
// This is the fact that forced the vault's departure window (nocx-58q7d).
// While the seal fired on the bare zero, every reconnect and every window
// reload shut the vault under the person still using it, and a modal covered
// the whole application on their next click. The answer is not to teach this
// file to guess — that would be the transport owning the vault's policy,
// against AD-8 — but to leave the count exact and let the vault decide what a
// zero means.
func TestClientPresence_AReconnectIsReportedAsADetachAndAnAttach(t *testing.T) {
	ws, rec := presenceServer(t)

	conn := connectWS(t, ws)
	waitForCount(t, rec, 1)

	// The socket drops the way a real one does — no close frame.
	_ = conn.UnderlyingConn().Close()
	waitForCount(t, rec, 0)

	// And the renderer comes back (AD-9).
	again := connectWS(t, ws)
	t.Cleanup(func() { _ = again.Close() })
	waitForCount(t, rec, 1)

	// Nothing here interpreted any of it: the counts are the raw sequence
	// and no seal, grace or departure appears in this package.
	if got := rec.seen(); len(got) < 3 {
		t.Fatalf("the observer saw %v, want at least the attach, the drop and the return", got)
	}
}
