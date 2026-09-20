package app

// The coordinator's capture storage (nocx-2v80t.2.2): the reverse handler
// that answers a helper's settled record by storing it against its entry —
// through the content ledger's own CaptureOutput, never a second storage
// path — and the fence→entry memory that tells it which entry.
//
// The refusals are tested THROUGH THE HANDLER, not against the store alone:
// the store half of every refusal is already proven in internal/content
// (TestCaptureOutput_*), and what this file adds is the PATH — a record
// arriving on the wire comes back as the result's {kept, reason}, with every
// paired positive beside it.

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/content"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log/logtest"
)

// ── the binding memory ───────────────────────────────────────────────────

// TestCaptureBindings_BindsAreReadableUntilTheBoundEvictsThem walks the
// memory's own contract: a bound nonce reads back its entry, a re-bound
// nonce reads back the newer value (a replayed completion binds the same
// fact, and a second row for it would grow without bound), and eviction
// runs oldest first — so a capture arriving within its ask timeout finds
// its entry unless the memory itself is miswired.
func TestCaptureBindings_BindsAreReadableUntilTheBoundEvictsThem(t *testing.T) {
	b := newCaptureBindings()

	b.Bind(fenceHex(0x01), "entry-1")
	if got, ok := b.entryFor(fenceHex(0x01)); !ok || got != "entry-1" {
		t.Fatalf("entryFor = %q, %v; want entry-1, true", got, ok)
	}

	// A re-bind of the same nonce is the same fact, not a second row.
	b.Bind(fenceHex(0x01), "entry-1b")
	if got, _ := b.entryFor(fenceHex(0x01)); got != "entry-1b" {
		t.Fatalf("a re-bound nonce reads %q, want the newest value", got)
	}
	if b.size() != 1 {
		t.Fatalf("the memory holds %d entries for one nonce, want 1", b.size())
	}

	// Enough DISTINCT nonces to run the bound over: the bound is exceeded,
	// so eviction genuinely fires, and the size lands exactly on it.
	for i := 0; i < captureBindingsBound+10; i++ {
		b.Bind(fenceFromCounter(i), fmt.Sprintf("entry-%d", i))
	}
	if b.size() != captureBindingsBound {
		t.Fatalf("the memory holds %d entries after %d binds, want exactly its bound of %d",
			b.size(), captureBindingsBound+10, captureBindingsBound)
	}
	// The nonce of the LAST bind is still readable and the FIRST is gone:
	// eviction ran oldest first.
	if _, ok := b.entryFor(fenceFromCounter(0)); ok {
		t.Fatal("the first-bound nonce survived a full bound of eviction")
	}
	newest := captureBindingsBound + 9
	if got, ok := b.entryFor(fenceFromCounter(newest)); !ok || got != fmt.Sprintf("entry-%d", newest) {
		t.Fatalf("the newest bound nonce %s reads %q, %v; eviction is not oldest-first",
			fenceFromCounter(newest), got, ok)
	}
}

// TestCaptureBindings_AnUnknownNonceIsNotAFatalAnswer pins the read half of
// the noEntry contract: a nonce the memory never bound reads back false and
// no entry, which is what the handler turns into the result's noEntry.
func TestCaptureBindings_AnUnknownNonceIsNotAFatalAnswer(t *testing.T) {
	b := newCaptureBindings()
	if got, ok := b.entryFor(fenceHex(0xEE)); ok || got != "" {
		t.Fatalf("an unknown nonce read back %q, %v; want empty, false", got, ok)
	}
}

// ── the handler, against a real store ────────────────────────────────────

// captureTestStore opens one real encrypted content store whose environment
// carries one observation of the named criticality, so a command recorded
// against it has an execution whose pinned observation is that one.
func captureTestStore(t *testing.T, criticality content.Criticality, policy *content.Policy) content.ContentDB {
	t.Helper()
	ctx := context.Background()
	cfg := content.Config{
		Path: t.TempDir() + "/content.db", Key: make([]byte, 32),
		Budget: content.Budget{
			RetentionBytes:   1 << 30,
			DiskCeilingBytes: 2 << 30,
			CompactionFloor:  0.8,
		},
	}
	if policy != nil {
		cfg.Policy = policy
	}
	db, err := content.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	led := db.Ledger()
	if err := led.EnsureEnvironment(ctx, content.Environment{ID: "local", Kind: content.EnvLocal}); err != nil {
		t.Fatalf("EnsureEnvironment: %v", err)
	}
	if _, err := led.RecordObservation(ctx, content.Observation{
		EnvironmentID: "local", Criticality: criticality, Payload: "{}",
	}); err != nil {
		t.Fatalf("RecordObservation: %v", err)
	}
	return db
}

