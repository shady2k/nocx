package transport

// connections.setIntegrationMethod — the write half of the connect-time ask
// (ADR-0069). These tests exercise the handler through the real socket, the
// way ws_probe_test.go exercises connections.trustHostKey — the sibling
// method this one is modelled on.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
)

// fakeHelperConsentGranter records what it was asked to grant and can be
// told to fail, so a test can prove the handler answers a failed write with
// an error rather than pretending an answer nobody can look up again was
// recorded.
type fakeHelperConsentGranter struct {
	mu sync.Mutex

	grantErr error
	granted  []string
}

func (f *fakeHelperConsentGranter) Grant(fingerprint string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.grantErr != nil {
		return f.grantErr
	}
	f.granted = append(f.granted, fingerprint)
	return nil
}

func (f *fakeHelperConsentGranter) grantedFingerprints() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.granted...)
}

// startIntegrationMethodServer wires the config domain (so ProfileID writes
// have somewhere to land) plus the optional granter. Tests that never send a
// profileId do not need the profile store at all, but wiring it uniformly
// keeps this harness one shape rather than two.
func startIntegrationMethodServer(t *testing.T, w HelperConsentGranter, ps *profile.JSONStore) *WSServer {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)
	opts := []WSServerOption{WithCredentialStore(newTestStore())}
	if w != nil {
		opts = append(opts, WithHelperConsentWriter(w))
	}
	if ps != nil {
		opts = append(opts, WithProfileRepository(ps), WithGroupRepository(ps))
	}
	srv := NewWSServer(logger, reg, opts...)
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })
	return srv
}

func newIntegrationMethodProfileStore(t *testing.T, id string) *profile.JSONStore {
	t.Helper()
	ps := profile.NewJSONStore(t.TempDir() + "/p.json")
	prof := profile.SSHProfile{
		Base: profile.Base{ID: id, Type: "ssh", Name: "test"},
		Options: profile.StoredSSHProfileOptions{
			Host: "host.example.com",
		},
	}
	if err := ps.CreateProfile(prof); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	return ps
}

// TestIntegrationMethod_ScriptWritesDesiredModeThroughThePatchPath: choosing
// script on a saved connection is read back through profiles.effective the
// same way an editor-driven patch would be — the acceptance criterion in
// nocx-xn63t.6.5 — and grants nothing.
func TestIntegrationMethod_ScriptWritesDesiredModeThroughThePatchPath(t *testing.T) {
	ps := newIntegrationMethodProfileStore(t, "ssh-1")
	granter := &fakeHelperConsentGranter{}
	srv := startIntegrationMethodServer(t, granter, ps)
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.setIntegrationMethod", map[string]any{
		"fingerprint": "SHA256:abc",
		"profileId":   "ssh-1",
		"method":      "script",
	})
	var result struct {
		Result integrationMethodResult `json:"result"`
		Error  *jsonrpcErrorObj        `json:"error,omitempty"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Error != nil {
		t.Fatalf("expected success, got RPC error %d: %s", result.Error.Code, result.Error.Message)
	}
	if result.Result.Method != "script" || result.Result.ProfileID != "ssh-1" {
		t.Errorf("result = %+v, want method script / profileId ssh-1", result.Result)
	}
	if len(granter.grantedFingerprints()) != 0 {
		t.Error("choosing script must not grant helper consent")
	}

	// Read back through the same seam the editor's Delivery mode field uses:
	// profiles.effective's desiredMode.
	effResp := jsonrpcCall(t, conn, "profiles.effective", map[string]any{"ids": []string{"ssh-1"}})
	var eff struct {
		Result struct {
			Profiles []struct {
				Fields struct {
					DesiredMode struct {
						Value string `json:"value"`
					} `json:"desiredMode"`
				} `json:"fields"`
			} `json:"profiles"`
		} `json:"result"`
	}
	if err := json.Unmarshal(effResp, &eff); err != nil {
		t.Fatalf("unmarshal effective: %v", err)
	}
	if len(eff.Result.Profiles) != 1 || eff.Result.Profiles[0].Fields.DesiredMode.Value != "script" {
		t.Fatalf("effective profiles = %+v, want one profile with desiredMode script", eff.Result.Profiles)
	}
}

// TestIntegrationMethod_HelperWritesDesiredModeAndGrantsConsent: choosing
// helper on a saved connection does both writes ADR-0069 names — the
// connection's desiredMode AND the machine's fingerprint-keyed grant.
func TestIntegrationMethod_HelperWritesDesiredModeAndGrantsConsent(t *testing.T) {
	ps := newIntegrationMethodProfileStore(t, "ssh-1")
	granter := &fakeHelperConsentGranter{}
	srv := startIntegrationMethodServer(t, granter, ps)
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.setIntegrationMethod", map[string]any{
		"fingerprint": "SHA256:abc",
		"profileId":   "ssh-1",
		"method":      "helper",
	})
	var result struct {
		Result integrationMethodResult `json:"result"`
		Error  *jsonrpcErrorObj        `json:"error,omitempty"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Error != nil {
		t.Fatalf("expected success, got RPC error %d: %s", result.Error.Code, result.Error.Message)
	}
	if got := granter.grantedFingerprints(); len(got) != 1 || got[0] != "SHA256:abc" {
		t.Errorf("Grant called with %v, want [SHA256:abc]", got)
	}
}

