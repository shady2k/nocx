package hostconn_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostconn/hostconntest"
)

func TestRetainedConnectionRetriesInSupervisor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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
		synctest.Wait()
		clock.Advance(time.Second)
		synctest.Wait()
		waitFor(t, "second attempt", func() bool { return carrier.dialCount() == 2 && sup.Current().Attempt == 2 })
		waitFor(t, "next retry timer", func() bool { return sup.RetryScheduled() })
		synctest.Wait()
		clock.Advance(2 * time.Second)
		synctest.Wait()
		waitFor(t, "third attempt", func() bool { return carrier.dialCount() == 3 && sup.Current().Attempt == 3 })
	})
}

func TestRetainedFirstDialFailureSchedulesAutoReconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := hostconntest.New()
		offline := errors.New("offline")
		carrier := &memoryCarrier{dial: func(sink hostconn.EventSink) (hostconn.Link, error) {
			sink(hostconn.Event{Kind: hostconn.Lost, Cause: hostconn.CauseLostLink})
			return nil, offline
		}}
		mgr, sup := managerOn(t, carrier, clock)
		release := sup.Retain("auto")
		defer release()
		if _, err := mgr.Acquire(context.Background(), plan()); !errors.Is(err, offline) {
			t.Fatalf("first dial error = %v, want %v", err, offline)
		}
		if state := sup.Current(); state.Kind != hostconn.Reconnecting || !sup.RetryScheduled() {
			t.Fatalf("first dial failure: state = %v, retry scheduled = %v", state, sup.RetryScheduled())
		}
		carrier.mu.Lock()
		carrier.dial = nil
		carrier.mu.Unlock()
		synctest.Wait()
		clock.Advance(time.Second)
		synctest.Wait()
		if state := sup.Current(); state.Kind != hostconn.Connected || carrier.dialCount() != 2 {
			t.Fatalf("automatic retry: state = %v, dials = %d", state, carrier.dialCount())
		}
	})
}

func TestRetainedFirstDialFailureAllowsManualReconnect(t *testing.T) {
	for _, policy := range []string{"manual", "never"} {
		t.Run(policy, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				offline := errors.New("offline")
				carrier := &memoryCarrier{dial: func(sink hostconn.EventSink) (hostconn.Link, error) {
					sink(hostconn.Event{Kind: hostconn.Lost, Cause: hostconn.CauseLostLink})
					return nil, offline
				}}
				mgr, sup := manager(t, carrier)
				release := sup.Retain(policy)
				defer release()
				if _, err := mgr.Acquire(context.Background(), plan()); !errors.Is(err, offline) {
					t.Fatalf("first dial error = %v, want %v", err, offline)
				}
				if state := sup.Current(); state.Kind != hostconn.Ended || sup.RetryScheduled() {
					t.Fatalf("first dial failure: state = %v, retry scheduled = %v", state, sup.RetryScheduled())
				}
				carrier.mu.Lock()
				carrier.dial = nil
				carrier.mu.Unlock()
				if err := sup.RequestReconnect(0); err != nil {
					t.Fatalf("manual reconnect after first dial failure: %v", err)
				}
				synctest.Wait()
				if state := sup.Current(); state.Kind != hostconn.Connected || carrier.dialCount() != 2 {
					t.Fatalf("manual retry: state = %v, dials = %d", state, carrier.dialCount())
				}
			})
		})
	}
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
