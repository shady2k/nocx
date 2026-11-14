package hostconn_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostconn/hostconntest"
)

func TestRetainedConnectionRetriesInSupervisor(t *testing.T) {
	clock := hostconntest.New()
	first := newMemoryLink()
	carrier := &memoryCarrier{dial: func(sink hostconn.EventSink) (hostconn.Link, error) { return first, nil }}
	mgr, sup := managerOn(t, carrier, clock)
	release := sup.Retain("auto")
	defer release()
	lease, err := mgr.Acquire(context.Background(), plan())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	carrier.mu.Lock()
	carrier.dial = func(sink hostconn.EventSink) (hostconn.Link, error) {
		sink(hostconn.Event{Kind: hostconn.Lost, Cause: hostconn.CauseLostLink})
		return nil, errors.New("offline")
	}
	carrier.mu.Unlock()
	first.end(errors.New("drop"))
	waitFor(t, "first retry timer", func() bool { return sup.Current().Kind == hostconn.Reconnecting && sup.RetryScheduled() })
	clock.Advance(time.Second)
	waitFor(t, "second attempt", func() bool { return carrier.dialCount() == 2 && sup.Current().Attempt == 2 })
	waitFor(t, "next retry timer", func() bool { return sup.RetryScheduled() })
	clock.Advance(2 * time.Second)
	waitFor(t, "third attempt", func() bool { return carrier.dialCount() == 3 && sup.Current().Attempt == 3 })
}

func TestRetainedNeverConnectionRestsAndRejectsHealthyReconnect(t *testing.T) {
	carrier := &memoryCarrier{}
	mgr, sup := manager(t, carrier)
	release := sup.Retain("never")
	defer release()
	lease, err := mgr.Acquire(context.Background(), plan())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if err := sup.RequestReconnect(0); err == nil {
		t.Fatal("healthy connection accepted reconnect")
	}
	link, ok := lease.Link().(*memoryLink)
	if !ok {
		t.Fatalf("link has type %T", lease.Link())
	}
	link.end(errors.New("drop"))
	waitFor(t, "ended", func() bool { return sup.Current().Kind == hostconn.Ended })
	if carrier.dialCount() != 1 {
		t.Fatal("never redialled")
	}
	if err := sup.RequestReconnect(0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "manual reconnect", func() bool { return sup.Current().Kind == hostconn.Connected && carrier.dialCount() == 2 })
}
