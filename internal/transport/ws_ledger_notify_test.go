package transport

// block.finished — the notification a closed block raises (nocx-n3nfg).
//
// The defect these assert against: the catalogue declared "A command
// finished" routable with channel toggles beside it, and NOTHING in
// production ever constructed the event, so a user who ran `ls` and then
// interrupted a `du -Hs` opened the notification centre and read "Nothing to
// catch up on". So the assertions are the user's: two commands, one that
// worked and one that did not, and the two must be told apart without
// opening anything.
//
// Driven through the REAL socket into the REAL store, like ws_ledger_test.go,
// because the point is that a close a renderer actually sends produces the
// event — not that a constructor called with hand-built arguments returns
// what it was passed (AGENTS.md rule 1).

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/commandnames"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/session"
)

// ── harness ───────────────────────────────────────────────────────────────

// blockRaiser is fakeNotifyRaiser with a signal. The raise is deliberately
// the LAST thing handleClose does — the ack must not wait on a sink — so a
// test that read the ack and then read the captured slice would be reading
// across two goroutines with nothing ordering them. Awaiting the channel
// waits on the observable state change itself rather than on a duration
// (AGENTS.md: a test may not depend on timing); the deadline below is only
// the backstop that turns a hang into a failure.
type blockRaiser struct {
	fakeNotifyRaiser
	arrived chan notify.Event
}

func newBlockRaiser() *blockRaiser {
	return &blockRaiser{arrived: make(chan notify.Event, 8)}
}

func (b *blockRaiser) Raise(ctx context.Context, ev notify.Event) notify.Outcome {
	out := b.fakeNotifyRaiser.Raise(ctx, ev)
	b.arrived <- ev
	return out
}

// errBlockNotifyBoom is the injected store failure. Its text is asserted
// nowhere: what the failure path owes is a refusal and no event, not a
// sentence.
var errBlockNotifyBoom = errors.New("store is down")

// await blocks until one more event has been raised, and returns it.
func (b *blockRaiser) await(t *testing.T) notify.Event {
	t.Helper()
	select {
	case ev := <-b.arrived:
		return ev
	case <-time.After(10 * time.Second):
		t.Fatal("no block.finished event was raised")
		return notify.Event{}
	}
}

// newBlockNotifyWS is newLedgerWSServer with the raiser wired, handing back
// the registry so a test can assert attribution against the entry the backend
// stamped from rather than against a literal — the shape
// newSessionEndedServer uses for the same reason.
func newBlockNotifyWS(t *testing.T, db content.ContentDB, raiser NotifyRaiser) (*session.Reg, *websocket.Conn) {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)
	ws := NewWSServer(logger, reg, WithContentDB(db), WithNotifyRaiser(raiser))
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	return reg, conn
}

// closeOf is one ledger.close, as a renderer sends it.
func closeOf(sid, id, intent, status, reason string, exitCode *int, clientSeq int) map[string]any {
	facts := map[string]any{"terminationReason": reason}
	if exitCode != nil {
		facts["exitCode"] = *exitCode
	}
	return map[string]any{
		"envelope":   ledgerEnv(sid, id, intent, clientSeq),
		"status":     status,
		"facts":      facts,
		"durationMs": 40,
	}
}

// registryHost is the host the registry holds for a live session — the value
// the raise must be attributed with, read from the registry rather than
// asserted as a literal.
func registryHost(t *testing.T, reg *session.Reg, sid string) string {
	t.Helper()
	sess, err := reg.Get(session.ID(sid))
	if err != nil {
		t.Fatalf("registry Get(%q): %v", sid, err)
	}
	return sess.Host()
}

// ── the event a close raises ──────────────────────────────────────────────

