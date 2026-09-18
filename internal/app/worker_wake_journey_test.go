package app

// THE END-TO-END CHECK (nocx-luqz9.7): one automated check that watches a
// coordinator LEARN — without calling anything — that its worker reported,
// went idle, blocked and exited, and watches the human be called when the
// coordinator does not read.
//
// It is AGENTS.md rule 2's one happy path for epic nocx-luqz9, written against
// the stand session_surface_realhelper_test.go already established: a REAL
// cmd/nocx-helper, REAL PTYs, the REAL libghostty-vt emulator beside them, the
// REAL bridge, authorizer and tool endpoint, the REAL worker record, mailbox
// and wake, under a disposable HOME — and, on both sides of the conversation,
// a real program in a real pane calling the product's own tools.
//
// # What is REAL here, step by step
//
//   - BOTH PANES ARE REAL. The worker's pane is opened by production's own
//     spawn path and the coordinator's by this stand, each a real shell on the
//     real helper with the mock agent (internal/app/testdata/s14mock) running
//     in it. Every frame either pane is classified from is reconstructed by
//     the helper's own emulator from bytes the mock painted.
//   - EVERY CALL IS REAL AND COMES FROM A PANE. The mock starts the MCP
//     bridge this repository ships (`<helper> mcp --socket …`, the invocation
//     the shell integration stages into a real agent's mcp.json) as its own
//     child, so the endpoint admits the call as that pane's session by the
//     process tree it runs in. The worker's report, the coordinator's spawn,
//     its inbox read, its catalogue question and its close all travel that
//     way, through the shipped authorizer, dispatcher and record. Nothing in
//     this check writes a mailbox or a record by hand.
//   - THE SPAWN is production's: workers.spawn → Registrar.Register → the real
//     spawner, which mints a tab in the real layout store, opens a session
//     through the real local helper, writes the command into the pane's own
//     input queue, and waits for the helper's emulator to classify the pane
//     free_text. The BRIEFING and the TASK are typed into that pane by the
//     task queue, preamble first.
//   - THE WORKER'S STATES come from the REAL sweep: paneobserve reads the
//     helper's emulator through the real store, classifies with the shipped
//     claude rule, and the production bridge (WorkerObservation, wired exactly
//     as app.go wires it) maps the reading onto the record.
//   - THE EXIT is a real process ending: that worker is spawned with `exec
//     claude`, so the mock IS its session's process, and cueing it to exit
//     takes the pty with it — the record learns it through the supervisor
//     watching the session, which is the only door an exit has.
//   - THE WAKE, the mailbox, the batch, the retries and the notice are the
//     product's, and the line is typed into the coordinator's REAL pane by the
//     shipped typist through the shipped gates: the frame it decides on is the
//     helper's emulator's, and the calibration verdict that permits typing is a
//     real one earned against the corpus.
//   - THE CLOSE is production's: the worker's session is ended and its tab is
//     taken out of the real layout store, then announced on the real WebSocket
//     to a connected renderer.
//
// # What is SUBSTITUTED, and what that keeps this check from proving
//
//   - THE AGENT IS A MOCK (internal/app/testdata/s14mock): the thing being
//     orchestrated is faked, the orchestration is not. It cannot decide to ask
//     a permission question, so the states it shows are cued by file — the same
//     explicit, test-driven role session_surface_happypath_test.go's own helper
//     plays in process. What it CANNOT decide, this check therefore does not
//     claim: that a real agent reaches for `workers.report` on its own.
//   - THE HUMAN IS REACHED THROUGH A RECORDING SEAM. What a notification may
//     reach is internal/notify's, and is asserted there; this check asserts
//     that the escalation was raised, with what, and in what order.
//   - THE COORDINATOR'S OWN READINGS ARE DELIVERED ONE AT A TIME, through the
//     production bridge the sweep feeds, instead of by the sweep's ticker. The
//     settle rule is what this check has to be able to attribute to a first and
//     a second reading (see the probe in step 3), and a reading on a 20 ms
//     ticker cannot be attributed to anything. The pane those readings are
//     ABOUT is real, and the classification they carry is the shipped rule's
//     own verdict on the real emulator's frame — asserted, so a reading this
//     check invented cannot stand. The worker panes' readings really do come
//     from the sweep.
//
// # Why no assertion here depends on a duration
//
// Every wait is on an observable: a row in the mailbox, a frame the emulator
// classifies a certain way, a byte on a pane's own stream, a notification on
// the wire, a notice raised. The two waits that are ABOUT time — the retry and
// the notice — wait for the pause to have produced its effect, never for the
// pause to elapse; the pause itself is an injected number (withCockpit) and no
// assertion quotes it.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/paneobserve"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
	"github.com/shady2k/nocx/internal/workers"
)

// ── the coordinator's own pane ────────────────────────────────────────────

// coordinatorSession is the session the coordinator's own pane belongs to: a
// real helper-hosted pane with the mock in it. Nothing names it in any call —
// who a caller is comes from the process tree the endpoint pins — so the
// mock's own spawn records it as the coordinator and its inbox read is its own
// mailbox, which is the whole of design §4.5's "who you are is decided by the
// session you are running in".
func (s *s14RealStand) coordinatorSession() string { return string(s.coord.ID()) }

// serve starts the transport, so a renderer can dial it: the tab a close takes
// out of the window is announced on that wire (nocx-xn63t.4.6).
func (s *s14RealStand) serve(t *testing.T) {
	t.Helper()
	if err := s.tp.Start(context.Background()); err != nil {
		t.Fatalf("start the transport: %v", err)
	}
}

