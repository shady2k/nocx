package session

import (
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/sessionruntime"
)

// Pause the owner inside finishRead, after it has observed EOF but before it
// returns to its event loop. Unlike a sleep this fixes the exact lifetime edge.
type reviewEOFPauseProcess struct {
	*ownerFakeProcess
	atEOF   chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *reviewEOFPauseProcess) WakeReadiness() {
	p.once.Do(func() {
		close(p.atEOF)
		<-p.release
	})
}

func TestReviewShutdownResolvesAlreadyAdmittedIncoming(t *testing.T) {
	proc := &reviewEOFPauseProcess{
		ownerFakeProcess: newOwnerFakeProcess(),
		atEOF:            make(chan struct{}), release: make(chan struct{}),
	}
	owner, _ := newTestOwner(t, proc)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(proc.release) }) }
	t.Cleanup(func() { release(); owner.stop(true, time.Time{}) })
	go owner.run()
	proc.endRead(io.EOF)
	select {
	case <-proc.atEOF:
	case <-time.After(5 * time.Second):
		t.Fatal("owner never observed EOF")
	}
	frameDone, err := owner.submit(ownerItem{kind: itemClientFrame, payload: []byte("accepted")})
	if err != nil {
		t.Fatalf("submit ordinary input: %v", err)
	}
	bumpDone, err := owner.submit(ownerItem{kind: itemAccessBump, above: 9})
	if err != nil {
		t.Fatalf("submit access bump: %v", err)
	}
	owner.closeOnce.Do(func() { close(owner.closingSignal) })
	release()
	select {
	case <-owner.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("owner did not stop")
	}
	select {
	case res := <-frameDone:
		if !errors.Is(res.Err, errOwnerClosing) || res.State != sessionruntime.IntentStateCancelled {
			t.Errorf("accepted frame = %+v, want cancelled/closing", res)
		}
	default:
		t.Error("owner stopped with accepted ordinary input unresolved")
	}
	select {
	case res := <-bumpDone:
		if res.Err != nil || res.Epoch <= 9 {
			t.Errorf("accepted bump = %+v, want epoch above 9", res)
		}
	default:
		t.Error("owner stopped with accepted access bump unresolved")
	}
	if got := owner.queueUsed.Load(); got != 0 {
		t.Errorf("queueUsed = %d after stop, want 0", got)
	}
	if got := proc.writtenPayloads(); len(got) != 0 {
		t.Errorf("shutdown wrote cancelled input: %q", got)
	}
}

// The admission fence has two outcomes. Force each ordering rather than hope
// that a stress race happened to exercise both of them.
func TestReviewSubmitBeforeShutdownFenceIsSettled(t *testing.T) {
	proc := newOwnerFakeProcess()
	owner, _ := newTestOwner(t, proc)
	defer owner.cancelRead()
	defer func() { _ = proc.Close() }()
	// This submission wins the fence and remains in incoming until shutdown.
	done, err := owner.submit(ownerItem{kind: itemClientFrame, payload: []byte("before")})
	if err != nil {
		t.Fatal(err)
	}
	owner.beginClosing()
	select {
	case res := <-done:
		if !errors.Is(res.Err, errOwnerClosing) {
			t.Fatalf("admitted submit = %+v", res)
		}
	default:
		t.Fatal("shutdown did not settle the submit that won admission")
	}
	if got := owner.queueUsed.Load(); got != 0 {
		t.Fatalf("queueUsed = %d", got)
	}
}

func TestReviewSubmitAfterShutdownFenceIsRefused(t *testing.T) {
	proc := newOwnerFakeProcess()
	owner, _ := newTestOwner(t, proc)
	defer owner.cancelRead()
	defer func() { _ = proc.Close() }()
	owner.beginClosing()
	for _, kind := range []ownerItemKind{itemClientFrame, itemResize, itemAccessBump} {
		done, err := owner.submit(ownerItem{kind: kind})
		if done != nil || !errors.Is(err, errOwnerClosing) {
			t.Fatalf("submit after fence (%v): done=%v err=%v", kind, done, err)
		}
	}
	if got := len(owner.incoming); got != 0 {
		t.Fatalf("refused submissions queued %d items", got)
	}
	if got := owner.queueUsed.Load(); got != 0 {
		t.Fatalf("refused submit reserved %d slots", got)
	}
}

func TestReviewConcurrentSubmitAndStopNeverDropsAcceptedWork(t *testing.T) {
	proc := newOwnerFakeProcess()
	owner, _ := newTestOwner(t, proc)
	go owner.run()
	start := make(chan struct{})
	type submission struct {
		done <-chan ownerResult
		err  error
	}
	results := make(chan submission, intentQueueMax)
	var workers sync.WaitGroup
	for i := range intentQueueMax {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			kind := itemClientFrame
			if i%2 == 0 {
				kind = itemAccessBump
			}
			done, err := owner.submit(ownerItem{kind: kind, above: 1})
			results <- submission{done, err}
		}()
	}
	stopDone := make(chan struct{})
	go func() { <-start; owner.stop(false, time.Time{}); close(stopDone) }()
	close(start)
	workers.Wait()
	close(results)
	select {
	case <-stopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not join owner")
	}
	for res := range results {
		if res.err != nil {
			if res.done != nil || (!errors.Is(res.err, errOwnerClosing) && !errors.Is(res.err, errBusy)) {
				t.Errorf("refused submit = %+v", res)
			}
			continue
		}
		select {
		case <-res.done:
		default:
			t.Error("shutdown dropped accepted concurrent submit")
		}
	}
	if got := owner.queueUsed.Load(); got != 0 {
		t.Errorf("queueUsed = %d after concurrent stop", got)
	}
}
