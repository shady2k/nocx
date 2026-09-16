package transport

// connections.helperConsent — the write half of the connect-time helper ask
// (ADR-0068). These tests exercise the handler through the real socket, the
// way ws_probe_test.go exercises connections.trustHostKey — the sibling
// method this one is modelled on.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

// fakeHelperConsentWriter records what it was asked to write and can be
// told to fail, so a test can prove the handler answers a failed write with
// an error rather than pretending an answer nobody can look up again was
// recorded.
type fakeHelperConsentWriter struct {
	mu sync.Mutex

	grantErr error
	denyErr  error

	granted []string
	denied  []string
}

func (f *fakeHelperConsentWriter) Grant(fingerprint string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.grantErr != nil {
		return f.grantErr
	}
	f.granted = append(f.granted, fingerprint)
	return nil
}

func (f *fakeHelperConsentWriter) Deny(fingerprint string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.denyErr != nil {
		return f.denyErr
	}
	f.denied = append(f.denied, fingerprint)
	return nil
}

func (f *fakeHelperConsentWriter) grantedFingerprints() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.granted...)
}

func (f *fakeHelperConsentWriter) deniedFingerprints() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.denied...)
}

func startHelperConsentServer(t *testing.T, w HelperConsentWriter) *WSServer {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)
	var srv *WSServer
	if w != nil {
		srv = NewWSServer(logger, reg, WithHelperConsentWriter(w))
	} else {
		srv = NewWSServer(logger, reg)
	}
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })
	return srv
}

// TestConnectionsHelperConsent_Accept: a person accepting the ask writes
// Grant under the fingerprint they were asked about — the resolver's
// DesiredHelper branch reads exactly this.
func TestConnectionsHelperConsent_Accept(t *testing.T) {
	writer := &fakeHelperConsentWriter{}
	srv := startHelperConsentServer(t, writer)
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.helperConsent", map[string]any{
		"fingerprint": "SHA256:abc",
		"host":        "host.example.com:22",
		"granted":     true,
	})
	var result struct {
		Result connectionsHelperConsentResult `json:"result"`
		Error  *jsonrpcErrorObj               `json:"error,omitempty"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Error != nil {
		t.Fatalf("expected success, got RPC error %d: %s", result.Error.Code, result.Error.Message)
	}
	if result.Result.Fingerprint != "SHA256:abc" || result.Result.Answer != "granted" {
		t.Errorf("result = %+v, want fingerprint SHA256:abc / granted", result.Result)
	}
	if got := writer.grantedFingerprints(); len(got) != 1 || got[0] != "SHA256:abc" {
		t.Errorf("Grant called with %v, want [SHA256:abc]", got)
	}
	if len(writer.deniedFingerprints()) != 0 {
		t.Error("Deny must not be called on an accept")
	}
}

// TestConnectionsHelperConsent_Decline: declining writes Deny, not merely a
// missing grant — the resolver must be able to tell "never asked" from
// "asked and said no", or it re-asks on every connect (the interval
// invariant: a fingerprint is asked exactly once).
func TestConnectionsHelperConsent_Decline(t *testing.T) {
	writer := &fakeHelperConsentWriter{}
	srv := startHelperConsentServer(t, writer)
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.helperConsent", map[string]any{
		"fingerprint": "SHA256:abc",
		"granted":     false,
	})
	var result struct {
		Result connectionsHelperConsentResult `json:"result"`
		Error  *jsonrpcErrorObj               `json:"error,omitempty"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Error != nil {
		t.Fatalf("expected success, got RPC error %d: %s", result.Error.Code, result.Error.Message)
	}
	if result.Result.Answer != "denied" {
		t.Errorf("answer = %q, want denied", result.Result.Answer)
	}
	if got := writer.deniedFingerprints(); len(got) != 1 || got[0] != "SHA256:abc" {
		t.Errorf("Deny called with %v, want [SHA256:abc]", got)
	}
	if len(writer.grantedFingerprints()) != 0 {
		t.Error("Grant must not be called on a decline")
	}
}

// TestConnectionsHelperConsent_NoWriter: not wired answers an error rather
// than silently accepting an answer nobody can ever look up.
func TestConnectionsHelperConsent_NoWriter(t *testing.T) {
	srv := startHelperConsentServer(t, nil)
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.helperConsent", map[string]any{
		"fingerprint": "SHA256:abc",
		"granted":     true,
	})
	var result struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Error == nil {
		t.Fatal("expected error when no writer is wired")
	}
	if result.Error.Code != -32603 {
		t.Errorf("expected code -32603, got %d", result.Error.Code)
	}
}

// TestConnectionsHelperConsent_MissingFingerprint: fingerprint is the
// write's whole identity (ADR-0034); a request naming none is refused
// before the store is touched.
func TestConnectionsHelperConsent_MissingFingerprint(t *testing.T) {
	writer := &fakeHelperConsentWriter{}
	srv := startHelperConsentServer(t, writer)
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.helperConsent", map[string]any{
		"granted": true,
	})
	var result struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Error == nil {
		t.Fatal("expected error for a missing fingerprint")
	}
	if result.Error.Code != -32602 {
		t.Errorf("expected code -32602, got %d", result.Error.Code)
	}
	if len(writer.grantedFingerprints()) != 0 || len(writer.deniedFingerprints()) != 0 {
		t.Error("the store must not be touched for a refused params shape")
	}
}

// TestConnectionsHelperConsent_WriteFailure: the store's own write failure
// (a full disk, an unwritable directory) is answered as an error — never a
// success that papers over an answer that was not actually persisted, which
// is the exact defect consent design §6 forbids for Grant and Deny alike.
func TestConnectionsHelperConsent_WriteFailure(t *testing.T) {
	writer := &fakeHelperConsentWriter{grantErr: errors.New("disk full")}
	srv := startHelperConsentServer(t, writer)
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.helperConsent", map[string]any{
		"fingerprint": "SHA256:abc",
		"granted":     true,
	})
	var result struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Error == nil {
		t.Fatal("expected an error when the writer fails")
	}
	if result.Error.Code != -32603 {
		t.Errorf("expected code -32603, got %d", result.Error.Code)
	}
}
