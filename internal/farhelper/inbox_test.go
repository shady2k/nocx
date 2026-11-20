package farhelper_test

import (
	"context"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostmode"
)

func TestPublicInputsReturnWhileRemoteStepIgnoresCancellation(t *testing.T) {
	for _, step := range []string{"probe", "dial"} {
		t.Run(step, func(t *testing.T) {
			h := newHarness(t)
			h.granted(machine)
			block := newGate()
			defer block.release()
			entered := h.probe.entered
			if step == "probe" {
				h.probe.gate, h.probe.ignoreCancel = block, true
			} else {
				h.dial.gate, h.dial.ignoreCancel = block, true
				entered = h.dial.entered
			}
			h.conns.Put(connected("inputs", 1))
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("step did not begin")
			}
			for _, action := range []struct {
				name string
				call func()
			}{
				{"observe", func() { h.conns.update("inputs", func(c *farhelper.Connection) { c.State = hostconn.Stalled }) }},
				{"storeChanged", func() { h.modes.Set(machine, hostmode.Raw) }},
				{"CloseFor", func() { h.reg.CloseFor(machine) }},
				{"FinishFor", func() { h.reg.FinishFor(machine) }},
				{"Refresh", func() { _ = h.reg.Refresh(context.Background(), "inputs") }},
				{"remove", func() { h.conns.end("inputs") }},
			} {
				done := make(chan struct{})
				go func() { action.call(); close(done) }()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatalf("%s blocked behind %s", action.name, step)
				}
			}
		})
	}
}

func TestObservationReturnsWhileRetiredClientCloseWaits(t *testing.T) {
	h := newHarness(t)
	h.granted(machine)
	h.conns.Put(connected("close", 1))
	h.want("close", farhelper.StateServing, "", farhelper.ActionNone)
	peer := h.dial.lastPeer(t)
	release := newGate()
	defer release.release()
	peer.lane.closeBlock = release.open
	peer.lane.closeEntered = make(chan struct{})
	returned := make(chan struct{})
	go func() {
		h.conns.update("close", func(c *farhelper.Connection) { c.State = hostconn.Stalled })
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("observation waited for client Close")
	}
	select {
	case <-peer.lane.closeEntered:
	case <-time.After(time.Second):
		t.Fatal("entry did not close retired client")
	}
	// CloseFor is the synchronous uninstall barrier, unlike observations.
	done := make(chan struct{})
	go func() { h.reg.CloseFor(machine); close(done) }()
	select {
	case <-done:
		t.Fatal("CloseFor returned before the retired client closed")
	case <-time.After(50 * time.Millisecond):
	}
	release.release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("CloseFor did not acknowledge client closure")
	}
	select {
	case <-peer.lane.waitDone:
	default:
		t.Fatal("CloseFor acknowledged before client closure")
	}
}