// A user runs a command and it finishes. The close the renderer sends reaches
// the router as an attested block.finished carrying the kind, trust, level
// and backend-stamped attribution the design names, and nothing else.
func TestLedgerClose_RaisesAnAttestedBlockFinished(t *testing.T) {
	db := newLedgerStore(t)
	raiser := newBlockRaiser()
	reg, conn := newBlockNotifyWS(t, db, raiser)
	sid := openLocalSession(t, conn)
	host := registryHost(t, reg, sid)

	if _, errObj := ledgerCall(t, conn, "ledger.open",
		map[string]any{"envelope": ledgerEnv(sid, "ok-1", "ls", 1)}, 2); errObj != nil {
		t.Fatalf("ledger.open error: %+v", errObj)
	}
	ack, errObj := ledgerCall(t, conn, "ledger.close",
		closeOf(sid, "ok-1", "ls", "success", "completed", intPtr(0), 2), 3)
	if errObj != nil {
		t.Fatalf("ledger.close error: %+v", errObj)
	}
	if ack.Outcome != ledgerApplied || ack.Phase != string(content.PhaseClosed) {
		t.Fatalf("close ack = %+v, want applied/closed", ack)
	}

	got := raiser.await(t)
	if got.Kind != notify.KindBlockFinished {
		t.Errorf("Kind = %q, want %q", got.Kind, notify.KindBlockFinished)
	}
	// The ledger is nocx's own record of the fact, so the class is attested —
	// which is also what lets it reach a completion subscription (design
	// §3.1). Anything weaker would silently disqualify it from the one
	// surface the kind exists for.
	if got.Trust != notify.TrustAttested {
		t.Errorf("Trust = %q, want %q — the ledger attested this, no program asked for it", got.Trust, notify.TrustAttested)
	}
	if got.Level != notify.LevelSuccess {
		t.Errorf("Level = %q, want success for a command that worked", got.Level)
	}
	if got.Title != "ls succeeded" {
		t.Errorf("Title = %q, want it to name the command and say it worked", got.Title)
	}
	if got.Body != "" {
		t.Errorf("Body = %q, want empty — `exit status 0` says nothing the title has not", got.Body)
	}
	if got.SessionID != sid {
		t.Errorf("SessionID = %q, want %q", got.SessionID, sid)
	}
	if got.Attribution.Backend != commandnames.LocalRoute {
		t.Errorf("Attribution.Backend = %q, want %q", got.Attribution.Backend, commandnames.LocalRoute)
	}
	if got.Attribution.Host != host {
		t.Errorf("Attribution.Host = %q, want the registry's %q", got.Attribution.Host, host)
	}
	if got.Attribution.Session != sid {
		t.Errorf("Attribution.Session = %q, want %q", got.Attribution.Session, sid)
	}
	// At is NOT stamped here: ingress owns that stamp, so a relay replaying a
	// buffered batch keeps its own instants (internal/notify/ingress.go).
	if !got.At.IsZero() {
		t.Errorf("At = %v, want the zero time — ingress stamps it, not the source", got.At)
	}
}

// ── the failure case, which is the one that matters ───────────────────────

