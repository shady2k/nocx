package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/waittest"
)

// ── connection-password ask, server → client ────────────────────────────

func TestPasswordRequest_NotifiesConnectedClient(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithVaultLifecycle(newFakeVaultLifecycle()))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck

	// The dial returns as soon as the socket is open, which is BEFORE the
	// server has registered the connection — broadcastAsk reads s.conns, so
	// asking too early answers "no client connected" and the read below waits
	// for a notification that was never sent. Wait for the registration the
	// broadcast actually consults; this is the race, not a slow machine.
	waitForConns(t, ws, 1)

	done := make(chan error, 1)
	go func() {
		_, err := ws.RequestConnectionPassword(ctx, ssh.PasswordRequest{
			Connection: "prod-web",
			User:       "deploy",
			Host:       "web.example.com",
			Reason:     "no password is stored for this connection",
		})
		done <- err
	}()

	// The connected client receives a connections.passwordRequest
	// notification naming the connection and the account (nocx-s8jn) —
	// the prompt must know which password it is asking for.
	_ = conn.SetReadDeadline(time.Now().Add(wantWithin))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("expected password request notification, got error: %v", err)
	}
	var notif struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  struct {
			RequestID  string `json:"requestId"`
			Connection string `json:"connection"`
			User       string `json:"user"`
			Host       string `json:"host"`
			Reason     string `json:"reason"`
		} `json:"params"`
	}
	if err := json.Unmarshal(data, &notif); err != nil {
		t.Fatalf("unmarshal notification: %v", err)
	}
	if notif.Method != "connections.passwordRequest" {
		t.Errorf("expected method connections.passwordRequest, got %q", notif.Method)
	}
	if notif.Params.RequestID == "" {
		t.Error("expected non-empty requestId")
	}
	if notif.Params.Connection != "prod-web" || notif.Params.User != "deploy" || notif.Params.Host != "web.example.com" {
		t.Errorf("notification does not name the connection and account: %+v", notif.Params)
	}
	if notif.Params.Reason != "no password is stored for this connection" {
		t.Errorf("reason = %q", notif.Params.Reason)
	}

	// Resolve it so the asker does not leak a pending request.
	resp := vaultCall(t, conn, "connections.passwordResolved", map[string]any{
		"requestId": notif.Params.RequestID,
		"outcome":   "cancelled",
	}, 2)
	if resp.Error != nil {
		t.Fatalf("passwordResolved error: %s", resp.Error.Message)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrPasswordPromptCancelled) {
			t.Fatalf("RequestConnectionPassword = %v, want ErrPasswordPromptCancelled", err)
		}
	case <-time.After(wantWithin):
		t.Fatal("RequestConnectionPassword did not resolve")
	}
}

// TestPasswordRequest_NoClientReturnsError pins the FIRST distinct outcome:
// no renderer attached → ErrPasswordNoClientConnected, with its own
// message — never the unlock ask's ErrNoClientConnected, which is a
// different question with a different fix.
func TestPasswordRequest_NoClientReturnsError(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithVaultLifecycle(newFakeVaultLifecycle()))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	_, err := ws.RequestConnectionPassword(ctx, ssh.PasswordRequest{Connection: "p", User: "u", Host: "h"})
	if !errors.Is(err, ErrPasswordNoClientConnected) {
		t.Fatalf("expected ErrPasswordNoClientConnected, got %v", err)
	}
	if err.Error() == ErrNoClientConnected.Error() {
		t.Fatal("password no-client message must be distinct from the unlock ask's")
	}
}

// TestPasswordRequest_SubmittedWithRemember pins the happy path: the answer
// carries the password AND the remember request, and the asker returns both.
func TestPasswordRequest_SubmittedWithRemember(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithVaultLifecycle(newFakeVaultLifecycle()))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck

	// Same race as TestPasswordRequest_NotifiesConnectedClient documents: the
	// dial returns before the server registers the connection, and a broadcast
	// that finds no client is not retried, so the read below can only end at
	// its deadline (nocx-yht3).
	waitForConns(t, ws, 1)

	done := make(chan error, 1)
	var answer ssh.PasswordAnswer
	go func() {
		var err error
		answer, err = ws.RequestConnectionPassword(ctx, ssh.PasswordRequest{Connection: "p", User: "u", Host: "h"})
		done <- err
	}()

	_ = conn.SetReadDeadline(time.Now().Add(wantWithin))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read notification: %v", err)
	}
	var notif struct {
		Params struct {
			RequestID string `json:"requestId"`
		} `json:"params"`
	}
	if err := json.Unmarshal(data, &notif); err != nil {
		t.Fatalf("unmarshal notification: %v", err)
	}

	resp := vaultCall(t, conn, "connections.passwordResolved", map[string]any{
		"requestId": notif.Params.RequestID,
		"outcome":   "submitted",
		"password":  "hunter2",
		"remember":  true,
	}, 2)
	if resp.Error != nil {
		t.Fatalf("passwordResolved error: %s", resp.Error.Message)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RequestConnectionPassword: %v", err)
		}
	case <-time.After(wantWithin):
		t.Fatal("RequestConnectionPassword did not resolve")
	}
	if answer.Password != "hunter2" || !answer.Remember {
		t.Errorf("answer = %+v, want the submitted password with remember", answer)
	}
}

