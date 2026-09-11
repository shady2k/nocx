package app

// THE EPIC'S OWN CHECK (nocx-f545a.5): "A worker stuck on a question says
// what it is asking, and its coordinator answers it." A real Claude Code,
// spawned as a worker into a directory it has not been trusted in, meets its
// folder-trust dialog. The coordinator reads the state and the question,
// answers it, and the worker then receives its task.
//
// # Why a real agent, when worker_orchestration_task_test.go already exists
//
// TestACoordinatorSpawnsAWorkerAndTypesItsTask is real in every part except
// the program named "claude" — a shell script replaying a RECORDED idle
// screen. A recording of an agent past its startup gate can never show a
// startup gate, which is exactly how the folder-trust hole this epic closes
// (nocx-f545a.1's own motivation) could ship behind a fully green suite: every
// test that ever ran "claude" ran a double that was never NOT trusted. This
// check runs the INSTALLED Claude Code itself, in a real interactive pane, so
// what shows up on screen when it starts in an unfamiliar directory is
// whatever the vendor CLI actually draws today — not a byte-for-byte replay of
// what it drew when internal/agentdriver/testdata/captures/claude-trust.jsonl
// was recorded. It does not replace that recorded-corpus test (which is fast,
// hermetic, and runs on every CI machine with no subscription); it proves the
// recording is still the truth.
//
// # What is real, exactly as worker_orchestration_task_test.go's own header
// states for its half of this stand: workers.spawn, workers.screen and
// workers.answer are dispatched over a real toolendpoint.Endpoint socket, the
// same one a coordinator's tool calls reach; the pane's shell is a real bash
// running the real internal/shellintegration/scripts/nocx.bash, whose
// `claude` shell function is what sends the real agent_enrol hello over the
// real internal/lifecyclechannel socketpair; a real agentdriver.Registry
// classifies the resulting frames; a real agentcalib.Calibrations verifies
// the shipped claude rule may be typed against; and workers.answer's option
// is chosen and confirmed through the real internal/agenttyping.Typist — the
// same gate a spawn's own task delivery and the coordinator's own wake go
// through (withHappyStandAnswering wires workerScreener and workerAnswerer
// exactly as app.go's composition root does).
//
// # Gating (nocx-f545a.5's own instruction, scripts/have-claude.sh's
// precedent)
//
// Skipped, not faked, when a real Claude Code is unavailable — distinguishing
// "not installed" (exit 1) from "installed but not authenticated" (exit 2),
// have-claude.sh's own contract. And gated a second way even when one IS
// available: NOCX_REAL_CLAUDE_WORKER=1 must be set, so this does not run on
// every `go test ./internal/app` on a machine that happens to have claude —
// it launches a real CLI process and burns a small amount of real usage. The
// Makefile's run_claude pass (around the CLAUDE_CONFORMANCE_PKG block) sets
// the variable and runs exactly this test, alongside — never instead of —
// internal/claudeconformance's own vendor check.
//
// # HOME (worked out, not assumed)
//
// Neither this stand (newHappyStand) nor happyRealPTYFactory sets or scrubs
// HOME: the worker pane's bash inherits this TEST PROCESS's own environment
// verbatim (pty_local.go's scrubLauncherSession drops only a short list of
// launcher-session variables, none of them HOME). Because nothing in this
// package isolates HOME the way keystore_stance_test.go deliberately does for
// its own reason, the worker's `claude` sees the developer's REAL
// ~/.claude.json and is authenticated exactly when the outer `go test`
// process is. No override was needed, and none is written: doing so would
// mean copying credentials somewhere, which nothing here should ever do.
//
// # The untrusted directory (worked out, not assumed)
//
// Claude Code inherits trust from ANCESTOR directories, and /tmp is trusted
// on this machine (and t.TempDir() is under it) — so a bare t.TempDir() would
// never show the dialog this check exists to force. untrustedClaudeWorkerDir
// reads ~/.claude.json READ-ONLY for every project path with
// hasTrustDialogAccepted, mints a directory under /var/tmp (untrusted here,
// and not exercised by any other test that could race it), and walks every
// ancestor of the resolved path checking none of them is in that set. If
// spawn nonetheless reports taskTyped:true, the test fails LOUDLY, naming the
// directory — refusing to pass vacuously rather than trusting that the walk
// above was exhaustive.
//
// nocx never derives a pane's cwd (workerSpawner.coordinatorCwd's own doc:
// "WHAT IT DOES NOT DO is derive a cwd of its own" — it reads a layout row
// SetPaneCwd wrote from a verified OSC 7, AD-5). This stand's coordinator pane
// never reports one, so coordinatorCwd always answers "" here regardless.
// Rather than plumb a fabricated layout row into a real product read whose
// whole contract is "only ever a verified renderer report" (which would put a
// second, test-only writer behind AD-5's one owner), the untrusted directory
// is reached the way a person types it: workers.spawn's own command field is
// "the command line that starts the worker, exactly as a person would type
// it" (contracts/tools/workers.spawn.schema.json), so the spawned command is
// `cd '<dir>' && claude` — a `cd` a person could type, not a synthetic seam.
//
// # THE ASSERTION THIS CHECK EXISTS TO MAKE (nocx-f545a.8)
//
// The first version of this check trusted nocx's OWN report — workers.answer
// coming back "submitted" — as proof the folder was trusted. Against the
// real, installed Claude Code 2.1.266 it was not: a down key written within
// ~100ms of the dialog's first paint repaints correctly (the marker moves to
// "Yes, I trust this folder") while Claude's own internal selection stays on
// its original default ("No, exit") permanently, so nocx wrote down, saw the
// repaint, confirmed with Enter, and Claude recorded NO trust and exited —
// invisibly, because the screen after the "bad" key is indistinguishable
// from the screen after a "good" one (menuSettle's own doc, workers.go, has
// the full measurement). workerAnswerer.Answer now waits for the menu to
// settle before its first key (nocx-f545a.8's fix); this test's own
// assertion is the other half — it no longer takes nocx's report on faith,
// and instead reads ~/.claude.json, the file and field the real CLI itself
// consults, to confirm the trust was ACTUALLY recorded. That read is what
// would have caught nocx-f545a.8 directly.
//
// # The cost this check has (stated once, here, per nocx-f545a.5's own
// instruction)
//
// Answering "Yes, I trust this folder" makes the REAL Claude Code record
// that temporary directory as trusted in ~/.claude.json — one harmless row
// per run, for a directory this test also deletes. Nothing here edits
// ~/.claude.json directly or removes that row: doing either would be this
// test reaching for a file whose only legitimate writer is the vendor CLI
// itself.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/waittest"
	"github.com/shady2k/nocx/internal/workers"
	"github.com/shady2k/nocx/internal/workspace"
)

