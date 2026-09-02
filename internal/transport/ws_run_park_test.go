package transport

// THE REPORTED CASE, end to end over a real session and a real process
// (nocx-6dzxq). The owner ran `df` through the assistant; it printed nothing
// for over two minutes — a stuck mount does exactly that — and the lease
// killed it. The command was healthy and the silence was normal.
//
// These tests stand in for the renderer the way ws_run_lease_test.go does:
// receive agent.runRequest, submit the command into the real session, and
// resolve when the block would freeze. The MODEL is scripted too, because
// the whole mechanism is a question put to it: the fake provider reads the
// still-running message out of the tool result and answers with a real
// session.wait call whose run id it could only have learned there.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/waittest"
)

// runIDInStillRunning pulls the parked run's handle out of the sentence the
// model was given. The model has no other source for it, which is the point:
// a continuation can only name a run nocx told it about.
var runIDInStillRunning = regexp.MustCompile(`run id ([0-9a-f]+)`)

// scriptQuietAnswer makes the fake provider behave like a model that reads
// the question and answers it: the first round runs the command, and EVERY
// round that carries a still-running question is answered with session.wait
// — for as long as it is asked, which is what a model choosing to keep
// waiting actually does. Any other round writes the answer and ends the
// turn.
//
// It is driven by what the conversation SAYS rather than by a round number,
// because a quiet command is asked about again on every renewal and nobody
// can predict how many rounds that is.
func scriptQuietAnswer(h *runLeaseHarness, t *testing.T, decision string, seen chan<- string) {
	var mu sync.Mutex
	answered := 0
	h.fake.script = func(n int64, body string) (string, string, bool) {
		if n == 1 {
			return "session.run", h.fake.args, true
		}
		mu.Lock()
		defer mu.Unlock()
		// EACH QUESTION IS ANSWERED ONCE. The body is the whole conversation
		// so far, so a question asked three rounds ago is still in it; a
		// script that simply looked for the words would answer the same
		// question forever and the turn would never end. Counting the
		// questions against the answers is what a model does — and it is
		// also what nocx tells the model to do, by putting the renewal count
		// in the sentence.
		if strings.Count(body, "STILL RUNNING") <= answered {
			return "", "", false
		}
		m := runIDInStillRunning.FindStringSubmatch(body)
		if m == nil {
			t.Errorf("a still-running question names no run id:\n%s", body)
			return "", "", false
		}
		// The FIRST question, kept for the assertion; later renewals ask the
		// same thing and would only overwrite it.
		select {
		case seen <- body:
		default:
		}
		answered++
		return "session.wait", `{"runId":` + strconv.Quote(m[1]) + `,"decision":"` + decision + `"}`, true
	}
}

