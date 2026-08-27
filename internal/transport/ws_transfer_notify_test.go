package transport

// transfer.finished — the notification a settled transfer raises
// (nocx-zlxmm).
//
// The defect these assert against, in the owner's words: "why is there no
// toast when an operation completes?". There was one, for a FAILURE only,
// and it came from `showToast` called straight out of upload-surface.ts —
// past the pipeline entirely, so the notification centre had no record of a
// transfer, ever. The toast appeared, expired, and nothing remained.
//
// So the assertions are the person's, and they are about what happens when
// somebody walks away from a transfer and comes back: each terminal outcome
// of each direction reaches the router as an attested transfer.finished,
// carrying a level chosen per outcome — because Level is what decides
// whether the feed may forget the row before they have read it
// (notify.MustAcknowledge).
//
// Driven through the REAL socket into the REAL transfer machinery, like
// ws_upload_notify_test.go: the point is that the gesture a renderer
// actually makes produces the event, not that a constructor called with
// hand-built arguments returns what it was passed (AGENTS.md rule 1).

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/commandnames"
	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transfer"
)

// ── harness ───────────────────────────────────────────────────────────────

// transferRaiser is fakeNotifyRaiser with a signal, for blockRaiser's
// reason. The raise happens on the transfer's own goroutine, after the
// terminal notification the test may have been reading, so a test that read
// files.uploadDone and then read the captured slice would be reading across
// two goroutines with nothing ordering them. Awaiting the channel waits on
// the observable state change itself rather than on a duration (AGENTS.md: a
// test may not depend on timing); the deadline is only the backstop that
// turns a hang into a failure.
type transferRaiser struct {
	fakeNotifyRaiser
	arrived chan notify.Event
}

func newTransferRaiser() *transferRaiser {
	return &transferRaiser{arrived: make(chan notify.Event, 8)}
}

func (r *transferRaiser) Raise(ctx context.Context, ev notify.Event) notify.Outcome {
	out := r.fakeNotifyRaiser.Raise(ctx, ev)
	r.arrived <- ev
	return out
}

// await blocks until one more event has been raised, and returns it.
func (r *transferRaiser) await(t *testing.T) notify.Event {
	t.Helper()
	select {
	case ev := <-r.arrived:
		return ev
	case <-time.After(10 * time.Second):
		t.Fatal("no transfer.finished event was raised")
		return notify.Event{}
	}
}

// strandingSink SUCCEEDS and leaves paths behind. It is the one outcome the
// enum cannot express on its own — Outcome.Stranded is orthogonal to State
// (ws_upload.go) — and the reason the level map has a crossing case: a file
// on the far host that nobody asked for is a warning whatever the transfer
// did.
type strandingSink struct{ stranded []string }

func (s *strandingSink) Put(_ context.Context, u transfer.Upload, r io.Reader, progress func(int64)) (transfer.Outcome, error) {
	if r != nil {
		n, _ := io.Copy(io.Discard, r)
		progress(n)
	}
	return transfer.Outcome{State: transfer.StateWritten, FinalName: u.Name, Stranded: s.stranded}, nil
}

// assertAttestedTransfer checks everything every raise owes regardless of
// outcome: the kind, the trust class that lets it reach a sink at all, and
// the three attribution fields, read from the REGISTRY rather than asserted
// as literals (AD-7 — the backend stamps from the session it holds).
func assertAttestedTransfer(t *testing.T, ev notify.Event, reg *session.Reg, sid string) {
	t.Helper()
	if ev.Kind != notify.KindTransferFinished {
		t.Errorf("kind = %q, want %q", ev.Kind, notify.KindTransferFinished)
	}
	if ev.Trust != notify.TrustAttested {
		t.Errorf("trust = %q, want %q — the outcome is nocx's own knowledge, not a renderer's report", ev.Trust, notify.TrustAttested)
	}
	if ev.SessionID != sid {
		t.Errorf("sessionId = %q, want %q", ev.SessionID, sid)
	}
	if ev.Attribution.Backend != commandnames.LocalRoute {
		t.Errorf("attribution.Backend = %q, want %q", ev.Attribution.Backend, commandnames.LocalRoute)
	}
	if ev.Attribution.Session != sid {
		t.Errorf("attribution.Session = %q, want %q", ev.Attribution.Session, sid)
	}
	sess, err := reg.Get(session.ID(sid))
	if err != nil {
		t.Fatalf("registry Get(%q): %v", sid, err)
	}
	if ev.Attribution.Host != sess.Host() {
		t.Errorf("attribution.Host = %q, want the registry's %q", ev.Attribution.Host, sess.Host())
	}
	if !ev.At.IsZero() {
		t.Error("the source stamped At; ingress is the one stage that stamps it (internal/notify/ingress.go)")
	}
}

