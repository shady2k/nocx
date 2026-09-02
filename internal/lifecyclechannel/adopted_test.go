package lifecyclechannel

import (
	"encoding/hex"
	"net"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclecodec"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/waittest"
)

// A shell whose coordinator was replaced is still speaking the identity it
// was handed at spawn. NewAdoptedStream is the coordinator↔helper leg being
// re-established under it (nocx-k6p18.31): no hello, no accept, no new epoch
// — and the very next event the shell sends must land.

func adoptedLaunch() Launch {
	cap := make([]byte, 32)
	rec := make([]byte, 32)
	for i := range cap {
		cap[i] = byte(i + 1)
		rec[i] = byte(i + 100)
	}
	return Launch{
		Lane:       "lane-fromspawn",
		Domain:     "dom-fromspawn",
		Epoch:      9,
		Capability: hex.EncodeToString(cap),
		Recovery:   hex.EncodeToString(rec),
	}
}

func TestAnAdoptedStreamDeliversTheShellsNextEventWithoutAHandshake(t *testing.T) {
	pub := newTestKernel()
	coordinator, shell := net.Pipe()
	t.Cleanup(func() { _ = shell.Close() })

	launch := adoptedLaunch()
	a, err := NewAdoptedStream(log.NewSlogAdapter(nil), pub, coordinator, launch)
	if err != nil {
		t.Fatalf("NewAdoptedStream: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })

	if a.Lane() != launch.Lane {
		t.Fatalf("adapter lane = %q, want the lane the shell was launched on (%q)", a.Lane(), launch.Lane)
	}
	d, ok := pub.Domain(launch.Domain)
	if !ok || d.State != lifecycle.DomainEstablished {
		t.Fatalf("adopted domain state = %v (found=%v), want Established", d.State, ok)
	}

	// The shell speaks next. Its sequence is whatever it had reached; the
	// coordinator that would have known it is gone.
	id := lifecycle.AttemptID("shell-77")
	go func() {
		_, _ = lifecyclecodec.Encode(shell, shellEnv(a, 77, lifecycle.Event{
			Kind: lifecycle.KindStart, Start: &lifecycle.Start{AttemptID: &id, Command: "make -j8"},
		}))
	}()
	waittest.WaitFor(t, "the command the returned pane ran opened an attempt", func() bool {
		snap, err := pub.State(launch.Lane)
		return err == nil && snap.Lifecycle == lifecycle.LifecycleRunning
	})
}

func TestAnAdoptedStreamRefusesALaunchItCannotAuthenticateWith(t *testing.T) {
	good := adoptedLaunch()
	cases := []struct {
		name   string
		mutate func(*Launch)
	}{
		{"no lane", func(l *Launch) { l.Lane = "" }},
		{"no domain", func(l *Launch) { l.Domain = "" }},
		{"no epoch", func(l *Launch) { l.Epoch = 0 }},
		{"no capability", func(l *Launch) { l.Capability = "" }},
		{"capability is not hex", func(l *Launch) { l.Capability = strings.Repeat("z", 64) }},
		{"capability is the wrong width", func(l *Launch) { l.Capability = "abcd" }},
		{"recovery is not hex", func(l *Launch) { l.Recovery = strings.Repeat("z", 64) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pub := newTestKernel()
			coordinator, shell := net.Pipe()
			t.Cleanup(func() { _ = shell.Close() })
			l := good
			tc.mutate(&l)
			a, err := NewAdoptedStream(log.NewSlogAdapter(nil), pub, coordinator, l)
			if err == nil {
				_ = a.Close()
				t.Fatalf("NewAdoptedStream accepted a launch it cannot authenticate with")
			}
		})
	}
}

// The transport is bound before the domain is adopted, so a refused adoption
// must leave no transport behind — otherwise a later loss report addresses a
// transport that carries nothing and the kernel's registry grows with it.
func TestARefusedAdoptionLeavesNoTransportBound(t *testing.T) {
	pub := newTestKernel()
	coordinator, shell := net.Pipe()
	t.Cleanup(func() { _ = shell.Close() })
	launch := adoptedLaunch()
	first, err := NewAdoptedStream(log.NewSlogAdapter(nil), pub, coordinator, launch)
	if err != nil {
		t.Fatalf("NewAdoptedStream: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })

	// The same domain id again: the kernel refuses it (ErrDomainExists).
	other, otherShell := net.Pipe()
	t.Cleanup(func() { _ = otherShell.Close() })
	second, err := NewAdoptedStream(log.NewSlogAdapter(nil), pub, other, launch)
	if err == nil {
		_ = second.Close()
		t.Fatalf("a second adoption of the same domain was accepted")
	}
	if err := pub.TransportLost("tpt-does-not-matter"); err == nil {
		t.Fatalf("sanity: an unbound transport must be unknown")
	}
}

// A returned pane whose shell has already exited: the stream ends, and the
// adopted domain must go Lost exactly as a minted one does — the pane says
// so instead of sitting on an authenticated domain nobody speaks for.
func TestAnAdoptedStreamLosesItsDomainWhenTheShellIsGone(t *testing.T) {
	pub := newTestKernel()
	coordinator, shell := net.Pipe()
	launch := adoptedLaunch()
	var lostLane lifecycle.LaneID
	var lostCause LossCause
	done := make(chan struct{})
	a, err := NewAdoptedStream(log.NewSlogAdapter(nil), pub, coordinator, launch,
		WithLossReporter(func(lane lifecycle.LaneID, cause LossCause) {
			lostLane, lostCause = lane, cause
			close(done)
		}))
	if err != nil {
		t.Fatalf("NewAdoptedStream: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	_ = shell.Close()
	<-done
	if lostLane != launch.Lane || lostCause != LossEndOfStream {
		t.Fatalf("loss reported as (%q,%q), want (%q,%q)", lostLane, lostCause, launch.Lane, LossEndOfStream)
	}
	waittest.WaitFor(t, "the adopted domain is lost", func() bool {
		d, ok := pub.Domain(launch.Domain)
		return ok && d.State == lifecycle.DomainLost
	})
}
