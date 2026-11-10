package transport

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

// ── unlock request notification, server → client ──────────────────────

func TestUnlockRequest_NotifiesConnectedClient(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithVaultLifecycle(newFakeVaultLifecycle()))
	// CANCELLABLE, AND CANCELLED (nocx-ouru5.1.16). This test is about the
	// notification, not the answer, so it never resolves the ask — and with a
	// background context the goroutine below waited on the broker for the rest
	// of the package's run: the goroutine dump from the gated run still had it
	// nine minutes in. Ending the context when the test ends is what lets it
	// return.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck

	// Verify the connection is registered by doing a simple RPC round-trip.
	resp := vaultCall(t, conn, "vault.status", nil, 1)
	if resp.Error != nil {
		t.Fatalf("vault.status failed: %s", resp.Error.Message)
	}

	// Request an unlock through the new seam.
	done := make(chan error, 1)
	go func() {
		done <- ws.RequestUnlock(ctx, "history needs the content key")
	}()

	// The connected client must receive a vault.unlockRequest notification.
	_ = conn.SetReadDeadline(time.Now().Add(wantWithin))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("expected unlock request notification, got error: %v", err)
	}

	var notif struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  struct {
			RequestID string `json:"requestId"`
			Reason    string `json:"reason"`
		} `json:"params"`
	}
	if err := json.Unmarshal(data, &notif); err != nil {
		t.Fatalf("unmarshal notification: %v", err)
	}
	if notif.JSONRPC != "2.0" {
		t.Errorf("expected jsonrpc 2.0, got %q", notif.JSONRPC)
	}
	if notif.Method != "vault.unlockRequest" {
		t.Errorf("expected method vault.unlockRequest, got %q", notif.Method)
	}
	if notif.Params.RequestID == "" {
		t.Error("expected non-empty requestId")
	}
	if notif.Params.Reason != "history needs the content key" {
		t.Errorf("expected reason, got %q", notif.Params.Reason)
	}
}

func TestUnlockRequest_NoClientReturnsError(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithVaultLifecycle(newFakeVaultLifecycle()))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	err := ws.RequestUnlock(ctx, "no one is listening")
	if err != ErrNoClientConnected {
		t.Errorf("expected ErrNoClientConnected, got %v", err)
	}
}

func TestUnlockRequest_ResolvedUnsealed(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithVaultLifecycle(newFakeVaultLifecycle()))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck

	// Verify connection is registered.
	resp := vaultCall(t, conn, "vault.status", nil, 1)
	if resp.Error != nil {
		t.Fatalf("vault.status failed: %s", resp.Error.Message)
	}

	// Start the unlock request; read the notification to extract the requestId.
	done := make(chan error, 1)
	go func() {
		done <- ws.RequestUnlock(ctx, "test unlock")
	}()

	_ = conn.SetReadDeadline(time.Now().Add(wantWithin))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("expected notification: %v", err)
	}
	var notif struct {
		Method string `json:"method"`
		Params struct {
			RequestID string `json:"requestId"`
		} `json:"params"`
	}
	if err := json.Unmarshal(data, &notif); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	rid := notif.Params.RequestID
	if rid == "" {
		t.Fatal("empty requestId")
	}

	// Send the resolved response.
	resp = vaultCall(t, conn, "vault.unlockResolved", map[string]any{
		"requestId": rid,
		"outcome":   "unsealed",
	}, 2)
	if resp.Error != nil {
		t.Fatalf("vault.unlockResolved error: %s", resp.Error.Message)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	case <-time.After(wantWithin):
		t.Fatal("RequestUnlock did not resolve")
	}
}

func TestUnlockRequest_ResolvedCancelled(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithVaultLifecycle(newFakeVaultLifecycle()))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck

	// Verify connection is registered.
	resp := vaultCall(t, conn, "vault.status", nil, 1)
	if resp.Error != nil {
		t.Fatalf("vault.status failed: %s", resp.Error.Message)
	}

	done := make(chan error, 1)
	go func() {
		done <- ws.RequestUnlock(ctx, "test unlock")
	}()

	_ = conn.SetReadDeadline(time.Now().Add(wantWithin))
	_, raw, readErr := conn.ReadMessage()
	if readErr != nil {
		t.Fatalf("expected notification: %v", readErr)
	}
	var notif struct {
		Params struct {
			RequestID string `json:"requestId"`
		} `json:"params"`
	}
	if err := json.Unmarshal(raw, &notif); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	rid := notif.Params.RequestID

	// Send cancelled outcome.
	resp = vaultCall(t, conn, "vault.unlockResolved", map[string]any{
		"requestId": rid,
		"outcome":   "cancelled",
	}, 2)
	if resp.Error != nil {
		t.Fatalf("vault.unlockResolved error: %s", resp.Error.Message)
	}

	select {
	case err := <-done:
		if err != ErrUnlockCancelled {
			t.Errorf("expected ErrUnlockCancelled, got %v", err)
		}
	case <-time.After(wantWithin):
		t.Fatal("RequestUnlock did not resolve")
	}
}

func TestUnlockRequest_ContextCancelled(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithVaultLifecycle(newFakeVaultLifecycle()))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck

	// Verify connection is registered.
	resp := vaultCall(t, conn, "vault.status", nil, 1)
	if resp.Error != nil {
		t.Fatalf("vault.status failed: %s", resp.Error.Message)
	}

	cancelCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- ws.RequestUnlock(cancelCtx, "test unlock")
	}()

	// Drain the notification so we know it was sent.
	_ = conn.SetReadDeadline(time.Now().Add(wantWithin))
	_, _, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("expected notification: %v", err)
	}

	// Cancel the context.
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(wantWithin):
		t.Fatal("RequestUnlock did not resolve after cancel")
	}
}

func TestUnlockResolved_UnknownRequestID(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithVaultLifecycle(newFakeVaultLifecycle()))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck

	// Verify connection is registered.
	statusResp := vaultCall(t, conn, "vault.status", nil, 1)
	if statusResp.Error != nil {
		t.Fatalf("vault.status failed: %s", statusResp.Error.Message)
	}

	resp := vaultCall(t, conn, "vault.unlockResolved", map[string]any{
		"requestId": "nonexistent",
		"outcome":   "unsealed",
	}, 1)
	if resp.Error == nil {
		t.Fatal("expected error for unknown requestId")
	}
}
