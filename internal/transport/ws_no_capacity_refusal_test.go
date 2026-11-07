package transport

// NOTHING A PERSON DOES IS ANSWERED WITH A CAPACITY ERROR (ADR-8eej3e66 §7,
// stage 8).
//
// The migration's last stage removes every bound that answers a request with
// "the control plane is busy": the ordinary execution lane, the per-operation
// queues that refused at submit time, and the capacity-one semaphores on the
// probe and the ask stream. What bounds accepted work is what was SENT (the
// per-connection credit window, credit.go) and how long a call may WAIT (its
// declared turn, catalog.Declaration.Turn); the only remaining capacity
// outcome is disconnecting a peer that exceeds its window.
//
// These tests state the guarantee each old refusal was standing for, over the
// wire, at the place a person meets it.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/waittest"
)

// blockingProbes is a Prober that holds every call until release, so a test
// can genuinely fill whatever used to bound execution. It reports when want
// probes are in flight.
type blockingProbes struct {
	mu      sync.Mutex
	started int
	want    int
	ready   chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingProbes(want int) *blockingProbes {
	return &blockingProbes{want: want, ready: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockingProbes) ProbeWithResult(ctx context.Context, _ string, _ *ssh.ConnectConfig) (string, error) {
	b.mu.Lock()
	b.started++
	if b.started >= b.want {
		b.once.Do(func() { close(b.ready) })
	}
	b.mu.Unlock()
	select {
	case <-b.release:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return "fp", nil
}

func (b *blockingProbes) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.started
}

func (b *blockingProbes) free() {
	select {
	case <-b.release:
	default:
		close(b.release)
	}
}

// blockedProbes holds n probes in flight and returns once they are all
// running, so the test's premise is an observed state rather than a duration.
func blockedProbes(t *testing.T, conn *websocket.Conn, prober *blockingProbes, n int) {
	t.Helper()
	for i := range n {
		sendControl(t, conn, "connections.test", map[string]any{"profileId": "ssh:test:1"}, i+1)
	}
	waittest.WaitFor(t, "every probe in flight", func() bool { return prober.count() >= n })
}

// RemovedCapacityCode is the -32004 the capacity refusal used to answer with.
// It must never appear on the wire again (ADR-8eej3e66 stage 8): the code is
// kept in the tests that assert its absence, and nowhere in the product.
const RemovedCapacityCode = -32004

// ── A second Test press is admitted, not refused ──────────────────────────

func TestConnectionsTest_SecondProbeIsAdmittedRatherThanRefused(t *testing.T) {
	prober := newBlockingProbes(2)
	t.Cleanup(prober.free)
	srv := newProbeTestServerWithProber(t, prober, probeResolver())

	first := connectWS(t, srv)
	t.Cleanup(func() { _ = first.Close() })
	second := connectWS(t, srv)
	t.Cleanup(func() { _ = second.Close() })

	sendControl(t, first, "connections.test", map[string]any{"profileId": "ssh:test:1"}, 1)
	sendControl(t, second, "connections.test", map[string]any{"profileId": "ssh:test:2"}, 2)

	// Both probes reach the prober: the second one is not answered with a
	// refusal while the first holds whatever used to bound execution.
	waittest.WaitFor(t, "both probes admitted", func() bool { return prober.count() >= 2 })

	prober.free()
	for _, c := range []struct {
		conn *websocket.Conn
		id   int
	}{{first, 1}, {second, 2}} {
		raw := readResponseFor(t, c.conn, c.id, 5*time.Second)
		if code := errorCodeOf(t, raw); code != 0 {
			t.Fatalf("probe %d answered with error %d: %s", c.id, code, raw)
		}
	}
}

// ── operation.outcome answers while other work holds every permit ─────────

func TestOperationOutcome_AnswersWhileProbesHoldEveryPermit(t *testing.T) {
	prober := newBlockingProbes(8)
	t.Cleanup(prober.free)
	srv := newProbeTestServerWithProber(t, prober, probeResolver())

	holder := connectWS(t, srv)
	t.Cleanup(func() { _ = holder.Close() })

	// Eight blocked probes is the server's whole execution bound before this
	// stage, and one connection's whole credit window: exactly the state in
	// which operation.outcome used to be refused instead of answered.
	blockedProbes(t, holder, prober, 8)

	// "what became of the mutation" is asked from a second connection: the
	// first one's window is deliberately full.
	other := connectWS(t, srv)
	t.Cleanup(func() { _ = other.Close() })
	raw := jsonrpcCall(t, other, "operation.outcome", map[string]any{"mutation": "unknown-identity"})
	if code := errorCodeOf(t, raw); code != 0 {
		t.Fatalf("operation.outcome was refused while work held every permit: %d: %s", code, raw)
	}
}

// ── vault.unseal answers while every permit is held (nocx-0dvf2) ──────────

func TestVaultUnseal_AnswersWhileOtherCallsHoldEveryPermit(t *testing.T) {
	prober := newBlockingProbes(8)
	t.Cleanup(prober.free)
	h := newInventoryHarness(t,
		WithProber(prober),
		WithProfileResolver(probeResolver()),
	)
	h.setupAndUnseal()

	// A second connection carries the blocking work, so the vault
	// connection keeps its own credit for the unseal it must send.
	holder := connectWS(t, h.ws)
	t.Cleanup(func() { _ = holder.Close() })
	blockedProbes(t, holder, prober, 8)

	raw := jsonrpcCallWithID(t, h.conn, "vault.unseal", map[string]any{
		"means": "passphrase", "secret": "test",
	}, 79)
	if code := errorCodeOf(t, raw); code == RemovedCapacityCode {
		t.Fatalf("vault.unseal was refused while every permit was held: %s", raw)
	}
}
