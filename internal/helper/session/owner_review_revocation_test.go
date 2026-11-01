package session

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/sessionruntime"
)

type reviewReadHookProcess struct {
	*rawReaderFakeProcess
	onRead func()
	once   sync.Once
}

func (p *reviewReadHookProcess) RawReadChunk(buf []byte) (int, bool, error) {
	p.once.Do(p.onRead)
	return p.rawReaderFakeProcess.RawReadChunk(buf)
}

func TestReviewRevocationQueuedDuringReadPrecedesConditionalCommit(t *testing.T) {
	proc := &reviewReadHookProcess{rawReaderFakeProcess: newRawReaderFakeProcess()}
	owner, rt := newTestOwner(t, proc)
	ctrl := grantAgent(t, rt)
	hs := &hostSession{owner: owner, runtime: rt, now: time.Now}
	hs.tokens = newTokenBook(rt.Incarnation(), time.Now)
	owner.SetTokens(hs.tokens)
	tok := mintRegionToken(t, hs)
	victim := submitIntent(t, hs, ctrl, tok, "must-not-write", 1<<62)
	owner.dispatchIncoming(<-owner.incoming)
	var bump <-chan ownerResult
	proc.onRead = func() { bump = submitBump(t, hs, tok.AccessEpoch) }
	// runWriter is the actual handoff seam; no owner event loop can consume
	// the newly enqueued bump until this single advance call returns.
	go owner.runWriter()
	defer close(owner.writeReq)
	defer func() { _ = proc.Close(); owner.cancelRead() }()
	owner.advance()
	if owner.writerBusy {
		// Consume the outcome so failure cleanup does not leave a goroutine.
		owner.completeWrite(<-owner.writeRes)
		t.Error("conditional intent reached writer before already-queued revocation")
	}
	owner.dispatchIncoming(<-owner.incoming)
	if res := awaitResult(t, bump); res.Epoch <= tok.AccessEpoch {
		t.Fatalf("bump = %+v", res)
	}
	if res := awaitResult(t, victim); !errors.Is(res.Err, errAccessRevoked) || res.State != sessionruntime.IntentStateRefused {
		t.Fatalf("conditional input after queued revocation = %+v", res)
	}
	if got := proc.writtenPayloads(); len(got) != 0 {
		t.Fatalf("revoked input wrote %q", got)
	}
}
