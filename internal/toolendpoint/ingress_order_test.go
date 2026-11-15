package toolendpoint

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/assistant"
)

type ingressOrderKey struct{}

// The first call is delayed before its approval pipeline. The later mutation
// reaches dispatch first, so only ingress reservation can keep it behind the no.
type ingressApprovalDispatcher struct {
	testDispatcher
	mu       sync.Mutex
	tail     chan struct{}
	later    chan struct{}
	declined bool
	mutated  bool
}

func (d *ingressApprovalDispatcher) ReserveInvocation(inv assistant.ToolInvocation) (assistant.ToolInvocation, func()) {
	d.mu.Lock()
	previous := d.tail
	done := make(chan struct{})
	d.tail = done
	d.mu.Unlock()
	inv.Context = context.WithValue(inv.Context, ingressOrderKey{}, previous)
	return inv, func() { close(done) }
}

func (d *ingressApprovalDispatcher) Dispatch(inv assistant.ToolInvocation) (string, error) {
	if inv.Method == "approval" {
		<-d.later
	} else {
		close(d.later)
	}
	if previous, ok := inv.Context.Value(ingressOrderKey{}).(chan struct{}); ok && previous != nil {
		<-previous
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if inv.Method == "approval" {
		d.declined = true
	} else if !d.declined {
		d.mutated = true
	}
	return `{}`, nil
}

func TestEndpointReservesApprovalBeforeLaterMutation(t *testing.T) {
	selected := &ingressApprovalDispatcher{later: make(chan struct{})}
	auth := &testAuthorizer{inv: assistant.ToolInvocation{Dispatcher: selected}}
	ep := startEndpoint(t, endpointConfig(t, auth, &testDispatcher{}))
	conn := dialEndpoint(t, ep)
	_, err := io.WriteString(conn, `{"jsonrpc":"2.0","id":1,"method":"approval","params":{}}`+"\n"+`{"jsonrpc":"2.0","id":2,"method":"mutation","params":{}}`+"\n")
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(conn)
	for range 2 {
		var response rpcResponse
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		if response.Error != nil {
			t.Fatalf("response: %+v", response)
		}
	}
	selected.mu.Lock()
	defer selected.mu.Unlock()
	if selected.mutated {
		t.Fatal("later mutation ran before the earlier approval was declined")
	}
}
