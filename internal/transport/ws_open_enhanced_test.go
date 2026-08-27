package transport

// Integration is requested by the backend, for every session, on every
// path — nocx-tr2n.
//
// The renderer used to carry the request as an `enhanced` open parameter,
// and both ssh openers omitted it. The consequence was invisible: an ssh
// session opened Enhanced=false, session.Open skipped ssh.WithEnhanced(),
// and the lifecycle-channel gate in ssh_real.Connect never ran — so no
// authenticated domain existed and, after ADR-0024, no block could ever
// form. The suite stayed green because every proof of the remote channel
// (live_sshd_test, ssh_lifecycle_test, ssh_launcher_test) passes
// WithEnhanced() itself: the flag under test was the test's, never the
// product's.
//
// These tests take the flag away from the test. They drive the real open
// RPC over a real websocket, with params that say nothing about
// integration, and assert on what reaches the layer below — the
// ConnectConfig the ssh factory is dialled with, and the pty.Config the
// factory is asked for. Nothing below fails closed: a refused channel or a
// declining launcher still yields a visible native prompt, which is the
// fallback the policy asks for.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
)

// recordingPTYFactory captures the pty.Config the session layer builds.
type recordingPTYFactory struct {
	stub *pty.Stub
	mu   sync.Mutex
	got  pty.Config
	seen bool
}

func (f *recordingPTYFactory) NewPTY(_ context.Context, cfg pty.Config) (pty.Pty, error) {
	f.mu.Lock()
	f.got, f.seen = cfg, true
	f.mu.Unlock()
	return f.stub, nil
}

func (f *recordingPTYFactory) config(t *testing.T) pty.Config {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.seen {
		t.Fatal("no pty was opened")
	}
	return f.got
}

// sshConfigRecorder captures the ConnectConfig the dial is given, rebuilt
// from the options exactly as session.Open assembles them.
type sshConfigRecorder struct {
	mu   sync.Mutex
	got  ssh.ConnectConfig
	seen bool
}

func (r *sshConfigRecorder) factory(logger log.Logger) *stubSSHFactory {
	return &stubSSHFactory{
		connectFn: func(_ context.Context, _ string, opts ...ssh.ConnectOption) (ssh.Channel, error) {
			cfg := &ssh.ConnectConfig{}
			for _, o := range opts {
				o(cfg)
			}
			r.mu.Lock()
			r.got, r.seen = *cfg, true
			r.mu.Unlock()
			return ssh.NewStubChannel(logger), nil
		},
	}
}

func (r *sshConfigRecorder) config(t *testing.T) ssh.ConnectConfig {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.seen {
		t.Fatal("no ssh dial happened")
	}
	return r.got
}

// openOverWire sends the open RPC with exactly the params given and fails
// on a JSON-RPC error. The params deliberately never mention integration.
func openOverWire(t *testing.T, ws *WSServer, params map[string]any) {
	t.Helper()
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })

	resp := jsonrpcCall(t, conn, "open", params)
	var parsed struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("unmarshal open response: %v", err)
	}
	if parsed.Error != nil {
		t.Fatalf("open failed: %s", parsed.Error.Message)
	}
}

// A profile-backed ssh session asks for integration. This is the path the
// connection manager uses, and the one the owner reported flat.
func TestWSServer_Open_SSHProfile_RequestsIntegration(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)
	rec := &sshConfigRecorder{}
	reg.WithSSHFactory(rec.factory(logger))

	ws := NewWSServer(logger, reg,
		WithProfileResolver(&fakeResolver{
			resolveFn: func(_ string) (string, *ssh.ConnectConfig, error) {
				return "host.example.com", &ssh.ConnectConfig{User: "test", Port: 22}, nil
			},
		}),
	)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })

	openOverWire(t, ws, map[string]any{
		"cols": 80, "rows": 24, "kind": "ssh", "profileId": "ssh:test:1",
	})

	if got := rec.config(t); !got.Enhanced {
		t.Error("ssh dial did not request integration: ConnectConfig.Enhanced is false, " +
			"so no lifecycle channel is established and the tab can never show blocks")
	}
}

// A direct-host ssh session (an alias typed into the host field, no saved
// profile) asks for integration too — the same tab, the same expectation.
func TestWSServer_Open_SSHDirectHost_RequestsIntegration(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)
	rec := &sshConfigRecorder{}
	reg.WithSSHFactory(rec.factory(logger))

	resolver := newLauncherTestResolver()
	resolver.add("pi@192.168.0.93", ssh.HostConfig{User: "pi", HostName: "192.168.0.93", Port: 22})

	ws := NewWSServer(logger, reg, WithSSHConfigResolver(resolver, "/nonexistent/config"))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })

	openOverWire(t, ws, map[string]any{
		"cols": 80, "rows": 24, "kind": "ssh", "host": "pi@192.168.0.93",
	})

	if got := rec.config(t); !got.Enhanced {
		t.Error("direct-host ssh dial did not request integration: ConnectConfig.Enhanced is false")
	}
}

func TestWSServer_Open_SSHDirectHost_RejectsOptionLikeHost(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)
	rec := &sshConfigRecorder{}
	reg.WithSSHFactory(rec.factory(logger))

	ws := NewWSServer(logger, reg, WithSSHConfigResolver(newLauncherTestResolver(), "/nonexistent/config"))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })

	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	resp := jsonrpcCall(t, conn, "open", map[string]any{
		"cols": 80, "rows": 24, "kind": "ssh", "host": "-F/tmp/attacker_config",
	})
	var envelope struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal open response: %v", err)
	}
	if envelope.Error == nil || envelope.Error.Code != -32602 {
		t.Fatalf("option-like host: got %+v, want -32602", envelope.Error)
	}

	rec.mu.Lock()
	dialed := rec.seen
	rec.mu.Unlock()
	if dialed {
		t.Error("option-like host reached the SSH dialer")
	}
}

// The local session asks for integration without the renderer saying so.
// It already worked, because the renderer happened to pass true; the
// assertion is that it keeps working once the parameter is gone.
func TestWSServer_Open_Local_RequestsIntegration(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	ptf := &recordingPTYFactory{stub: pty.NewStub(logger)}
	ws := NewWSServer(logger, session.New(logger, ptf))

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })

	openOverWire(t, ws, map[string]any{"cols": 80, "rows": 24})

	if got := ptf.config(t); !got.Enhanced {
		t.Error("local pty did not request integration: pty.Config.Enhanced is false")
	}
}
