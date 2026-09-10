package app

// THE BEAD'S OWN CHECK (nocx-ui8q6.5): a coordinator spawns a worker that
// reaches live and is told what it was started for, through the real
// launcher — not a harness beside it.
//
// # What is real here
//
// Everything between the coordinator's socket and the participant's pty is
// the shipped mechanism, unmodified: workers.spawn is dispatched over a real
// toolendpoint.Endpoint socket; the pane's shell is a real bash running the
// real internal/shellintegration/scripts/nocx.bash, which sends a real
// agent_enrol hello over the real internal/lifecyclechannel socketpair and
// waits for a real accept; the enrolment reaches a real paneEnroller, which
// opens a real panegrid.Store interval and calls a real paneobserve.Watcher's
// Watch; the pane's bytes are fed into that same grid on the pump path
// transport.WSServer already runs for every session; a real
// agentdriver.Registry (the shipped Claude() rule) classifies the resulting
// frame; a real agentcalib.Calibrations answers whether that rule may be
// typed against, verified against real corpus captures
// (internal/agentdriver/testdata/captures) exactly as
// internal/agentdriver/verify_corpus_test.go verifies the shipped rule; and
// the coordinator's task is put into the pane by the real
// internal/agenttyping.Typist — the bracketed paste and the submit key, each
// gated on a frame read immediately before it, same as production.
//
// # What is substituted, and why it is not the mechanism under test
//
// The only fake is the program named "claude" on PATH. AGENTS.md's own
// framing of this bead settles why that is legitimate rather than the
// forbidden substitution: "the agent is the thing being orchestrated, not
// the orchestration." A CI machine has no claude subscription, so something
// has to stand in for the model — but it cannot be a bare stub that prints
// nothing, because agenttyping refuses to type into anything but a
// POSITIVELY IDENTIFIED free_text screen, and a blank pane classifies
// unknown. So the stub's only job is to look like a claude session to the
// same driver a real one would be read by: it replays the exact recorded
// bytes of a real claude idle prompt (claude-idle.jsonl) to its own stdout,
// which is the only way to earn a free_text classification without also
// faking the classifier. Once the coordinator's task is typed into the
// pane's real input queue, the stub captures whatever raw bytes arrive on
// its stdin — proof the delivery happened, not a simulation of it.
//
// # The hole this closes
//
// workerSpawner.deliverTask treats a nil readiness or nil typist as "nobody
// asked", and lets the spawn succeed anyway (nocx-66gd0's own comment names
// this explicitly). Every other worker test in this package builds its stand
// with those two seams nil, because they only need the pane's shell to
// enrol, not to be typed into. This test is the one that asks
// newHappyStand for withHappyStandRealTyping — which wires the same
// paneobserve.Watcher, agentdriver.Registry, agentcalib.Calibrations and
// agenttyping.Typist the composition root builds — so a spawn that no
// longer types anything fails HERE, on the pane never receiving its task,
// rather than nowhere.
//
// Verification of this check (done by hand, not committed): commenting out
// workers.go's `spawner.readiness = realWatch` / `spawner.typist =
// paneTyping` lines in newHappyStand reproduces the nocx-66gd0 hole exactly
// — the spawn still succeeds and reaches live, and this test times out on
// "the coordinator's task to be typed into the participant's pane" because
// typedFile never receives anything. Muting the shell's enrolment (renaming
// the `claude` function nocx.bash defines, so __nocx_agent_run is never
// called) instead fails the same test earlier, on the enrolment log
// assertion, because workers.spawn itself never reaches live within its
// deadline and the RPC returns an error.

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agentcapture"
	"github.com/shady2k/nocx/internal/waittest"
	"github.com/shady2k/nocx/internal/workers"
)

// happyIdleCaptureBytes concatenates a real capture's raw PTY bytes, in
// order, exactly as internal/agentdriver's own capture replay feeds them to
// a grid (internal/agentdriver/capture_test.go's replayStore: "store.Feed(pane,
// []byte(c.Data))"). Read through agentcapture.Read rather than a second
// JSONL parser, because that is the one owner of this format
// (agentcapture's own package doc).
func happyIdleCaptureBytes(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join("..", "agentdriver", "testdata", "captures", "claude-idle.jsonl")
	_, chunks, err := agentcapture.Read(path)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	var out []byte
	for _, c := range chunks {
		out = append(out, []byte(c.Data)...)
	}
	return out
}

