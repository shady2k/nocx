package session

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestDaemonLifecycleDeadlineAdmissionWinsAtomically(t *testing.T) {
	drained := make(chan struct{})
	l := newDaemonLifecycle(time.Hour, func() { close(drained) })
	release, err := l.admit()
	if err != nil {
		t.Fatal(err)
	}
	l.expire()
	if _, err := l.admit(); !errors.Is(err, ErrDraining) {
		t.Fatalf("admission after deadline = %v, want draining", err)
	}
	l.sessionStarted()
	release()
	select {
	case <-drained:
		t.Fatal("daemon drained despite winning admission")
	default:
	}
	l.sessionEnded()
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("last session did not drain daemon")
	}
	l.stop()
}

func TestDaemonLifecycleEmptyGraceExpires(t *testing.T) {
	drained := make(chan struct{})
	l := newDaemonLifecycle(time.Hour, func() { close(drained) })
	l.expire()
	if _, err := l.admit(); !errors.Is(err, ErrDraining) {
		t.Fatalf("post-grace admission = %v, want draining", err)
	}
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("empty daemon did not drain")
	}
	l.stop()
}

func TestDaemonLifecycleDeadlineRacesAdmission(t *testing.T) {
	for i := 0; i < 100; i++ {
		var mu sync.Mutex
		admitted, refused, drained := false, false, false
		l := newDaemonLifecycle(time.Hour, func() { mu.Lock(); drained = true; mu.Unlock() })
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); <-start; l.expire() }()
		go func() {
			defer wg.Done()
			<-start
			release, err := l.admit()
			if err != nil {
				refused = true
				return
			}
			admitted = true
			l.sessionStarted()
			release()
		}()
		close(start)
		wg.Wait()
		mu.Lock()
		d := drained
		mu.Unlock()
		if admitted == refused {
			t.Fatalf("iteration %d: admitted=%t refused=%t", i, admitted, refused)
		}
		if admitted && d {
			t.Fatalf("iteration %d: acknowledged session but daemon drained", i)
		}
		if admitted {
			l.sessionEnded()
		}
		if refused && !d {
			t.Fatalf("iteration %d: refused admission but daemon did not drain", i)
		}
		l.stop()
	}
}