// recordOneCommand writes one completed command row and answers its id —
// the row a capture body hangs on.
func recordOneCommand(t *testing.T, db content.ContentDB, intent string) string {
	t.Helper()
	id, err := db.Ledger().RecordCompleted(context.Background(), content.CompletedCommand{
		Client: "capture-test",
		Env:    content.Environment{ID: "local", Kind: content.EnvLocal},
		Intent: intent,
		Source: content.SourceUser,
		Status: content.EntrySuccess,
	})
	if err != nil {
		t.Fatalf("RecordCompleted(%q): %v", intent, err)
	}
	if id == "" {
		t.Fatalf("RecordCompleted(%q) minted no row", intent)
	}
	return id
}

// fenceHex is one syntactically valid fence nonce: 64 lowercase hex chars.
func fenceHex(b byte) string {
	n := make([]byte, 32)
	for i := range n {
		n[i] = b
	}
	return hex.EncodeToString(n)
}

// fenceFromCounter is one syntactically valid, distinct fence nonce.
func fenceFromCounter(i int) string {
	return fmt.Sprintf("%064x", i)
}

// aCaptureRecord builds the record one settled interval carries, with a
// closing screen whose content the assertions can name.
func aCaptureRecord(nonce string) proto.CaptureParams {
	cols, rows := 20, 3
	return proto.CaptureParams{
		Session:      proto.HostSessionID{Generation: "gentest", Session: "6e6f63782d7465737431"},
		Incarnation:  proto.Incarnation{Generation: 1, Session: "6e6f63782d7465737431"},
		Nonce:        nonce,
		Revision:     41,
		Completeness: proto.CompletenessComplete,
		Opening:      proto.CaptureScreen{Cols: cols, Rows: rows},
		Closing: proto.CaptureScreen{
			Cols: cols, Rows: rows,
			CursorX: 7, CursorVisible: true,
			Lines: []proto.CaptureRow{{
				Cells: []proto.CaptureCell{
					{Text: "m", Width: 1, HasText: true},
					{Text: "a", Width: 1, HasText: true},
					{Text: "k", Width: 1, HasText: true},
					{Text: "e", Width: 1, HasText: true},
				},
			}},
		},
	}
}

// captureSinkFor wires a sink the way the composition root does: the ledger
// arrives only when the store is real (a stub store leaves the sink
// unwired), and the binding memory is the sink's own.
func captureSinkFor(db content.ContentDB) *captureSink {
	s := newCaptureSink()
	if db != nil {
		s.set(db.Ledger())
	}
	return s
}

// TestTheCaptureHandlerStoresTheRecordAgainstItsEntry is the spine and the
// paired positive for every refusal below: a settled record whose nonce is
// bound lands as ONE body of the entry the bind named, and the body is what
// crossed the wire — byte for byte, not by length.
func TestTheCaptureHandlerStoresTheRecordAgainstItsEntry(t *testing.T) {
	db := captureTestStore(t, content.CriticalityRoutine, nil)
	entryID := recordOneCommand(t, db, "ls -la")
	sink := captureSinkFor(db)

	nonce := fenceHex(0xA1)
	sink.binds.Bind(nonce, entryID)
	raw, err := json.Marshal(aCaptureRecord(nonce))
	if err != nil {
		t.Fatalf("marshal the record: %v", err)
	}

	out, captureErr := sink.capture(context.Background(), raw)
	if captureErr != nil {
		t.Fatalf("capture: %v", captureErr)
	}
	result, ok := out.(proto.CaptureResult)
	if !ok || !result.Kept || result.Reason != "" {
		t.Fatalf("capture answered %+v (%T), want kept with no reason", out, out)
	}

	// The artifact id is the fence's own name, so a retried ask is the
	// store's idempotent replay and not a second body.
	art, err := db.Ledger().Artifact(context.Background(), captureArtifactID(nonce))
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	if art == nil {
		t.Fatal("the record was not stored against the entry")
	}
	if art.EntryID != entryID {
		t.Fatalf("the body hung on entry %q, want %q", art.EntryID, entryID)
	}
	if art.MediaType != content.MediaJSON || art.CaptureMethod != content.CaptureTerminalCells || art.CaptureVersion != 1 {
		t.Fatalf("provenance = %q/%q v%d, want application/json by terminal-cells v1",
			art.MediaType, art.CaptureMethod, art.CaptureVersion)
	}
	if art.TerminalCols == nil || *art.TerminalCols != 20 || art.TerminalRows == nil || *art.TerminalRows != 3 {
		t.Fatalf("geometry = %v x %v, want the closing screen's 20x3", art.TerminalCols, art.TerminalRows)
	}
	var body bytes.Buffer
	for _, chunk := range art.Chunks {
		body.Write(chunk)
	}
	if !bytes.Equal(body.Bytes(), raw) {
		t.Fatalf("the stored body is not the record that crossed:\n stored: %s\n sent:   %s", body.String(), raw)
	}

	// The replay: the same record asking again is the store's own no-op,
	// and the answer is still kept — a retried ask must not double the body.
	out, captureErr = sink.capture(context.Background(), raw)
	if captureErr != nil {
		t.Fatalf("replayed capture: %v", captureErr)
	}
	if result, ok = out.(proto.CaptureResult); !ok || !result.Kept {
		t.Fatalf("the replay answered %+v, want kept", out)
	}
	art, _ = db.Ledger().Artifact(context.Background(), captureArtifactID(nonce))
	if art.ChunkCount != 1 || art.ByteLen != int64(len(raw)) {
		t.Fatalf("after the replay the body is %d chunks/%d bytes, want 1/%d",
			art.ChunkCount, art.ByteLen, len(raw))
	}
}

