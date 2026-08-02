package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/ssh"
)

// ---------------------------------------------------------------------------
// classifyProbeError unit tests
// ---------------------------------------------------------------------------

func TestClassifyProbeError_Nil(t *testing.T) {
	r := classifyProbeError(nil)
	if r.outcome != OutcomeAccepted {
		t.Errorf("expected accepted, got %s", r.outcome)
	}
	if r.err != nil {
		t.Errorf("expected nil err, got %v", r.err)
	}
	if r.detail == "" {
		t.Error("expected non-empty detail for accepted")
	}
}

func TestClassifyProbeError_ErrAuthFailed(t *testing.T) {
	inner := errors.New("ssh: unable to authenticate")
	authErr := &ssh.ErrAuthFailed{User: "alice", Host: "host.example.com", Err: inner}
	r := classifyProbeError(authErr)
	if r.outcome != OutcomeRejected {
		t.Errorf("expected rejected, got %s", r.outcome)
	}
	if r.err != nil {
		t.Errorf("expected nil err, got %v", r.err)
	}
}

func TestClassifyProbeError_ErrUnknownHostKey(t *testing.T) {
	hkErr := &ssh.ErrUnknownHostKey{Addr: "1.2.3.4:22", KeyAlgo: "ssh-ed25519", Fingerprint: "SHA256:abc"}
	r := classifyProbeError(hkErr)
	if r.outcome != OutcomeHostKeyUnknown {
		t.Errorf("expected host-key-unknown, got %s", r.outcome)
	}
	if r.err != nil {
		t.Errorf("expected nil err, got %v", r.err)
	}
}

func TestClassifyProbeError_ErrHostKeyMismatch(t *testing.T) {
	hkErr := &ssh.ErrHostKeyMismatch{Addr: "1.2.3.4:22", Fingerprint: "abc", Expected: "def"}
	r := classifyProbeError(hkErr)
	if r.outcome != OutcomeHostKeyChanged {
		t.Errorf("expected host-key-changed, got %s", r.outcome)
	}
	if r.err != nil {
		t.Errorf("expected nil err, got %v", r.err)
	}
}

func TestClassifyProbeError_ErrEncryptedKey(t *testing.T) {
	encErr := &ssh.ErrEncryptedKey{Path: "/home/user/.ssh/id_rsa"}
	r := classifyProbeError(encErr)
	if r.outcome != OutcomeNeedsInteractive {
		t.Errorf("expected needs-interactive, got %s", r.outcome)
	}
	if r.err != nil {
		t.Errorf("expected nil err, got %v", r.err)
	}
}

func TestClassifyProbeError_NetOpError(t *testing.T) {
	netErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	r := classifyProbeError(netErr)
	if r.outcome != OutcomeUnreachable {
		t.Errorf("expected unreachable, got %s", r.outcome)
	}
	if r.err != nil {
		t.Errorf("expected nil err, got %v", r.err)
	}
}

func TestClassifyProbeError_ContextDeadline(t *testing.T) {
	err := context.DeadlineExceeded
	r := classifyProbeError(err)
	if r.outcome != OutcomeUnreachable {
		t.Errorf("expected unreachable for deadline, got %s", r.outcome)
	}
}

func TestClassifyProbeError_ContextCancelled(t *testing.T) {
	err := context.Canceled
	r := classifyProbeError(err)
	if r.outcome != OutcomeUnreachable {
		t.Errorf("expected unreachable for cancel, got %s", r.outcome)
	}
}

func TestClassifyProbeError_Unclassifiable(t *testing.T) {
	err := errors.New("something unexpected")
	r := classifyProbeError(err)
	if r.outcome != "" {
		t.Errorf("expected empty outcome, got %s", r.outcome)
	}
	if r.err == nil {
		t.Error("expected non-nil err for unclassifiable")
	}
}

func TestClassifyProbeError_UnclassifiableWrappedAuth(t *testing.T) {
	// An ErrAuthFailed wrapped with %w must be recoverable via errors.As.
	inner := &ssh.ErrAuthFailed{User: "alice", Host: "host.example.com", Err: errors.New("bad auth")}
	wrapped := fmt.Errorf("probe config: %w", inner)
	r := classifyProbeError(wrapped)
	if r.outcome != OutcomeRejected {
		t.Errorf("expected rejected, got %s", r.outcome)
	}
	if r.err != nil {
		t.Errorf("expected nil err, got %v", r.err)
	}
}

// ---------------------------------------------------------------------------
// Fake prober and resolver for handler tests
// ---------------------------------------------------------------------------