// writeMCPConfig writes what an agent's call needs: the installed helper's
// path and the endpoint's socket. It is a FILE rather than an environment
// variable because the mock is spawned by a pane's own shell — an environment
// fixed when the daemon started — and because the socket does not exist until
// the endpoint has.
func (s *s14RealStand) writeMCPConfig(t *testing.T) {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"helper": s.binary, "socket": s.endpoint.SocketPath()})
	if err != nil {
		t.Fatalf("encode the mcp config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(s.stateDir, "mcp.json"), raw, 0o600); err != nil { // #nosec G306 -- this test's own state directory
		t.Fatalf("write the mcp config: %v", err)
	}
}

// liveSession is the backend session a participant's pane was opened as — the
// id its own shell has as NOCX_SESSION_ID (AD-7) and the one every call about
// it names.
func (s *s14RealStand) liveSession(t *testing.T, participant string) string {
	t.Helper()
	var stored workers.Participant
	waittest.WaitFor(t, "the spawned worker to settle in the record", func() bool {
		var err error
		stored, err = s.store.Participant(context.Background(), workers.ParticipantID(participant))
		return err == nil && stored.Liveness.SessionID != ""
	})
	return stored.Liveness.SessionID
}

// readWorker is a coordinator's own session.read of one of its workers, made
// over the wire by the agent in the coordinator's pane — the call the product
// gives a coordinator for "what is my worker's pane showing, and where is the
// mail I queued into it".
func (s *s14RealStand) readWorker(t *testing.T, coordinator, sessionID string) s14ReadResult {
	t.Helper()
	raw := s.cueCall(t, coordinator, "session.read", map[string]any{"sessionId": sessionID})
	var out s14ReadResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode session.read result %s: %v", raw, err)
	}
	return out
}

// cueCoordinator moves the coordinator's pane to one of the mock's own screens
// and waits for the emulator to have it: a real pane showing a real frame,
// which is what the typist's decision is taken on.
func (s *s14RealStand) cueCoordinator(t *testing.T, phase string, want agentdriver.State) {
	t.Helper()
	s.cueMock(t, s.coordinatorSession(), phase)
	s.waitForState(t, s.coordinatorSession(), want)
}

// cueMock writes one of the mock's own phase cues and does not wait.
func (s *s14RealStand) cueMock(t *testing.T, sessionID, phase string) {
	t.Helper()
	if err := os.WriteFile(s.cuePath(sessionID), []byte(phase), 0o600); err != nil { // #nosec G306 -- this test's own state directory
		t.Fatalf("cue %q for %s: %v", phase, sessionID, err)
	}
}

// waitForState waits until the pane's own screen classifies as want — read
// from the real emulator through the product's own store, with the shipped
// rule. It is the observable every state step is built on, and it needs no
// call: the frame is here.
func (s *s14RealStand) waitForState(t *testing.T, sessionID string, want agentdriver.State) {
	t.Helper()
	waittest.WaitForDetail(t, fmt.Sprintf("%s to show %s", sessionID, want), func() string {
		f, err := s.views.Frame(sessionID)
		if err != nil {
			return "the pane's own frame is unreadable: " + err.Error()
		}
		return fmt.Sprintf("the pane shows %q", s.rules.Classify(wakeAgent, f))
	}, func() bool {
		f, err := s.views.Frame(sessionID)
		return err == nil && s.rules.Classify(wakeAgent, f) == want
	})
}

// readCoordinator delivers ONE reading of the coordinator's own pane the way
// one sweep does: the frame is read from the real emulator through the
// product's own store, classified by the shipped rule — asserted against what
// this call claims, so a reading this test invented cannot stand — and handed
// to the production bridge, which is the mapping and the announce hop the unit
// tests cannot cover (worker_wake_test.go's own reads says the same).
//
// One call is one reading and not a settled state: with the settle window at
// zero the record's own rule is that a state is a fact on its SECOND reading,
// which is what lets the probe in step 3 attribute the wake to the reading
// that held.
func (s *s14RealStand) readCoordinator(t *testing.T, want agentdriver.State) {
	t.Helper()
	f, err := s.views.Frame(s.coordinatorSession())
	if err != nil {
		t.Fatalf("read the coordinator's own frame: %v", err)
	}
	if got := s.rules.Classify(wakeAgent, f); got != want {
		t.Fatalf("the coordinator's own pane is %q and this reading says %q", got, want)
	}
	s.observe.ObserveSession(paneobserve.Observation{PaneID: s.coordinatorSession(), Agent: wakeAgent, State: want})
}

// coordinatorTyped is every byte the agent in the coordinator's pane has read
// from its own stdin — the real byte stream production wrote to a real PTY,
// which is where a wake line lands.
func (s *s14RealStand) coordinatorTyped(t *testing.T) string {
	t.Helper()
	return s.paneTyped(t, s.coordinatorSession())
}

// s14WakeTexts is every wake sentence a pane's stream carries, in the order
// the pane received them — each one cut at the sentence's own end, so an
// assertion is on the product's whole line and not on a fragment a different
// sentence would also satisfy.
func s14WakeTexts(typed string) []string {
	const (
		open  = "nocx: you have "
		close = "Call workers.inbox."
	)
	var out []string
	for {
		at := strings.Index(typed, open)
		if at < 0 {
			return out
		}
		typed = typed[at:]
		end := strings.Index(typed, close)
		if end < 0 {
			out = append(out, typed)
			return out
		}
		end += len(close)
		out = append(out, typed[:end])
		typed = typed[end:]
	}
}

