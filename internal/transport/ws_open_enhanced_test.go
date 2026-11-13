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
	"strings"
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

// An ssh pane asks to be integrated, and the request reaches the party that
// builds the shell: this machine's helper. It is asserted on the config the
// helper is handed, because that is where the request now travels — the
// coordinator dials nothing, so there is no ConnectOptions seam left to read.
func TestWSServer_Open_SSHProfile_RequestsIntegration(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)
	var handed session.Config
	opener := stubHelper(reg)
	opener.onOpen = func(cfg session.Config) { handed = cfg }

	ws := NewWSServer(logger, reg,
		WithHelperSessionOpener(opener),
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
	if !handed.Enhanced {
		t.Error("the ssh open did not ask to be integrated: session.Config.Enhanced is false, " +
			"so the helper builds a command with no domain and the tab can never show blocks")
	}
	if handed.Remote == nil {
		t.Fatal("the ssh open reached the helper with no resolved destination")
	}
}

// A direct-host ssh session (an alias typed into the host field, no saved
// profile) asks for integration too — the same tab, the same expectation.
func TestWSServer_Open_SSHDirectHost_RequestsIntegration(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)
	var handed session.Config
	opener := stubHelper(reg)
	opener.onOpen = func(cfg session.Config) { handed = cfg }

	resolver := newLauncherTestResolver()
	resolver.add("pi@192.0.2.93", ssh.HostConfig{User: "pi", HostName: "192.0.2.93", Port: 22})

	ws := NewWSServer(logger, reg,
		WithHelperSessionOpener(opener),
		WithSSHConfigResolver(resolver, "/nonexistent/config"))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })

	openOverWire(t, ws, map[string]any{
		"cols": 80, "rows": 24, "kind": "ssh", "host": "pi@192.0.2.93",
	})
	if !handed.Enhanced {
		t.Error("the direct-host ssh open did not ask to be integrated: session.Config.Enhanced is false")
	}
	if handed.Remote == nil {
		t.Fatal("the direct-host ssh open reached the helper with no resolved destination")
	}
}

func TestWSServer_Open_SSHDirectHost_RejectsOptionLikeHost(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)

	resolver := newLauncherTestResolver()
	ws := NewWSServer(logger, reg, WithSSHConfigResolver(resolver, "/nonexistent/config"))
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
	var parsed struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("unmarshal open response: %v", err)
	}
	if parsed.Error == nil || parsed.Error.Message == "" {
		t.Fatalf("open response = %s, want validation error", resp)
	}
	if !strings.Contains(parsed.Error.Message, "host must not begin with a dash") {
		t.Fatalf("open error = %q, want option-like host rejection", parsed.Error.Message)
	}
	// No helper is wired at all, so reaching one would answer the named
	// refusal instead; the validation message above is what proves the host
	// never got that far.
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
