package turnbearer

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/toolendpoint"
)

type socketPeers struct{}

func (socketPeers) PeerUID(*net.UnixConn) (uint32, error) { return 1000, nil }
func (socketPeers) PeerPID(*net.UnixConn) (int, error)    { return 1234, nil }

type socketOwner struct{}

func (socketOwner) OwnerUID(string) (uint32, error) { return 1000, nil }

type socketAuthorizer struct{}

func (socketAuthorizer) Admit(_ toolendpoint.Peer, publish func(string, toolendpoint.AdmissionEpoch) bool) (assistant.ToolInvocation, func(), error) {
	if !publish(toolendpoint.TurnScope("run"), toolendpoint.TurnEpoch) {
		return assistant.ToolInvocation{}, nil, toolendpoint.ErrNotEnrolled
	}
	return assistant.ToolInvocation{RunContext: agenttools.RunContext{Session: "pane", RunID: "run"}}, func() {}, nil
}

type socketDispatcher struct{ started, cancelled, release chan struct{} }

func (d *socketDispatcher) Dispatch(inv assistant.ToolInvocation) (string, error) {
	close(d.started)
	<-inv.Context.Done()
	close(d.cancelled)
	<-d.release
	return "", inv.Context.Err()
}
func (*socketDispatcher) Catalogue(content.Grant) []agenttools.Tool { return nil }

func TestToolSessionSettlesRealEndpointBeforeReuse(t *testing.T) {
	d := &socketDispatcher{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	ep, err := toolendpoint.New(toolendpoint.Config{Dir: t.TempDir(), Peers: socketPeers{}, Owner: socketOwner{}, SelfUID: 1000, Auth: socketAuthorizer{}, Dispatch: d, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if err = ep.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ep.Close() }()
	defer func() {
		select {
		case <-d.release:
		default:
			close(d.release)
		}
	}()
	m := NewMinter(ep, "helper")
	session, _, err := m.ToolSessions()(context.Background(), harness.SessionInfo{ConversationID: "conversation", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err = session.BeginTurn(testRunTools("run")); err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", ep.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err = io.WriteString(conn, `{"jsonrpc":"2.0","id":1,"method":"workers.holdings","params":{}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-d.started:
	case <-time.After(5 * time.Second):
		t.Fatal("call not accepted")
	}
	if !session.Pending() {
		t.Fatal("real accepted call not pending")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = session.EndTurn(ctx); err != context.Canceled {
		t.Fatalf("EndTurn: %v", err)
	}
	select {
	case <-d.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("accepted call not cancelled")
	}
	if !session.Pending() {
		t.Fatal("cancelled call disappeared before settlement")
	}
	if err = session.BeginTurn(testRunTools("next")); err == nil {
		t.Fatal("overlapping turn admitted")
	}
	close(d.release)
	settled, cancelSettle := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelSettle()
	if err = session.EndTurn(settled); err != nil {
		t.Fatal(err)
	}
	if session.Pending() {
		t.Fatal("settled call still pending")
	}
	if err = session.BeginTurn(testRunTools("next")); err != nil {
		t.Fatal(err)
	}
	if err = session.Retire(settled); err != nil {
		t.Fatal(err)
	}
}