// s14WakeLines is how many wake lines a pane's stream carries.
func s14WakeLines(typed string) int { return len(s14WakeTexts(typed)) }

// s14WakeLine is the whole sentence the wake types for n waiting messages,
// spelled out here so an assertion on it is an assertion on the product's own
// words rather than on a fragment a different sentence would also satisfy.
func s14WakeLine(n int) string {
	return fmt.Sprintf("nocx: you have %d new messages from your workers. Call workers.inbox.", n)
}

// ── the mailbox, read the way the wake reads it ───────────────────────────

// mailRows is the coordinator's mailbox as the record holds it, read WITHOUT
// advancing the caller's cursor: the wake counts this same sequence
// (workers.Mailboxes.Since, over the same store), so a test that read it
// through a fetch would be clearing the very batch it is measuring.
func (s *s14RealStand) mailRows(t *testing.T) []workers.Message {
	t.Helper()
	rows, err := s.store.Since(context.Background(), workers.ReaderID(s.coordinatorSession()), 0, workers.MaxFetch)
	if err != nil {
		t.Fatalf("read the coordinator's mailbox: %v", err)
	}
	return rows
}

// waitForObservations waits until the mailbox holds at least want observations
// of one worker in one state, and returns them. It is what every "the worker
// settled / blocked / exited" step waits on, and the COUNT is the point: a
// worker that comes up idle and then settles idle again after its turn
// produces two rows of the same worker in the same state, so a wait for "an
// idle observation" would be satisfied by the arrival and would let a step
// that needs the second one run early — measured, under a loaded machine, in
// this check's own first package-wide run.
func (s *s14RealStand) waitForObservations(t *testing.T, worker string, state workers.ObservedState, want int) []workers.Message {
	t.Helper()
	var found []workers.Message
	waittest.WaitForDetail(t, fmt.Sprintf("%q to be seen %s %d time(s)", worker, state, want), func() string {
		rows := s.mailRows(t)
		seen := make([]string, 0, len(rows))
		for _, row := range rows {
			seen = append(seen, s14ObservedWorker(row)+":"+s14ObservedState(row))
		}
		return fmt.Sprintf("the mailbox holds %d rows: %v", len(rows), seen)
	}, func() bool {
		found = nil
		for _, row := range s.mailRows(t) {
			if row.Observed != nil && row.Observed.Worker == workers.ParticipantID(worker) && row.Observed.State == state {
				found = append(found, row)
			}
		}
		return len(found) >= want
	})
	return found
}

// waitForReport waits until the mailbox holds a report of one kind from one
// worker — a kind and not just a sender, because a worker's checkpoints and
// its completion report are both mail from the same hand.
func (s *s14RealStand) waitForReport(t *testing.T, worker string, kind workers.MessageKind) workers.Message {
	t.Helper()
	var found workers.Message
	waittest.WaitFor(t, fmt.Sprintf("the coordinator's mailbox to hold %q's %s report", worker, kind), func() bool {
		for _, row := range s.mailRows(t) {
			if row.Observed == nil && row.Sender == workers.ReaderID(worker) && row.Kind == kind {
				found = row
				return true
			}
		}
		return false
	})
	return found
}

// unreadAfter is how many rows the mailbox holds past a position — the number
// the wake counts when it decides what to type, computed here from the record
// the wake itself reads (workers.MemoryStore).
func (s *s14RealStand) unreadAfter(t *testing.T, position int64) []workers.Message {
	t.Helper()
	var out []workers.Message
	for _, row := range s.mailRows(t) {
		if row.Seq > position {
			out = append(out, row)
		}
	}
	return out
}

// ── the mock's own side channels ──────────────────────────────────────────

// cueCall has the agent in a pane make one real tool call, and returns the
// endpoint's own result for it. The call is cued by file and answered into a
// file, because the mock is the caller: it is a program in a pane, and the
// endpoint admits it as that pane's session by the process tree — which is the
// property this whole journey rests on.
//
// A CALL THAT FAILED IS FATAL AND SAYS WHAT CAME BACK, with the ancestry the
// bridge connected from: the failures this differs between are the ones a
// journey must never mistake for a delivered call — a refusal at the MCP
// layer, a refusal from the endpoint, or an answer with no result at all.
func (s *s14RealStand) cueCall(t *testing.T, sessionID, tool string, args any) json.RawMessage {
	t.Helper()
	return s14ToolResult(t, tool, s.cueAnswer(t, sessionID, tool, args), s14BridgeTree(s.stateDir))
}