func TestACoordinatorSpawnsAWorkerAndTypesItsTask(t *testing.T) {
	const task = "read AGENTS.md and report back what it says about workers"

	idleFile := filepath.Join(t.TempDir(), "idle.bin")
	if err := os.WriteFile(idleFile, happyIdleCaptureBytes(t), 0o600); err != nil {
		t.Fatalf("write idle capture: %v", err)
	}
	typedFile := filepath.Join(t.TempDir(), "typed.bin")

	// The stub: raw mode so a keystroke lands as a byte rather than being
	// echoed back onto the screen the driver is reading (which would corrupt
	// the very classification agenttyping's gate depends on), then the real
	// idle screen, then everything nocx ever writes into this pane's input
	// captured verbatim.
	fakeDir := t.TempDir()
	fakeClaude := filepath.Join(fakeDir, "claude")
	script := "#!/bin/sh\n" +
		"stty raw -echo 2>/dev/null || true\n" +
		"cat \"$NOCX_TEST_IDLE_BYTES\"\n" +
		"exec cat >> \"$NOCX_TEST_TYPED_BYTES\"\n"
	if err := os.WriteFile(fakeClaude, []byte(script), 0o700); err != nil { //nolint:gosec // the test launcher must be executable
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NOCX_TEST_IDLE_BYTES", idleFile)
	t.Setenv("NOCX_TEST_TYPED_BYTES", typedFile)

	var logs safeBuffer
	stand := newHappyStand(t,
		withHappyStandLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))),
		withHappyStandRealTyping(),
		withHappyStandEnrolmentDeadline(20*time.Second),
	)

	// #1: workers.spawn dispatched through the real tool endpoint socket,
	// the way a coordinator reaches it (nocx-helper's own path).
	conn, err := net.Dial("unix", stand.endpoint.SocketPath())
	if err != nil {
		t.Fatalf("dial the tool socket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	params, err := json.Marshal(map[string]string{"command": "claude", "task": task})
	if err != nil {
		t.Fatalf("marshal spawn params: %v", err)
	}
	request, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "spawn-task-1", "method": "workers.spawn",
		"params": json.RawMessage(params),
	})
	if err != nil {
		t.Fatalf("marshal spawn request: %v", err)
	}
	if _, writeErr := conn.Write(append(request, '\n')); writeErr != nil {
		t.Fatalf("write spawn request: %v", writeErr)
	}
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	var response struct {
		Result *struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if decodeErr := json.NewDecoder(bufio.NewReader(conn)).Decode(&response); decodeErr != nil {
		t.Fatalf("decode spawn response: %v", decodeErr)
	}
	if response.Error != nil {
		t.Fatalf("workers.spawn refused: %+v\nlog:\n%s", response.Error, logs.String())
	}
	// #1 and #5: the tool answered with the participant, live.
	if response.Result == nil || response.Result.ID == "" || response.Result.State != string(workers.StateLive) {
		t.Fatalf("spawn result = %+v, want a live participant id\nlog:\n%s", response.Result, logs.String())
	}
	participant := response.Result.ID

	// #3: the agent enrolled — the record reached live for a real session,
	// and the log names that same session's enrolment.
	stored, err := stand.store.Participant(context.Background(), workers.ParticipantID(participant))
	if err != nil {
		t.Fatalf("read stored participant: %v", err)
	}
	if stored.State != workers.StateLive || stored.Liveness.SessionID == "" {
		t.Fatalf("stored participant = %+v, want a live worker with a session", stored)
	}
	written := logs.String()
	if !strings.Contains(written, "agent enrolled") || !strings.Contains(written, "session_id="+stored.Liveness.SessionID) {
		t.Fatalf("the log does not carry this participant's enrolment (session %q):\n%s",
			stored.Liveness.SessionID, written)
	}

	// #2: the pane's shell integrated for real. The enrolment just asserted
	// above could only have arrived over the real lifecycle channel's
	// hello/accept exchange (internal/lifecyclechannel, opened per pane by
	// happyRealPTYFactory) — this stand has no second, faked channel an
	// enrolment could have gone over instead.

	// #4: the coordinator's task, typed into the pane through
	// internal/agenttyping's real gate — a bracketed paste and a separate
	// submit key, each re-checked against a live frame immediately before
	// the write (agenttyping's own documented guarantee).
	// The paste and the submit key are two separate writes (agenttyping's own
	// package doc: "the submission is two writes, and never one"), so the
	// pane's input queue can hold the first without the second having
	// arrived yet — waiting on the paste alone would be exactly the
	// timing-dependent check AGENTS.md's testing rules forbid. Wait for
	// both.
	pasted := "\x1b[200~" + task + "\x1b[201~"
	var typed []byte
	waittest.WaitForTimeout(t, "the coordinator's task and its submit key to be typed into the participant's pane", 10*time.Second, func() bool {
		b, readErr := os.ReadFile(typedFile) //nolint:gosec // typedFile is this test's own tempdir path
		if readErr != nil {
			return false
		}
		typed = b
		return strings.Contains(string(b), pasted) && strings.Contains(string(b), "\r")
	})
	if !strings.Contains(string(typed), pasted) {
		t.Fatalf("typed bytes = %q, want the task framed as a bracketed paste", typed)
	}
	if !strings.Contains(string(typed), "\r") {
		t.Fatalf("typed bytes = %q, want the submit key sent after the paste", typed)
	}
}