// assertRefusal is the shape every refusal path asserts: the handler
// ANSWERS, the answer names why nothing was kept, and nothing was stored —
// checked at the artifact id a stored body would have created.
func assertRefusal(t *testing.T, db content.ContentDB, out any, err error, nonce, wantReason string) {
	t.Helper()
	if err != nil {
		t.Fatalf("capture: %v — refusing to store is an answer, never an error", err)
	}
	result, ok := out.(proto.CaptureResult)
	if !ok {
		t.Fatalf("capture answered %T, want proto.CaptureResult", out)
	}
	if result.Kept {
		t.Fatal("capture answered kept for a refusal that must keep nothing")
	}
	if result.Reason != wantReason {
		t.Fatalf("reason = %q, want %q", result.Reason, wantReason)
	}
	if db != nil {
		if art, _ := db.Ledger().Artifact(context.Background(), captureArtifactID(nonce)); art != nil {
			t.Fatalf("a %q refusal stored a body", wantReason)
		}
	}
}

// TestTheCaptureHandlerCarriesTheStoreSRefusals walks every refusal through
// the handler. Each is the PATH half of a criterion whose store half is
// already proven in internal/content, and each names the reason the frozen
// contract pins: outputOff, sensitive, critical — and noEntry, whose two
// causes (no store wired, the bind never landed) are both real states of
// this machine.
func TestTheCaptureHandlerCarriesTheStoreSRefusals(t *testing.T) {
	t.Run("output retention off", func(t *testing.T) {
		policy := content.NewPolicy()
		policy.SetOutputEnabled(false)
		db := captureTestStore(t, content.CriticalityRoutine, policy)
		entryID := recordOneCommand(t, db, "cat report.txt")
		sink := captureSinkFor(db)
		nonce := fenceHex(0xB2)
		sink.binds.Bind(nonce, entryID)
		raw, err := json.Marshal(aCaptureRecord(nonce))
		if err != nil {
			t.Fatalf("marshal the record: %v", err)
		}
		out, captureErr := sink.capture(context.Background(), raw)
		assertRefusal(t, db, out, captureErr, nonce, "outputOff")
	})

	t.Run("sensitive entry", func(t *testing.T) {
		db := captureTestStore(t, content.CriticalityRoutine, nil)
		// Nothing sets the sensitivity column in the product yet
		// (content's own note); the row is recorded sensitive directly,
		// the way the store's own tests do.
		entryID, err := db.Ledger().RecordCompleted(context.Background(), content.CompletedCommand{
			Client:      "capture-test",
			Env:         content.Environment{ID: "local", Kind: content.EnvLocal},
			Intent:      "aws configure",
			Source:      content.SourceUser,
			Status:      content.EntrySuccess,
			Sensitivity: content.SensitivitySensitive,
		})
		if err != nil {
			t.Fatalf("RecordCompleted(sensitive): %v", err)
		}
		sink := captureSinkFor(db)
		nonce := fenceHex(0xD6)
		sink.binds.Bind(nonce, entryID)
		raw, err := json.Marshal(aCaptureRecord(nonce))
		if err != nil {
			t.Fatalf("marshal the record: %v", err)
		}
		out, captureErr := sink.capture(context.Background(), raw)
		assertRefusal(t, db, out, captureErr, nonce, "sensitive")
	})

	t.Run("critical environment", func(t *testing.T) {
		db := captureTestStore(t, content.CriticalityCritical, nil)
		entryID := recordOneCommand(t, db, "kubectl apply -f prod.yaml")
		sink := captureSinkFor(db)
		nonce := fenceHex(0xD7)
		sink.binds.Bind(nonce, entryID)
		raw, err := json.Marshal(aCaptureRecord(nonce))
		if err != nil {
			t.Fatalf("marshal the record: %v", err)
		}
		out, captureErr := sink.capture(context.Background(), raw)
		assertRefusal(t, db, out, captureErr, nonce, "critical")
	})

	t.Run("no store wired", func(t *testing.T) {
		// The stub-store path: the composition root leaves the sink
		// unwired, no row was ever recorded, and noEntry is the honest
		// answer rather than a kept that would be a lie.
		sink := captureSinkFor(nil)
		raw, err := json.Marshal(aCaptureRecord(fenceHex(0xD4)))
		if err != nil {
			t.Fatalf("marshal the record: %v", err)
		}
		out, captureErr := sink.capture(context.Background(), raw)
		assertRefusal(t, nil, out, captureErr, fenceHex(0xD4), "noEntry")
	})

	t.Run("unbound nonce", func(t *testing.T) {
		db := captureTestStore(t, content.CriticalityRoutine, nil)
		recordOneCommand(t, db, "echo hi")
		sink := captureSinkFor(db)
		raw, err := json.Marshal(aCaptureRecord(fenceHex(0xD5)))
		if err != nil {
			t.Fatalf("marshal the record: %v", err)
		}
		out, captureErr := sink.capture(context.Background(), raw)
		assertRefusal(t, db, out, captureErr, fenceHex(0xD5), "noEntry")
	})
}