// A command that goes quiet for longer than the quiet bound is NOT killed:
// the model is asked, answers "keep waiting", and the SAME execution runs to
// completion and reports its exit status. One test, all three steps.
func TestRunLease_AQuietCommandIsAskedAboutAndThenCompletes(t *testing.T) {
	h := newRunLeaseHarness(t, RunLeaseConfig{
		WallClock: 60 * time.Second, // cannot explain anything here
		// Long enough that the command outlives roughly one renewal and
		// short enough that the whole test is brisk. The exact number is not
		// load-bearing: the scripted model answers every question it is
		// asked, so a slow machine that renews three times proves the same
		// thing as a fast one that renews once.
		Inactivity:  1200 * time.Millisecond,
		SignalGrace: 200 * time.Millisecond,
	})
	h.createEndpointAt()
	sid := h.openSession(t)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	// Silent for far longer than the quiet bound, then it speaks and exits
	// 0 — the shape of the owner's `df` against a stuck mount that was in
	// fact fine.
	//
	// THE SENTINEL IS ASSEMBLED BY THE SHELL, and that is load-bearing.
	// tapDataFor scans the session's RAW output, which begins with the pty's
	// echo of the command line itself — so a sentinel spelled literally in
	// the command matches the echo, milliseconds after submission, and this
	// test resolved the request before the quiet bound could ever fire (the
	// run then completed normally and nothing was ever asked). Writing it as
	// ${M}shed keeps the literal out of the echoed line, so the only thing
	// that can match is what the command actually printed, three seconds in.
	cmd := "sh -c 'M=fini; echo $$ > " + pidFile + "; sleep 2; echo ${M}shed'"
	question := make(chan string, 1)
	// The script is installed BEFORE the ask: the first request reaches the
	// provider as soon as the stream starts.
	scriptQuietAnswer(h, t, "continue", question)
	res := h.askRunsTool(sid, cmd)
	tap := newSocketTap(h.conn)

	raw := tapNotify(t, tap, "agent.runRequest", 10*time.Second)
	requestID, _, _ := decodeRunRequest(t, raw)
	h.submitLeaseCommand(t, tap, sid, cmd, requestID)
	pid := readPidFile(t, pidFile)
	waitChildAlive(t, pid)

	// The command finally speaks — and it is still the same process, never
	// restarted, because nothing ever wrote the command a second time. By
	// the time this returns the quiet bound has long since fired: the
	// sentinel cannot appear before the command prints it (see the command
	// above), so this wait cannot outrun the question.
	tapDataFor(t, tap, sid, "finished", 30*time.Second)
	reply := tapCall(t, h.conn, tap, 41, "agent.runResolved",
		runResolvedWire(requestID, "entry-park-ok", 0, "success", 1, 0, 1, "finished"))
	var rerr struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(reply, &rerr); err != nil {
		t.Fatalf("resolution response unmarshal: %v", err)
	}
	if rerr.Error != nil {
		t.Fatalf("runResolved on a PARKED request was refused: %+v — a parked request must stay resolvable", rerr.Error)
	}

	runID, sentence := waitForRunState(t, tap, "completed")
	if runID != res.RunID {
		t.Fatalf("runState runId = %d, want %d", runID, res.RunID)
	}
	if sentence != "" {
		t.Fatalf("the run carries an error: %q — the command was healthy", sentence)
	}

	// STEP 1: the model was asked, and the question named the bound and the
	// continuation.
	select {
	case body := <-question:
		for _, want := range []string{"STILL RUNNING", "has NOT been stopped", "session.wait"} {
			if !strings.Contains(body, want) {
				t.Fatalf("the question the model was asked does not say %q:\n%s", want, body)
			}
		}
	default:
		t.Fatal("the model was never asked about the quiet command")
	}
	// STEP 3: the command's own result reached the model after the wait. The
	// round it lands in is not fixed — the model is asked again on every
	// renewal — so what is asserted is that it arrived, not where.
	//
	// The word is unambiguous here for the same reason the command spells it
	// in two halves: `finished` appears nowhere in the command line, nowhere
	// in the arguments and nowhere in the question, so the only way it can
	// reach the conversation is as output the command actually produced.
	gotOutput := false
	for _, b := range h.fake.bodies {
		if strings.Contains(b, "finished") {
			gotOutput = true
		}
	}
	if !gotOutput {
		t.Fatalf("the model never received the command's own output after keeping waiting: %v", h.fake.bodies)
	}
	// And the ledger agrees nothing was bounded: the command completed.
	reason := terminationReasonOfRun(t, h)
	if reason == nil || *reason != content.TermCompleted {
		t.Fatalf("ledger termination = %v, want completed — a quiet command that finished was not terminalized", reason)
	}
	// The session is untouched: no signal reached it.
	submitCommand(t, h.conn, sid, "echo still-alive")
	tapDataFor(t, tap, sid, "still-alive", 15*time.Second)
}

// The other answer. "stop" ends the execution, and the run reports the STOP —
// not a timeout, which is not what happened.
func TestRunLease_TheModelAnsweringStopEndsTheCommandAndTheRunSaysSo(t *testing.T) {
	h := newRunLeaseHarness(t, RunLeaseConfig{
		WallClock:   60 * time.Second,
		Inactivity:  700 * time.Millisecond,
		SignalGrace: 200 * time.Millisecond,
	})
	h.createEndpointAt()
	sid := h.openSession(t)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := "sh -c 'echo $$ > " + pidFile + "; exec sleep 100'"
	scriptQuietAnswer(h, t, "stop", make(chan string, 1))
	h.askRunsTool(sid, cmd)
	tap := newSocketTap(h.conn)

	raw := tapNotify(t, tap, "agent.runRequest", 10*time.Second)
	requestID, _, _ := decodeRunRequest(t, raw)
	h.submitLeaseCommand(t, tap, sid, cmd, requestID)
	pid := readPidFile(t, pidFile)
	waitChildAlive(t, pid)

	// The model was asked and answered stop: the command dies.
	waitChildDead(t, pid)

	if _, sentence := waitForRunState(t, tap, "completed"); sentence != "" {
		t.Fatalf("the run carries a run-level error %q; a stop the model chose is a tool outcome, not a failed run", sentence)
	}
	waittest.WaitForTimeout(t, "the model to be told the command was stopped", 10*time.Second, func() bool {
		for _, b := range h.fake.bodies {
			if strings.Contains(b, "answered stop") {
				return true
			}
		}
		return false
	})
	// THE LEDGER REPORTS THE STOP, AND BLAMES NO BOUND. That is the owner's
	// criterion for this path, and it is stated here as what it is rather
	// than as "the reason on one particular row": the model may make more
	// than one continuation — a second one arriving after the command is
	// gone is legitimate and gets its own honest attempt — so an assertion
	// pinned to whichever row a listing happens to return first would be
	// testing the ordering, not the criterion.
	//
	// It still fails on everything it must: a stop recorded as a timeout, a
	// stop recorded only as completed, or no continuation attempt at all.
	reasons := terminationReasonsOfIntent(t, h, "session.wait")
	if len(reasons) == 0 {
		t.Fatal("no ledger attempt was recorded for session.wait — the continuation is not a ledger fact, " +
			"and ADR-0020 decision 4's 'each continuation is its own attempt' does not hold in practice")
	}
	declined := false
	for _, r := range reasons {
		if r == string(content.TermAgentDeclined) {
			declined = true
		}
		if r == string(content.TermTimeout) || r == string(content.TermInactivity) {
			t.Fatalf("a continuation attempt is recorded as %q; the model chose to stop this command "+
				"and no bound ended it — attempts: %v", r, reasons)
		}
	}
	if !declined {
		t.Fatalf("no continuation attempt carries %q — attempts: %v", content.TermAgentDeclined, reasons)
	}
}