// requireRealClaudeWorker asks scripts/have-claude.sh — the SAME script and
// the SAME two-exit-code contract the Makefile's run_claude pass and
// internal/claudeconformance's requireClaude already use — rather than
// re-deriving "is claude installed and authenticated" a third way. Skips
// with a sentence naming which of the two is missing; never falls back to a
// double.
func requireRealClaudeWorker(t *testing.T) string {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "have-claude.sh"))
	if err != nil {
		t.Fatalf("resolve scripts/have-claude.sh: %v", err)
	}
	cmd := exec.Command(script) //nolint:gosec // fixed repo-relative script, no dynamic input
	out, runErr := cmd.Output()
	if runErr == nil {
		path := strings.TrimSpace(string(out))
		if path == "" {
			t.Fatalf("scripts/have-claude.sh exited 0 but printed no path")
		}
		return path
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		switch exitErr.ExitCode() {
		case 1:
			t.Skip("Claude Code is not installed; install claude before running this check (scripts/have-claude.sh)")
		case 2:
			t.Skip("Claude Code is installed but not authenticated; run `claude auth login` before running this check (scripts/have-claude.sh)")
		}
	}
	t.Fatalf("scripts/have-claude.sh: unexpected failure: %v", runErr)
	return ""
}

// realClaudeTrustedProjects reads ~/.claude.json READ-ONLY and answers the
// set of project paths Claude Code itself already considers trusted
// (hasTrustDialogAccepted:true) — the same file and the same field the real
// CLI consults before it ever draws the folder-trust dialog. A missing file
// is an empty set, not a failure: a machine that has never trusted anything
// is a valid starting point for this check.
func realClaudeTrustedProjects(t *testing.T) map[string]bool {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve the home directory: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude.json")) //nolint:gosec // fixed path under the user's own home, read-only
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}
		}
		t.Fatalf("read ~/.claude.json: %v", err)
	}
	var parsed struct {
		Projects map[string]struct {
			HasTrustDialogAccepted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse ~/.claude.json: %v", err)
	}
	out := make(map[string]bool, len(parsed.Projects))
	for path, p := range parsed.Projects {
		if p.HasTrustDialogAccepted {
			out[path] = true
		}
	}
	return out
}

