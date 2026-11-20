package farhelper_test

import (
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/hostmode"
)

// D25 requires closing the client before the caller removes the directory.
func TestReviewALX699CloseForClosesTheClientBeforeReturning(t *testing.T) {
	h := newHarness(t)
	h.granted(machine)
	h.conns.Put(connected("d25", 1))
	h.want("d25", farhelper.StateServing, "", farhelper.ActionNone)
	peer := h.dial.lastPeer(t)
	h.reg.CloseFor(machine)
	select {
	case <-peer.lane.waitDone:
	default:
		t.Fatal("CloseFor returned while the far helper client is still open: uninstall may remove the directory under it")
	}
}

func TestReviewALX699UnrelatedStoreWriteDoesNotRestartStep(t *testing.T) {
	h := newHarness(t)
	h.granted(machine)
	h.probe.gate = newGate()
	defer h.probe.gate.release()
	h.conns.Put(connected("unrelated", 1))
	select {
	case <-h.probe.entered:
	case <-time.After(time.Second):
		t.Fatal("probe did not begin")
	}
	h.modes.Set(otherMachine, hostmode.Raw)
	select {
	case <-h.probe.entered:
		t.Fatalf("an unrelated store write cancelled and restarted the step in flight (probe calls=%d)", h.probe.count())
	case <-time.After(500 * time.Millisecond):
	}
}

func TestRegistryCloseClosesClientsBeforeReturning(t *testing.T) {
	h := newHarness(t)
	h.granted(machine)
	h.conns.Put(connected("shutdown", 1))
	h.want("shutdown", farhelper.StateServing, "", farhelper.ActionNone)
	peer := h.dial.lastPeer(t)
	h.reg.Close()
	select {
	case <-peer.lane.waitDone:
	default:
		t.Fatal("Registry.Close returned before closing its far client")
	}
}

func TestRegistryCloseWaitsForRemovedClient(t *testing.T) {
	h := newHarness(t)
	h.granted(machine)
	h.conns.Put(connected("removed-close", 1))
	h.want("removed-close", farhelper.StateServing, "", farhelper.ActionNone)
	peer := h.dial.lastPeer(t)
	release := newGate()
	defer release.release()
	peer.lane.closeBlock = release.open
	peer.lane.closeEntered = make(chan struct{})
	h.conns.end("removed-close")
	select {
	case <-peer.lane.closeEntered:
	case <-time.After(time.Second):
		t.Fatal("retired client did not begin closing")
	}
	done := make(chan struct{})
	go func() { h.reg.Close(); close(done) }()
	select {
	case <-done:
		t.Fatal("Registry.Close skipped a removed client still closing")
	case <-time.After(50 * time.Millisecond):
	}
	release.release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Registry.Close did not finish after removed client closed")
	}
}