// cueAnswer is cueCall without the decoding: it cues ONE call and answers the
// MCP line the bridge wrote back, so a caller reading a shape other than a
// tools/call result — the catalogue list — can decode it itself.
func (s *s14RealStand) cueAnswer(t *testing.T, sessionID, tool string, args any) string {
	t.Helper()
	before := len(s14CallLog(t, s.stateDir, sessionID))
	raw, err := json.Marshal(map[string]any{
		// seq is what makes two identical calls two cues: the mock ignores a
		// cue equal to the one before it, and a second `workers.inbox {}` is
		// the same bytes as the first. It never reaches the endpoint.
		"seq": before + 1, "tool": tool, "args": args,
	})
	if err != nil {
		t.Fatalf("encode the %s cue: %v", tool, err)
	}
	if err := os.WriteFile(filepath.Join(s.stateDir, "call", sessionID), raw, 0o600); err != nil { // #nosec G306 -- this test's own state directory
		t.Fatalf("cue %s for %s: %v", tool, sessionID, err)
	}
	var answer string
	waittest.WaitForDetail(t, fmt.Sprintf("%s to answer the %s call from %s", sessionID, tool, sessionID), func() string {
		lines := s14CallLog(t, s.stateDir, sessionID)
		return fmt.Sprintf("the call log holds %d answers, wanted more than %d: %q", len(lines), before, strings.Join(lines, " | "))
	}, func() bool {
		lines := s14CallLog(t, s.stateDir, sessionID)
		if len(lines) <= before {
			return false
		}
		answer = lines[len(lines)-1]
		return true
	})
	return answer
}

// s14CallLog is every answer a pane's agent has recorded, oldest first.
func s14CallLog(t *testing.T, stateDir, sessionID string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(stateDir, "calllog", sessionID)) // #nosec G304 -- this test's own state directory
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read the call log for %s: %v", sessionID, err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// cueCatalogue is what an agent in a pane discovers its OWN tools to be: one
// MCP tools/list, answered by the bridge from the endpoint's catalogue. It is
// the call an agent makes to find out what it may do, which is why the
// vocabulary a journey asserts on is read here rather than from a table.
func (s *s14RealStand) cueCatalogue(t *testing.T, sessionID string) map[string]struct{} {
	t.Helper()
	raw := s14Answer(t, "tools/list", s.cueAnswer(t, sessionID, "tools/list", nil), s14BridgeTree(s.stateDir))
	var envelope struct {
		Result *struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("the tools/list answer is not JSON: %s: %v", raw, err)
	}
	if envelope.Result == nil {
		t.Fatalf("the tools/list answer carries no result: %s", raw)
	}
	names := make(map[string]struct{}, len(envelope.Result.Tools))
	for _, tool := range envelope.Result.Tools {
		names[tool.Name] = struct{}{}
	}
	return names
}