// The owner's screenshot: `ls` was fine and `du -Hs` came back 130, and the
// centre said "Nothing to catch up on". Both commands are run here through
// one server, and the two events are compared with each other — a test that
// asserted only the failing one could not have reported that the two read the
// same.
func TestLedgerClose_AFailedCommandIsDistinguishableFromASuccessfulOne(t *testing.T) {
	db := newLedgerStore(t)
	raiser := newBlockRaiser()
	_, conn := newBlockNotifyWS(t, db, raiser)
	sid := openLocalSession(t, conn)

	if _, errObj := ledgerCall(t, conn, "ledger.close",
		closeOf(sid, "good", "ls", "success", "completed", intPtr(0), 1), 2); errObj != nil {
		t.Fatalf("ledger.close (ls): %+v", errObj)
	}
	good := raiser.await(t)

	if _, errObj := ledgerCall(t, conn, "ledger.close",
		closeOf(sid, "bad", "du -Hs", "interrupted", "interrupted", intPtr(130), 2), 3); errObj != nil {
		t.Fatalf("ledger.close (du -Hs): %+v", errObj)
	}
	bad := raiser.await(t)

	if bad.Level != notify.LevelWarning {
		t.Errorf("failing command Level = %q, want warning", bad.Level)
	}
	if bad.Level == good.Level {
		t.Errorf("both commands raised Level %q — a user cannot tell them apart", bad.Level)
	}
	if bad.Title != "du -Hs was interrupted" {
		t.Errorf("Title = %q, want it to name the command and say it did not finish normally", bad.Title)
	}
	if bad.Body != "exit status 130" {
		t.Errorf("Body = %q, want the exit code the shell reported", bad.Body)
	}
	if bad.Title == good.Title || bad.Body == good.Body {
		t.Errorf("the two events read the same:\n ok  %q / %q\n bad %q / %q",
			good.Title, good.Body, bad.Title, bad.Body)
	}
	// Both are members of the feed. Whether either reaches a banner is the
	// router's decision from the user's table, and the catalogue ships
	// blockFinished with no default channel — but a suppressed event is
	// still remembered (internal/notify/ingress.go), which is what makes the
	// centre able to answer "what did I miss".
	if good.Kind != notify.KindBlockFinished || bad.Kind != notify.KindBlockFinished {
		t.Errorf("kinds = %q / %q, want both %q", good.Kind, bad.Kind, notify.KindBlockFinished)
	}
}

// A close with no exit code — every non-shell kind, and an interruption that
// never produced one — still says how it went. The three statuses that say a
// run ended without saying how take the neutral verb rather than claiming
// success.
func TestBlockFinishedWording_CoversTheClosedSets(t *testing.T) {
	for _, tc := range []struct {
		name      string
		intent    string
		status    content.EntryStatus
		facts     ledgerCloseFacts
		wantTitle string
		wantBody  string
		wantLevel notify.Level
	}{
		{
			name: "a clean run", intent: "make test", status: content.EntrySuccess,
			facts:     ledgerCloseFacts{TerminationReason: string(content.TermCompleted), ExitCode: intPtr(0)},
			wantTitle: "make test succeeded", wantBody: "", wantLevel: notify.LevelSuccess,
		},
		{
			name: "a non-zero exit", intent: "go build ./...", status: content.EntryFailure,
			facts:     ledgerCloseFacts{TerminationReason: string(content.TermFailed), ExitCode: intPtr(2)},
			wantTitle: "go build ./... failed", wantBody: "exit status 2", wantLevel: notify.LevelWarning,
		},
		{
			name: "killed with no code", intent: "sleep 100", status: content.EntryInterrupted,
			facts:     ledgerCloseFacts{TerminationReason: string(content.TermUserKilled)},
			wantTitle: "sleep 100 was interrupted", wantBody: "stopped from nocx", wantLevel: notify.LevelWarning,
		},
		{
			name: "the session went away", intent: "rsync -a . host:/", status: content.EntryUnknown,
			facts:     ledgerCloseFacts{TerminationReason: string(content.TermTransportGone)},
			wantTitle: "rsync -a . host:/ finished", wantBody: "the connection was lost", wantLevel: notify.LevelWarning,
		},
		{
			// An orphan OSC 133 C is an entry with no intent (design §4.4).
			// It must still read as a sentence.
			name: "no intent at all", intent: "", status: content.EntrySuccess,
			facts:     ledgerCloseFacts{TerminationReason: string(content.TermCompleted)},
			wantTitle: "A command succeeded", wantBody: "", wantLevel: notify.LevelSuccess,
		},
		{
			name: "a multi-line heredoc", intent: "cat <<EOF\nhello\nEOF", status: content.EntrySuccess,
			facts:     ledgerCloseFacts{TerminationReason: string(content.TermCompleted)},
			wantTitle: "cat <<EOF hello EOF succeeded", wantBody: "", wantLevel: notify.LevelSuccess,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := blockFinishedTitle(tc.intent, tc.status); got != tc.wantTitle {
				t.Errorf("title = %q, want %q", got, tc.wantTitle)
			}
			if got := blockFinishedBody(tc.facts); got != tc.wantBody {
				t.Errorf("body = %q, want %q", got, tc.wantBody)
			}
			if got := blockFinishedLevel(tc.status); got != tc.wantLevel {
				t.Errorf("level = %q, want %q", got, tc.wantLevel)
			}
		})
	}
}