// terminationReasonsOfIntent lists the termination reason of every attempt
// recorded under one tool's action entries — the continuation's audit rows,
// not the command's.
//
// IT RETURNS STRINGS, AND THAT IS THE POINT. The pointer form of this helper
// printed `0x7b46d040bd0` in the one message that mattered, which said
// nothing at all about what the ledger held; the two cases it hid — no row
// at all versus a row with the wrong reason — have completely different
// causes and completely different fixes. An attempt still open (a NULL
// reason) is spelled out rather than dropped, because "the row exists and
// was never closed" is its own third answer.
func terminationReasonsOfIntent(t *testing.T, h *runLeaseHarness, intent string) []string {
	t.Helper()
	summaries, err := h.db.Ledger().ListEntries(context.Background(), 200)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	var reasons []string
	for _, s := range summaries {
		if s.Kind != content.EntryAction || s.Intent != intent {
			continue
		}
		e, err := h.db.Ledger().Entry(context.Background(), s.ID)
		if err != nil {
			t.Fatalf("Entry: %v", err)
		}
		for _, ex := range e.Executions {
			if ex.TerminationReason == nil {
				reasons = append(reasons, "<still open: no termination reason>")
				continue
			}
			reasons = append(reasons, string(*ex.TerminationReason))
		}
	}
	return reasons
}

// THE SETTINGS→LEASE PATH, end to end rather than on the store: the person
// sets a quiet bound of one minute, and the NEXT run is bound by it — a
// command that goes quiet is asked about at the person's number, with no
// WithRunLease value able to explain the outcome.
func TestRunLease_ChangingTheSettingChangesWhatTheNextRunIsBoundBy(t *testing.T) {
	reg := settings.New(storage.NewDocumentStore(t.TempDir()), &fakeSecretStore{})
	// Deliberately absurd as a lease config: if the settings were not read,
	// nothing would ever park and this test would time out instead.
	h := newRunLeaseHarness(t, RunLeaseConfig{
		WallClock:   60 * time.Second,
		Inactivity:  59 * time.Second,
		SignalGrace: 200 * time.Millisecond,
	}, WithSettingsRegistry(reg))
	if err := reg.SetNumber(settings.AgentRunQuietMinutes, 1); err != nil {
		t.Fatalf("SetNumber(quiet): %v", err)
	}
	if err := reg.SetNumber(settings.AgentRunWallClockMinutes, 240); err != nil {
		t.Fatalf("SetNumber(wall clock): %v", err)
	}

	cfg := h.ws.effectiveRunLease()
	if cfg.Inactivity != time.Minute {
		t.Fatalf("the next run's quiet bound = %v, want the person's 1 minute", cfg.Inactivity)
	}
	if cfg.WallClock != 240*time.Minute {
		t.Fatalf("the next run's ceiling = %v, want the person's 240 minutes", cfg.WallClock)
	}
	// The output budget and the escalation grace are NOT the person's and
	// are still the composition root's.
	if cfg.SignalGrace != 200*time.Millisecond {
		t.Fatalf("signal grace = %v, want the named 200ms — settings own the two time bounds and nothing else", cfg.SignalGrace)
	}
}