// TestIntegrationMethod_RawWritesNoConsent: raw never touches the consent
// store either — the same rule as script, checked separately because raw and
// script are refused by two different arms of the resolver.
func TestIntegrationMethod_RawWritesNoConsent(t *testing.T) {
	ps := newIntegrationMethodProfileStore(t, "ssh-1")
	granter := &fakeHelperConsentGranter{}
	srv := startIntegrationMethodServer(t, granter, ps)
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	jsonrpcCall(t, conn, "connections.setIntegrationMethod", map[string]any{
		"fingerprint": "SHA256:abc",
		"profileId":   "ssh-1",
		"method":      "raw",
	})
	if len(granter.grantedFingerprints()) != 0 {
		t.Error("choosing raw must not grant helper consent")
	}
}

// TestIntegrationMethod_NoProfileID: a hand-typed connection has nowhere to
// keep the answer. Helper still grants — the machine consent is independent
// of any saved connection — but nothing is patched, and no profile store
// needs to be wired at all.
func TestIntegrationMethod_NoProfileID(t *testing.T) {
	granter := &fakeHelperConsentGranter{}
	srv := startIntegrationMethodServer(t, granter, nil)
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.setIntegrationMethod", map[string]any{
		"fingerprint": "SHA256:abc",
		"method":      "helper",
	})
	var result struct {
		Result integrationMethodResult `json:"result"`
		Error  *jsonrpcErrorObj        `json:"error,omitempty"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Error != nil {
		t.Fatalf("expected success, got RPC error %d: %s", result.Error.Code, result.Error.Message)
	}
	if result.Result.ProfileID != "" {
		t.Errorf("profileId = %q, want empty when none was named", result.Result.ProfileID)
	}
	if got := granter.grantedFingerprints(); len(got) != 1 || got[0] != "SHA256:abc" {
		t.Errorf("Grant called with %v, want [SHA256:abc]", got)
	}
}

// TestIntegrationMethod_NoGranter_Helper: not wired answers an error rather
// than silently accepting a grant nobody can ever look up — but only for
// helper; raw and script need no granter.
func TestIntegrationMethod_NoGranter_Helper(t *testing.T) {
	srv := startIntegrationMethodServer(t, nil, nil)
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.setIntegrationMethod", map[string]any{
		"fingerprint": "SHA256:abc",
		"method":      "helper",
	})
	var result struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Error == nil {
		t.Fatal("expected error when no granter is wired")
	}
	if result.Error.Code != -32603 {
		t.Errorf("expected code -32603, got %d", result.Error.Code)
	}
}

// TestIntegrationMethod_NoGranter_Script: script needs no granter at all, so
// an ask answered script succeeds even when nothing was wired to grant the
// helper.
func TestIntegrationMethod_NoGranter_Script(t *testing.T) {
	srv := startIntegrationMethodServer(t, nil, nil)
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.setIntegrationMethod", map[string]any{
		"fingerprint": "SHA256:abc",
		"method":      "script",
	})
	var result struct {
		Result integrationMethodResult `json:"result"`
		Error  *jsonrpcErrorObj        `json:"error,omitempty"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Error != nil {
		t.Fatalf("expected success, got RPC error %d: %s", result.Error.Code, result.Error.Message)
	}
}

// TestIntegrationMethod_MissingFingerprint: fingerprint is the write's whole
// identity (ADR-0034); a request naming none is refused before any store is
// touched.
func TestIntegrationMethod_MissingFingerprint(t *testing.T) {
	granter := &fakeHelperConsentGranter{}
	srv := startIntegrationMethodServer(t, granter, nil)
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.setIntegrationMethod", map[string]any{
		"method": "helper",
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
	if len(granter.grantedFingerprints()) != 0 {
		t.Error("the store must not be touched for a refused params shape")
	}
}

// TestIntegrationMethod_InvalidMethod: "auto" is not an answer (ADR-0033)
// and neither is anything else outside the three the product offers.
func TestIntegrationMethod_InvalidMethod(t *testing.T) {
	srv := startIntegrationMethodServer(t, nil, nil)
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	for _, method := range []string{"auto", "bogus", ""} {
		resp := jsonrpcCall(t, conn, "connections.setIntegrationMethod", map[string]any{
			"fingerprint": "SHA256:abc",
			"method":      method,
		})
		var result struct {
			Error *jsonrpcErrorObj `json:"error"`
		}
		if err := json.Unmarshal(resp, &result); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if result.Error == nil {
			t.Fatalf("method %q: expected error", method)
		}
		if result.Error.Code != -32602 {
			t.Errorf("method %q: expected code -32602, got %d", method, result.Error.Code)
		}
	}
}

// TestIntegrationMethod_GrantFailure: the consent store's own write failure
// (a full disk, an unwritable directory) is answered as an error — never a
// success that papers over an answer that was not actually persisted
// (consent design §6).
func TestIntegrationMethod_GrantFailure(t *testing.T) {
	granter := &fakeHelperConsentGranter{grantErr: errors.New("disk full")}
	srv := startIntegrationMethodServer(t, granter, nil)
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.setIntegrationMethod", map[string]any{
		"fingerprint": "SHA256:abc",
		"method":      "helper",
	})
	var result struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Error == nil {
		t.Fatal("expected an error when the granter fails")
	}
	if result.Error.Code != -32603 {
		t.Errorf("expected code -32603, got %d", result.Error.Code)
	}
}

// TestIntegrationMethod_UnknownProfileID: a profileId naming no stored
// connection is refused rather than silently accepted — the same refusal
// profiles.patch itself gives.
func TestIntegrationMethod_UnknownProfileID(t *testing.T) {
	ps := newIntegrationMethodProfileStore(t, "ssh-1")
	srv := startIntegrationMethodServer(t, nil, ps)
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.setIntegrationMethod", map[string]any{
		"fingerprint": "SHA256:abc",
		"profileId":   "does-not-exist",
		"method":      "script",
	})
	var result struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Error == nil {
		t.Fatal("expected an error for an unknown profileId")
	}
}
