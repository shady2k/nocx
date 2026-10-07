package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agentrecord"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/waittest"
	"github.com/shady2k/nocx/internal/workers"
)

type restartOpenRecorder struct {
	inner  sessionOpenerSeam
	specs  []transport.OpenSpec
	opened []transport.OpenedSession
}

func (r *restartOpenRecorder) OpenSession(ctx context.Context, spec transport.OpenSpec) (transport.OpenedSession, error) {
	r.specs = append(r.specs, spec)
	opened, err := r.inner.OpenSession(ctx, spec)
	if err == nil {
		r.opened = append(r.opened, opened)
	}
	return opened, err
}

// The restart path uses the durable pane row, the built-in Claude resume
// record, the actual configured executable on PATH, and the ordinary PTY and
// enrolment path. Assertions observe the process argv and its enrolment, not
// a constructed command string.
func TestRestartRelaunchesClaudeInItsPriorPaneAndEnrols(t *testing.T) {
	ctx := context.Background()
	invocation := filepath.Join(t.TempDir(), "invocation")
	fakeDir := t.TempDir()
	fakeClaude := filepath.Join(fakeDir, "claude")
	script := "#!/bin/sh\nprintf '%s\\n' \"$PWD\" \"$@\" > \"$NOCX_RESUME_INVOCATION\"\nexec sleep 3600\n"
	if err := os.WriteFile(fakeClaude, []byte(script), 0o700); err != nil { //nolint:gosec // test executable
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NOCX_RESUME_INVOCATION", invocation)

	var logs safeBuffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	stand := newHappyStand(t, withHappyStandLogger(logger))
	cwd := t.TempDir()
	const workspaceID = "00000000-0000-7000-8000-00000000f101"
	const tabID = "00000000-0000-7000-8000-00000000f102"
	const paneID = "00000000-0000-7000-8000-00000000f103"
	if _, err := stand.db.Layout().CreateWorkspace(ctx,
		content.Workspace{ID: workspaceID, Name: "restored", Position: 0},
		content.Tab{ID: tabID, WorkspaceID: workspaceID, Layout: content.LayoutRow},
		content.Pane{ID: paneID, TabID: tabID, Cwd: cwd, Kind: content.PaneLocal, SizeShare: 1},
	); err != nil {
		t.Fatalf("create prior worker tab: %v", err)
	}

	docs := storage.NewDocumentStore(t.TempDir())
	restarts := workers.NewFileRestartStore(docs, "worker-restarts.json")
	rec := workers.RestartRecord{
		Participant: "prior-worker", Group: "coordinator", CoordinatorSession: "old-coordinator",
		PaneID: paneID, TabID: tabID, Agent: "claude", Command: "claude", Cwd: cwd,
		Worktree: workers.Worktree{Path: cwd, Branch: "task-resume"},
		Resume:   workers.ResumeIdentity{Mode: workers.ResumeByCwd},
	}
	if err := restarts.Record(ctx, rec); err != nil {
		t.Fatalf("persist prior worker: %v", err)
	}
	agents, err := agentrecord.New(t.TempDir())
	if err != nil {
		t.Fatalf("agentrecord.New: %v", err)
	}

	// The startup read is the durable record + the layout chain. The normal
	// session opener then launches the saved command with the configured
	// resume argv from Claude's shipped record.
	restartedLogger := log.NewSlogAdapter(logger)
	restored := restoreWorkerRecords(ctx, restartedLogger, restarts, stand.db.Layout(), agentProbe{store: agents})
	if len(restored) != 1 || !restored[0].Restorable() || restored[0].Request.PaneID != paneID {
		t.Fatalf("startup restore = %+v, want prior pane %q restorable", restored, paneID)
	}
	opener := &restartOpenRecorder{inner: stand.tp}
	relaunchWorkerRecords(ctx, restored, opener, stand.tp)

	waittest.WaitForTimeout(t, "configured resumed CLI invocation", 10*time.Second, func() bool {
		_, statErr := os.Stat(invocation)
		return statErr == nil
	})
	got, err := os.ReadFile(invocation) //nolint:gosec // path is this test's own temp file
	if err != nil {
		t.Fatalf("read actual CLI invocation: %v", err)
	}
	if strings.TrimSpace(string(got)) != cwd+"\n--continue" {
		t.Fatalf("CLI received cwd and args %q, want %q and configured resume arguments", got, cwd)
	}
	waittest.WaitForTimeoutDetail(t, "agent enrolment from restored pane", 10*time.Second, func() string {
		if len(opener.opened) == 0 {
			return "no restored session was opened"
		}
		sid := string(opener.opened[0].Session.ID())
		return fmt.Sprintf("restored session %s watched=%t; app logs:\n%s\npane transcript:\n%s",
			sid, stand.grid.Watched(sid), logs.String(), stand.factory.transcript.dump())
	}, func() bool {
		return len(opener.opened) == 1 && stand.grid.Watched(string(opener.opened[0].Session.ID()))
	})
	if len(opener.specs) != 1 || opener.specs[0].PaneID != paneID {
		t.Fatalf("relaunch open specs = %+v, want exactly the prior pane %q", opener.specs, paneID)
	}
	if len(opener.opened) != 1 {
		t.Fatalf("opened sessions = %d, want the single restored session", len(opener.opened))
	}
	sid := string(opener.opened[0].Session.ID())
	if !stand.grid.Watched(sid) {
		t.Fatalf("restored session %s is not enrolled:\n%s", sid, logs.String())
	}
}

func TestRestartLeavesUnsupportedAgentPaneWithVisibleReason(t *testing.T) {
	ctx := context.Background()
	var logs safeBuffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	stand := newHappyStand(t, withHappyStandLogger(logger))
	cwd := t.TempDir()
	const workspaceID = "00000000-0000-7000-8000-00000000f201"
	const tabID = "00000000-0000-7000-8000-00000000f202"
	const paneID = "00000000-0000-7000-8000-00000000f203"
	if _, err := stand.db.Layout().CreateWorkspace(ctx,
		content.Workspace{ID: workspaceID, Name: "unsupported", Position: 0},
		content.Tab{ID: tabID, WorkspaceID: workspaceID, Layout: content.LayoutRow},
		content.Pane{ID: paneID, TabID: tabID, Cwd: cwd, Kind: content.PaneLocal, SizeShare: 1},
	); err != nil {
		t.Fatalf("create prior worker tab: %v", err)
	}
	docs := storage.NewDocumentStore(t.TempDir())
	restarts := workers.NewFileRestartStore(docs, "worker-restarts.json")
	if err := restarts.Record(ctx, workers.RestartRecord{
		Participant: "unsupported-worker", Group: "coordinator", CoordinatorSession: "old-coordinator",
		PaneID: paneID, TabID: tabID, Agent: "not-installed", Command: "not-installed", Cwd: cwd,
		Worktree: workers.Worktree{Path: cwd, Branch: "task-resume"},
		Resume:   workers.ResumeIdentity{Mode: workers.ResumeByCwd},
	}); err != nil {
		t.Fatalf("persist prior worker: %v", err)
	}
	agents, err := agentrecord.New(t.TempDir())
	if err != nil {
		t.Fatalf("agentrecord.New: %v", err)
	}
	restartedLogger := log.NewSlogAdapter(logger)
	restored := restoreWorkerRecords(ctx, restartedLogger, restarts, stand.db.Layout(), agentProbe{store: agents})
	if len(restored) != 1 || restored[0].Failure == nil {
		t.Fatalf("startup restore = %+v, want explicit unsupported-agent failure", restored)
	}
	relaunchWorkerRecords(ctx, restored, stand.tp, stand.tp)
	waittest.WaitForTimeout(t, "visible unsupported-agent explanation in the restored terminal", 10*time.Second, func() bool {
		return strings.Contains(stand.factory.transcript.dump(), "nocx could not resume not-installed")
	})
}