// TestTheCaptureHandlerRefusesAMalformedNonce closes the bad-params arm: a
// nonce that is not a fence is a request this coordinator will not answer as
// it stands, with the same code every other reverse op refuses by.
func TestTheCaptureHandlerRefusesAMalformedNonce(t *testing.T) {
	db := captureTestStore(t, content.CriticalityRoutine, nil)
	sink := captureSinkFor(db)
	rec := aCaptureRecord(fenceHex(0xD8))
	rec.Nonce = "NOT-A-FENCE"
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal the record: %v", err)
	}
	_, captureErr := sink.capture(context.Background(), raw)
	var refusal *proto.Refusal
	if !errors.As(captureErr, &refusal) {
		t.Fatalf("err = %v, want a proto.Refusal", captureErr)
	}
	if refusal.Code != proto.ErrCodeBadParams {
		t.Fatalf("refusal code = %q, want %q", refusal.Code, proto.ErrCodeBadParams)
	}
}

// ── the over-the-wire contract check ─────────────────────────────────────

// fakeHelperConn is a HelperConn backed by io.Pipe, whose peer runs the REAL
// helper host. It is the app-package twin of the fixture client_test drives,
// because the reverse direction's real socket path starts on THIS side: the
// client answers what a host asks.
type fakeHelperConn struct {
	stdin  io.WriteCloser
	stdout io.Reader
	exited chan struct{}
	code   int
	done   chan struct{}
}

func newFakeHelperConn(peer func(stdin io.Reader, stdout io.Writer) int) *fakeHelperConn {
	toPeerR, toPeerW := io.Pipe()
	fromPeerR, fromPeerW := io.Pipe()
	f := &fakeHelperConn{
		stdin: toPeerW, stdout: fromPeerR,
		exited: make(chan struct{}), done: make(chan struct{}),
	}
	go func() {
		f.code = peer(toPeerR, fromPeerW)
		_ = fromPeerW.Close()
		close(f.exited)
	}()
	return f
}

func (f *fakeHelperConn) Stdin() io.WriteCloser { return f.stdin }
func (f *fakeHelperConn) Stdout() io.Reader     { return f.stdout }
func (f *fakeHelperConn) Stderr() io.Reader     { return bytes.NewReader(nil) }
func (f *fakeHelperConn) Start(string) error    { return nil }
func (f *fakeHelperConn) Wait() (int, error) {
	<-f.exited
	return f.code, nil
}
func (f *fakeHelperConn) Done() <-chan struct{} { return f.done }
func (f *fakeHelperConn) LostErr() error        { return nil }
func (f *fakeHelperConn) Close() error          { return f.stdin.Close() }

