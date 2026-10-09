package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/waittest"
	"github.com/shady2k/nocx/internal/workers"
)

// This crosses the composition root twice: the first app leaves a worker tab
// and its durable restart identity, and the second app must reopen that pane,
// run the agent record's configured resume argv through the real helper, and
// receive the launcher's authenticated enrolment for that same pane.
func TestAppRestartRelaunchesConfiguredWorkerAndEnrolsItsPriorPane(t *testing.T) {
	artifacts := realHelperArtifacts(t)
	home := storagetest.IsolateWithHome(t)
	helperBinary := filepath.Join(helperRoot(home, artifacts.hash()), "nocx-helper")
	t.Cleanup(func() { endTheDaemon(t, helperBinary) })

	invocation := filepath.Join(t.TempDir(), "resume-invocation")
	fakeBin := t.TempDir()
	fakeClaude := filepath.Join(fakeBin, "claude")
	script := "#!/bin/sh\nprintf '%s\\n' \"$PWD\" \"$@\" > \"$NOCX_RESTART_INVOCATION\"\nexec sleep 3600\n"
	if err := os.WriteFile(fakeClaude, []byte(script), 0o700); err != nil { //nolint:gosec // test executable
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NOCX_RESTART_INVOCATION", invocation)

	ctx := context.Background()
	cwdTarget := t.TempDir()
	cwd := filepath.Join(t.TempDir(), "cwd-link")
	if err := os.Symlink(cwdTarget, cwd); err != nil {
		t.Fatalf("symlink worker cwd: %v", err)
	}
	// A shell reports the resolved spelling of PWD for a symlinked CWD. Keep
	// this directory behind a link so Linux covers macOS's /var -> /private/var
	// path spelling too, while the restart record retains the logical spelling.
	resolvedCwd, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatalf("resolve symlink worker cwd: %v", err)
	}
	wantInvocation := resolvedCwd + "\n--continue"
	const workspaceID = "00000000-0000-7000-8000-00000000f301"
	const tabID = "00000000-0000-7000-8000-00000000f302"
	const paneID = "00000000-0000-7000-8000-00000000f303"

	first, err := newTestApp(t, withLocalHelperArtifacts(artifacts))
	if err != nil {
		t.Fatalf("New first app: %v", err)
	}
	t.Cleanup(func() { first.Shutdown(context.Background()) })
	if firstStartErr := first.Start(ctx); firstStartErr != nil {
		t.Fatalf("Start first app: %v", firstStartErr)
	}
	conn := dialAppWS(t, first)
	resp := callAppWS(t, conn, "workspaces.create", map[string]any{
		"id": workspaceID, "name": "prior worker", "colour": nil, "position": 0,
		"firstTab": map[string]any{
			"id": tabID, "name": nil, "colour": nil, "position": 0,
			"pinned": false, "layout": "row",
		},
		"firstPane": map[string]any{
			"id": paneID, "cwd": cwd, "kind": "local", "endpoint": nil, "sizeShare": 1,
		},
	}, 1)
	if resp.Error != nil {
		t.Fatalf("create prior worker tab: %+v", resp.Error)
	}
	if got := tabsInWindow(t, conn, 2); len(got) != 1 || got[0] != tabID {
		t.Fatalf("prior app tabs = %v, want the worker tab %q", got, tabID)
	}
	opened := callAppWS(t, conn, "open", map[string]any{"paneId": paneID, "cols": 80, "rows": 24}, 2)
	if opened.Error != nil {
		t.Fatalf("open prior worker pane: %+v", opened.Error)
	}
	var priorSession struct {
		SessionID string `json:"sessionId"`
	}
	if openedDecodeErr := json.Unmarshal(opened.Result, &priorSession); openedDecodeErr != nil || priorSession.SessionID == "" {
		t.Fatalf("decode prior worker session: id=%q err=%v", priorSession.SessionID, openedDecodeErr)
	}
	// Exercise the composition-root approval service and answer its real host
	// request over the attached renderer socket. The second app must reuse the
	// durable grant this consent writes; the test never edits its document.
	pending := pendingOf(t, first.agentEnroller.approval.Approve(ctx, session.ID(priorSession.SessionID), "claude"))
	resolveAgentConsent(t, conn)
	if reason := settledOf(t, pending); reason != "" {
		t.Fatalf("the consent request settled with %q", reason)
	}
	// The shell retries enrolment when this pending answer settles. The same
	// service must now read the just-persisted grant without asking again.
	if approvalRetryErr := first.agentEnroller.approval.Approve(ctx, session.ID(priorSession.SessionID), "claude"); approvalRetryErr != nil {
		t.Fatalf("retry approval after consent: %v", approvalRetryErr)
	}
	_ = conn.Close()

	paths, err := storage.NewAppPaths()
	if err != nil {
		t.Fatalf("app paths: %v", err)
	}
	restarts := workers.NewFileRestartStore(storage.NewDocumentStore(paths.ConfigDir()), "worker-restarts.json")
	if restartRecordErr := restarts.Record(ctx, workers.RestartRecord{
		Participant: "prior-worker", Group: "coordinator", CoordinatorSession: "old-coordinator",
		PaneID: paneID, TabID: tabID, Agent: "claude", Command: "claude", Cwd: cwd,
		Worktree: workers.Worktree{Path: cwd, Branch: "task-resume"},
		Resume:   workers.ResumeIdentity{Mode: workers.ResumeByCwd},
	}); restartRecordErr != nil {
		t.Fatalf("persist worker restart identity: %v", restartRecordErr)
	}
	first.Shutdown(ctx)
	// Shutdown deliberately detaches rather than kills helper-held sessions.
	// This acceptance models the bead's lost-PTY case, so end the old helper
	// process after its coordinator shuts down, without touching the durable
	// layout or worker restart record.
	endTheDaemon(t, helperBinary)

	// New reads the real layout and restart documents and uses the installed
	// local helper. Start then makes the restarted app available to the client.
	restarted, err := newTestApp(t, withLocalHelperArtifacts(artifacts))
	if err != nil {
		t.Fatalf("New restarted app: %v", err)
	}
	if restartedStartErr := restarted.Start(ctx); restartedStartErr != nil {
		t.Fatalf("Start restarted app: %v", restartedStartErr)
	}
	defer restarted.Shutdown(ctx)
	conn = dialAppWS(t, restarted)
	defer func() { _ = conn.Close() }()
	if got := tabsInWindow(t, conn, 3); len(got) != 1 || got[0] != tabID {
		t.Fatalf("tabs after restart = %v, want the prior worker tab %q exactly once", got, tabID)
	}
	if _, priorSessionLookupErr := restarted.Session.Get(session.ID(priorSession.SessionID)); priorSessionLookupErr == nil {
		t.Fatalf("prior helper session %q was re-adopted after its helper process was ended", priorSession.SessionID)
	}

	waittest.WaitForTimeout(t, "configured Claude resume invocation", 10*time.Second, func() bool {
		actual, readErr := os.ReadFile(invocation) //nolint:gosec // test-owned temporary path
		return readErr == nil && strings.TrimSpace(string(actual)) == wantInvocation
	})
	actual, err := os.ReadFile(invocation) //nolint:gosec // test-owned temporary path
	if err != nil {
		t.Fatalf("read actual Claude invocation: %v", err)
	}
	if got := strings.TrimSpace(string(actual)); got != wantInvocation {
		t.Fatalf("configured CLI received %q, want cwd and agent-record resume argv %q", got, wantInvocation)
	}

	var resumedSessionID string
	waittest.WaitForTimeout(t, "session restored for the prior worker pane", 10*time.Second, func() bool {
		var found bool
		resumedSessionID, found = sessionForPane(restarted, paneID)
		return found
	})
	if resumedSessionID == priorSession.SessionID {
		t.Fatalf("resumed session %q reused the prior session id", resumedSessionID)
	}
	waittest.WaitForTimeout(t, "authenticated agent enrolment of the restored pane", 10*time.Second, func() bool {
		return restarted.paneViews.Watched(resumedSessionID)
	})
	if !restarted.paneViews.Watched(resumedSessionID) {
		t.Fatalf("restored pane %q (session %q) ran the resume command but was not enrolled", paneID, resumedSessionID)
	}
}

