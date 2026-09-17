package transport

// nocx-xn63t.6.4: monitorExit used to call unwatchPane — which ends in
// helperRegistry.SessionEnded — BEFORE it called EndSession on the session's
// own channel. Since nocx-xn63t.6.3, SessionEnded legitimately closes a
// far-helper session's shared client once no git binding holds it open, so
// calling it before EndSession could close the transport EndSession's own
// CloseSession round trip needed to tell the far helper the session was
// over. The far helper then never heard it, and kept listing a session this
// coordinator had already given up on — measured against
// e2e/remote-coordinator-reclaim.spec.ts's 60s bound ("the host still lists
// a session it has already ended").
//
// This test proves the ORDER directly: a channel double records WHEN its
// EndSession ran relative to a fake paneAdmissions' SessionEnded, and the
// assertion is that EndSession's message went out first.

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

// orderRecordingAdmissions is a paneAdmissions double that records the
// sequence number it was called at, off a counter the test's channel double
// shares with it.
type orderRecordingAdmissions struct {
	seq *sequenceCounter

	mu    sync.Mutex
	order int // 0 means "not yet called"
}

func (a *orderRecordingAdmissions) SessionEnded(string) {
	n := a.seq.next()
	a.mu.Lock()
	a.order = n
	a.mu.Unlock()
}

func (a *orderRecordingAdmissions) calledAt() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.order
}

// sequenceCounter hands out the next tick, shared between the channel
// double's EndSession and orderRecordingAdmissions' SessionEnded — the
// same counter under one mutex is what makes "which ran first" a fact
// rather than a race between two independent clocks.
type sequenceCounter struct {
	mu sync.Mutex
	n  int
}

func (s *sequenceCounter) next() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return s.n
}

// endSessionOrderChannel is exitEndableChannel's twin, extended to record
// the sequence number EndSession ran at.
type endSessionOrderChannel struct {
	exitEndableChannel
	seq *sequenceCounter

	mu        sync.Mutex
	endedSeq  int
	endCalled bool
}

func newEndSessionOrderChannel(seq *sequenceCounter) *endSessionOrderChannel {
	return &endSessionOrderChannel{exitEndableChannel: *newExitEndableChannel(), seq: seq}
}

func (c *endSessionOrderChannel) EndSession(ctx context.Context) error {
	n := c.seq.next()
	c.mu.Lock()
	c.endedSeq = n
	c.endCalled = true
	c.mu.Unlock()
	return c.exitEndableChannel.EndSession(ctx)
}

func (c *endSessionOrderChannel) endedAt() (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.endedSeq, c.endCalled
}

func TestMonitorExitTellsTheHelperBeforeEndingPaneAdmissions(t *testing.T) {
	logger := log.NewSlogAdapter(slog.New(slog.NewTextHandler(io.Discard, nil)))
	reg := session.New(logger, nil)

	seq := &sequenceCounter{}
	admissions := &orderRecordingAdmissions{seq: seq}
	ws := NewWSServer(logger, reg, WithPaneAdmissions(admissions))

	const sid = session.ID("cccccccccccccccccccccccccccccccc")
	ch := newEndSessionOrderChannel(seq)

	err := ws.ReadoptHostedSession(context.Background(), sid,
		func(ctx context.Context, from uint64) (HostedSessionOpen, error) {
			sess, adoptErr := reg.Adopt(ctx,
				session.Config{Kind: session.KindRemote, Host: "far-host", PaneID: "pane-under-test"},
				sid, ch)
			if adoptErr != nil {
				return HostedSessionOpen{}, adoptErr
			}
			return HostedSessionOpen{Session: sess}, nil
		})
	if err != nil {
		t.Fatalf("adopting the session: %v", err)
	}

	// THE SHELL EXITS on its own — closer 2, monitorExit's own path.
	ch.exitAt(&fakeHelperExitStatus{code: 0})

	waittest.WaitForTimeoutDetail(t, "both EndSession and SessionEnded to have run", wantWithin,
		func() string {
			endedSeq, endCalled := ch.endedAt()
			return "endSession=(" + boolStr(endCalled) + "," + strconv.Itoa(endedSeq) + ") sessionEnded=" + strconv.Itoa(admissions.calledAt())
		},
		func() bool {
			_, endCalled := ch.endedAt()
			return endCalled && admissions.calledAt() != 0
		})

	endedSeq, endCalled := ch.endedAt()
	sessionEndedSeq := admissions.calledAt()
	if !endCalled {
		t.Fatal("the channel's EndSession never ran")
	}
	if sessionEndedSeq == 0 {
		t.Fatal("paneAdmissions.SessionEnded never ran")
	}
	if endedSeq > sessionEndedSeq {
		t.Fatalf("SessionEnded ran (tick %d) before EndSession (tick %d) — a caller reacting to SessionEnded "+
			"(helperRegistry.SessionEnded, which may close the session's shared helper client once no git "+
			"binding holds it) could close the transport before EndSession's own message ever reached the "+
			"far helper, which is exactly the regression this test pins", sessionEndedSeq, endedSeq)
	}
}