// A command 16 384 runes long is a legal intent (maxLedgerIntentRunes) and an
// illegal banner. The verb has to survive the cut, because it is the half
// that says how it went.
func TestBlockFinishedTitle_BoundsTheCommandAndKeepsTheVerb(t *testing.T) {
	long := strings.Repeat("a", maxLedgerIntentRunes)
	got := blockFinishedTitle(long, content.EntryFailure)
	if !strings.HasSuffix(got, " failed") {
		t.Errorf("title = %q…, want it to still end in the verb", got[:40])
	}
	if n := len([]rune(got)); n > maxBlockSubjectRunes+len(" failed") {
		t.Errorf("title is %d runes, want the subject bounded to %d", n, maxBlockSubjectRunes)
	}
}

// The title carries the command text, and the command text reaches a banner,
// a toast and — once targets land — the network. So it must be the MASKED
// intent the row stores, never the envelope's.
func TestLedgerClose_TitleCarriesTheMaskedIntentNeverTheSecret(t *testing.T) {
	db := newLedgerStore(t)
	raiser := newBlockRaiser()
	_, conn := newBlockNotifyWS(t, db, raiser)
	sid := openLocalSession(t, conn)

	if _, errObj := ledgerCall(t, conn, "ledger.close",
		closeOf(sid, "secret-1", ledgerSecretIntent, "success", "completed", intPtr(0), 1), 2); errObj != nil {
		t.Fatalf("ledger.close: %+v", errObj)
	}
	got := raiser.await(t)
	if strings.Contains(got.Title, ledgerSecret) {
		t.Fatalf("the raw secret is in the notification title: %q", got.Title)
	}
	if !strings.Contains(got.Title, "curl") {
		t.Fatalf("Title = %q, want it to still name the command", got.Title)
	}
}

// ── exactly once ──────────────────────────────────────────────────────────

// Design §A3: "exactly one notification when it exits and no second one". The
// renderer's outbox re-delivers a close it never saw acknowledged, and a
// re-delivery is a `replay` that changes no row — so it must not become a
// second banner for one command.
//
// The second close below is the barrier: it is sent only after the replay has
// been acknowledged, and the event it raises is identified by its own title,
// so a replay that had raised would be the event this test receives second.
func TestLedgerClose_ReplayRaisesNothingASecondTime(t *testing.T) {
	db := newLedgerStore(t)
	raiser := newBlockRaiser()
	_, conn := newBlockNotifyWS(t, db, raiser)
	sid := openLocalSession(t, conn)

	if _, errObj := ledgerCall(t, conn, "ledger.close",
		closeOf(sid, "once", "first-cmd", "success", "completed", intPtr(0), 1), 2); errObj != nil {
		t.Fatalf("ledger.close: %+v", errObj)
	}
	if first := raiser.await(t); first.Title != "first-cmd succeeded" {
		t.Fatalf("first event Title = %q, want %q", first.Title, "first-cmd succeeded")
	}

	ack, errObj := ledgerCall(t, conn, "ledger.close",
		closeOf(sid, "once", "first-cmd", "success", "completed", intPtr(0), 2), 3)
	if errObj != nil {
		t.Fatalf("replayed ledger.close: %+v", errObj)
	}
	if ack.Outcome != ledgerReplay {
		t.Fatalf("replayed close ack outcome = %q, want %q", ack.Outcome, ledgerReplay)
	}

	if _, errObj := ledgerCall(t, conn, "ledger.close",
		closeOf(sid, "barrier", "second-cmd", "success", "completed", intPtr(0), 3), 4); errObj != nil {
		t.Fatalf("barrier ledger.close: %+v", errObj)
	}
	second := raiser.await(t)
	if second.Title != "second-cmd succeeded" {
		t.Fatalf("the event after the replay was %q — the replay raised a second time", second.Title)
	}
	if evs := raiser.captured(); len(evs) != 2 {
		t.Fatalf("raised %d events for two commands and one replay, want exactly 2", len(evs))
	}
}

