package client_test

// Acceptance order is the downlink's contract (nocx-2v80t.3.25). The pane's
// own transport and an ssh child's listener are two observing kernels over
// ONE downlink (nocx-2v80t.3.24), and they run on two goroutines. The
// helper's runtime reads a completion whose nonce differs from the parked
// one as the end of the parked interval (sessionruntime's Completed and
// settlePendingLocked), so two completions delivered in the opposite order
// to the one the kernel accepted them in seal the wrong interval as no-fence
// and hand its rows to the next one. What is judged here is that order, off
// the carrier, with both sources driven at once.

import (
	"context"
	"encoding/hex"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/log/logtest"
)

// orderingKernel is the kernel both sources share: it accepts every
// completion and records the order it accepted them in: the order the
// runtime must see.
type orderingKernel struct {
	lifecyclechannel.Kernel

	mu       sync.Mutex
	accepted []lifecycle.FenceNonce
}

func (k *orderingKernel) Ingest(_ lifecycle.TransportID, env lifecycle.Envelope) error {
	k.mu.Lock()
	k.accepted = append(k.accepted, env.Event.Complete.Fence)
	k.mu.Unlock()
	return nil
}

func (k *orderingKernel) acceptedOrder() []lifecycle.FenceNonce {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]lifecycle.FenceNonce(nil), k.accepted...)
}

// orderSender is the carrier: it records the order completions reached it
// and says when the last one has.
type orderSender struct {
	want int
	all  chan struct{}

	mu   sync.Mutex
	sent []lifecycle.FenceNonce
}

func (s *orderSender) LifecycleComplete(_ context.Context, p proto.LifecycleCompleteParams) error {
	raw, err := hex.DecodeString(p.Nonce)
	if err != nil || len(raw) != 32 {
		panic("orderSender: a nonce that is not 64 hex characters: " + p.Nonce)
	}
	var f lifecycle.FenceNonce
	copy(f[:], raw)
	s.mu.Lock()
	s.sent = append(s.sent, f)
	done := len(s.sent) == s.want
	s.mu.Unlock()
	if done {
		close(s.all)
	}
	return nil
}

func (s *orderSender) LifecycleEntered(context.Context, proto.LifecycleEnteredParams) error {
	return nil
}

func (s *orderSender) delivered() []lifecycle.FenceNonce {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]lifecycle.FenceNonce(nil), s.sent...)
}

// TestCompletionsFromBothSourcesReachTheRuntimeInAcceptanceOrder drives the
// pane's wrapper and the ssh child's wrapper — the same downlink behind
// both, as childdomain.go's sshChildKernel wires it — from several
// goroutines at once, and asserts the carrier saw exactly the order the
// kernel accepted. Run it under -race.
func TestCompletionsFromBothSourcesReachTheRuntimeInAcceptanceOrder(t *testing.T) {
	const perSource, sources = 200, 4
	kernel := &orderingKernel{}
	sender := &orderSender{want: perSource * sources, all: make(chan struct{})}
	downlink := newOrderedDownlink(t, sender)
	downlink.Bind(client.HostSessionID{Generation: "gen-1", Session: "sess-1"})

	// Two sources, as production has them: the pane's own observing kernel
	// and the child listener's, each a wrapper over the same kernel and the
	// same downlink. Two goroutines apiece, standing in for a pane's pump
	// and a child's pump racing each other.
	pane := client.NewCompletionObservingKernel(kernel, downlink)
	child := client.NewCompletionObservingKernel(kernel, downlink)

	var wg sync.WaitGroup
	for s := 0; s < sources; s++ {
		k := pane
		if s%2 == 1 {
			k = child
		}
		wg.Add(1)
		go func(s int, k *client.CompletionObservingKernel) {
			defer wg.Done()
			for i := 0; i < perSource; i++ {
				var fence lifecycle.FenceNonce
				fence[0], fence[1], fence[2] = byte(s), byte(i), byte(i>>8)
				if err := k.Ingest("t", lifecycle.Envelope{Event: lifecycle.Event{
					Kind: lifecycle.KindComplete, Complete: &lifecycle.Complete{Fence: fence},
				}}); err != nil {
					t.Errorf("ingest: %v", err)
					return
				}
			}
		}(s, k)
	}
	wg.Wait()

	select {
	case <-sender.all:
	case <-time.After(10 * time.Second):
		t.Fatalf("%d of %d accepted completions reached the carrier", len(sender.delivered()), perSource*sources)
	}
	accepted, delivered := kernel.acceptedOrder(), sender.delivered()
	if len(delivered) != len(accepted) {
		t.Fatalf("%d completions delivered for %d accepted", len(delivered), len(accepted))
	}
	for i := range accepted {
		if delivered[i] != accepted[i] {
			t.Fatalf("delivery %d carried %x, but the kernel accepted %x there: completions reached the runtime out of acceptance order",
				i, delivered[i][:3], accepted[i][:3])
		}
	}
}

func newOrderedDownlink(t *testing.T, sender client.CompletionSender) *client.CompletionDownlink {
	t.Helper()
	ctx, _ := downlinkLog(t)
	return client.NewCompletionDownlink(sender, ctx, nil)
}

// downlinkLog is the context a test builds a downlink over: its logger is
// private to the test (logtest), and it is where the downlink's own lines
// about a failed or lost delivery are read back from.
func downlinkLog(t *testing.T) (context.Context, *slog.Logger) {
	t.Helper()
	sl := logtest.Slog(t)
	return log.WithLogger(context.Background(), log.NewSlogAdapter(sl)), sl
}

// lostLines are the lines that say an accepted fact did not reach the
// helper session.
func lostLines(sl *slog.Logger) []logtest.Record {
	var out []logtest.Record
	for _, r := range logtest.RecordsSlog(sl) {
		if strings.Contains(r.Message, "did not reach the helper session") {
			out = append(out, r)
		}
	}
	return out
}

// awaitLost waits for the first line that says an accepted fact was lost —
// the worker writes it on its own goroutine — and answers the error it
// carries, as the error it was logged as.
func awaitLost(t *testing.T, sl *slog.Logger) error {
	t.Helper()
	if !logtest.WaitForSlog(sl, 5*time.Second, func(r logtest.Record) bool {
		return strings.Contains(r.Message, "did not reach the helper session")
	}) {
		t.Fatal("no line said the accepted completion was lost")
	}
	for _, a := range lostLines(sl)[0].Attrs {
		if a.Key == "err" {
			if err, ok := a.Value.Any().(error); ok {
				return err
			}
		}
	}
	t.Fatal("the loss was logged without the error itself")
	return nil
}