// TestPasswordRequest_SubmittedUseOnce pins the decline path: remember is
// not set, the password is still returned — the caller uses it once and
// stores nothing.
func TestPasswordRequest_SubmittedUseOnce(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithVaultLifecycle(newFakeVaultLifecycle()))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck

	// Same race as TestPasswordRequest_NotifiesConnectedClient documents: the
	// dial returns before the server registers the connection, and a broadcast
	// that finds no client is not retried, so the read below can only end at
	// its deadline (nocx-yht3).
	waitForConns(t, ws, 1)

	done := make(chan error, 1)
	var answer ssh.PasswordAnswer
	go func() {
		var err error
		answer, err = ws.RequestConnectionPassword(ctx, ssh.PasswordRequest{Connection: "p", User: "u", Host: "h"})
		done <- err
	}()

	_ = conn.SetReadDeadline(time.Now().Add(wantWithin))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read notification: %v", err)
	}
	var notif struct {
		Params struct {
			RequestID string `json:"requestId"`
		} `json:"params"`
	}
	if err := json.Unmarshal(data, &notif); err != nil {
		t.Fatalf("unmarshal notification: %v", err)
	}

	resp := vaultCall(t, conn, "connections.passwordResolved", map[string]any{
		"requestId": notif.Params.RequestID,
		"outcome":   "submitted",
		"password":  "once",
		"remember":  false,
	}, 2)
	if resp.Error != nil {
		t.Fatalf("passwordResolved error: %s", resp.Error.Message)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RequestConnectionPassword: %v", err)
		}
	case <-time.After(wantWithin):
		t.Fatal("RequestConnectionPassword did not resolve")
	}
	if answer.Password != "once" || answer.Remember {
		t.Errorf("answer = %+v, want the password with remember=false", answer)
	}
}

// TestPasswordRequest_Cancelled pins the THIRD distinct outcome: the user
// dismissed the prompt → ErrPasswordPromptCancelled with its own message.
func TestPasswordRequest_Cancelled(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithVaultLifecycle(newFakeVaultLifecycle()))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck

	// Same race as TestPasswordRequest_NotifiesConnectedClient documents: the
	// dial returns before the server registers the connection, and a broadcast
	// that finds no client is not retried, so the read below can only end at
	// its deadline (nocx-yht3).
	waitForConns(t, ws, 1)

	done := make(chan error, 1)
	go func() {
		_, err := ws.RequestConnectionPassword(ctx, ssh.PasswordRequest{Connection: "p", User: "u", Host: "h"})
		done <- err
	}()

	_ = conn.SetReadDeadline(time.Now().Add(wantWithin))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read notification: %v", err)
	}
	var notif struct {
		Params struct {
			RequestID string `json:"requestId"`
		} `json:"params"`
	}
	if err := json.Unmarshal(data, &notif); err != nil {
		t.Fatalf("unmarshal notification: %v", err)
	}

	resp := vaultCall(t, conn, "connections.passwordResolved", map[string]any{
		"requestId": notif.Params.RequestID,
		"outcome":   "cancelled",
	}, 2)
	if resp.Error != nil {
		t.Fatalf("passwordResolved error: %s", resp.Error.Message)
	}

	select {
	case err := <-done:
		if !errors.Is(err, ErrPasswordPromptCancelled) {
			t.Fatalf("expected ErrPasswordPromptCancelled, got %v", err)
		}
	case <-time.After(wantWithin):
		t.Fatal("RequestConnectionPassword did not resolve")
	}
}

// TestPasswordRequest_ContextCancelled verifies a cancelled ask context
// wakes the asker and drops the pending request.
func TestPasswordRequest_ContextCancelled(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithVaultLifecycle(newFakeVaultLifecycle()))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck

	// Same race as TestPasswordRequest_NotifiesConnectedClient documents: the
	// dial returns before the server registers the connection, and a broadcast
	// that finds no client is not retried, so the read below can only end at
	// its deadline (nocx-yht3).
	waitForConns(t, ws, 1)

	cancelCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := ws.RequestConnectionPassword(cancelCtx, ssh.PasswordRequest{Connection: "p", User: "u", Host: "h"})
		done <- err
	}()

	// Drain the notification so we know it was sent.
	_ = conn.SetReadDeadline(time.Now().Add(wantWithin))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read notification: %v", err)
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(wantWithin):
		t.Fatal("RequestConnectionPassword did not resolve after cancel")
	}
}

// TestPasswordResolved_UnknownRequestID verifies a resolution for a
// request that was never asked (or already answered) is an error, not a
// silent no-op.
func TestPasswordResolved_UnknownRequestID(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithVaultLifecycle(newFakeVaultLifecycle()))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck

	// Same race as TestPasswordRequest_NotifiesConnectedClient documents: the
	// dial returns before the server registers the connection, and a broadcast
	// that finds no client is not retried, so the read below can only end at
	// its deadline (nocx-yht3).
	waitForConns(t, ws, 1)

	resp := vaultCall(t, conn, "connections.passwordResolved", map[string]any{
		"requestId": "nonexistent",
		"outcome":   "cancelled",
	}, 1)
	if resp.Error == nil {
		t.Fatal("expected an error for an unknown request id")
	}
}

// waitForConns blocks until the server has registered n connections, which is
// what broadcastAsk consults — a dial that has returned is not yet a client.
func waitForConns(t *testing.T, ws *WSServer, n int) {
	t.Helper()
	registered := func() int {
		ws.connsMu.Lock()
		defer ws.connsMu.Unlock()
		return len(ws.conns)
	}
	waittest.WaitForTimeoutDetail(t, fmt.Sprintf("server registration of %d connection(s)", n), wantWithin,
		func() string {
			return fmt.Sprintf("server registered %d connection(s) within %s", registered(), wantWithin)
		},
		func() bool { return registered() >= n })
}