// teeWriter records everything written through it, so the test can validate
// the frames the coordinator actually put on the wire.
type teeWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *teeWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

// teeReader records everything read through it, so the test can see the
// frames the coordinator WROTE — a reverse response travels client→host,
// the direction the host's own writer does not carry.
type teeReader struct {
	r io.Reader
	w *teeWriter
}

func (t *teeReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if n > 0 {
		_, _ = t.w.Write(p[:n])
	}
	return n, err
}

// loadCaptureResultSchema compiles the frozen result contract. The schema
// names no cross-file refs, so one file compiles alone.
func loadCaptureResultSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	f, err := os.Open("../../contracts/helper/session.capture.schema.json")
	if err != nil {
		t.Fatalf("open the capture contract: %v", err)
	}
	defer func() { _ = f.Close() }()
	doc, docErr := jsonschema.UnmarshalJSON(f)
	if docErr != nil {
		t.Fatalf("read the capture contract: %v", docErr)
	}
	const id = "https://nocx.local/contracts/helper/session.capture.schema.json"
	c := jsonschema.NewCompiler()
	if addErr := c.AddResource(id, doc); addErr != nil {
		t.Fatalf("register the capture contract: %v", addErr)
	}
	s, compileErr := c.Compile(id)
	if compileErr != nil {
		t.Fatalf("compile the capture contract: %v", compileErr)
	}
	return s
}

// TestTheCaptureResultOffTheRealSocketConformsToItsContract is criterion 6,
// the check a hand-built payload cannot make: the REAL registry (the one the
// composition root builds) answering a REAL host's ask across a REAL client
// connection, and the response frame the client wrote validating against the
// frozen schema.
func TestTheCaptureResultOffTheRealSocketConformsToItsContract(t *testing.T) {
	db := captureTestStore(t, content.CriticalityRoutine, nil)
	entryID := recordOneCommand(t, db, "make deploy")
	sink := captureSinkFor(db)
	nonce := fenceHex(0xC3)
	sink.binds.Bind(nonce, entryID)

	registry := helperReverseHandlers(nil, nil, &helperPrompt{log: logtest.Slog(t)}, nil, logtest.Slog(t), sink)

	tee := &teeWriter{}
	hostReady := make(chan *host.Host, 1)
	conn := newFakeHelperConn(func(stdin io.Reader, stdout io.Writer) int {
		h := host.New(&teeReader{r: stdin, w: tee}, io.MultiWriter(stdout, tee), "testhash", "instance-1", logtest.Slog(t))
		hostReady <- h
		if err := h.Serve(context.Background()); err != nil {
			return 1
		}
		return 0
	})
	c, err := helperclient.Dial(context.Background(), helperclient.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: 5 * time.Second,
		Reverse: registry,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	h := <-hostReady
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out proto.CaptureResult
	if err := h.Ask(ctx, proto.ServiceSession, proto.OpCapture, aCaptureRecord(nonce), &out); err != nil {
		t.Fatalf("the capture ask did not reach its answer: %v", err)
	}
	if !out.Kept {
		t.Fatalf("the coordinator answered kept=false reason=%q over the wire, want kept", out.Reason)
	}

	// What the CLIENT wrote, not what the handler returned: every response
	// frame the tee captured is decoded and the capture answer among them
	// must satisfy the contract AND be the answer the asker read.
	tee.mu.Lock()
	wire := append([]byte(nil), tee.buf.Bytes()...)
	tee.mu.Unlock()
	saw := false
	dec := proto.NewDecoder(func(ty proto.FrameType, _, _ uint32, payload []byte) {
		if ty != proto.TypeResponse {
			return
		}
		var resp proto.Response
		if err := json.Unmarshal(payload, &resp); err != nil || resp.Result == nil {
			return
		}
		var result proto.CaptureResult
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			return
		}
		saw = true
		schema := loadCaptureResultSchema(t)
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(resp.Result))
		if err != nil {
			t.Fatalf("read the wire result: %v", err)
		}
		if err := schema.Validate(doc); err != nil {
			t.Fatalf("the capture result off the socket does not satisfy its contract:\n%v\n\npayload was:\n%s",
				err, resp.Result)
		}
		if result.Kept != out.Kept || result.Reason != out.Reason {
			t.Fatalf("the wire carried %+v but the asker read %+v", result, out)
		}
	}, func(int) {})
	if err := dec.Feed(wire); err != nil {
		t.Fatalf("decode the recorded wire: %v", err)
	}
	if !saw {
		t.Fatal("no capture answer was found on the recorded wire")
	}
}
