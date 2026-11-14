package hostconn_test

import (
	"context"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostconn/hostconntest"
)

func TestManualReconnectClosesTransportBeforeDialAndPublishesLossFirst(t *testing.T) {
	first := newMemoryLink()
	first.closeGate = make(chan struct{})
	var sink hostconn.EventSink
	carrier := &memoryCarrier{dial: func(report hostconn.EventSink) (hostconn.Link, error) {
		sink = report
		return first, nil
	}}
	clock := hostconntest.New()
	mgr, sup := managerOn(t, carrier, clock)
	release := sup.Retain("never")
	defer release()
	lease, err := mgr.Acquire(context.Background(), plan())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	sink(hostconn.Event{Kind: hostconn.Silent, Silent: time.Minute})
	if sup.Current().Kind != hostconn.Stalled {
		t.Fatal("premise: connection did not stall")
	}

	// The replacement dial itself asserts the closing interval, so the test
	// cannot pass merely because the retry goroutine had not run at an earlier
	// negative assertion.
	dialProof := make(chan bool, 1)
	carrier.mu.Lock()
	carrier.dial = func(hostconn.EventSink) (hostconn.Link, error) {
		select {
		case <-first.done:
			states := sup.States()
			lost := false
			for _, state := range states {
				if state.Kind == hostconn.Reconnecting {
					lost = true
				}
			}
			dialProof <- lost
		default:
			dialProof <- false
		}
		return newMemoryLink(), nil
	}
	carrier.mu.Unlock()
	registered := make(chan error, 1)
	go func() { registered <- sup.RequestReconnect(0) }()
	waitFor(t, "physical close entered", func() bool {
		select {
		case <-first.closeStarted:
			return true
		default:
			return false
		}
	})
	if sup.Current().Kind != hostconn.Stalled {
		t.Fatal("connection moved before physical close finished")
	}
	close(first.closeGate)
	waitFor(t, "reconnect registration", func() bool { return len(registered) == 1 })
	if err := <-registered; err != nil {
		t.Fatal(err)
	}
	waitFor(t, "replacement dial proof", func() bool { return len(dialProof) == 1 })
	if !<-dialProof {
		t.Fatal("replacement dial preceded physical close or loss observation")
	}
	waitFor(t, "replacement connected", func() bool { return sup.Current().Kind == hostconn.Connected })
}
