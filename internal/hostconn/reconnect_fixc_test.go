package hostconn_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostconn/hostconntest"
)

func TestManualReconnectInterruptsAutomaticBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := hostconntest.New()
		first := newMemoryLink()
		carrier := &memoryCarrier{dial: func(hostconn.EventSink) (hostconn.Link, error) { return first, nil }}
		mgr, sup := managerOn(t, carrier, clock)
		release := sup.Retain("auto")
		defer release()
		lease, err := mgr.Acquire(context.Background(), plan())
		if err != nil {
			t.Fatal(err)
		}
		defer lease.Release()
		carrier.mu.Lock()
		carrier.dial = nil
		carrier.mu.Unlock()
		first.end(errors.New("drop"))
		synctest.Wait()
		if sup.Current().Kind != hostconn.Reconnecting {
			t.Fatalf("drop reads %v", sup.Current())
		}
		if reqErr := sup.RequestReconnect(0); reqErr != nil {
			t.Fatal(reqErr)
		}
		synctest.Wait()
		// Fake time has not advanced. A registered press must dial immediately.
		if got := carrier.dialCount(); got != 2 {
			t.Fatalf("manual press during backoff made %d dials, want 2 without advancing the retry clock", got)
		}
		if sup.Current().Kind != hostconn.Connected {
			t.Fatalf("manual reconnect reads %v", sup.Current())
		}
	})
}

func TestRetryKeeperDoesNotInventPlainShellsOrHopRiders(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := hostconntest.New()
		first := newMemoryLink()
		carrier := &memoryCarrier{dial: func(hostconn.EventSink) (hostconn.Link, error) { return first, nil }}
		mgr, sup := managerOn(t, carrier, clock)
		panePlan := plan()
		panePlan.Holds = hostconn.HoldsPlainShell
		hop := hostconn.Key{Host: "bastion.example", Port: 22, User: "dev", Identity: "key"}
		panePlan.Route = []hostconn.Key{hop}
		release := sup.Retain("never")
		defer release()
		lease, err := mgr.Acquire(context.Background(), panePlan)
		if err != nil {
			t.Fatal(err)
		}
		defer lease.Release()
		carrier.mu.Lock()
		carrier.dial = nil
		carrier.mu.Unlock()
		first.end(errors.New("drop"))
		synctest.Wait()
		lease.Release()
		if reqErr := sup.RequestReconnect(0); reqErr != nil {
			t.Fatal(reqErr)
		}
		synctest.Wait()
		assertPlainShellCount(t, sup, 0)
		assertPlainShellCount(t, mgr.Supervisor(hop), 0)
		replacement, err := mgr.Acquire(context.Background(), panePlan)
		if err != nil {
			t.Fatal(err)
		}
		defer replacement.Release()
		assertPlainShellCount(t, sup, 1)
		assertPlainShellCount(t, mgr.Supervisor(hop), 1)
		replacement.Release()
		assertPlainShellCount(t, sup, 0)
		assertPlainShellCount(t, mgr.Supervisor(hop), 0)
	})
}

func assertPlainShellCount(t *testing.T, sup *hostconn.Supervisor, want int) {
	t.Helper()
	facts := make(chan hostconn.Facts, 64)
	stop := sup.Watch(func(f hostconn.Facts) { facts <- f })
	defer stop()
	if got := (<-facts).PlainShells; got != want {
		t.Fatalf("%s plain shells = %d, want %d", sup.Current(), got, want)
	}
}