type fakeProber struct {
	probeFn           func(ctx context.Context, host string, cfg *ssh.ConnectConfig) error
	probeWithResultFn func(ctx context.Context, host string, cfg *ssh.ConnectConfig) (string, error)
}

func (f *fakeProber) Probe(ctx context.Context, host string, cfg *ssh.ConnectConfig) error {
	return f.probeFn(ctx, host, cfg)
}

func (f *fakeProber) ProbeWithResult(ctx context.Context, host string, cfg *ssh.ConnectConfig) (string, error) {
	if f.probeWithResultFn != nil {
		return f.probeWithResultFn(ctx, host, cfg)
	}
	// Fall back to Probe behavior for existing tests that don't
	// care about the fingerprint.
	err := f.probeFn(ctx, host, cfg)
	return "", err
}

// fakeResolver implements ProfileResolver for tests.
type fakeResolver struct {
	resolveFn func(profileID string) (host string, cfg *ssh.ConnectConfig, err error)
}

func (f *fakeResolver) Resolve(profileID string) (string, *ssh.ConnectConfig, error) {
	return f.resolveFn(profileID)
}

// ---------------------------------------------------------------------------
// Handler integration tests
// ---------------------------------------------------------------------------

func TestConnectionsTest_Accepted(t *testing.T) {
	srv := newProbeTestServer(t, nil, &fakeResolver{
		resolveFn: func(profileID string) (string, *ssh.ConnectConfig, error) {
			return "host.example.com", &ssh.ConnectConfig{User: "test"}, nil
		},
	})
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.test", map[string]any{
		"profileId": "ssh:test:1",
	})

	var result struct {
		Result connectionsTestResult `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Result.Outcome != OutcomeAccepted {
		t.Errorf("expected accepted, got %s", result.Result.Outcome)
	}
}

func TestConnectionsTest_Rejected(t *testing.T) {
	probeErr := &ssh.ErrAuthFailed{User: "test", Host: "host.example.com", Err: errors.New("bad password")}
	srv := newProbeTestServer(t, probeErr, &fakeResolver{
		resolveFn: func(profileID string) (string, *ssh.ConnectConfig, error) {
			return "host.example.com", &ssh.ConnectConfig{User: "test"}, nil
		},
	})
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.test", map[string]any{
		"profileId": "ssh:test:1",
	})

	var result struct {
		Result connectionsTestResult `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Result.Outcome != OutcomeRejected {
		t.Errorf("expected rejected, got %s", result.Result.Outcome)
	}
}

func TestConnectionsTest_Unreachable(t *testing.T) {
	probeErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	srv := newProbeTestServer(t, probeErr, &fakeResolver{
		resolveFn: func(profileID string) (string, *ssh.ConnectConfig, error) {
			return "host.example.com", &ssh.ConnectConfig{User: "test"}, nil
		},
	})
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.test", map[string]any{
		"profileId": "ssh:test:1",
	})

	var result struct {
		Result connectionsTestResult `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Result.Outcome != OutcomeUnreachable {
		t.Errorf("expected unreachable, got %s", result.Result.Outcome)
	}
}

func TestConnectionsTest_HostKeyUnknown(t *testing.T) {
	probeErr := &ssh.ErrUnknownHostKey{
		Addr:        "host.example.com:22",
		KeyAlgo:     "ssh-ed25519",
		Fingerprint: "SHA256:abc",
		Key:         []byte("offered-key-blob"),
	}
	srv := newProbeTestServer(t, probeErr, &fakeResolver{
		resolveFn: func(profileID string) (string, *ssh.ConnectConfig, error) {
			return "host.example.com", &ssh.ConnectConfig{User: "test"}, nil
		},
	})
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.test", map[string]any{
		"profileId": "ssh:test:1",
	})

	var result struct {
		Result connectionsTestResult `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Result.Outcome != OutcomeHostKeyUnknown {
		t.Errorf("expected host-key-unknown, got %s", result.Result.Outcome)
	}
	hk := result.Result.HostKey
	if hk == nil {
		t.Fatal("expected hostKey evidence on the result")
	}
	if hk.Host != "host.example.com:22" {
		t.Errorf("expected host host.example.com:22, got %q", hk.Host)
	}
	if hk.Algorithm != "ssh-ed25519" {
		t.Errorf("expected algorithm ssh-ed25519, got %q", hk.Algorithm)
	}
	if hk.Fingerprint != "SHA256:abc" {
		t.Errorf("expected fingerprint SHA256:abc, got %q", hk.Fingerprint)
	}
	if hk.Key != "b2ZmZXJlZC1rZXktYmxvYg==" {
		t.Errorf("expected base64 key b2ZmZXJlZC1rZXktYmxvYg==, got %q", hk.Key)
	}
	if hk.StoredFingerprint != "" {
		t.Errorf("expected no stored fingerprint for an unknown key, got %q", hk.StoredFingerprint)
	}
}

