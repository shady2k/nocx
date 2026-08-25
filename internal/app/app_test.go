package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

func TestNew(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	if a == nil {
		t.Fatal("New() returned nil app")
	}
}

func TestNew_AllModulesInjected(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	if a.Logger == nil {
		t.Error("Logger is nil")
	}
	if a.Pty == nil {
		t.Error("Pty is nil")
	}
	if a.Session == nil {
		t.Error("Session is nil")
	}
	if a.Transport == nil {
		t.Error("Transport is nil")
	}
	if a.ShellIntegration == nil {
		t.Error("ShellIntegration is nil")
	}
}

func TestStartShutdown(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	ctx := context.Background()
	if err := a.Start(ctx); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	if a.WSPort() == 0 {
		t.Fatal("WSPort() == 0 after Start")
	}

	a.Shutdown(ctx)
}

func TestWSPortBeforeStart(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	if a.WSPort() != 0 {
		t.Fatalf("expected 0 before Start, got %d", a.WSPort())
	}
}

// ── History settings → store wiring ──────────────────────────────────────

// The two-number budget flows from the History settings (MiB) into the
// store's byte budget; a zero or inverted configuration is refused so the
// store stays closed rather than shipping an unbounded database.
func TestBudgetFromSettings(t *testing.T) {
	fd := &appFakeDoc{}
	reg := settings.New(fd, nil)
	_ = reg.SetNumber(settings.HistoryRetentionMiB, 256)
	_ = reg.SetNumber(settings.HistoryDiskCeilingMiB, 1024)

	b, err := budgetFromSettings(reg)
	if err != nil {
		t.Fatalf("budgetFromSettings: %v", err)
	}
	if b.RetentionBytes != 256<<20 {
		t.Errorf("RetentionBytes = %d, want 256 MiB", b.RetentionBytes)
	}
	if b.DiskCeilingBytes != 1024<<20 {
		t.Errorf("DiskCeilingBytes = %d, want 1024 MiB", b.DiskCeilingBytes)
	}
	if err := b.Validate(); err != nil {
		t.Fatalf("assembled budget invalid: %v", err)
	}

	// Inverted configuration is refused, not shipped.
	_ = reg.SetNumber(settings.HistoryRetentionMiB, 2048) // above the ceiling
	if _, err := budgetFromSettings(reg); err == nil {
		t.Fatal("inverted budget accepted")
	}
}

// The live policy flows from the History settings, and the settings change
// notifier updates it — a toggle applies without a restart.
func TestPolicyFromSettingsAndLiveUpdates(t *testing.T) {
	fd := &appFakeDoc{}
	reg := settings.New(fd, nil)
	_ = reg.SetBool(settings.HistoryEnabled, false)
	_ = reg.SetNumber(settings.HistoryRetentionDays, 30)
	_ = reg.SetBool(settings.HistoryOutputEnabled, false)

	p := policyFromSettings(reg)
	if p.Enabled() {
		t.Error("policy enabled despite history.enabled=false")
	}
	if p.RetentionDays() != 30 {
		t.Errorf("RetentionDays = %d, want 30", p.RetentionDays())
	}
	if p.OutputEnabled() {
		t.Error("policy output enabled despite history.outputEnabled=false")
	}

	// Live: the notifier re-reads the registry after a mutation.
	reg.AddNotifier(func(_ int, keys []string) {
		for _, k := range keys {
			switch k {
			case settings.HistoryEnabled.Key():
				if v, err := reg.GetBool(settings.HistoryEnabled); err == nil {
					p.SetEnabled(v)
				}
			case settings.HistoryRetentionDays.Key():
				if v, err := reg.GetNumber(settings.HistoryRetentionDays); err == nil {
					p.SetRetentionDays(int(v))
				}
			}
		}
	})
	_ = reg.SetBool(settings.HistoryEnabled, true)
	_ = reg.SetNumber(settings.HistoryRetentionDays, 7)
	if !p.Enabled() {
		t.Error("policy not live-updated for history.enabled")
	}
	if p.RetentionDays() != 7 {
		t.Errorf("RetentionDays = %d after live update, want 7", p.RetentionDays())
	}
}

