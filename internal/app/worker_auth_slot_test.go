package app

import (
	"context"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/peerpin"
	"github.com/shady2k/nocx/internal/toolendpoint"
)

func TestToolAuthorizerRefusesASecondLiveCallerForTheSameSession(t *testing.T) {
	reg, sess, grid := openWorkerAuthSession(t)
	const ownedPID = 4242
	if err := reg.RecordOwnedProcessPID(sess.ID(), ownedPID); err != nil {
		t.Fatalf("record owned process pid: %v", err)
	}
	if err := grid.Watch(string(sess.ID()), 80, 24); err != nil {
		t.Fatalf("enrol session grid: %v", err)
	}

	pinner := &workerAuthPinner{
		root:   peerpin.Root{PID: ownedPID, StartTime: time.Unix(123, 0)},
		member: map[int]bool{9001: true},
	}
	auth := mustToolAuthorizer(t, pinner, reg, grid, emptyWorkerRecord(), workerTestWorkspace, allowWorkerApproval{})
	peer := toolendpoint.Peer{UID: 1000, PID: 9001}
	if _, _, err := auth.Admit(peer); err != nil {
		t.Fatalf("first caller admission: %v", err)
	}

	if _, _, err := auth.Admit(peer); err == nil {
		t.Fatal("second live caller admitted")
	} else if got, want := err.Error(), "session already has a worker caller"; got != want {
		t.Fatalf("second caller error = %q, want %q", got, want)
	}
}

func TestToolAuthorizerReleasesSlotAfterConnectionAndInFlightCallSettle(t *testing.T) {
	reg, sess, grid := openWorkerAuthSession(t)
	const ownedPID = 4242
	if err := reg.RecordOwnedProcessPID(sess.ID(), ownedPID); err != nil {
		t.Fatalf("record owned process pid: %v", err)
	}
	if err := grid.Watch(string(sess.ID()), 80, 24); err != nil {
		t.Fatalf("enrol session grid: %v", err)
	}

	pinner := &workerAuthPinner{
		root:   peerpin.Root{PID: ownedPID, StartTime: time.Unix(123, 0)},
		member: map[int]bool{9001: true},
	}
	auth := mustToolAuthorizer(t, pinner, reg, grid, emptyWorkerRecord(), workerTestWorkspace, allowWorkerApproval{})
	peer := toolendpoint.Peer{UID: 1000, PID: 9001}
	first, release, err := auth.Admit(peer)
	if err != nil {
		t.Fatalf("first caller admission: %v", err)
	}
	t.Cleanup(release)

	connectionCtx, closeConnection := context.WithCancel(first.Context)
	requestCtx, finishRequest := context.WithCancel(connectionCtx)
	requestStopped := make(chan struct{})
	go func() {
		<-requestCtx.Done()
		close(requestStopped)
	}()

	if _, _, admitErr := auth.Admit(peer); admitErr == nil {
		t.Fatal("replacement admitted while first caller was live")
	} else if got, want := admitErr.Error(), "session already has a worker caller"; got != want {
		t.Fatalf("live caller refusal = %q, want %q", got, want)
	}

	closeConnection()
	if _, _, admitErr := auth.Admit(peer); admitErr == nil {
		t.Fatal("replacement admitted before the in-flight call settled")
	} else if got, want := admitErr.Error(), "session already has a worker caller"; got != want {
		t.Fatalf("early replacement error = %q, want %q", got, want)
	}

	finishRequest()
	select {
	case <-requestStopped:
	case <-time.After(time.Second):
		t.Fatal("in-flight call did not settle")
	}
	release()

	replacement, replacementRelease, err := auth.Admit(peer)
	if err != nil {
		t.Fatalf("replacement admission after connection close: %v", err)
	}
	t.Cleanup(replacementRelease)
	if replacement.RunContext.Session != string(sess.ID()) {
		t.Fatalf("replacement session = %q, want %q", replacement.RunContext.Session, sess.ID())
	}
	release()
	if _, _, err := auth.Admit(peer); err == nil {
		t.Fatal("an old admission release freed the replacement slot")
	}
}