func TestConnectionsTest_HostKeyChanged(t *testing.T) {
	probeErr := &ssh.ErrHostKeyMismatch{
		Addr:        "host.example.com:22",
		Fingerprint: "SHA256:new",
		Expected:    "SHA256:stored",
		KeyAlgo:     "ecdsa-sha2-nistp256",
		Key:         []byte("new-key-blob"),
	}
	srv := newProbeTestServer(t, probeErr, &fakeResolver{
		resolveFn: func(profileID string) (string, *ssh.ConnectConfig, error) {
			return "host.example.com", &ssh.ConnectConfig{User: "test"}, nil
		},
	})
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.test", map[string]any{
		"profileId": "ssh:test:1",
	})

	var result struct {
		Result connectionsTestResult `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Result.Outcome != OutcomeHostKeyChanged {
		t.Errorf("expected host-key-changed, got %s", result.Result.Outcome)
	}
	hk := result.Result.HostKey
	if hk == nil {
		t.Fatal("expected hostKey evidence on the result")
	}
	if hk.Fingerprint != "SHA256:new" {
		t.Errorf("expected offered fingerprint SHA256:new, got %q", hk.Fingerprint)
	}
	if hk.StoredFingerprint != "SHA256:stored" {
		t.Errorf("expected stored fingerprint SHA256:stored, got %q", hk.StoredFingerprint)
	}
	if hk.Algorithm != "ecdsa-sha2-nistp256" {
		t.Errorf("expected algorithm ecdsa-sha2-nistp256, got %q", hk.Algorithm)
	}
	if hk.Key != "bmV3LWtleS1ibG9i" {
		t.Errorf("expected base64 key bmV3LWtleS1ibG9i, got %q", hk.Key)
	}
}

func TestConnectionsTest_NeedsInteractive(t *testing.T) {
	probeErr := &ssh.ErrEncryptedKey{Path: "/home/user/.ssh/id_rsa"}
	srv := newProbeTestServer(t, probeErr, &fakeResolver{
		resolveFn: func(profileID string) (string, *ssh.ConnectConfig, error) {
			return "host.example.com", &ssh.ConnectConfig{User: "test"}, nil
		},
	})
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.test", map[string]any{
		"profileId": "ssh:test:1",
	})

	var result struct {
		Result connectionsTestResult `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Result.Outcome != OutcomeNeedsInteractive {
		t.Errorf("expected needs-interactive, got %s", result.Result.Outcome)
	}
}

func TestConnectionsTest_MissingProfileID(t *testing.T) {
	srv := newProbeTestServer(t, nil, &fakeResolver{
		resolveFn: func(profileID string) (string, *ssh.ConnectConfig, error) {
			return "host.example.com", &ssh.ConnectConfig{User: "test"}, nil
		},
	})
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.test", map[string]any{})

	var result struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Error.Code != -32602 {
		t.Errorf("expected code -32602, got %d", result.Error.Code)
	}
}

func TestConnectionsTest_ResolveError(t *testing.T) {
	srv := newProbeTestServer(t, nil, &fakeResolver{
		resolveFn: func(profileID string) (string, *ssh.ConnectConfig, error) {
			return "", nil, errors.New("profile not found")
		},
	})
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.test", map[string]any{
		"profileId": "ssh:missing:1",
	})

	var result struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Error == nil {
		t.Fatal("expected error for missing profile")
	}
}

func TestConnectionsTest_NoProber(t *testing.T) {
	// A WSServer WITHOUT a prober wired should return an error.
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)
	srv := NewWSServer(logger, reg, WithProfileResolver(&fakeResolver{
		resolveFn: func(profileID string) (string, *ssh.ConnectConfig, error) {
			return "host.example.com", &ssh.ConnectConfig{User: "test"}, nil
		},
	}))
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = srv.Stop(ctx) }()

	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.test", map[string]any{
		"profileId": "ssh:test:1",
	})

	var result struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Error == nil {
		t.Fatal("expected error when no prober is wired")
	}
	if result.Error.Code != -32603 {
		t.Errorf("expected code -32603, got %d", result.Error.Code)
	}
}

