package app

import (
	"bytes"
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/mcpstdio"
	"github.com/shady2k/nocx/internal/turnbearer"
)

// Cut a real endpoint frame before its newline, then hold the write result
// until the session has been retired and its replacement is running.
type retirementRetryDialer struct {
	entered chan struct{}
	release chan struct{}
	dials   atomic.Int32
	once    sync.Once
}

func (d *retirementRetryDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	d.dials.Add(1)
	return &retirementRetryConn{Conn: conn, owner: d}, nil
}

type retirementRetryConn struct {
	net.Conn
	owner *retirementRetryDialer
}

func (c *retirementRetryConn) Write(p []byte) (int, error) {
	cut := false
	if bytes.Contains(p, []byte(`"method":"alpha.call"`)) {
		c.owner.once.Do(func() { cut = true })
	}
	if !cut {
		return c.Conn.Write(p)
	}
	n, err := c.Conn.Write(p[:len(p)-1])
	if err != nil {
		return n, err
	}
	close(c.owner.entered)
	<-c.owner.release
	return n, nil
}

func TestSessionBearerIncompleteFrameRetryCannotReachReplacement(t *testing.T) {
	stand := newTurnStand(t, &turnDispatcher{})
	old := &testTurnTools{ctx: agenttools.RunContext{RunID: "retry-N", Session: string(stand.sess.ID())}, grant: turnGrantForTest(), dispatcher: stand.dispatcher}
	bridge, retire, err := mintTurn(t, stand, turnbearer.Authority{Tools: old})
	if err != nil {
		t.Fatal(err)
	}
	dialer := &retirementRetryDialer{entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(dialer.release) }) })
	client := newS14MCPClientWithConfig(t, mcpstdio.Config{Socket: stand.endpoint.SocketPath(), TurnToken: envToken(t, bridge.Env), Dialer: dialer, Logger: logtest.Slog(t)})
	response := client.startCall(t, "alpha.call", map[string]any{})
	awaitLateSignal(t, dialer.entered, "incomplete request write")
	before := dialer.dials.Load()
	retire()
	next := &testTurnTools{ctx: agenttools.RunContext{RunID: "retry-N+1", Session: string(stand.sess.ID())}, grant: turnGrantForTest(), dispatcher: stand.dispatcher}
	replacement, _, err := mintTurn(t, stand, turnbearer.Authority{Tools: next})
	if err != nil {
		t.Fatal(err)
	}
	release.Do(func() { close(dialer.release) })
	if resp := s14Await(t, response); resp.failed == "" {
		t.Fatal("old incomplete frame retry succeeded")
	}
	if got := dialer.dials.Load(); got != before+1 {
		t.Fatalf("retry did not reconnect exactly once: before %d after %d", before, got)
	}
	if next.admissions != 0 {
		t.Fatal("old retry admitted under replacement authority")
	}
	if calls := stand.dispatcher.recorded(); len(calls) != 0 {
		t.Fatalf("incomplete request reached dispatch: %+v", calls)
	}
	if resp := callWithToken(t, stand.endpoint.SocketPath(), envToken(t, replacement.Env), "alpha.call", map[string]any{}); resp.failed != "" {
		t.Fatal(resp.failed)
	}
}
