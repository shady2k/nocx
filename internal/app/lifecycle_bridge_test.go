package app

import (
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	nocxlog "github.com/shady2k/nocx/internal/log"
)

// THE BRIDGE IS THE HOP NOTHING COULD SEE (nocx-n14oo.7).
//
// On 2026-09-10 a worker pane's shell wrote 219 bytes of hello into its
// lifecycle descriptor — the helper said so — and the coordinator's adapter
// timed out ten seconds later having never seen an envelope, accepted or
// rejected. Between those two facts sits this bridge, and it wrote nothing at
// all: not that it had started, not how many bytes it had carried, not which
// end had ended it. A hop that is silent in both directions cannot be cleared
// of a loss, which is what made the reading impossible.
func TestTheLifecycleBridgeSaysItStartedAndWhatItCarried(t *testing.T) {
	var buf safeBuffer
	lg := nocxlog.NewSlogAdapter(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	peer, adapterEnd := net.Pipe()
	carrier := newFakeCarrier()

	bridgeLifecycle(lg, lifecycle.TransportID("tpt-b52fcdbe2de97285"), peer, carrier)

	// The shell's hello: the helper pushes it onto the carrier and the
	// adapter's end of the pipe is what has to see it.
	carrier.push([]byte("hello from the shell"))
	got := make([]byte, 64)
	_ = adapterEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := adapterEnd.Read(got)
	if err != nil {
		t.Fatalf("the adapter's end never saw the shell's bytes: %v", err)
	}
	if string(got[:n]) != "hello from the shell" {
		t.Fatalf("the adapter's end read %q", got[:n])
	}

	carrier.close()
	waitFor(t, "the bridge to report the shell's end", func() bool { return strings.Contains(buf.String(), "lifecycle bridge: the shell's end closed") })

	out := buf.String()
	for _, want := range []string{
		"lifecycle bridge started",
		"transport=tpt-b52fcdbe2de97285",
		"lifecycle bridge: the shell's first bytes reached the adapter",
		"bytes=20",
		"lifecycle bridge: the shell's end closed",
		"to_adapter_bytes=20",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the bridge does not say %q:\n%s", want, out)
		}
	}
}

// A bridge that is never started must be as visible as one that is: the
// difference between "nothing was carried" and "nothing was asked to carry"
// is the whole of the diagnosis.
func TestABridgeThatEndsWithoutCarryingAnythingSaysSo(t *testing.T) {
	var buf safeBuffer
	lg := nocxlog.NewSlogAdapter(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	peer, _ := net.Pipe()
	carrier := newFakeCarrier()
	bridgeLifecycle(lg, lifecycle.TransportID("tpt-quiet"), peer, carrier)
	carrier.close()

	waitFor(t, "the bridge to report an empty carry", func() bool { return strings.Contains(buf.String(), "carried_nothing=true") })
}

// fakeCarrier stands in for the helper attachment's lifecycle stream: bytes
// pushed onto it are what the far helper delivered.
type fakeCarrier struct {
	r      *io.PipeReader
	w      *io.PipeWriter
	closed sync.Once
}

func newFakeCarrier() *fakeCarrier {
	r, w := io.Pipe()
	return &fakeCarrier{r: r, w: w}
}

func (c *fakeCarrier) push(b []byte) { go func() { _, _ = c.w.Write(b) }() }
func (c *fakeCarrier) close()        { c.closed.Do(func() { _ = c.w.Close() }) }

func (c *fakeCarrier) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *fakeCarrier) Write(p []byte) (int, error) { return len(p), nil }
func (c *fakeCarrier) Close() error {
	c.close()
	return c.r.Close()
}
