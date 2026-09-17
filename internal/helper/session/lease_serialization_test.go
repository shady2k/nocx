package session

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// lockProbeProcess is the fixture both tests below drive: a Process whose
// Write and Read report whether hs.mu was held while they ran, which is
// exactly the fact nocx-6q1uh.3 moved from "held" to "never" for the write
// side and kept at "never" — now structurally, not by discipline — for the
// read side.
type lockProbeProcess struct {
	host *hostSession

	// sawMutexFree is set the one time Write is called, if hs.mu could be
	// acquired at that moment (TryLock succeeding means it was free).
	sawMutexFree bool

	readStarted chan struct{}
	releaseRead <-chan struct{}
}

func (p *lockProbeProcess) Read(b []byte) (int, error) {
	if p.readStarted == nil {
		return 0, io.EOF
	}
	close(p.readStarted)
	<-p.releaseRead
	if len(b) > 0 {
		b[0] = 'x'
	}
	return 1, io.EOF
}

func (p *lockProbeProcess) Write(b []byte) (int, error) {
	if p.host.mu.TryLock() {
		p.host.mu.Unlock()
		p.sawMutexFree = true
	}
	return len(b), nil
}

func (p *lockProbeProcess) Close() error { return nil }

func (p *lockProbeProcess) Resize(context.Context, uint16, uint16, uint16, uint16) error {
	return nil
}

func (p *lockProbeProcess) Done() <-chan struct{} { return make(chan struct{}) }

func (p *lockProbeProcess) Pid() int { return 1 }

func (p *lockProbeProcess) Shell() string { return "probe" }

func (p *lockProbeProcess) WaitErr() (error, bool) { return nil, false }

func (p *lockProbeProcess) ForegroundProcessGroup() (int, error) { return 0, nil }

// TestWriteReleasesTheSessionMutexBeforeTheWrite is the correction of what
// this test used to assert (TestWriteSerializesProcessWriteWithLeaseTransition,
// before nocx-6q1uh.3): that write held hs.mu from its lease check through the
// return of proc.Write, so a lease transition could never land between the
// check and the write. Holding it there is exactly what let a write blocked
// on a program that is not reading its input stall pump's own ingest, which
// needed the same lock (nocx-6q1uh.1) — the bug this whole task exists to
// close.
//
// write now holds hs.mu ONLY for the lease check, and submits the payload to
// the session's I/O owner afterward, off that lock entirely: the owner is
// what now serialises a write against a concurrent lease transition, by
// being the one thing that ever calls proc.Write, one item at a time, in
// arrival order — never by holding a mutex a slow write can stall.
func TestWriteReleasesTheSessionMutexBeforeTheWrite(t *testing.T) {
	var sessionRaw [16]byte
	id := proto.SubscriberID("11111111111111111111111111111111")
	subscriberRaw, err := proto.SessionBytes(string(id))
	if err != nil {
		t.Fatalf("subscriber id: %v", err)
	}
	att := proto.AttachmentID("att-1")
	proc := &lockProbeProcess{}
	rt := pumpRuntime(t, proc)
	hs := &hostSession{
		raw:         sessionRaw,
		proc:        proc,
		log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		subs:        map[proto.SubscriberID]*subscriber{id: {id: id, raw: subscriberRaw}},
		attachments: map[proto.AttachmentID]*attachment{att: {id: att, subscriber: id}},
		writer:      &id,
		writerAtt:   att,
		epoch:       1,
		runtime:     rt,
		win:         newWindow(2 * creditLimit),
	}
	proc.host = hs
	owner := newSessionOwner(proc, rt, hs.win, hs.log)
	rt.SetReplies(owner)
	hs.owner = owner
	go owner.run()
	t.Cleanup(func() { owner.stop(true, time.Time{}) })

	if err := hs.write(nil, proto.SessionFrame{Subscriber: subscriberRaw, Epoch: 1, Payload: []byte("x")}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !proc.sawMutexFree {
		t.Fatal("proc.Write ran while hs.mu was still held: a slow write would stall the " +
			"owner's own drain exactly the way it stalled pump before nocx-6q1uh.3")
	}
}

// TestTheOwnersReadPathNeverTouchesTheSessionMutex is
// TestOutputPumpDoesNotTakeSessionMutex's replacement: hostSession.pump is
// gone (nocx-6q1uh.3), and the property it guarded — the read/ingest path
// never contends with hs.mu — is now structural rather than a discipline to
// verify, because the session's I/O owner holds no reference to hostSession
// or its mutex at all. This exercises it the same way the retired test did:
// hs.mu held for the whole of a read-then-EOF cycle, and the owner must
// still reach proc.Read, ingest the byte and close the window without ever
// needing it.
func TestTheOwnersReadPathNeverTouchesTheSessionMutex(t *testing.T) {
	readStarted := make(chan struct{})
	releaseRead := make(chan struct{})
	proc := &lockProbeProcess{readStarted: readStarted, releaseRead: releaseRead}
	rt := pumpRuntime(t, proc)
	hs := &hostSession{
		proc:    proc,
		win:     newWindow(2 * creditLimit),
		runtime: rt,
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	proc.host = hs
	owner := newSessionOwner(proc, rt, hs.win, hs.log)
	rt.SetReplies(owner)
	hs.owner = owner

	hs.mu.Lock()
	go owner.run()
	t.Cleanup(func() { owner.stop(true, time.Time{}) })

	select {
	case <-readStarted:
	case <-t.Context().Done():
		hs.mu.Unlock()
		close(releaseRead)
		t.Fatal("the owner could not reach proc.Read while hs.mu was held")
	}
	closed := hs.win.changed()
	close(releaseRead)
	select {
	case <-closed:
	case <-t.Context().Done():
		hs.mu.Unlock()
		t.Fatal("the owner's read did not reach the window after proc.Read returned EOF")
	}
	hs.mu.Unlock()
	if !hs.win.isClosed() {
		t.Fatal("the window is not closed after the read side reached EOF")
	}
}