// appFakeDoc is a minimal in-memory DocumentStore for settings.New.
type appFakeDoc struct {
	data map[string][]byte
}

func (f *appFakeDoc) Read(name string, into any) (bool, error) {
	b, ok := f.data[name]
	if !ok || b == nil {
		return false, nil
	}
	return true, json.Unmarshal(b, into)
}

func (f *appFakeDoc) Write(name string, doc any) error {
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if f.data == nil {
		f.data = make(map[string][]byte)
	}
	f.data[name] = b
	return nil
}

func (f *appFakeDoc) Delete(name string) error {
	delete(f.data, name)
	return nil
}

// TestNew_LogFile: a running session can say where the log lives. The
// pinned path is reported by LogFilePath, the file exists, and its first
// line names the path — a reader who finds the file learns where it is.
func TestNew_LogFile(t *testing.T) {
	storagetest.Isolate(t)
	path := filepath.Join(t.TempDir(), "nocx.log")
	a, err := newTestApp(t, WithLogFilePath(path))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer a.Shutdown(context.Background())

	if got := a.LogFilePath(); got != path {
		t.Errorf("LogFilePath() = %q, want %q", got, path)
	}
	b, err := os.ReadFile(path) // #nosec G304 — the test's own temp path.
	if err != nil {
		t.Fatalf("log file was not written: %v", err)
	}
	if !bytes.Contains(b, []byte("backend log file")) || !bytes.Contains(b, []byte(path)) {
		t.Errorf("log file does not name its own path; content:\n%s", b)
	}
	if !bytes.Contains(b, []byte("application initialized")) {
		t.Errorf("log file missing the initialization line; content:\n%s", b)
	}
}

// TestNew_LogFileDisabled: an empty pinned path disables file logging and
// LogFilePath reports it — nothing is written anywhere unexpected.
func TestNew_LogFileDisabled(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t, WithLogFilePath(""))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer a.Shutdown(context.Background())

	if got := a.LogFilePath(); got != "" {
		t.Errorf("LogFilePath() = %q, want \"\" when file logging is disabled", got)
	}
}

// TestNew_LogFileStderrOnlyOnFailure: when the pinned directory cannot be
// created, the app still starts — fail-open, stderr only — and says the
// path is unavailable.
func TestNew_LogFileUnavailableStartsAnyway(t *testing.T) {
	storagetest.Isolate(t)
	// A path whose parent is a regular file cannot be a directory.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	a, err := newTestApp(t, WithLogFilePath(filepath.Join(blocker, "nocx.log")))
	if err != nil {
		t.Fatalf("New must fail open when the log file cannot be opened: %v", err)
	}
	defer a.Shutdown(context.Background())
	if got := a.LogFilePath(); got != "" {
		t.Errorf("LogFilePath() = %q, want \"\" when the file could not be opened", got)
	}
}