// untrustedClaudeWorkerDir mints a directory with NO trusted ancestor — see
// this file's own header for why /tmp (and so t.TempDir()) cannot be used.
// It is removed in cleanup; the one row Claude Code itself writes into
// ~/.claude.json for it once this test answers "trust this folder" is not
// (this file's header states that cost).
func untrustedClaudeWorkerDir(t *testing.T) string {
	t.Helper()
	trusted := realClaudeTrustedProjects(t)
	base, err := os.MkdirTemp("/var/tmp", "nocx-real-worker-trust-")
	if err != nil {
		t.Fatalf("mkdir a base with no trusted ancestor under /var/tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	dir := filepath.Join(base, "work")
	if mkdirErr := os.MkdirAll(dir, 0o750); mkdirErr != nil {
		t.Fatalf("mkdir the worker's own directory: %v", mkdirErr)
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve the worker's directory: %v", err)
	}
	for ancestor := real; ; {
		if trusted[ancestor] {
			t.Fatalf("the worker's directory %q has an ancestor %q already trusted in ~/.claude.json; "+
				"the folder-trust dialog would never appear there, and this check would pass vacuously — "+
				"pick a base with no trusted ancestor", real, ancestor)
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			break
		}
		ancestor = parent
	}
	return real
}

// singleQuoteShellArg quotes dir the way a person would, for the `cd` this
// file's own header explains is the spawn command's first act.
func singleQuoteShellArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// trustOptionFromRows finds the folder-trust option ON THE SCREEN rather than
// hard-coding its wording — asserting it is there is part of what this check
// proves. It strips the same two things optionText (internal/agenttyping)
// strips before comparing — surrounding space and a leading non-alphanumeric
// selection marker — so the returned string is what workers.answer's own
// schema says to pass: the option "exactly as its screen shows it .. Leave
// off the selection marker".
func trustOptionFromRows(t *testing.T, rows []string) string {
	t.Helper()
	for _, row := range rows {
		trimmed := strings.TrimSpace(row)
		if trimmed == "" {
			continue
		}
		cleaned := trimmed
		if r, size := utf8.DecodeRuneInString(cleaned); !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			cleaned = strings.TrimSpace(cleaned[size:])
		}
		lower := strings.ToLower(cleaned)
		if strings.Contains(lower, "trust") && strings.Contains(lower, "folder") {
			return cleaned
		}
	}
	t.Fatalf("no row on the worker's screen named a folder-trust option; screen:\n%s", strings.Join(rows, "\n"))
	return ""
}

type realClaudeRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type realClaudeRPCResponse struct {
	Result json.RawMessage     `json:"result"`
	Error  *realClaudeRPCError `json:"error"`
}

// realClaudeCall is one JSON-RPC round trip over conn, through dec — the SAME
// decoder for every call on one connection, so a response is never decoded
// out of order with the request that produced it. This is the raw
// toolendpoint wire a coordinator actually reaches (the same socket
// worker_orchestration_task_test.go's TestACoordinatorSpawnsAWorkerAndTypesItsTask
// dials), not a Go-level call into workers.Registrar.
func realClaudeCall(t *testing.T, conn net.Conn, dec *json.Decoder, id, method string, params any, timeout time.Duration) realClaudeRPCResponse {
	t.Helper()
	p, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal %s params: %v", method, err)
	}
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": json.RawMessage(p),
	})
	if err != nil {
		t.Fatalf("marshal %s request: %v", method, err)
	}
	if _, writeErr := conn.Write(append(req, '\n')); writeErr != nil {
		t.Fatalf("write %s request: %v", method, writeErr)
	}
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	var resp realClaudeRPCResponse
	if decodeErr := dec.Decode(&resp); decodeErr != nil {
		t.Fatalf("decode %s response: %v", method, decodeErr)
	}
	return resp
}

