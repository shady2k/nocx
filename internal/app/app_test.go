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
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/skill"
	"github.com/shady2k/nocx/internal/storage"
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
	if a.localHelper == nil {
		t.Error("localHelper is nil: this machine's panes have no opener")
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
	if a.Transport.Port() == 0 {
		t.Fatal("Transport.Port() == 0 after Start")
	}

	a.Shutdown(ctx)
}

func TestWSPortBeforeStart(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	if a.Transport.Port() != 0 {
		t.Fatalf("expected 0 before Start, got %d", a.Transport.Port())
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

	if got := a.logFilePath; got != path {
		t.Errorf("logFilePath = %q, want %q", got, path)
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

	if got := a.logFilePath; got != "" {
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
	if got := a.logFilePath; got != "" {
		t.Errorf("LogFilePath() = %q, want \"\" when the file could not be opened", got)
	}
}

// THE COMPOSITION ROOT WIRES THIS MACHINE'S OPENER, and every seam it reaches
// through (nocx-ie23r.3).
//
// It replaces three tests that asserted the same thing about internal/app's
// localPTYFactory — that it held the lifecycle kernel, that it bound a lane to
// its session, and that the enhanced pty it returned carried a channel. That
// factory is gone: the daemon forks the shell and the coordinator adopts what
// it minted. What is left to assert here is that every fact the factory used
// to carry still has somebody carrying it, because a nil seam here is a
// feature that silently reports nothing — which is how nocx-dvql, nocx-cgzc
// and nocx-u7uh.11 each shipped half-wired once already.
//
// It is a wiring check and it says so: it proves the seams EXIST, never that
// they work. That they work — that a pane really is the daemon's child, really
// integrates, really resizes and really reports its exit — is local_pane_test.
// go, which opens one through the shipped composition root.
func TestLocalPaneOpenerIsWiredAtTheCompositionRoot(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	defer a.Shutdown(context.Background())

	o := a.localHelper
	if o == nil {
		t.Fatal("no local pane opener: this machine has no route to its own helper")
	}
	for name, wired := range map[string]bool{
		"the session registry to adopt into":         o.registry != nil,
		"the lifecycle kernel (ADR-0024)":            o.kernel != nil,
		"the lifecycle loss report (nocx-dvql)":      o.lifecycleLoss != nil,
		"the process observer (nocx-cgzc)":           o.procs != nil,
		"the shell-replacement report (nocx-cgzc)":   o.reportShellReplaced != nil,
		"the child-domain registries (nocx-u7uh.11)": o.noteChildDomainParent != nil,
	} {
		if !wired {
			t.Errorf("this machine's pane opener has no %s", name)
		}
	}

	// And the registry it adopts into has NO local PTY factory, which is the
	// other half of the same statement: nothing in this process forks a shell
	// any more, so a local open that did not reach the helper has nowhere else
	// to go (ADR-0057).
	if _, oerr := a.Session.Open(context.Background(), session.Config{Kind: session.KindLocal}); oerr == nil {
		t.Fatal("the session registry opened a local session by itself: the second PTY owner is back")
	} else if !strings.Contains(oerr.Error(), "helper") {
		t.Fatalf("refusal = %q, want one that says the helper owns this machine's panes", oerr)
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
//
// SINCE nocx-ie23r.3 IT IS ALSO THE PROOF THAT THE MOVE KEPT IT. The shell is
// now forked by this machine's helper daemon and its lifecycle descriptor is
// the daemon's socketpair, carried to this coordinator over the helper's
// lifecycle window and interpreted by a stream adapter here — four hops where
// there used to be one inherited fd. Nothing about the ASSERTION changed,
// which is the point: the same fact reaches the same renderer over the same
// socket, or the epic broke shell integration on this machine and said
// nothing.
func TestLocalEnhancedSessionEstablishesThroughProductionWiring(t *testing.T) {
	requireIntegratedLoginShell(t)
	a := newLocalPaneApp(t)

	wsURL := "ws://127.0.0.1:" + strconv.Itoa(a.Transport.Port()) + "/session"
	conn, _, err := (&websocket.Dialer{
		Subprotocols: []string{"nocx.token." + a.Transport.Token()},
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

func TestNew_WiresOneWritableSkillStoreIntoTheAssistant(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	store, ok := a.skills.(*skill.Store)
	if !ok {
		t.Fatalf("skills = %T, want *skill.Store", a.skills)
	}
	createErr := store.Create("deploy", "d", "body")
	if createErr != nil {
		t.Fatalf("Create through the app's skill seam: %v", createErr)
	}
	paths, err := storage.NewAppPaths()
	if err != nil {
		t.Fatalf("NewAppPaths: %v", err)
	}
	path := filepath.Join(paths.ConfigDir(), "managed-skills", "deploy", "SKILL.md")
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("created skill %s is not on disk: %v", path, statErr)
	}
}
