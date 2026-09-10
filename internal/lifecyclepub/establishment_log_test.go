package lifecyclepub_test

// THE GATE THAT DECIDES EVERY HANDSHAKE HAD NO VOICE (nocx-n14oo.8).
//
// A worker pane's shell authenticated, its hello crossed three carriage hops
// with every byte accounted for, and the channel died at exactly ten seconds
// with cause=hello-timeout. What happened in between is decision 9: the
// accept is minted and held until the renderer acknowledges it. That episode
// — minted, waited, expired — wrote nothing anywhere, so a handshake that was
// never going to be acknowledged looked exactly like one that was lost in
// transit.

import (
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	nocxlog "github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/waittest"
)

func TestAnAcceptWaitingOnTheRendererIsSaidOutLoud(t *testing.T) {
	var buf syncBuffer
	k := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(k,
		lifecyclepub.WithEstablishmentTimeout(40*time.Millisecond),
		lifecyclepub.WithLogger(nocxlog.NewSlogAdapter(
			slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))))
	pub.SetEmitter(&recorder{})
	_ = pub.BindTransport("T", &recordingPort{})
	h, _ := pub.RequestDomain("L", nil, "T")
	mustIngest(t, pub, "T", env("L", h, 1, helloEvt()))

	waittest.WaitForDetail(t, "the minted accept to be announced",
		func() string { return buf.String() },
		func() bool {
			return strings.Contains(buf.String(), "lifecycle: an accept awaits the renderer's acknowledgement")
		})

	// And when it never comes, the expiry names the wait rather than leaving
	// the adapter's bare hello-timeout to be read as a transport failure.
	waittest.WaitForDetail(t, "the unacknowledged accept to be reported",
		func() string { return buf.String() },
		func() bool {
			out := buf.String()
			return strings.Contains(out, "lifecycle: the renderer never acknowledged the accept") &&
				strings.Contains(out, "waited_ms=")
		})
	for _, want := range []string{"lane=L", "domain=" + string(h.Domain)} {
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("the episode does not name %q:\n%s", want, buf.String())
		}
	}
}

// A handshake that WORKS says so and says how long the renderer took, because
// "is this slow or is it not happening" cannot be answered from the failure
// alone. On this machine the answer turned out to be 78ms against a 10s bound.
func TestAnAcknowledgedAcceptSaysHowLongItWaited(t *testing.T) {
	var buf syncBuffer
	k := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(k, lifecyclepub.WithLogger(nocxlog.NewSlogAdapter(
		slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))))
	r := &recorder{}
	pub.SetEmitter(r)
	_ = pub.BindTransport("T", &recordingPort{})
	h, _ := pub.RequestDomain("L", nil, "T")
	mustIngest(t, pub, "T", env("L", h, 1, helloEvt()))
	mustAckEstablishment(t, pub, r, "L", h)

	out := buf.String()
	for _, want := range []string{
		"lifecycle: the accept was flushed on the renderer's acknowledgement",
		"waited_ms=",
		"domain=" + string(h.Domain),
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("an acknowledged establishment does not say %q:\n%s", want, out)
		}
	}
}

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