// TestLifecycleChannelWiring proves the composition root constructs the
// lifecycle kernel and wires one descriptor transport per enhanced session:
// an enhanced pty gets a channel that closes with it; a conventional pty
// gets none (ADR-0024 decision 4: no channel, conventional session).
func TestLifecycleChannelWiring(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	f, ok := a.Pty.(*localPTYFactory)
	if !ok {
		t.Fatalf("Pty factory is %T, want *localPTYFactory", a.Pty)
	}
	if f.kernel == nil {
		t.Fatal("lifecycle kernel was not constructed at the composition root")
	}
	if f.registerLane == nil {
		t.Fatal("lifecycle lane registration was not wired at the composition root")
	}

	enhanced, err := f.NewPTY(context.Background(), pty.Config{Cols: 80, Rows: 24, Enhanced: true, SessionID: "sid-wiring"})
	if err != nil {
		t.Fatalf("NewPTY(enhanced): %v", err)
	}
	wrapped, ok := enhanced.(*lifecyclePTY)
	if !ok {
		t.Fatalf("enhanced pty is %T, want *lifecyclePTY", enhanced)
	}
	if wrapped.ch == nil {
		t.Fatal("enhanced session got no lifecycle channel")
	}
	_ = wrapped.Close()

	plain, err := f.NewPTY(context.Background(), pty.Config{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("NewPTY(conventional): %v", err)
	}
	if _, ok := plain.(*lifecyclePTY); ok {
		t.Fatal("conventional session must not carry a lifecycle channel")
	}
	_ = plain.Close()
}

// TestLifecycleLaneRegistrationBindsSession proves the production wiring of
// the lane→session registry: an enhanced pty spawn with a session id reports
// the minted lane bound to that id, and a conventional spawn reports
// nothing. Without this binding every published lifecycle fact is dropped at
// the transport and enhanced mode never engages — the fact path's dead half.
func TestLifecycleLaneRegistrationBindsSession(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	f, ok := a.Pty.(*localPTYFactory)
	if !ok {
		t.Fatalf("Pty factory is %T, want *localPTYFactory", a.Pty)
	}
	var registered []string
	f.registerLane = func(lane lifecycle.LaneID, sid string) {
		registered = append(registered, string(lane)+"->"+sid)
	}

	enhanced, err := f.NewPTY(context.Background(), pty.Config{
		Cols: 80, Rows: 24, Enhanced: true, SessionID: "sid-test-1",
	})
	if err != nil {
		t.Fatalf("NewPTY(enhanced): %v", err)
	}
	if len(registered) != 1 || !strings.HasSuffix(registered[0], "->sid-test-1") {
		t.Fatalf("registered = %v, want exactly one lane bound to sid-test-1", registered)
	}
	_ = enhanced.Close()

	plain, err := f.NewPTY(context.Background(), pty.Config{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("NewPTY(conventional): %v", err)
	}
	if len(registered) != 1 {
		t.Fatalf("a conventional spawn must not register a lane, got %v", registered)
	}
	_ = plain.Close()
}

// TestLocalEnhancedChildEnv_SecretNeverReachesIt is the nocx-u7uh.21
// acceptance assertion in its strongest form, over the SHELL'S ACTUAL
// environment rather than over what the factory intended to pass: a local
// enhanced session's shell carries the lifecycle addressing (the non-secret
// NOCX_LIFECYCLE_* names) but never the capability or the recovery fence —
// those ride the rcfile text, and a value in the environment would leak the
// authenticator to every child (ADR-0024 decision 2).
func TestLocalEnhancedChildEnv_SecretNeverReachesIt(t *testing.T) {
	storagetest.IsolateWithHome(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	f, ok := a.Pty.(*localPTYFactory)
	if !ok {
		t.Fatalf("Pty factory is %T, want *localPTYFactory", a.Pty)
	}

	pt, err := f.NewPTY(context.Background(), pty.Config{
		Cols: 80, Rows: 24, Enhanced: true, SessionID: "sid-env-proof",
	})
	if err != nil {
		t.Fatalf("NewPTY(enhanced): %v", err)
	}
	// Closed AND waited for: the shell writes ~/.bash_history from its exit
	// trap, and the disposable home outlives the test only if nothing is
	// still writing to it.
	defer func() {
		_ = pt.Close()
		select {
		case <-pt.Done():
		case <-time.After(10 * time.Second):
			t.Error("the shell did not exit after Close")
		}
	}()
	if _, ok := pt.(*lifecyclePTY); !ok {
		t.Fatalf("enhanced pty is %T, want *lifecyclePTY", pt)
	}
	// The shell reports its own environment (shellenviron_test.go); on macOS
	// the kernel will not report another process's to anybody.
	env := readShellEnviron(t, pt)
	if env["NOCX_SHELL_INTEGRATION"] != "1" {
		t.Fatal("the child's actual environment must carry the activation gate (proving this is the right process)")
	}
	// The point of the assertion: neither the capability nor the recovery
	// fence — both 64 lowercase hex — may exist anywhere in the delivered
	// environment. They ride the rcfile text only.
	for key, val := range env {
		if len(val) == 64 && isLowerHex(val) {
			t.Fatalf("child environment leaked a 64-hex authenticator (%s)", key)
		}
	}
}

func isLowerHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// jsonrpcCall sends one JSON-RPC request over conn and returns the envelope,
// skipping any notifications (exit, ack, lifecycle.changed) that may arrive
// first — the same reach pattern the launcher tests use.
func jsonrpcCall(t *testing.T, conn *websocket.Conn, method string, params any) struct {
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
} {
	t.Helper()
	req, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write %s: %v", method, err)
	}
	for {
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		_, raw, rerr := conn.ReadMessage()
		if rerr != nil {
			t.Fatalf("read %s response: %v", method, rerr)
		}
		var check struct {
			ID *json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(raw, &check)
		if check.ID == nil {
			continue // a notification (exit, ack, lifecycle.changed)
		}
		var resp struct {
			Error *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("decode %s response: %v", method, err)
		}
		return resp
	}
}

// TestLocalEnhancedSessionEstablishesThroughProductionWiring is the
// nocx-u7uh.21 acceptance proof (AGENTS.md rule 2: a feature that was never
// reachable must be proven through production wiring, not by mounting
// pieces): boot the REAL composition root, open a local enhanced session
// over the REAL WebSocket, and watch the published lifecycle fact reach
// prompt_ready — which exists only after the shell proved the per-epoch
// capability over the inherited descriptor. Before .21 this test could not
// pass: the local shell never learned its lane, domain, epoch or capability,
// so no local session ever established.
func TestLocalEnhancedSessionEstablishesThroughProductionWiring(t *testing.T) {
	storagetest.IsolateWithHome(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	if serr := a.Start(context.Background()); serr != nil {
		t.Fatalf("Start: %v", serr)
	}
	defer a.Shutdown(context.Background())

	wsURL := "ws://127.0.0.1:" + strconv.Itoa(a.WSPort()) + "/session"
	conn, _, err := (&websocket.Dialer{
		Subprotocols: []string{"nocx.token." + a.WSToken()},
	}).Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	open := jsonrpcCall(t, conn, "open", map[string]any{
		"cols": 80, "rows": 24,
	})
	if open.Error != nil {
		t.Fatalf("open: %+v", open.Error)
	}

	// ONE deadline for the whole wait, set once. A gorilla connection is
	// permanently failed by ANY read error, timeout included, and reading it
	// again panics ("repeated read on failed websocket connection"). The
	// inner 2-second deadline this used to re-arm each pass therefore turned
	// the first slow message into a panic that took the whole package down —
	// invisible on a runner where the fact always arrived inside two seconds,
	// reproducible on a loaded workstation (nocx-58gq).
	if err := conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v — the wait would be unbounded", err)
	}
	for {
		_, raw, rerr := conn.ReadMessage()
		if rerr != nil {
			t.Fatalf("a local enhanced session never reached prompt_ready through production wiring: %v", rerr)
		}
		var notif struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(raw, &notif); err != nil || notif.Method != "lifecycle.changed" {
			continue
		}
		var fact struct {
			Lifecycle string `json:"lifecycle"`
			Domain    string `json:"domain"`
		}
		if err := json.Unmarshal(notif.Params, &fact); err != nil {
			continue
		}
		if fact.Lifecycle == "prompt_ready" && fact.Domain != "" {
			return // the local shell established through production wiring
		}
	}
}