// closeRealClaudeWorker is the CLEANUP path (registered right after a spawn
// succeeds, so a failing run still closes the real claude process): a
// best-effort workers.close over its OWN short-lived connection, logged
// rather than fatal, because a cleanup that panics on an already-closed
// worker would hide whatever the test itself already reported.
func closeRealClaudeWorker(t *testing.T, socket, worker string) {
	t.Helper()
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Logf("cleanup: dial to close worker %s: %v", worker, err)
		return
	}
	defer func() { _ = conn.Close() }()
	params, err := json.Marshal(map[string]string{"worker": worker})
	if err != nil {
		t.Logf("cleanup: marshal close params for %s: %v", worker, err)
		return
	}
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "cleanup-close", "method": "workers.close",
		"params": json.RawMessage(params),
	})
	if err != nil {
		t.Logf("cleanup: marshal close request for %s: %v", worker, err)
		return
	}
	if _, writeErr := conn.Write(append(req, '\n')); writeErr != nil {
		t.Logf("cleanup: write close request for %s: %v", worker, writeErr)
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	var resp realClaudeRPCResponse
	if decodeErr := json.NewDecoder(conn).Decode(&resp); decodeErr != nil {
		t.Logf("cleanup: decode close response for %s: %v", worker, decodeErr)
		return
	}
	if resp.Error != nil {
		t.Logf("cleanup: workers.close for %s: %+v", worker, resp.Error)
	}
}

type realClaudeSpawnResult struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	TaskTyped bool   `json:"taskTyped"`
	WaitingOn string `json:"waitingOn"`
}

type realClaudeScreenResult struct {
	Worker   string   `json:"worker"`
	Readable bool     `json:"readable"`
	State    string   `json:"state"`
	Rows     []string `json:"rows"`
}

type realClaudeTaskOutcome struct {
	Delivery string `json:"delivery"`
	State    string `json:"state"`
	Reason   string `json:"reason"`
}

type realClaudeAnswerResult struct {
	Worker  string                 `json:"worker"`
	Outcome string                 `json:"outcome"`
	State   string                 `json:"state"`
	Reason  string                 `json:"reason"`
	Task    *realClaudeTaskOutcome `json:"task"`
}

type realClaudeCloseResult struct {
	ID    string `json:"id"`
	Ended bool   `json:"ended"`
}

