package transport

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/completion"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/ssh"
)

// cancelWatchingCompleter reports the interval its context was alive for: it
// announces that it started and then blocks until either the test releases it
// or the context is cancelled, naming which one happened.
type cancelWatchingCompleter struct {
	entered   chan struct{}
	cancelled chan struct{}
	release   chan struct{}
}

func newCancelWatchingCompleter() *cancelWatchingCompleter {
	return &cancelWatchingCompleter{
		entered:   make(chan struct{}, 1),
		cancelled: make(chan struct{}, 1),
		release:   make(chan struct{}),
	}
}

func (c *cancelWatchingCompleter) Complete(ctx context.Context, _ completion.Request, _ ...ssh.ConnectOption) (*completion.Response, error) {
	select {
	case c.entered <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-c.release:
		return &completion.Response{Candidates: []completion.Candidate{}}, nil
	case <-ctx.Done():
		c.cancelled <- struct{}{}
		return nil, ctx.Err()
	}
}

// cancelRig opens a remote session whose shell.complete is served by a
// completer the test controls, and returns the live socket beside it.
func cancelRig(t *testing.T) (*websocket.Conn, *cancelWatchingCompleter, string) {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)
	reg.WithSSHFactory(&stubSSHFactory{
		connectFn: func(_ context.Context, _ string, _ ...ssh.ConnectOption) (ssh.Channel, error) {
			return ssh.NewStubChannel(logger), nil
		},
	})
	remote := newCancelWatchingCompleter()
	ws := NewWSServer(
		logger,
		reg,
		WithCompleters(completion.NewLocal(), remote),
		WithProfileResolver(&fakeResolver{
			resolveFn: func(_ string) (string, *ssh.ConnectConfig, error) {
				return "target.example", &ssh.ConnectConfig{User: "alice", Port: 22}, nil
			},
		}),
	)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	t.Cleanup(func() { close(remote.release) })

	openResp := vaultCall(t, conn, "open", map[string]any{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
		"kind": "ssh", "profileId": "ssh:test:cancel",
	}, 1)
	if openResp.Error != nil {
		t.Fatalf("open: %+v", openResp.Error)
	}
	var opened struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(openResp.Result, &opened); err != nil || opened.SessionID == "" {
		t.Fatalf("open result: %s", openResp.Result)
	}
	return conn, remote, opened.SessionID
}

func writeFrame(t *testing.T, conn *websocket.Conn, frame map[string]any) {
	t.Helper()
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal %v: %v", frame, err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
		t.Fatalf("write %v: %v", frame, err)
	}
}

// TestRPCCancel_WithdrawsAnInFlightCompletion is the backend half of
// nocx-7jujk's second criterion, over the REAL socket: a superseded keystroke
// must not leave the backend doing work whose answer is discarded. The
// renderer withdraws request 2 while the remote completer is still inside it;
// the completer's context must end, and no result may arrive for that id.
func TestRPCCancel_WithdrawsAnInFlightCompletion(t *testing.T) {
	conn, remote, sessionID := cancelRig(t)

	writeFrame(t, conn, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "shell.complete",
		"params": map[string]any{
			"sessionId": sessionID, "cwd": "/home/alice", "line": "cat rea", "pos": 7,
		},
	})
	select {
	case <-remote.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("remote completer did not start")
	}

	// The keystroke that supersedes it. The renderer sends this as a
	// NOTIFICATION — no id of its own.
	writeFrame(t, conn, map[string]any{
		"jsonrpc": "2.0", "method": "rpc.cancel", "params": map[string]any{"id": 2},
	})

	select {
	case <-remote.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("rpc.cancel did not reach the completer: it was still working after the withdrawal")
	}

	// And the withdrawn request is answered by nothing at all, while the
	// connection goes on serving — a ping after it proves the socket is live
	// rather than merely quiet.
	writeFrame(t, conn, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "transport.ping"})
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("no answer to the ping that followed the withdrawal: %v", err)
		}
		var resp struct {
			ID *int `json:"id"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil || resp.ID == nil {
			continue
		}
		if *resp.ID == 2 {
			t.Fatalf("the withdrawn request answered anyway: %s", raw)
		}
		if *resp.ID == 3 {
			return
		}
	}
}

// TestRPCCancel_UnknownIDIsIdempotent — a withdrawal races the answer it is
// withdrawing, so an id the server no longer holds is normal, not an error.
// It must not disturb the connection.
func TestRPCCancel_UnknownIDIsIdempotent(t *testing.T) {
	conn, _, _ := cancelRig(t)

	writeFrame(t, conn, map[string]any{
		"jsonrpc": "2.0", "method": "rpc.cancel", "params": map[string]any{"id": 4242},
	})
	writeFrame(t, conn, map[string]any{"jsonrpc": "2.0", "id": 5, "method": "transport.ping"})

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("connection did not survive a withdrawal for an unknown id: %v", err)
		}
		var resp struct {
			ID    *int             `json:"id"`
			Error *jsonrpcErrorObj `json:"error"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil || resp.ID == nil {
			continue
		}
		if *resp.ID == 5 {
			if resp.Error != nil {
				t.Fatalf("ping refused after an unknown-id withdrawal: %+v", resp.Error)
			}
			return
		}
	}
}

// TestRPCCancel_LeavesAMutatingMethodAlone — only the read-only completion
// calls are withdrawable. A resize the renderer has already asked for must
// not be cancellable by a stray withdrawal carrying its id.
func TestRPCCancel_LeavesAMutatingMethodAlone(t *testing.T) {
	conn, _, sessionID := cancelRig(t)

	writeFrame(t, conn, map[string]any{
		"jsonrpc": "2.0", "id": 6, "method": "resize",
		"params": map[string]any{"sessionId": sessionID, "cols": 100, "rows": 30},
	})
	writeFrame(t, conn, map[string]any{
		"jsonrpc": "2.0", "method": "rpc.cancel", "params": map[string]any{"id": 6},
	})

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("resize never answered: %v", err)
		}
		var resp struct {
			ID    *int             `json:"id"`
			Error *jsonrpcErrorObj `json:"error"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil || resp.ID == nil {
			continue
		}
		if *resp.ID == 6 {
			if resp.Error != nil {
				t.Fatalf("resize was disturbed by a withdrawal it is not subject to: %+v", resp.Error)
			}
			return
		}
	}
}
