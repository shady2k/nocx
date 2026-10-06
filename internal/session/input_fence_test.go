package session

import (
	"context"
	"errors"
	"io"
	"runtime"
	"testing"
	"time"
)

type fenceBlockingChannel struct{ *stuckChannel }

func newFenceBlockingChannel() *fenceBlockingChannel {
	return &fenceBlockingChannel{stuckChannel: newStuckChannel()}
}

func (c *fenceBlockingChannel) Read([]byte) (int, error) {
	<-c.done
	return 0, io.EOF
}

func waitFenceResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("input fence did not settle")
		return nil
	}
}

func waitForGateFence(t *testing.T, gate *inputGate) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		gate.mu.Lock()
		fencing := gate.fencing
		gate.mu.Unlock()
		if fencing {
			return
		}
		select {
		case <-deadline:
			t.Fatal("input fence did not close admission")
		default:
			runtime.Gosched()
		}
	}
}

func TestFenceInputDrainsQueuedAndDirectWritesBeforeCommit(t *testing.T) {
	ch := newFenceBlockingChannel()
	reg, sess := openWith(t, ch)
	defer func() { _ = reg.Close(sess.ID()) }()
	ref := Ref{ID: sess.ID(), Identity: sess.Identity()}

	direct := make(chan error, 1)
	go func() {
		_, err := sess.Write([]byte("direct"))
		direct <- err
	}()
	<-ch.writeEntered
	if !sess.EnqueueWrite([]byte("queued")) {
		t.Fatal("queued write was refused")
	}

	committed := make(chan struct{})
	fenced := make(chan error, 1)
	go func() {
		fenced <- reg.FenceInput(context.Background(), ref, func() error {
			close(committed)
			return nil
		}, nil)
	}()
	real, ok := sess.(*realSession)
	if !ok {
		t.Fatalf("registry session type: %T", sess)
	}
	waitForGateFence(t, real.inputGate)
	if sess.EnqueueWrite([]byte("late")) {
		t.Fatal("fence admitted fresh input")
	}
	select {
	case <-committed:
		t.Fatal("commit ran before prior writes drained")
	default:
	}
	_ = ch.Close()
	if err := <-direct; err == nil {
		t.Fatal("direct write unexpectedly succeeded")
	}
	if err := waitFenceResult(t, fenced); err != nil {
		t.Fatalf("fence: %v", err)
	}
	if sess.EnqueueWrite([]byte("retired")) {
		t.Fatal("retired source admitted input")
	}
}

func TestFenceInputFailureAndCancellationResumeAdmission(t *testing.T) {
	ch := newFenceBlockingChannel()
	reg, sess := openWith(t, ch)
	defer func() { _ = reg.Close(sess.ID()) }()
	ref := Ref{ID: sess.ID(), Identity: sess.Identity()}
	commitErr := errors.New("commit failed")
	if err := reg.FenceInput(context.Background(), ref, func() error { return commitErr }, nil); !errors.Is(err, commitErr) {
		t.Fatalf("commit error = %v", err)
	}
	if !sess.EnqueueWrite([]byte("resumed")) {
		t.Fatal("failed commit did not resume input")
	}
	<-ch.writeEntered

	ctx, cancel := context.WithCancel(context.Background())
	fenced := make(chan error, 1)
	go func() { fenced <- reg.FenceInput(ctx, ref, func() error { return nil }, nil) }()
	real, ok := sess.(*realSession)
	if !ok {
		t.Fatalf("registry session type: %T", sess)
	}
	waitForGateFence(t, real.inputGate)
	cancel()
	if err := waitFenceResult(t, fenced); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled fence error = %v", err)
	}
	if !sess.EnqueueWrite([]byte("after cancellation")) {
		t.Fatal("cancelled fence did not resume input")
	}
	_ = ch.Close()
}

func TestFenceInputWaitsForConditionalDiscardAndRejectsStaleRef(t *testing.T) {
	ch := newRecordingChannel()
	reg, sess := openWith(t, ch)
	defer func() { _ = reg.Close(sess.ID()) }()
	ref := Ref{ID: sess.ID(), Identity: sess.Identity()}
	settled := make(chan bool, 1)
	if !sess.EnqueueInputIf([]byte("discard"), func() bool { return false }, func(written bool, err error) {
		if err != nil {
			t.Errorf("conditional discard error: %v", err)
		}
		settled <- written
	}) {
		t.Fatal("conditional write was refused")
	}
	select {
	case written := <-settled:
		if written {
			t.Fatal("conditional write was not discarded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("conditional write did not settle")
	}
	if err := reg.FenceInput(context.Background(), ref, func() error { return nil }, nil); err != nil {
		t.Fatalf("fence: %v", err)
	}
	if reg.InputAllowed(sess.ID()) || reg.ToolAdmissionAllowed(sess.ID()) {
		t.Fatal("retired source still reports admission allowed")
	}
	stale := ref
	stale.Identity.Epoch++
	if err := reg.WithInput(context.Background(), stale, func() error { return nil }); !errors.Is(err, ErrInputFenced) {
		t.Fatalf("stale input error = %v", err)
	}
	if err := reg.FenceInput(context.Background(), stale, func() error { return nil }, nil); !errors.Is(err, ErrInputFenced) {
		t.Fatalf("stale fence error = %v", err)
	}
}