// regOf reaches the session registry the env's server was built on. Every
// files env builds it with newRegWithStub, and a test that asserted
// attribution against a literal host would pass while the backend stamped
// the renderer's claim instead.
func regOf(t *testing.T, e *filesTestEnv) *session.Reg {
	t.Helper()
	reg, ok := e.ws.registry.(*session.Reg)
	if !ok {
		t.Fatalf("the env's registry is a %T, not the *session.Reg these assertions read", e.ws.registry)
	}
	return reg
}

// ── uploads: one outcome at a time ───────────────────────────────────────

// The ordinary success, and the whole of what the owner asked for: a file
// goes up, and when it lands the person is told — through the pipeline, so
// the notification centre has the row afterwards.
func TestTransferFinished_AnUploadThatLandedIsASuccess(t *testing.T) {
	raiser := newTransferRaiser()
	e := newUploadTestEnv(t, WithNotifyRaiser(raiser))
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	body := []byte("the bytes the person walked away from")
	started := callUpload(t, e.conn, uploadParams(bid, dir, "report.pdf", int64(len(body))), 3).mustResult(t)
	go postUploadAsync(e.ws, started.Ticket, body)

	ev := raiser.await(t)
	assertAttestedTransfer(t, ev, regOf(t, e), sid)
	if ev.Level != notify.LevelSuccess {
		t.Errorf("level = %q, want %q — the file is there and nothing is owed", ev.Level, notify.LevelSuccess)
	}
	if notify.MustAcknowledge(ev) {
		t.Error("a written upload must not wait to be acknowledged; every successful drop would then jam the feed")
	}
	if !strings.Contains(ev.Title, "report.pdf") {
		t.Errorf("title = %q, want the file named in it", ev.Title)
	}
	if !strings.Contains(ev.Title, "Uploaded") {
		t.Errorf("title = %q, want the direction and the outcome in it", ev.Title)
	}
	if ev.Body != "" {
		t.Errorf("body = %q, want nothing: the title said everything there is", ev.Body)
	}
}

// A failure. This is the outcome the direct toast already reported, at
// `danger`, and the pipeline must not soften it on the way past — it is the
// one thing the person came back for, and MustAcknowledge is what keeps it
// in the feed until they have read it.
func TestTransferFinished_AFailedUploadIsDangerAndWaitsToBeRead(t *testing.T) {
	raiser := newTransferRaiser()
	boom := errors.New("the far host refused the write")
	e := newUploadTestEnvWithSink(t, &failingSink{err: boom}, WithNotifyRaiser(raiser))
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	body := []byte("never arrives")
	started := callUpload(t, e.conn, uploadParams(bid, dir, "a.txt", int64(len(body))), 3).mustResult(t)
	go postUploadAsync(e.ws, started.Ticket, body)

	ev := raiser.await(t)
	assertAttestedTransfer(t, ev, regOf(t, e), sid)
	if ev.Level != notify.LevelDanger {
		t.Errorf("level = %q, want %q", ev.Level, notify.LevelDanger)
	}
	if !notify.MustAcknowledge(ev) {
		t.Error("a failed transfer can be evicted before the person reads it; the one outcome they came back for must wait")
	}
	if !strings.Contains(ev.Title, "failed") || !strings.Contains(ev.Title, "a.txt") {
		t.Errorf("title = %q, want the file and the verb", ev.Title)
	}
	if !strings.Contains(ev.Body, boom.Error()) {
		t.Errorf("body = %q, want the reason the sink gave", ev.Body)
	}
}

