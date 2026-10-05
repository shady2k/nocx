package client_test

// THE ROWS A HELPER SENDS WHILE IT ANSWERS THE ATTACH (nocx-zg3k3.5.11).
//
// The helper registers a new subscriber and wakes its row pump BEFORE it
// writes the attach's result (internal/helper/session's attach): a coordinator
// that returns after an absence is owed a read-back of everything it did not
// confirm, and the pump sends it the moment the subscriber exists. Those
// frames can reach the coordinator ahead of the attach's own answer — the
// connection is one ordered stream, and the pump and the handler both write
// to it. A consumer registered only once Attach has returned has missed them,
// and the attachment drops a frame nobody watches without a word, so the
// rows the coordinator was owed are gone and its block seals short of the
// command's output (the restart acceptance under load: sealed at 193 of 300).
//
// The scripted session service here does exactly what the helper does, in
// the order it does it: the rows frame, then the result.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// rowsFirstSessions answers `attach` by sending one rows batch to the
// subscriber it names and only then returning the attachment.
type rowsFirstSessions struct {
	mu   sync.Mutex
	host *host.Host
}

func (s *rowsFirstSessions) Name() string  { return proto.ServiceSession }
func (s *rowsFirstSessions) Ops() []string { return []string{proto.OpAttach, proto.OpDetach} }
func (s *rowsFirstSessions) ParamsSchema(string) *host.Schema {
	return host.SchemaFor(json.RawMessage{})
}

func (s *rowsFirstSessions) Call(_ context.Context, op string, params json.RawMessage) (any, error) {
	if op == proto.OpDetach {
		return struct{}{}, nil
	}
	var p proto.AttachParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	session, err := proto.SessionBytes(p.Session.Session)
	if err != nil {
		return nil, err
	}
	raw, err := hex.DecodeString(string(p.Subscriber))
	if err != nil || len(raw) != 16 {
		return nil, errors.New("subscriber")
	}
	var subscriber [16]byte
	copy(subscriber[:], raw)
	doc, _ := json.Marshal(proto.OutputRowsDoc{FromRow: 177, Rows: json.RawMessage(`[{"text":"R178"}]`)})
	s.mu.Lock()
	h := s.host
	s.mu.Unlock()
	if err := h.SendOutputRows(proto.OutputRowsFrame{
		Session: session, Subscriber: subscriber, FromRow: 177, Payload: doc,
	}); err != nil {
		return nil, err
	}
	return proto.AttachResult{
		Attachment:      "att-1",
		Resume:          proto.Resume{Resumed: true, From: p.Offset},
		LifecycleResume: proto.Resume{Resumed: true, From: p.LifecycleOffset},
	}, nil
}

func rowsFirstPeer(svc *rowsFirstSessions) func(io.Reader, io.Writer) int {
	return func(in io.Reader, out io.Writer) int {
		h := host.New(in, out, "testhash", "instance-1", slog.New(slog.NewTextHandler(io.Discard, nil)))
		svc.mu.Lock()
		svc.host = h
		svc.mu.Unlock()
		h.Register(svc)
		if err := h.Serve(context.Background()); err != nil {
			return 1
		}
		return 0
	}
}

func TestRowsTheHelperSendsWhileAnsweringTheAttachReachTheConsumer(t *testing.T) {
	svc := &rowsFirstSessions{}
	conn := newFakeConn(rowsFirstPeer(svc))
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	var mu sync.Mutex
	var got []client.OutputRows
	attached, err := c.Attach(context.Background(), proto.AttachParams{
		Subscriber: "0123456789abcdef0123456789abcdef",
		Session:    proto.HostSessionID{Generation: "g", Session: "00112233445566778899aabbccddeeff"},
		Offset:     0,
	}, client.ObserveBeforeAttach(func(a *client.AttachedSession) {
		a.OnOutputRows(func(o client.OutputRows) {
			mu.Lock()
			got = append(got, o)
			mu.Unlock()
		})
	}))
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer func() { _ = attached.Close() }()

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].FromRow != 177 || len(got[0].Rows) != 1 {
		t.Fatalf("the consumer received %+v, want the one batch (row 177) the helper sent while answering the attach: "+
			"a frame that reaches the attachment before anyone watches it is dropped", got)
	}
}