func TestConnectionsTest_UnclassifiableError(t *testing.T) {
	unknownErr := errors.New("unexpected internal error")
	srv := newProbeTestServer(t, unknownErr, &fakeResolver{
		resolveFn: func(profileID string) (string, *ssh.ConnectConfig, error) {
			return "host.example.com", &ssh.ConnectConfig{User: "test"}, nil
		},
	})
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.test", map[string]any{
		"profileId": "ssh:test:1",
	})

	var result struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Error == nil {
		t.Fatal("expected error for unclassifiable probe error")
	}
}

func TestConnectionsTest_ProbeResultJSON(t *testing.T) {
	// Verify the wire format matches the spec: outcome string, no extra fields.
	tests := []struct {
		err      error
		expected ProbeOutcome
	}{
		{nil, OutcomeAccepted},
		{&ssh.ErrAuthFailed{User: "u", Host: "h", Err: errors.New("bad auth")}, OutcomeRejected},
		{&ssh.ErrUnknownHostKey{Addr: "h:22", KeyAlgo: "k", Fingerprint: "f"}, OutcomeHostKeyUnknown},
		{&ssh.ErrHostKeyMismatch{Addr: "h:22", Fingerprint: "f", Expected: "e"}, OutcomeHostKeyChanged},
		{&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}, OutcomeUnreachable},
		{&ssh.ErrEncryptedKey{Path: "/key"}, OutcomeNeedsInteractive},
	}
	for _, tt := range tests {
		srv := newProbeTestServer(t, tt.err, &fakeResolver{
			resolveFn: func(profileID string) (string, *ssh.ConnectConfig, error) {
				return "h", &ssh.ConnectConfig{User: "u"}, nil
			},
		})
		conn := connectWS(t, srv)
		resp := jsonrpcCall(t, conn, "connections.test", map[string]any{
			"profileId": "ssh:test:1",
		})

		var envelope struct {
			Result json.RawMessage `json:"result,omitempty"`
			Error  *struct{}       `json:"error,omitempty"`
		}
		if err := json.Unmarshal(resp, &envelope); err != nil {
			_ = conn.Close()
			t.Fatalf("unmarshal envelope: %v", err)
		}

		if tt.err == nil {
			// Accepted goes via result, not error.
			var res connectionsTestResult
			if err := json.Unmarshal(envelope.Result, &res); err != nil {
				_ = conn.Close()
				t.Fatalf("unmarshal result: %v", err)
			}
			if res.Outcome != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, res.Outcome)
			}
		} else {
			// Error cases still produce results (typed outcomes), not errors.
			var res connectionsTestResult
			if err := json.Unmarshal(envelope.Result, &res); err != nil {
				_ = conn.Close()
				t.Fatalf("unmarshal result: %v", err)
			}
			if res.Outcome != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, res.Outcome)
			}
		}
		_ = conn.Close()
	}
}

