package farhelper_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/monoclock"
	"github.com/shady2k/nocx/internal/waittest"
)

func TestEqualSeqAuthenticatedFingerprintRevisesDecision(t *testing.T) {
	h := newHarness(t)
	h.modes.Set(machine, hostmode.Raw)
	c := connected("fingerprint", 1)
	c.Seq = 7
	c.HostKeyFingerprint = ""
	h.conns.Put(c)
	h.want(c.ID, farhelper.StateUnavailable, farhelper.ReasonConsentPending, farhelper.ActionNone)
	c.HostKeyFingerprint = machine
	h.conns.Put(c)
	h.want(c.ID, farhelper.StateUnavailable, farhelper.ReasonModeRaw, farhelper.ActionHostSettings)
	if h.probe.count() != 0 {
		t.Fatal("raw authenticated host ran remote work")
	}
}

func TestProbeAndFindFailuresAreVisibleAndBounded(t *testing.T) {
	for _, step := range []string{"probe", "find"} {
		for _, refusal := range []struct {
			name   string
			err    error
			reason farhelper.Reason
		}{
			{"temporary", errors.New("read unavailable"), farhelper.ReasonInstallFailed},
			{"forbidden", farhelper.ErrExecForbidden, farhelper.ReasonExecForbidden},
			{"platform", farhelper.ErrUnsupportedPlatform, farhelper.ReasonUnsupportedPlatform},
		} {
			t.Run(step+"/"+refusal.name, func(t *testing.T) {
				h := newHarness(t)
				h.granted(machine)
				if step == "probe" {
					h.probe.err = refusal.err
				} else {
					h.install.findErr = refusal.err
				}
				clock := &observedRetryClock{Clock: h.clock, armed: make(chan struct{}, 8)}
				h.reg.Close()
				h.reg = farhelper.New(farhelper.Config{Connections: h.conns, Modes: h.modes, Consents: h.consents, Probe: h.probe, Install: h.install, Dial: h.dial, Clock: clock, Log: testLogger(t)})
				t.Cleanup(h.reg.Close)
				h.conns.Put(connected("retries", 1))
				if refusal.name == "temporary" {
					waittest.WaitFor(t, "first read", func() bool { return h.probe.count() == 1 })
					for attempt := 2; attempt <= 3; attempt++ {
						// A result must finish and arm its timer before advancing fake time.
						// Status is starting throughout retry; the injected clock exposes waiters.
						select {
						case <-clock.armed:
						case <-time.After(time.Second):
							t.Fatal("retry not armed")
						}
						h.clock.Advance(30 * time.Second)
						want := attempt
						waittest.WaitFor(t, "next read", func() bool { return h.probe.count() == want })
					}
				}
				h.want("retries", farhelper.StateUnavailable, refusal.reason, farhelper.ActionNone)
				want := 1
				if refusal.name == "temporary" {
					want = 3
				}
				if h.probe.count() != want || h.install.count() != 0 {
					t.Fatalf("probe=%d install=%d want %d/0", h.probe.count(), h.install.count(), want)
				}
			})
		}
	}
}

type observedRetryClock struct {
	hostconn.Clock
	armed chan struct{}
}

func (c *observedRetryClock) After(d time.Duration) <-chan monoclock.Nanos {
	ch := c.Clock.After(d)
	c.armed <- struct{}{}
	return ch
}

func TestUnrelatedStoreNotificationDoesNotFailDecision(t *testing.T) {
	h := newHarness(t)
	h.granted(machine)
	h.probe.gate = newGate()
	h.conns.Put(connected("unrelated", 1))
	select {
	case <-h.probe.entered:
	case <-time.After(time.Second):
		t.Fatal("probe did not begin")
	}
	h.modes.Set(otherMachine, hostmode.Raw)
	h.probe.gate.release()
	h.want("unrelated", farhelper.StateServing, "", farhelper.ActionNone)
}

func TestDelayedOldLossCannotHideCurrentPeerLoss(t *testing.T) {
	old := newFakePeer(newFakeDialer().logger())
	current := newFakePeer(newFakeDialer().logger())
	defer func() { _ = old.client.Close() }()
	defer func() { _ = current.client.Close() }()
	current.Lose()
	select {
	case <-current.client.Done():
	case <-time.After(time.Second):
		t.Fatal("peer did not end")
	}
	farhelper.AssertCoalescedClientLoss(t, current.client, old.client)
}

func TestWatchPublishesFinalUnavailableForRemovedServingEntry(t *testing.T) {
	h := newHarness(t)
	h.granted(machine)
	final := make(chan farhelper.Status, 1)
	stop := h.reg.Watch(func(id string, s farhelper.Status) {
		// Snapshot readers are allowed from a watcher.
		_, _ = h.reg.Axis(id)
		if id == "removed" && s.State == farhelper.StateUnavailable {
			select {
			case final <- s:
			default:
			}
		}
	})
	defer stop()
	h.conns.Put(connected("removed", 1))
	h.want("removed", farhelper.StateServing, "", farhelper.ActionNone)
	h.conns.end("removed")
	select {
	case s := <-final:
		if s.Reason != farhelper.ReasonConnection {
			t.Fatalf("final=%+v", s)
		}
	case <-time.After(time.Second):
		t.Fatal("no final removal status")
	}
}

func TestReplacementPublicationFollowsBlockedOldCallback(t *testing.T) {
	h := newHarness(t)
	h.granted(machine)
	entered := make(chan struct{})
	release := make(chan struct{})
	replacement := make(chan struct{}, 1)
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	stop := h.reg.Watch(func(id string, s farhelper.Status) {
		if id != "reuse" {
			return
		}
		if s.State == farhelper.StateServing {
			close(entered)
			<-release
		}
		if s.Reason == farhelper.ReasonModeRaw {
			select {
			case replacement <- struct{}{}:
			default:
			}
		}
	})
	defer stop()
	h.conns.Put(connected("reuse", 1))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("old callback did not start")
	}
	done := make(chan struct{})
	go func() {
		h.conns.end("reuse")
		h.modes.Set(otherMachine, hostmode.Raw)
		c := connected("reuse", 2)
		c.HostKeyFingerprint = otherMachine
		h.conns.Put(c)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publication blocked external input")
	}
	// Initial input reduction commits this snapshot before trying publication.
	h.want("reuse", farhelper.StateUnavailable, farhelper.ReasonModeRaw, farhelper.ActionHostSettings)
	select {
	case <-replacement:
		t.Fatal("replacement overtook old callback")
	default:
	}
	unblock()
	select {
	case <-replacement:
	case <-time.After(time.Second):
		t.Fatal("replacement publication did not follow")
	}
}