// Skip is the person's own answer to a collision they were shown, and
// nothing was written. Not a success — no file moved — and not a failure —
// nothing went wrong — so it is info, and info is what keeps it from
// demanding an acknowledgement of a decision they just made.
func TestTransferFinished_ASkippedUploadIsNeitherSuccessNorFailure(t *testing.T) {
	raiser := newTransferRaiser()
	e := newUploadTestEnv(t, WithNotifyRaiser(raiser))
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("original"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	bid := e.openBinding(t, sid, dir, 2)

	p := uploadParams(bid, dir, "a.txt", 5)
	p["onExists"] = "skip"
	started := callUpload(t, e.conn, p, 3).mustResult(t)
	if state := awaitTransferState(t, e.ws, started.TransferID); state != uploadStateSkipped {
		t.Fatalf("state = %q, want %q", state, uploadStateSkipped)
	}

	ev := raiser.await(t)
	if ev.Level != notify.LevelInfo {
		t.Errorf("level = %q, want %q — nothing was written and nothing went wrong", ev.Level, notify.LevelInfo)
	}
	if notify.MustAcknowledge(ev) {
		t.Error("a skipped transfer waits to be acknowledged; it is the person's own answer being read back to them")
	}
	if !strings.Contains(ev.Title, "skipped") || !strings.Contains(ev.Title, "a.txt") {
		t.Errorf("title = %q, want the file and the verb", ev.Title)
	}
}

// Cancel is the person's own doing (or the binding going away underneath
// them). Reporting it as a failure would put a `danger` row in the feed for
// the button they just pressed.
func TestTransferFinished_ACancelledUploadIsNotAFailure(t *testing.T) {
	raiser := newTransferRaiser()
	e := newUploadTestEnv(t, WithNotifyRaiser(raiser))
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	started := callUpload(t, e.conn, uploadParams(bid, dir, "big.iso", 1<<20), 3).mustResult(t)
	if raw := jsonrpcCallWithID(t, e.conn, "files.uploadCancel",
		map[string]any{"transferId": started.TransferID}, 4); len(raw) == 0 {
		t.Fatal("files.uploadCancel answered nothing")
	}
	if state := awaitTransferState(t, e.ws, started.TransferID); state != uploadStateCancelled {
		t.Fatalf("state = %q, want %q", state, uploadStateCancelled)
	}

	ev := raiser.await(t)
	if ev.Level != notify.LevelInfo {
		t.Errorf("level = %q, want %q — they pressed cancel", ev.Level, notify.LevelInfo)
	}
	if notify.MustAcknowledge(ev) {
		t.Error("a cancelled transfer waits to be acknowledged")
	}
	if !strings.Contains(ev.Title, "cancelled") {
		t.Errorf("title = %q, want the verb", ev.Title)
	}
	if ev.Body != "" {
		t.Errorf("body = %q, want nothing: a cancellation's error is context.Canceled and is not a fault", ev.Body)
	}
}

// The crossing case. The upload SUCCEEDED and left a file on the far host
// that nobody asked for — the state says written and the disk says
// otherwise. It is a warning, so it survives eviction until somebody reads
// it, and the paths are named: this is the whole content of the direct
// stranded toast that this raise replaces.
func TestTransferFinished_AWrittenUploadThatStrandedAPathIsAWarning(t *testing.T) {
	raiser := newTransferRaiser()
	e := newUploadTestEnvWithSink(t,
		&strandingSink{stranded: []string{"/srv/.nocx-upload-7f3a", "/srv/a.txt.bak"}},
		WithNotifyRaiser(raiser))
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	body := []byte("landed, and left litter")
	started := callUpload(t, e.conn, uploadParams(bid, dir, "a.txt", int64(len(body))), 3).mustResult(t)
	go postUploadAsync(e.ws, started.Ticket, body)

	ev := raiser.await(t)
	if ev.Level != notify.LevelWarning {
		t.Errorf("level = %q, want %q — a file nobody asked for is on their disk", ev.Level, notify.LevelWarning)
	}
	if !notify.MustAcknowledge(ev) {
		t.Error("a stranded path can be evicted unread; it is the one thing nobody will be able to explain later")
	}
	for _, want := range []string{"/srv/.nocx-upload-7f3a", "/srv/a.txt.bak", "left behind"} {
		if !strings.Contains(ev.Body, want) {
			t.Errorf("body = %q, want %q named in it", ev.Body, want)
		}
	}
}

// Two removed toasts, one row. A failed upload that also stranded a path
// used to produce a `danger` toast with the reason AND a `warning` toast with
// the paths; folding them into one event must not drop either half, because
// the reason says what went wrong and the paths name files nobody will be
// able to explain later.
func TestTransferFinished_AFailureThatAlsoStrandedAPathKeepsBothHalves(t *testing.T) {
	raiser := newTransferRaiser()
	boom := errors.New("the far host refused the write")
	e := newUploadTestEnvWithSink(t,
		&failingSink{err: boom, stranded: []string{"/srv/.nocx-upload-91cc"}},
		WithNotifyRaiser(raiser))
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	body := []byte("neither half may be dropped")
	started := callUpload(t, e.conn, uploadParams(bid, dir, "a.txt", int64(len(body))), 3).mustResult(t)
	go postUploadAsync(e.ws, started.Ticket, body)

	ev := raiser.await(t)
	if ev.Level != notify.LevelDanger {
		t.Errorf("level = %q, want %q — the transfer failed, whatever else it did", ev.Level, notify.LevelDanger)
	}
	for _, want := range []string{boom.Error(), "/srv/.nocx-upload-91cc"} {
		if !strings.Contains(ev.Body, want) {
			t.Errorf("body = %q, want %q in it", ev.Body, want)
		}
	}
}

// ── downloads: the same kind, the other direction ────────────────────────

// The download's success word is `sent`, not `written` — the wire's own
// vocabulary (frontend/src/ui/operation.ts) — and one kind covers both
// directions, so a person answers "tell me when a transfer ends" once.
func TestTransferFinished_ADownloadThatArrivedIsASuccess(t *testing.T) {
	raiser := newTransferRaiser()
	e := newDownloadTestEnv(t, WithNotifyRaiser(raiser))
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	p := fixture(t, dir, "notes.md", "the file they came back for")
	bid := e.openBinding(t, sid, dir, 2)
	started := callDownload(t, e.conn, downloadParams(bid, p), 3).mustResult(t)

	if code, _ := getDownload(t, e.ws, started.Ticket); code != http.StatusOK {
		t.Fatalf("GET /download = %d, want 200", code)
	}

	ev := raiser.await(t)
	assertAttestedTransfer(t, ev, regOf(t, e), sid)
	if ev.Level != notify.LevelSuccess {
		t.Errorf("level = %q, want %q", ev.Level, notify.LevelSuccess)
	}
	if !strings.Contains(ev.Title, "Downloaded") || !strings.Contains(ev.Title, "notes.md") {
		t.Errorf("title = %q, want the direction and the file", ev.Title)
	}
}

func TestTransferFinished_AFailedDownloadIsDangerAndCarriesTheReason(t *testing.T) {
	raiser := newTransferRaiser()
	boom := io.ErrUnexpectedEOF
	e := newDownloadTestEnvWith(t, downloadFactoryWithSource(&failingSource{err: boom, sent: 1024}),
		WithNotifyRaiser(raiser))
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	p := fixture(t, dir, "a.txt", "x")
	bid := e.openBinding(t, sid, dir, 2)
	started := callDownload(t, e.conn, downloadParams(bid, p), 3).mustResult(t)
	go func() { _, _ = getDownloadRaw(e.ws, started.Ticket) }()

	ev := raiser.await(t)
	if ev.Level != notify.LevelDanger {
		t.Errorf("level = %q, want %q", ev.Level, notify.LevelDanger)
	}
	if !notify.MustAcknowledge(ev) {
		t.Error("a failed download can be evicted before it is read")
	}
	if !strings.Contains(ev.Title, "Download") || !strings.Contains(ev.Title, "failed") {
		t.Errorf("title = %q, want the direction and the verb", ev.Title)
	}
	if !strings.Contains(ev.Body, boom.Error()) {
		t.Errorf("body = %q, want the reason", ev.Body)
	}
}

// A download nobody ever fetched: the ticket's TTL elapses, the transfer is
// cancelled, and that is not a failure either.
func TestTransferFinished_ACancelledDownloadIsNotAFailure(t *testing.T) {
	raiser := newTransferRaiser()
	e := newDownloadTestEnv(t, WithNotifyRaiser(raiser))
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	p := fixture(t, dir, "a.txt", "x")
	bid := e.openBinding(t, sid, dir, 2)
	started := callDownload(t, e.conn, downloadParams(bid, p), 3).mustResult(t)

	_ = jsonrpcCallWithID(t, e.conn, "files.downloadCancel",
		map[string]any{"transferId": started.TransferID}, 4)
	if state := awaitTransferState(t, e.ws, started.TransferID); state != downloadStateCancelled {
		t.Fatalf("state = %q, want %q", state, downloadStateCancelled)
	}

	ev := raiser.await(t)
	if ev.Level != notify.LevelInfo {
		t.Errorf("level = %q, want %q", ev.Level, notify.LevelInfo)
	}
	if !strings.Contains(ev.Title, "cancelled") {
		t.Errorf("title = %q, want the verb", ev.Title)
	}
}

// ── rule 3: the raiser is an external call, so it fails ──────────────────

// failingRaiser is the pipeline refusing the event — what ingress answers
// when its context is done, and what the router answers when a global
// admission limit is exceeded (notify.RefusedError). The raise is the LAST
// thing settle does, and nothing reads its Outcome; this is the test that
// says so.
type failingRaiser struct {
	transferRaiser
}

func (r *failingRaiser) Raise(ctx context.Context, ev notify.Event) notify.Outcome {
	out := r.transferRaiser.Raise(ctx, ev)
	out.Err = &notify.RefusedError{Limit: notify.LimitQueued}
	return out
}

// A pipeline that refuses the event must cost the transfer nothing: the
// bytes are on disk, the terminal notification the Files panel draws from
// still arrives, and the transfer still settles. A notification surface that
// could break a file transfer would be the tail wagging the dog.
func TestTransferFinished_ARefusingPipelineDoesNotCostTheTransferAnything(t *testing.T) {
	raiser := &failingRaiser{transferRaiser: *newTransferRaiser()}
	e := newUploadTestEnv(t, WithNotifyRaiser(raiser))
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	body := []byte("the pipeline is full; the file is not")
	started := callUpload(t, e.conn, uploadParams(bid, dir, "a.txt", int64(len(body))), 3).mustResult(t)
	go postUploadAsync(e.ws, started.Ticket, body)

	// The terminal notification still reaches the person the way it always
	// did — the assertion is on the wire, not on the raiser.
	done := readUploadDone(t, e.conn)
	if done.Outcome != uploadStateWritten {
		t.Fatalf("outcome = %q, want %q", done.Outcome, uploadStateWritten)
	}
	if state := awaitTransferState(t, e.ws, started.TransferID); state != uploadStateWritten {
		t.Fatalf("state = %q, want %q", state, uploadStateWritten)
	}
	// #nosec G304 — a path this test built under its own t.TempDir().
	if got, err := os.ReadFile(filepath.Join(dir, "a.txt")); err != nil || string(got) != string(body) { //nolint:gosec // see above
		t.Fatalf("destination = %q, %v; want the uploaded bytes", got, err)
	}
	// And the raise was still attempted: a refusal is the pipeline's answer,
	// not a reason to stop asking it.
	if ev := raiser.await(t); ev.Kind != notify.KindTransferFinished {
		t.Errorf("kind = %q, want %q", ev.Kind, notify.KindTransferFinished)
	}
}

// AD-7, as a guard rather than as a comment: a notification's attribution
// may only come from the session the BACKEND holds, so a transfer whose
// session has left the registry raises nothing at all. It is the same answer
// deliverTransferDone gives the outcome a line earlier, and the only way to
// reach it is a session being torn down — where the outcome is `cancelled`
// on a tab the person just closed.
func TestTransferFinished_ASessionThatIsGoneRaisesNothing(t *testing.T) {
	raiser := newTransferRaiser()
	e := newUploadTestEnv(t, WithNotifyRaiser(raiser))

	e.ws.raiseTransferFinished(
		&runningTransfer{id: "t1", sessionID: session.ID("no-such-session")},
		transferOutcome{up: true, state: uploadStateCancelled, name: "a.txt"},
	)

	select {
	case ev := <-raiser.arrived:
		t.Fatalf("an event was raised for a session the registry does not hold: %+v", ev.Attribution)
	default:
	}
}