func TestConnectionsTest_StoresResult(t *testing.T) {
	store := NewProbeResultStore()
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)

	testFingerprint := "SHA256:testprobe"
	prober := &fakeProber{
		probeWithResultFn: func(_ context.Context, _ string, _ *ssh.ConnectConfig) (string, error) {
			return testFingerprint, nil
		},
	}

	srv := NewWSServer(
		logger, reg,
		WithProfileResolver(&fakeResolver{
			resolveFn: func(profileID string) (string, *ssh.ConnectConfig, error) {
				return "host.example.com", &ssh.ConnectConfig{
					User:     "test",
					Port:     2222,
					AuthMode: "publicKey",
				}, nil
			},
		}),
		WithProber(prober),
		WithProbeResultStore(store),
	)
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })

	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.test", map[string]any{
		"profileId": "ssh:test:1",
	})

	var result struct {
		Result connectionsTestResult `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Result.Outcome != OutcomeAccepted {
		t.Errorf("expected accepted, got %s", result.Result.Outcome)
	}

	// Verify the store recorded the probe result.
	if got := store.Len(); got != 1 {
		t.Fatalf("expected 1 stored result, got %d", got)
	}
	records := store.List()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	rec := records[0]
	if rec.Outcome != OutcomeAccepted {
		t.Errorf("expected stored outcome accepted, got %s", rec.Outcome)
	}
	if rec.Identity.HostKeyFingerprint != testFingerprint {
		t.Errorf("expected fingerprint %s, got %s", testFingerprint, rec.Identity.HostKeyFingerprint)
	}
	if rec.Identity.Endpoint != "host.example.com:2222" {
		t.Errorf("expected endpoint host.example.com:2222, got %s", rec.Identity.Endpoint)
	}
	if rec.Identity.Username != "test" {
		t.Errorf("expected username test, got %s", rec.Identity.Username)
	}
	if rec.Identity.AuthPolicy != "publicKey" {
		t.Errorf("expected auth policy publicKey, got %s", rec.Identity.AuthPolicy)
	}
	if rec.Identity.Timestamp.IsZero() {
		t.Errorf("expected non-zero timestamp")
	}
	if rec.Detail != "ok" {
		t.Errorf("expected detail 'ok' on accepted probe, got %s", rec.Detail)
	}
}

// ── connections.trustHostKey ────────────────────────────────────────────

type fakeHostKeyTruster struct {
	mu          sync.Mutex
	addr        string
	key         []byte
	fingerprint string
	err         error
	called      bool
}

func (f *fakeHostKeyTruster) TrustHostKey(_ context.Context, addr string, key []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called = true
	f.addr = addr
	f.key = key
	return f.fingerprint, f.err
}

func TestConnectionsTrustHostKey_Success(t *testing.T) {
	truster := &fakeHostKeyTruster{fingerprint: "SHA256:trusted"}
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)
	srv := NewWSServer(logger, reg, WithHostKeyTruster(truster))
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })

	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.trustHostKey", map[string]any{
		"host": "host.example.com:22",
		"key":  "b2ZmZXJlZC1rZXktYmxvYg==",
	})

	var result struct {
		Result connectionsTrustHostKeyResult `json:"result"`
		Error  *jsonrpcErrorObj              `json:"error,omitempty"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Error != nil {
		t.Fatalf("expected success, got RPC error %d: %s", result.Error.Code, result.Error.Message)
	}
	if result.Result.Fingerprint != "SHA256:trusted" {
		t.Errorf("expected fingerprint SHA256:trusted, got %q", result.Result.Fingerprint)
	}
	if !truster.called {
		t.Fatal("expected the truster to be called")
	}
	if truster.addr != "host.example.com:22" {
		t.Errorf("expected addr host.example.com:22, got %q", truster.addr)
	}
	if string(truster.key) != "offered-key-blob" {
		t.Errorf("expected decoded key blob, got %q", string(truster.key))
	}
}

func TestConnectionsTrustHostKey_NoTruster(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)
	srv := NewWSServer(logger, reg)
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })

	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.trustHostKey", map[string]any{
		"host": "host.example.com:22",
		"key":  "b2ZmZXJlZC1rZXktYmxvYg==",
	})

	var result struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Error == nil {
		t.Fatal("expected error when no truster is wired")
	}
	if result.Error.Code != -32603 {
		t.Errorf("expected code -32603, got %d", result.Error.Code)
	}
}

func TestConnectionsTrustHostKey_InvalidParams(t *testing.T) {
	truster := &fakeHostKeyTruster{}
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)
	srv := NewWSServer(logger, reg, WithHostKeyTruster(truster))
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })

	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	// Missing key → invalid params, truster untouched.
	resp := jsonrpcCall(t, conn, "connections.trustHostKey", map[string]any{
		"host": "host.example.com:22",
	})
	var result struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Error == nil || result.Error.Code != -32602 {
		t.Fatalf("expected -32602 for missing key, got %+v", result.Error)
	}
	if truster.called {
		t.Fatal("truster must not be called with invalid params")
	}
}

func TestConnectionsTrustHostKey_InvalidKeyEncoding(t *testing.T) {
	truster := &fakeHostKeyTruster{}
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)
	srv := NewWSServer(logger, reg, WithHostKeyTruster(truster))
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })

	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.trustHostKey", map[string]any{
		"host": "host.example.com:22",
		"key":  "!!!not-base64!!!",
	})
	var result struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Error == nil || result.Error.Code != -32603 {
		t.Fatalf("expected -32603 for undecodable key, got %+v", result.Error)
	}
	if truster.called {
		t.Fatal("truster must not be called with an undecodable key")
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newProbeTestServer(t *testing.T, probeReturn error, resolver ProfileResolver) *WSServer {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)

	prober := &fakeProber{
		probeFn: func(_ context.Context, _ string, _ *ssh.ConnectConfig) error {
			return probeReturn
		},
	}

	srv := NewWSServer(
		logger, reg,
		WithProfileResolver(resolver),
		WithProber(prober),
	)
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })
	return srv
}
