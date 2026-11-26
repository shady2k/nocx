package host_test

// A connection that asked `git` or `fs` is doing panel work, and the daemon's
// connected-idle bound is a question about a client on its way to a spawn
// (nocx-oalo1.12.5). The DISPATCHER is where that fact is born — it is the one
// place that knows which service a request named — so the fact is observed
// here, at a real host over a real frame, and the session service learns it
// through the host.PanelWork capability.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// panelObserver is the capability's implementation on the test's side: it
// records every connection it was told has done panel work.
type panelObserver struct {
	mu    sync.Mutex
	calls []*host.Host
}

func (o *panelObserver) ConnectionPanelWork(h *host.Host) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, h)
}

func (o *panelObserver) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.calls)
}

// serveRequests runs one real host over the given request frames, with the
// services the caller registered, until the input ends.
func serveRequests(t *testing.T, services []host.Service, requests ...proto.Request) {
	t.Helper()
	var frames [][]byte
	frames = append(frames, proto.EncodeFrame(proto.TypeHello, 0, 0, mustJSON(proto.Hello{Version: proto.Version, Nonce: "n"})))
	for _, req := range requests {
		frames = append(frames, proto.EncodeFrame(proto.TypeRequest, 0, 0, mustJSON(req)))
	}
	var in []byte
	for _, f := range frames {
		in = append(in, f...)
	}
	h := host.New(bytes.NewReader(in), io.Discard, "hash", "inst", discardLogger())
	for _, s := range services {
		h.Register(s)
	}
	if err := h.Serve(context.Background()); err != nil {
		t.Fatalf("serve: %v", err)
	}
}

func answeringService(name string, ops ...string) *fakeService {
	table := make(map[string]any, len(ops))
	for _, op := range ops {
		table[op] = struct{}{}
	}
	return &fakeService{
		name: name,
		ops:  table,
		callFn: func(context.Context, string, json.RawMessage) (any, error) {
			return struct{}{}, nil
		},
	}
}

// TestAPanelRequestMarksItsConnection: the first request that names a panel
// service tells the session service this connection is working, and exactly
// once — the fact is a property of the connection, not of each request.
func TestAPanelRequestMarksItsConnection(t *testing.T) {
	for _, service := range []string{proto.ServiceGit, proto.ServiceFS} {
		t.Run(service, func(t *testing.T) {
			observer := &panelObserver{}
			panel := answeringService(service, "op")
			sessions := &observedPanelService{fakeService: answeringService(proto.ServiceSession, "attach"), observer: observer}

			serveRequests(t, []host.Service{sessions, panel},
				proto.Request{ID: 1, Service: service, Op: "op"},
				proto.Request{ID: 2, Service: service, Op: "op"},
			)

			if got := observer.count(); got != 1 {
				t.Fatalf("%s requests notified the session service %d times, want exactly 1", service, got)
			}
			if !sessions.hostPanelWorked() {
				t.Fatalf("a %s request left its connection unmarked", service)
			}
		})
	}
}

// TestASessionRequestDoesNotMarkItsConnection is the other half of nocx-398ca:
// a client that asked the session service something — the attach a probe or a
// spawn begins with — is still a client on its way to a spawn, and the bound
// must go on asking.
func TestASessionRequestDoesNotMarkItsConnection(t *testing.T) {
	observer := &panelObserver{}
	sessions := &observedPanelService{fakeService: answeringService(proto.ServiceSession, "attach"), observer: observer}
	serveRequests(t, []host.Service{sessions}, proto.Request{ID: 1, Service: proto.ServiceSession, Op: "attach"})

	if got := observer.count(); got != 0 {
		t.Fatalf("a session request panel-marked its connection %d times, want 0", got)
	}
	if sessions.hostPanelWorked() {
		t.Fatal("a session request marked its connection as panel work")
	}
}

// TestANamedPanelServiceMarksEvenWhenThisBuildDoesNotServeIt: the mark is the
// client's own fact — it came here to do panel work — not a fact about the
// build's composition. A generation without `git` would otherwise retire a
// panel client as if it had asked nothing at all.
func TestANamedPanelServiceMarksEvenWhenThisBuildDoesNotServeIt(t *testing.T) {
	observer := &panelObserver{}
	sessions := &observedPanelService{fakeService: answeringService(proto.ServiceSession, "attach"), observer: observer}
	serveRequests(t, []host.Service{sessions}, proto.Request{ID: 1, Service: proto.ServiceGit, Op: "status"})

	if got := observer.count(); got != 1 {
		t.Fatalf("a request for an unserved panel service notified %d times, want 1", got)
	}
}

// observedPanelService is a session service that carries the host.PanelWork
// capability, which is how cmd/nocx-helper's real session service learns the
// fact.
type observedPanelService struct {
	*fakeService
	observer *panelObserver
	mu       sync.Mutex
	h        *host.Host
}

func (s *observedPanelService) ConnectionPanelWork(h *host.Host) {
	s.mu.Lock()
	s.h = h
	s.mu.Unlock()
	s.observer.ConnectionPanelWork(h)
}

func (s *observedPanelService) hostPanelWorked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.h != nil && s.h.PanelWorked()
}