// An open and a bind are not outcomes: nothing may be told about them. Only
// the close raises.
func TestLedgerOpenAndBind_RaiseNothing(t *testing.T) {
	db := newLedgerStore(t)
	raiser := newBlockRaiser()
	_, conn := newBlockNotifyWS(t, db, raiser)
	sid := openLocalSession(t, conn)

	if _, errObj := ledgerCall(t, conn, "ledger.open",
		map[string]any{"envelope": ledgerEnv(sid, "quiet", "make", 1)}, 2); errObj != nil {
		t.Fatalf("ledger.open: %+v", errObj)
	}
	if _, errObj := ledgerCall(t, conn, "ledger.bind",
		map[string]any{"envelope": ledgerEnv(sid, "quiet", "make", 2)}, 3); errObj != nil {
		t.Fatalf("ledger.bind: %+v", errObj)
	}
	// The close is the barrier AND the positive control: its event proves the
	// raiser was reachable all along, so the two zeros above are an absence
	// and not a broken harness.
	if _, errObj := ledgerCall(t, conn, "ledger.close",
		closeOf(sid, "quiet", "make", "success", "completed", intPtr(0), 3), 4); errObj != nil {
		t.Fatalf("ledger.close: %+v", errObj)
	}
	raiser.await(t)
	if evs := raiser.captured(); len(evs) != 1 {
		t.Fatalf("open + bind + close raised %d events, want exactly 1", len(evs))
	}
}

// ── the failure path (AGENTS.md rule 3) ───────────────────────────────────

// The store call the close depends on fails. There is no closed block, so
// there is nothing to announce — a notification for a command whose end was
// never recorded is a notification about a fact that does not exist.
//
// This one needs no barrier: the raise is unreachable from the error path by
// construction (apply reports false and handleClose returns), so the count is
// final the moment the error ack arrives.
func TestLedgerClose_StoreFailure_RaisesNothing(t *testing.T) {
	real := newLedgerStore(t)
	raiser := newBlockRaiser()
	db := &failingLedgerDB{ContentDB: real, failOn: "FinishExecution", err: errBlockNotifyBoom}
	_, conn := newBlockNotifyWS(t, db, raiser)
	sid := openLocalSession(t, conn)

	if _, errObj := ledgerCall(t, conn, "ledger.open",
		map[string]any{"envelope": ledgerEnv(sid, "doomed", "make test", 1)}, 2); errObj != nil {
		t.Fatalf("ledger.open: %+v", errObj)
	}
	_, errObj := ledgerCall(t, conn, "ledger.close",
		closeOf(sid, "doomed", "make test", "failure", "failed", intPtr(1), 2), 3)
	if errObj == nil {
		t.Fatal("ledger.close succeeded with FinishExecution failing")
	}
	if evs := raiser.captured(); len(evs) != 0 {
		t.Fatalf("a close that failed to record raised %d events, want none: %+v", len(evs), evs)
	}
	// The row is still open, which is the state the absence of an event has
	// to agree with: nothing closed, so nothing finished.
	if row := mustEntry(t, real, "doomed"); row.Phase == content.PhaseClosed {
		t.Fatalf("phase = %q after a failed close, want it not closed", row.Phase)
	}
}