// s14Names is a catalogue's names in a stable order, for a failure message.
func s14Names(names map[string]struct{}) []string {
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// s14ToolResult decodes one MCP answer to a tools/call and returns the
// endpoint's own result object.
func s14ToolResult(t *testing.T, tool, line, tree string) json.RawMessage {
	t.Helper()
	line = s14Answer(t, tool, line, tree)
	var envelope struct {
		Result *struct {
			Structured json.RawMessage `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &envelope); err != nil {
		t.Fatalf("the %s answer is not JSON: %s: %v", tool, line, err)
	}
	if envelope.Result == nil || len(envelope.Result.Structured) == 0 {
		t.Fatalf("the %s answer carries no result object: %s (bridge tree: %s)", tool, line, tree)
	}
	return envelope.Result.Structured
}

// s14Answer checks one MCP answer for the two ways a call can fail and returns
// the line for the caller to decode. A failure at the MCP layer is a JSON-RPC
// error; a refusal from the endpoint is a RESULT with isError set, because the
// bridge keeps the endpoint's own words for a business refusal (mcpstdio's
// toolError) — and a journey must never read either as a delivered call.
func s14Answer(t *testing.T, tool, line, tree string) string {
	t.Helper()
	var envelope struct {
		Error *struct {
			Message string `json:"message"`
			Data    *struct {
				Reason string `json:"reason"`
			} `json:"data"`
		} `json:"error"`
		Result *struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &envelope); err != nil {
		t.Fatalf("the %s answer is not JSON: %s: %v", tool, line, err)
	}
	if envelope.Error != nil {
		reason := envelope.Error.Message
		if envelope.Error.Data != nil && envelope.Error.Data.Reason != "" {
			reason = envelope.Error.Data.Reason
		}
		t.Fatalf("the %s call was refused at the MCP layer: %s (bridge tree: %s)", tool, reason, tree)
	}
	if envelope.Result == nil {
		t.Fatalf("the %s answer carries no result: %s (bridge tree: %s)", tool, line, tree)
	}
	if envelope.Result.IsError {
		explain := ""
		if len(envelope.Result.Content) > 0 {
			explain = envelope.Result.Content[0].Text
		}
		t.Fatalf("the %s call was refused by the endpoint: %s (bridge tree: %s)", tool, explain, tree)
	}
	return line
}

// s14BridgeTree is the ancestry the MCP bridge actually connected from, as the
// mock recorded it — the chain the endpoint walks to decide which pane a local
// caller belongs to, quoted in a refusal so the reader is not left guessing
// which tree the call came from.
func s14BridgeTree(stateDir string) string {
	raw, err := os.ReadFile(filepath.Join(stateDir, "tree", "bridge.txt")) // #nosec G304 -- this test's own state directory
	if err != nil {
		return "(the mock recorded no tree)"
	}
	return strings.TrimSpace(string(raw))
}

// paneTyped is every byte the program in a pane has read from its own stdin —
// the real byte stream production wrote to a real PTY, appended verbatim by
// the mock as it reads it (s14RealStand.writtenBytes' own source).
func (s *s14RealStand) paneTyped(t *testing.T, sessionID string) string {
	t.Helper()
	raw, err := os.ReadFile(s.logPath(sessionID)) // #nosec G304 -- this test's own state directory
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("read the mock's own stdin log for %s: %v", sessionID, err)
	}
	return string(raw)
}

// s14PendingPhase is what the task queue reports for one queued message, or
// the empty string when the queue holds no record of it — the difference
// between an id nobody queued and a message that is still being delivered.
func s14PendingPhase(read s14ReadResult, id string) string {
	for _, pending := range read.Pending {
		if pending.ID == id {
			return pending.Phase
		}
	}
	return ""
}

// s14ObservedState and s14ObservedWorker name what a row observes, for a
// failure message: a row that is not an observation at all says so rather than
// printing a nil.
func s14ObservedState(row workers.Message) string {
	if row.Observed == nil {
		return "(not an observation: " + string(row.Kind) + ")"
	}
	return string(row.Observed.State)
}

func s14ObservedWorker(row workers.Message) string {
	if row.Observed == nil {
		return string(row.Sender)
	}
	return string(row.Observed.Worker)
}

// tabOfSession answers which tab a session's pane was minted in, by walking
// the chain rather than by remembering an id: the session names its pane (AD-7)
// and the pane names its tab. It is worker_close_tab_test.go's own lookup, over
// this stand's real layout store.
func (s *s14RealStand) tabOfSession(t *testing.T, sid string) string {
	t.Helper()
	sess, err := s.reg.Get(session.ID(sid))
	if err != nil {
		t.Fatalf("the worker's session is not in the registry: %v", err)
	}
	snap, err := s.db.Layout().Snapshot(context.Background())
	if err != nil {
		t.Fatalf("layout snapshot: %v", err)
	}
	for _, pane := range snap.Panes {
		if pane.ID == sess.PaneID() {
			return pane.TabID
		}
	}
	t.Fatalf("the pane the worker's session opened in (%s) is in no tab", sess.PaneID())
	return ""
}

// ── the check ─────────────────────────────────────────────────────────────

// TestACoordinatorHearsItsWorkersThroughTheRealHelper is nocx-luqz9's own
// acceptance check, in the order the design states it: a worker is spawned and
// briefed, reports and settles idle, the idle coordinator is typed at exactly
// once without asking anything, reads its own mailbox, is told about a worker
// stuck on a menu and one whose process is gone, is retyped and finally
// reported to a person when it still does not read — and its worker's tab
// leaves the window when it closes that worker.
func TestACoordinatorHearsItsWorkersThroughTheRealHelper(t *testing.T) {
	mockDir := s14RealBuildMock(t)
	stateDir := t.TempDir()
	capturesDir, err := filepath.Abs(filepath.Join("..", "agentdriver", "testdata", "captures"))
	if err != nil {
		t.Fatalf("resolve captures dir: %v", err)
	}
	// Set BEFORE the stand: the real daemon it starts inherits this process's
	// environment at THAT moment, and every shell it forks afterwards — the
	// coordinator's own pane and every worker's alike — inherits the daemon's.
	t.Setenv("PATH", mockDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("S14_CAPTURES_DIR", capturesDir)
	t.Setenv("S14_STATE_DIR", stateDir)

	stand := newS14RealStand(t, mockDir, stateDir, withCockpit())
	stand.writeMCPConfig(t)
	stand.serve(t)
	renderer := readWorkerNotifications(t, workerTestDial(t, stand.tp))
	coordinator := stand.coordinatorSession()

	// ── 1. A coordinator spawns a worker, and the worker's pane is given the
	// briefing and then the task, both submitted. ──────────────────────────
	//
	// The coordinator's own pane is idle from the start and NOTHING has been
	// delivered to it yet: no reading of its pane has been admitted, which is
	// the state the settle-window probe in step 3 is written against.
	stand.cueCoordinator(t, "idle", agentdriver.StateFreeText)

	const task = "count the frames in the capture and report"
	spawned := stand.cueCall(t, coordinator, "workers.spawn", map[string]any{"command": "claude", "task": task})
	var spawnResult struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(spawned, &spawnResult); err != nil {
		t.Fatalf("decode workers.spawn result %s: %v", spawned, err)
	}
	if spawnResult.ID == "" || spawnResult.State != string(workers.StateLive) {
		t.Fatalf("workers.spawn = %s, want a live participant id", spawned)
	}
	w1 := spawnResult.ID
	w1Session := stand.liveSession(t, w1)
	briefing := workers.Preamble(coordinator)

	// Both halves submitted — production's own verdict on its own delivery,
	// read the way a coordinator reads it (a session.read of its descendant).
	w1Read := s14ReadResult{}
	waittest.WaitForDetail(t, "the worker's pane to be given the briefing and then the task", func() string {
		read := stand.readWorker(t, coordinator, w1Session)
		pending := make([]string, 0, len(read.Pending))
		for _, p := range read.Pending {
			pending = append(pending, p.ID+":"+p.Phase)
		}
		return fmt.Sprintf("pane %s classifies %q, pending %v; the mock's own stdin log holds %q",
			w1Session, read.Classification, pending, stand.paneTyped(t, w1Session))
	}, func() bool {
		w1Read = stand.readWorker(t, coordinator, w1Session)
		return s14PendingPhase(w1Read, briefingPreambleID) == "submitted" &&
			s14PendingPhase(w1Read, briefingTaskID) == "submitted"
	})

	// And the ORDER, from the pane's own byte stream: the briefing was typed
	// before the task, and the program in the pane saw both.
	w1Typed := stand.paneTyped(t, w1Session)
	briefAt, taskAt := strings.Index(w1Typed, briefing), strings.Index(w1Typed, task)
	if briefAt < 0 {
		t.Fatalf("the worker's pane was never given the briefing; it received:\n%q", w1Typed)
	}
	if taskAt < briefAt {
		t.Fatalf("the worker's pane was given the task BEFORE the briefing (briefing at %d, task at %d):\n%q",
			briefAt, taskAt, w1Typed)
	}

	// ── 2. The worker starts its turn, reports `done` in its own words, and
	// settles idle. ───────────────────────────────────────────────────────
	//
	// The worker's arrival is already a fact: its pane came up idle and a state
	// that HELD is news (design §4.3) — the first row the coordinator's mailbox
	// holds, and the row the settle probe below counts on. It is not the
	// report: nothing has been said yet.
	stand.waitForObservations(t, w1, workers.ObservedIdle, 1)

	// THE COORDINATOR'S OWN FIRST READING, and the settle-window probe begins
	// with it: nothing has been delivered to the coordinator yet, so this is
	// the FIRST reading of an idle state, and with the settle window at zero
	// the record's own rule admits nothing here.
	stand.readCoordinator(t, agentdriver.StateFreeText)

	stand.cueMock(t, w1Session, "working")
	stand.waitForState(t, w1Session, agentdriver.StateWorking)

	// A CHECKPOINT FIRST, and it is what "progress never wakes" MEANS: the row
	// lands in the mailbox (a coordinator reads it at its next call) and the
	// line this check is about must not count it. The two numbers below are
	// the sentence's own, so a kinds table that woke on progress would name
	// three here and fail.
	stand.cueCall(t, w1Session, "workers.report", map[string]any{"kind": "progress", "text": "two frames done", "estimate": 60})
	stand.waitForReport(t, w1, workers.KindProgress)

	const words = "3 frames counted; the third is the light one"
	stand.cueCall(t, w1Session, "workers.report", map[string]any{"kind": "done", "text": words})

	// ── 3. The idle coordinator is typed at, ONCE, and the line names what was
	// waiting WHEN IT WAS TYPED. ────────────────────────────────────────────
	//
	// The second reading is taken with the report in the mailbox and the pane
	// still idle, and the line it produces must name TWO — the observation and
	// the report. A settle check that admitted the FIRST reading would have
	// typed "1" before the report existed, which is what the number below
	// catches: the count is taken at the moment of typing (wake.go's typeNow
	// re-reads it), so it is the settle rule made visible.
	stand.waitForReport(t, w1, workers.KindDone)
	stand.readCoordinator(t, agentdriver.StateFreeText)

	waittest.WaitFor(t, "the coordinator's pane to be given a wake line", func() bool {
		return s14WakeLines(stand.coordinatorTyped(t)) >= 1
	})
	typed := stand.coordinatorTyped(t)
	if lines := s14WakeTexts(typed); len(lines) != 1 || lines[0] != s14WakeLine(2) {
		t.Fatalf("the idle coordinator was given %q, want exactly one line naming the two messages waiting when it was typed:\n%q",
			lines, typed)
	}
	// And the line carries nothing but the pointer: no worker's words ever
	// travel this way (ADR-0070 — a screen read typed into an input box would
	// be prompt injection with our own hands).
	if strings.Contains(typed, words) || strings.Contains(typed, w1) {
		t.Fatalf("the wake line carried worker content:\n%q", typed)
	}

	// The turn ends and the worker's arms go down again: idle news TWICE,
	// because it worked in between — which is what puts the observation that
	// FOLLOWS the report in the mailbox, in the order the brief states.
	stand.cueMock(t, w1Session, "idle")
	stand.waitForState(t, w1Session, agentdriver.StateFreeText)
	stand.waitForObservations(t, w1, workers.ObservedIdle, 2)

	// NOTHING BUT A NOTE IS NEEDED FOR THIS: the call that ends the search for
	// workers.wait is that the vocabulary is gone, so the coordinator's own
	// catalogue is asked over the wire, and it must offer its inbox and no wait
	// of any kind.
	catalogue := stand.cueCatalogue(t, coordinator)
	if _, ok := catalogue["workers.inbox"]; !ok {
		t.Fatalf("the coordinator's own catalogue does not offer workers.inbox: %v", s14Names(catalogue))
	}
	if _, ok := catalogue["workers.wait"]; ok {
		t.Fatalf("the coordinator's own catalogue still offers workers.wait, which ADR-0070 removed: %v", s14Names(catalogue))
	}

	// ── 4. The coordinator reads its own mailbox, and the read clears the
	// batch. ───────────────────────────────────────────────────────────────
	inbox := decodeInbox(t, stand.cueCall(t, coordinator, "workers.inbox", map[string]any{}))
	if inbox.Messages == nil || inbox.Observations == nil {
		t.Fatalf("the inbox answer is missing one of its two lists: %+v", inbox)
	}
	if len(*inbox.Messages) != 2 {
		t.Fatalf("the coordinator was handed %d messages, want the checkpoint and the report: %+v", len(*inbox.Messages), *inbox.Messages)
	}
	checkpoint, report := (*inbox.Messages)[0], (*inbox.Messages)[1]
	if checkpoint.From != w1 || checkpoint.Message != "two frames done" {
		t.Fatalf("the checkpoint read back as %+v, want %q's own words from %q", checkpoint, "two frames done", w1)
	}
	if report.From != w1 || report.Message != words {
		t.Fatalf("the report read back as %+v, want %q's own words from %q", report, words, w1)
	}
	// Two observations, and both are the same worker idle: it came up idle and
	// it settled idle again after its turn. The report sits between them, which
	// is the order the record committed.
	if len(*inbox.Observations) != 2 {
		t.Fatalf("the coordinator was handed %d observations, want the two settled idles: %+v", len(*inbox.Observations), *inbox.Observations)
	}
	for i, observed := range *inbox.Observations {
		if observed.Worker != w1 || observed.State != string(workers.ObservedIdle) || observed.At == "" {
			t.Fatalf("observation %d read back as %+v, want %q settled idle with a time", i, observed, w1)
		}
	}

	// The ORDER is the record's own: the worker came up idle, then reported,
	// then settled idle again after its turn — and the cursor the answer
	// carries is the position of the last row it handed over.
	rows := stand.mailRows(t)
	if len(rows) != 4 {
		t.Fatalf("the mailbox holds %d rows, want the arrival, the checkpoint, the report and the settle: %+v", len(rows), rows)
	}
	if rows[0].Observed == nil || rows[0].Observed.State != workers.ObservedIdle {
		t.Fatalf("the mailbox's first row observes %q, want the worker seen idle as it came up", s14ObservedState(rows[0]))
	}
	if rows[1].Sender != workers.ReaderID(w1) || rows[1].Kind != workers.KindProgress {
		t.Fatalf("the mailbox's second row is %+v, want the worker's checkpoint", rows[1])
	}
	if rows[2].Sender != workers.ReaderID(w1) || rows[2].Kind != workers.KindDone {
		t.Fatalf("the mailbox's third row is %+v, want the worker's report", rows[2])
	}
	if rows[3].Observed == nil || rows[3].Observed.State != workers.ObservedIdle || rows[3].Observed.Worker != workers.ParticipantID(w1) {
		t.Fatalf("the mailbox's fourth row observes %q about %q, want %q settled idle",
			s14ObservedState(rows[3]), s14ObservedWorker(rows[3]), workers.ObservedIdle)
	}
	for i := 1; i < len(rows); i++ {
		if rows[i].Seq <= rows[i-1].Seq {
			t.Fatalf("the rows sit at %d, %d, %d and %d — that is not the commit order",
				rows[0].Seq, rows[1].Seq, rows[2].Seq, rows[3].Seq)
		}
	}
	if inbox.Cursor != rows[3].Seq {
		t.Fatalf("the answer's cursor is %d and the last row of the page is at %d", inbox.Cursor, rows[3].Seq)
	}

	// ── 5. A second worker blocked on a menu wakes it, and a worker whose
	// process ends wakes it too. ────────────────────────────────────────────
	//
	// The coordinator is WORKING while both facts land, which is the design's
	// own rule rather than a convenience: while it is not idle nothing is typed
	// and no timer runs, so both messages are still there when it comes back —
	// and the line it is then given names both, which is what proves the read
	// above cleared the batch it closed.
	stand.cueCoordinator(t, "working", agentdriver.StateWorking)
	stand.readCoordinator(t, agentdriver.StateWorking)
	stand.readCoordinator(t, agentdriver.StateWorking)

	blocked := stand.cueCall(t, coordinator, "workers.spawn", map[string]any{"command": "claude", "task": "ask before you touch anything"})
	var blockedSpawn struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(blocked, &blockedSpawn); err != nil {
		t.Fatalf("decode the blocked worker's spawn: %v", err)
	}
	blockedSession := stand.liveSession(t, blockedSpawn.ID)
	stand.cueMock(t, blockedSession, "menu")
	stand.waitForState(t, blockedSession, agentdriver.StatePermissionChoice)
	stand.waitForObservations(t, blockedSpawn.ID, workers.ObservedBlocked, 1)

	// The worker whose PROCESS ends. The command runs the agent and then ENDS
	// THE PANE, which is what a worker's process ending actually is here: the
	// supervisor watches the SESSION (workers.go's Attach), so a pane whose
	// shell is gone is the fact, and an agent that merely returns to its
	// prompt leaves one behind. `exec` would be the other way to make the mock
	// the session's process, and it is not usable: the shell integration wraps
	// the agent's name in an enrolment function, and a pane that never
	// enrolled is a pane the readiness axis never observed at all — measured,
	// this journey's own first attempt, which failed with "nocx never observed
	// this pane".
	doomed := stand.cueCall(t, coordinator, "workers.spawn", map[string]any{"command": "claude; exit", "task": "you will not get far"})
	var doomedSpawn struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(doomed, &doomedSpawn); err != nil {
		t.Fatalf("decode the doomed worker's spawn: %v", err)
	}
	doomedSession := stand.liveSession(t, doomedSpawn.ID)
	stand.cueMock(t, doomedSession, "exit")
	stand.waitForObservations(t, doomedSpawn.ID, workers.ObservedExited, 1)
	// What nocx SAW, as the coordinator is about to read it back: the pane's
	// own classification for the blocked one, and the process fact for the
	// other — both addressed to this coordinator by the record itself.
	blockedRow := stand.waitForObservations(t, blockedSpawn.ID, workers.ObservedBlocked, 1)[0]
	if blockedRow.Sender != "nocx" {
		t.Fatalf("the blocked observation is %+v, want the record's own sender", blockedRow)
	}

	// THE BATCH the coordinator has not read is whatever sits past the cursor
	// its own last read left — measured from the record, not assumed, because
	// the number the wake types is that count and nothing else.
	batch := stand.unreadAfter(t, inbox.Cursor)
	if len(batch) < 2 {
		t.Fatalf("the unread batch holds %d rows, want at least the blocked and the exited: %+v", len(batch), batch)
	}

	stand.cueCoordinator(t, "idle", agentdriver.StateFreeText)
	stand.readCoordinator(t, agentdriver.StateFreeText)
	stand.readCoordinator(t, agentdriver.StateFreeText)

	waittest.WaitFor(t, "the coordinator to be given a line for the batch it has not read", func() bool {
		return s14WakeLines(stand.coordinatorTyped(t)) >= 2
	})
	typed = stand.coordinatorTyped(t)
	lines := s14WakeTexts(typed)
	if len(lines) != 2 || lines[0] != s14WakeLine(2) || lines[1] != s14WakeLine(len(batch)) {
		t.Fatalf("the coordinator was given %q, want the first batch's line and then one naming the %d messages that were waiting while it worked:\n%q",
			lines, len(batch), typed)
	}

	// ── 6. A coordinator left idle with the batch unread is retyped at the
	// pause, and then the human is called. ─────────────────────────────────
	//
	// THE PAUSE IS THE INJECTED CLOCK and nothing here waits for it: the wait
	// is for the line it produces, and then for the notice the pause after that
	// produces — two effects, in order, each an observable.
	waittest.WaitFor(t, "the pause to have produced a second line for the unread batch", func() bool {
		return s14WakeLines(stand.coordinatorTyped(t)) >= 3
	})
	typed = stand.coordinatorTyped(t)
	lines = s14WakeTexts(typed)
	if len(lines) != 3 {
		t.Fatalf("the unread batch got %d lines with an attempt limit of two, want the retry and no more: %q", len(lines), lines)
	}
	if lines[2] != lines[1] {
		t.Fatalf("the retype is not the line it repeats: %q then %q", lines[1], lines[2])
	}

	waittest.WaitFor(t, "the human to be told the coordinator is not reading", func() bool {
		return len(stand.raiser.raised()) >= 1
	})
	notices := stand.raiser.raised()
	if len(notices) != 1 {
		t.Fatalf("the human was told %d times about one unread batch, want once: %+v", len(notices), notices)
	}
	notice := notices[0]
	if notice.Kind != notify.KindCoordinatorStalled {
		t.Fatalf("the notice is %q, want %q", notice.Kind, notify.KindCoordinatorStalled)
	}
	if notice.SessionID != coordinator {
		t.Fatalf("the notice names session %q, want the coordinator's own %q", notice.SessionID, coordinator)
	}
	if !strings.Contains(notice.Body, fmt.Sprintf("%d unread", len(batch))) {
		t.Fatalf("the notice does not name the batch it is about (%d unread): %q", len(batch), notice.Body)
	}

	// AND NOTHING MORE IS TYPED. The notice is the last event this batch has:
	// two further readings of an idle, unread coordinator are handed to the
	// record, and the line count is unchanged afterwards.
	stand.readCoordinator(t, agentdriver.StateFreeText)
	stand.readCoordinator(t, agentdriver.StateFreeText)
	if lines := s14WakeLines(stand.coordinatorTyped(t)); lines != 3 {
		t.Fatalf("the coordinator was typed at %d times after the human was called, want the three already counted:\n%q",
			lines, stand.coordinatorTyped(t))
	}

	// ── 7. workers.close ends the worker and takes its tab out of the
	// window. ─────────────────────────────────────────────────────────────
	tabID := stand.tabOfSession(t, w1Session)
	if ids := idsOf(t, stripOf(t, stand.db.Layout(), content.DefaultWorkspaceID)); !containsID(ids, tabID) {
		t.Fatalf("the worker's tab is not in the strip before the close: %v (tab %s)", ids, tabID)
	}
	stand.cueCall(t, coordinator, "workers.close", map[string]any{"worker": w1})
	if _, err := stand.reg.Get(session.ID(w1Session)); err == nil {
		t.Fatalf("workers.close left the worker's session %s in the registry", w1Session)
	}
	tabs := stripOf(t, stand.db.Layout(), content.DefaultWorkspaceID)
	if ids := idsOf(t, tabs); containsID(ids, tabID) {
		t.Fatalf("the closed worker's tab is still in the window: %v", ids)
	}
	// THE REST OF THE STRIP KEEPS ITS ORDER. The window reads a workspace's
	// tabs in position order, and the seats of the tabs that stayed are the
	// ones they already had — the store renumbers around an INSERT
	// (content/layout_sqlite.go's createTab: "the strip is renumbered around
	// the new tab") and does not renumber on a close, so asserting density
	// here would be asserting an invariant the store never claimed. This
	// check's worker is the OLDEST of three, so its seat is not the last one:
	// worker_close_tab_test.go's own density assertion is made where it
	// holds, on a strip whose closed tab is last.
	positions := make([]int, 0, len(tabs))
	for _, tab := range tabs {
		positions = append(positions, tab.Position)
	}
	for i := 1; i < len(positions); i++ {
		if positions[i] <= positions[i-1] {
			t.Fatalf("the strip's remaining tabs are out of order after the close: %v", positions)
		}
	}
	var closed struct {
		TabID string `json:"tabId"`
	}
	if err := json.Unmarshal(renderer.await(t, "workers.tabClosed"), &closed); err != nil {
		t.Fatalf("decode workers.tabClosed: %v", err)
	}
	if closed.TabID != tabID {
		t.Fatalf("workers.tabClosed named tab %q, want %q", closed.TabID, tabID)
	}
}
