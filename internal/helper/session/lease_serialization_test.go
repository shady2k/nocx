package session

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
)

type lockProbeProcess struct {
	host   *hostSession
	locked bool
}

func (p *lockProbeProcess) Read([]byte) (int, error) { return 0, io.EOF }

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
