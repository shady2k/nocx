package session

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
)

type lockProbeProcess struct {
	host        *hostSession
	locked      bool
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
		return 0, io.ErrClosedPipe
	}
	p.locked = true
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

func TestWriteSerializesProcessWriteWithLeaseTransition(t *testing.T) {
	var sessionRaw [16]byte
	id := proto.SubscriberID("11111111111111111111111111111111")
	subscriberRaw, err := proto.SessionBytes(string(id))
	if err != nil {
		t.Fatalf("subscriber id: %v", err)
	}
	att := proto.AttachmentID("att-1")
	proc := &lockProbeProcess{}
	hs := &hostSession{
		raw:         sessionRaw,
		proc:        proc,
		log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		subs:        map[proto.SubscriberID]*subscriber{id: {id: id, raw: subscriberRaw}},
		attachments: map[proto.AttachmentID]*attachment{att: {id: att, subscriber: id}},
		writer:      &id,
		writerAtt:   att,
		epoch:       1,
	}
	proc.host = hs

	if err := hs.write(nil, proto.SessionFrame{Subscriber: subscriberRaw, Epoch: 1, Payload: []byte("x")}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !proc.locked {
		t.Fatal("process write ran outside the lease mutex: a concurrent detach could grant the next epoch before these bytes landed")
	}
}

func TestOutputPumpDoesNotTakeSessionMutex(t *testing.T) {
	readStarted := make(chan struct{})
	releaseRead := make(chan struct{})
	proc := &lockProbeProcess{readStarted: readStarted, releaseRead: releaseRead}
	hs := &hostSession{
		proc: proc,
		win:  newWindow(2 * creditLimit),
		log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	hs.mu.Lock()
	pumpDone := make(chan struct{})
	go func() {
		hs.pump()
		close(pumpDone)
	}()
	select {
	case <-readStarted:
	case <-t.Context().Done():
		hs.mu.Unlock()
		close(releaseRead)
		t.Fatal("output pump could not reach proc.Read while s.mu was held")
	}
	close(releaseRead)
	select {
	case <-pumpDone:
		hs.mu.Unlock()
	case <-t.Context().Done():
		hs.mu.Unlock()
		t.Fatal("output pump contended on s.mu after proc.Read")
	}
}