func TestARealClaudeCoordinatorAnswersTheFolderTrustDialog(t *testing.T) {
	if os.Getenv("NOCX_REAL_CLAUDE_WORKER") != "1" {
		t.Skip("set NOCX_REAL_CLAUDE_WORKER=1 to run this check against the installed, authenticated Claude Code " +
			"(see scripts/have-claude.sh and the Makefile's run_claude pass); it does not run on every " +
			"`go test ./internal/app` just because a machine happens to have claude")
	}
	claudePath := requireRealClaudeWorker(t)
	workDir := untrustedClaudeWorkerDir(t)

	var logs safeBuffer
	stand := newHappyStand(t,
		withHappyStandLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))),
		withHappyStandAnswering(),
		// A real CLI's cold start (auth check, first paint of the trust
		// dialog) is slower than a replayed capture; generous so a hung CLI
		// fails as a failure, per AGENTS.md's own testing rule against
		// depending on timing to be right eventually — this is a deadline
		// for giving up, never a wait this test expects to spend.
		withHappyStandEnrolmentDeadline(45*time.Second),
	)
	ctx := context.Background()

	tabsBefore, err := stand.db.Layout().Tabs(ctx, string(workspace.Default))
	if err != nil {
		t.Fatalf("list tabs before spawn: %v", err)
	}

	conn, err := net.Dial("unix", stand.endpoint.SocketPath())
	if err != nil {
		t.Fatalf("dial the tool socket: %v", err)
	}
	defer func() { _ = conn.Close() }()
	dec := json.NewDecoder(conn)

	const task = "Reply with the single word ok and nothing else."
	// The untrusted directory as a person would reach it — a `cd`, not a
	// fabricated pane-cwd row. This file's own header explains why.
	command := "cd " + singleQuoteShellArg(workDir) + " && claude"
	t.Logf("real claude at %s; worker command %q; untrusted dir %s", claudePath, command, workDir)

	spawnResp := realClaudeCall(t, conn, dec, "spawn-1", "workers.spawn",
		map[string]string{"command": command, "task": task}, 60*time.Second)
	if spawnResp.Error != nil {
		t.Fatalf("workers.spawn refused: %+v\nlog:\n%s", spawnResp.Error, logs.String())
	}
	var spawned realClaudeSpawnResult
	if unmarshalErr := json.Unmarshal(spawnResp.Result, &spawned); unmarshalErr != nil {
		t.Fatalf("decode spawn result %s: %v", spawnResp.Result, unmarshalErr)
	}
	if spawned.ID == "" || spawned.State != string(workers.StateLive) {
		t.Fatalf("spawn result = %+v, want a live participant id\nlog:\n%s", spawned, logs.String())
	}
	worker := spawned.ID
	t.Logf("spawned worker %s: taskTyped=%v waitingOn=%q", worker, spawned.TaskTyped, spawned.WaitingOn)

	// Registered NOW, so a failure anywhere below still closes the real
	// claude process this test started — nocx-f545a.5's own instruction.
	t.Cleanup(func() { closeRealClaudeWorker(t, stand.endpoint.SocketPath(), worker) })

	// THE CHECK REFUSING TO PASS VACUOUSLY. A recorded idle capture can never
	// show a startup gate (this file's own header); the live equivalent of
	// that mistake is a directory that turns out to be trusted after all —
	// untrustedClaudeWorkerDir already checked ~/.claude.json's ancestors for
	// that, and this is the second, POSITIVE half of the same guard: if the
	// dialog did not appear, say so loudly rather than reporting green.
	if spawned.TaskTyped {
		t.Fatalf("the task was typed immediately — the folder-trust dialog never appeared for worker %s in %s; "+
			"this check would pass vacuously\nlog:\n%s", worker, workDir, logs.String())
	}
	if spawned.WaitingOn != string(agentdriver.StatePermissionChoice) && spawned.WaitingOn != string(agentdriver.StateModalChoice) {
		t.Fatalf("spawn result = %+v, want waitingOn to name a real question (permission_choice or modal_choice), not %q\nlog:\n%s",
			spawned, spawned.WaitingOn, logs.String())
	}

	screenResp := realClaudeCall(t, conn, dec, "screen-1", "workers.screen",
		map[string]string{"worker": worker}, 15*time.Second)
	if screenResp.Error != nil {
		t.Fatalf("workers.screen refused: %+v\nlog:\n%s", screenResp.Error, logs.String())
	}
	var screen realClaudeScreenResult
	if unmarshalErr := json.Unmarshal(screenResp.Result, &screen); unmarshalErr != nil {
		t.Fatalf("decode screen result %s: %v", screenResp.Result, unmarshalErr)
	}
	if !screen.Readable {
		t.Fatalf("workers.screen said the pane is not readable right after a spawn that reported waitingOn=%q", spawned.WaitingOn)
	}
	if screen.State != string(agentdriver.StatePermissionChoice) && screen.State != string(agentdriver.StateModalChoice) {
		t.Fatalf("workers.screen state = %q, want a question state; rows:\n%s", screen.State, strings.Join(screen.Rows, "\n"))
	}
	// The participant's tab still exists: it is on the same call that just
	// read the screen through it.
	option := trustOptionFromRows(t, screen.Rows)
	t.Logf("screen: state=%q option=%q rows=%d", screen.State, option, len(screen.Rows))

	answerResp := realClaudeCall(t, conn, dec, "answer-1", "workers.answer",
		map[string]string{"worker": worker, "option": option}, 40*time.Second)
	if answerResp.Error != nil {
		t.Fatalf("workers.answer refused: %+v\nlog:\n%s", answerResp.Error, logs.String())
	}
	var answer realClaudeAnswerResult
	if unmarshalErr := json.Unmarshal(answerResp.Result, &answer); unmarshalErr != nil {
		t.Fatalf("decode answer result %s: %v", answerResp.Result, unmarshalErr)
	}
	t.Logf("answer raw = %s", answerResp.Result)
	if answer.Outcome != "submitted" {
		t.Fatalf("answer outcome = %+v, want the folder-trust option confirmed\nlog:\n%s", answer, logs.String())
	}
	if answer.Task == nil || answer.Task.Delivery != "typed" {
		diagResp := realClaudeCall(t, conn, dec, "screen-2", "workers.screen",
			map[string]string{"worker": worker}, 15*time.Second)
		var diag realClaudeScreenResult
		_ = json.Unmarshal(diagResp.Result, &diag)
		t.Fatalf("answer = %+v, want task.delivery == \"typed\"; screen after the answer (state=%q):\n%s\nlog:\n%s",
			answer, diag.State, strings.Join(diag.Rows, "\n"), logs.String())
	}
	t.Logf("answer: outcome=%q task.delivery=%q", answer.Outcome, answer.Task.Delivery)

	// THE ASSERTION nocx-f545a.8 ADDS: nocx's own "submitted" report is not
	// proof the folder was actually trusted (this file's own header, "THE
	// ASSERTION THIS CHECK EXISTS TO MAKE", has the full account of why not).
	// ~/.claude.json is the file and field the real CLI itself consults
	// before it ever draws this dialog again, so reading it back is what
	// would have caught the desync directly rather than trusting the screen.
	// Waited on state, never on a duration: the CLI writes the file some
	// short time after it accepts the confirm key.
	waittest.WaitForTimeoutDetail(t, "Claude Code to record this directory as trusted in ~/.claude.json", 15*time.Second,
		func() string {
			trusted := realClaudeTrustedProjects(t)
			return fmt.Sprintf("trusted projects = %v; want %q present\nlog:\n%s", trusted, workDir, logs.String())
		},
		func() bool {
			return realClaudeTrustedProjects(t)[workDir]
		})
	t.Logf("Claude Code recorded %s as trusted in ~/.claude.json", workDir)

	// THE OBSERVABLE CONSEQUENCE. answer.Task.Delivery == "typed" is nocx's
	// own report; with a real agent nothing here can capture its stdin the
	// way the fake claude in worker_orchestration_task_test.go does, so what
	// closes the loop is what a coordinator could look at itself: the task's
	// own bytes on screen, or the pane having left free_text to start
	// working on it. Waited on state, never on a duration.
	waittest.WaitForTimeoutDetail(t, "the worker's pane to show the task was delivered", 15*time.Second,
		func() string {
			resp := realClaudeCall(t, conn, dec, "screen-detail", "workers.screen",
				map[string]string{"worker": worker}, 15*time.Second)
			var s realClaudeScreenResult
			_ = json.Unmarshal(resp.Result, &s)
			return fmt.Sprintf("state=%q rows:\n%s", s.State, strings.Join(s.Rows, "\n"))
		},
		func() bool {
			resp := realClaudeCall(t, conn, dec, "screen-poll", "workers.screen",
				map[string]string{"worker": worker}, 15*time.Second)
			if resp.Error != nil {
				return false
			}
			var s realClaudeScreenResult
			if unmarshalErr := json.Unmarshal(resp.Result, &s); unmarshalErr != nil {
				return false
			}
			if strings.Contains(strings.Join(s.Rows, "\n"), task) {
				return true
			}
			return s.State == string(agentdriver.StateWorking)
		})

	tabsAfter, err := stand.db.Layout().Tabs(ctx, string(workspace.Default))
	if err != nil {
		t.Fatalf("list tabs after answering: %v", err)
	}
	if len(tabsAfter) != len(tabsBefore)+1 {
		t.Fatalf("tabs before=%d after=%d, want exactly one more — the participant's tab must still exist throughout",
			len(tabsBefore), len(tabsAfter))
	}

	closeResp := realClaudeCall(t, conn, dec, "close-1", "workers.close",
		map[string]string{"worker": worker}, 15*time.Second)
	if closeResp.Error != nil {
		t.Fatalf("workers.close refused: %+v", closeResp.Error)
	}
	var closed realClaudeCloseResult
	if unmarshalErr := json.Unmarshal(closeResp.Result, &closed); unmarshalErr != nil {
		t.Fatalf("decode close result %s: %v", closeResp.Result, unmarshalErr)
	}
	if closed.ID != worker || !closed.Ended {
		t.Fatalf("close result = %+v, want ended worker %q", closed, worker)
	}
}