// resolveAgentConsent answers the real host.request emitted by the first app's
// agent-approval service. The caller waits on the pending enrollment's settle event.
func resolveAgentConsent(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	var request struct {
		RequestID  string `json:"requestId"`
		Capability string `json:"capability"`
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		_ = conn.SetReadDeadline(deadline)
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read agent consent request: %v", err)
		}
		var frame struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(raw, &frame) != nil || frame.Method != "host.request" {
			continue
		}
		if err := json.Unmarshal(frame.Params, &request); err != nil {
			t.Fatalf("decode agent consent request: %v", err)
		}
		break
	}
	if request.RequestID == "" || request.Capability != "agent.approval" {
		t.Fatalf("host request = %+v, want an agent approval request", request)
	}
	answer := callAppWS(t, conn, "host.resolved", map[string]any{
		"requestId": request.RequestID, "outcome": "ok", "approved": true,
	}, 3001)
	if answer.Error != nil {
		t.Fatalf("answer agent consent: %+v", answer.Error)
	}
}

// sessionForPane uses the session registry's durable pane association. The
// watcher store is keyed by session id, not by the renderer's pane id.
func sessionForPane(a *App, paneID string) (string, bool) {
	for _, sess := range a.Session.List() {
		if sess.PaneID() == paneID {
			return string(sess.ID()), true
		}
	}
	return "", false
}
