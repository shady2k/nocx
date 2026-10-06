package session

import (
	"context"
	"testing"
	"time"
)

func TestFenceInputDrainsAnAdmissionAlreadyInProgress(t *testing.T) {
	reg, sess := openWith(t, newRecordingChannel())
	defer func() { _ = reg.Close(sess.ID()) }()
	ref := Ref{ID: sess.ID(), Identity: sess.Identity()}
	entered, release := make(chan struct{}), make(chan struct{})
	admitted := make(chan bool, 1)
	go func() {
		admitted <- reg.WithToolAdmission(ref, func() bool {
			close(entered)
			<-release
			return true
		})
	}()
	<-entered
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
	select {
	case <-committed:
		t.Fatal("fence committed while tool admission was in progress")
	default:
	}
	close(release)
	if !<-admitted {
		t.Fatal("admission did not complete")
	}
	select {
	case <-committed:
	case <-time.After(5 * time.Second):
		t.Fatal("fence did not commit after admission completed")
	}
	if err := waitFenceResult(t, fenced); err != nil {
		t.Fatalf("fence: %v", err)
	}
}
