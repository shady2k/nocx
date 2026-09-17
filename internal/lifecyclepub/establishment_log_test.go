package lifecyclepub_test

// THE GATE THAT DECIDES EVERY HANDSHAKE HAD NO VOICE (nocx-n14oo.8).
//
// A worker pane's shell authenticated, its hello crossed three carriage hops
// with every byte accounted for, and the channel died at exactly ten seconds
// with cause=hello-timeout. What had been happening in between, before
// ADR-0062, was decision 9's old mechanism: the accept was minted and held
// until a renderer acknowledged it — and a pane the backend itself opens
// subscribes no renderer, so that episode — minted, waited, expired — wrote
// nothing anywhere, and a handshake that was never going to be acknowledged
// looked exactly like one that was lost in transit.
//
// ADR-0062 removed the wait: the accept is now flushed on the backend's own
// authority as soon as the kernel mints it. What these tests assert is what
// is then true — that the flush is said out loud, and its outcome — rather
// than the mechanism that used to gate it.

import (
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	nocxlog "github.com/shady2k/nocx/internal/log"
)

// A handshake that WORKS says so: the accept was flushed, naming the lane,
// domain and epoch it went out for.
func TestAnAcceptIsFlushedOnItsOwnAuthority(t *testing.T) {
	var buf syncBuffer
	k := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(k, lifecyclepub.WithLogger(nocxlog.NewSlogAdapter(
		slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))))
	pub.SetEmitter(&recorder{})
	_ = pub.BindTransport("T", &recordingPort{})
	h, _ := pub.RequestDomain("L", nil, "T")
	mustIngest(t, pub, "T", env("L", h, 1, helloEvt()))

	out := buf.String()
	for _, want := range []string{
		"lifecycle: the accept was flushed",
		"lane=L",
		"domain=" + string(h.Domain),
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("a flushed accept does not say %q:\n%s", want, out)
		}
	}
}

// A handshake whose accept cannot reach the transport says so too, and
// names the reason — the failure a bare hello-timeout ten seconds later
// could never distinguish from a renderer that was simply never there.
func TestAnAcceptThatCannotBeFlushedIsReported(t *testing.T) {
	var buf syncBuffer
	k := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(k, lifecyclepub.WithLogger(nocxlog.NewSlogAdapter(
		slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))))
	pub.SetEmitter(&recorder{})
	_ = pub.BindTransport("T", failingPort{})
	h, _ := pub.RequestDomain("L", nil, "T")
	mustIngest(t, pub, "T", env("L", h, 1, helloEvt()))

	out := buf.String()
	for _, want := range []string{
		"lifecycle: the accept could not be flushed",
		"lane=L",
		"domain=" + string(h.Domain),
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("a failed flush does not say %q:\n%s", want, out)
		}
	}
}

// failingPort refuses every send, standing in for a transport that dies
// between the accept being minted and it reaching the wire.
type failingPort struct{}

func (failingPort) Send(lifecycle.Envelope) error { return errors.New("transport gone") }

type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
