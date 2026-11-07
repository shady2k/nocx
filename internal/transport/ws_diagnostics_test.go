package transport

// app.diagnostics — the contract, the record, and the one property that makes
// it useful: it answers while other work is blocked (nocx-3an1s.1.8).
//
// The tests here drive the REAL socket. The blocked case holds a real
// (blocked) open driven through the real session opener, then asks
// app.diagnostics: it must answer, and it must name what is holding the
// resources it reports. A diagnostics method behind the same admission it
// diagnoses is a method that is missing exactly when it is needed.

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/diagnostics"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

type stubDiagnosticsProvider struct{}

func (stubDiagnosticsProvider) HelperStatus() diagnostics.HelperStatus {
	return diagnostics.HelperStatus{Known: true, Connected: true, ConnectedFor: 2 * time.Hour}
}
func (stubDiagnosticsProvider) LogSize() (int64, bool) { return 1234567, true }

// blockingHelperOpener holds every open that reaches it, in the Dial phase,
// until the test releases it. It is a far side that never answers, expressed at
// the seam the transport owns.
type blockingHelperOpener struct {
	entered chan struct{}
	release chan struct{}
}

func newBlockingHelperOpener() *blockingHelperOpener {
	return &blockingHelperOpener{entered: make(chan struct{}, 16), release: make(chan struct{})}
}

func (b *blockingHelperOpener) OpenHosted(ctx context.Context, _ session.Config, _ string) (HostedSessionOpen, bool, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
		return HostedSessionOpen{}, true, context.Canceled
	case <-ctx.Done():
		return HostedSessionOpen{}, true, ctx.Err()
	}
}

func newDiagnosticsWSServer(t *testing.T, rec *diagnostics.Recorder, opener HelperSessionOpener) (*WSServer, *slog.Logger, func()) {
	t.Helper()
	sl := logtest.Slog(t)
	logger := log.NewSlogAdapter(sl)
	opts := []WSServerOption{
		WithDiagnostics(rec, stubDiagnosticsProvider{}),
		WithProfileResolver(&openProfileResolver{host: "host.example:22"}),
	}
	if opener != nil {
		opts = append(opts, WithHelperSessionOpener(opener))
	}
	ws := NewWSServer(logger, newRegWithStub(logger), opts...)
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return ws, sl, func() { _ = ws.Stop(ctx) }
}

func recordAttr(r logtest.Record, key string) (string, bool) {
	for _, a := range r.Attrs {
		if a.Key == key {
			return a.Value.String(), true
		}
	}
	return "", false
}

func TestAppDiagnostics_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "app.diagnostics.schema.json")
	for name, dto := range map[string]diagnosticsResult{
		"a record":            {Text: "nocx diagnostics\n"},
		"the unavailable one": {Text: "nocx diagnostics\nrecord: unavailable in this build\n"},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(dto)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "app.diagnostics result ("+name+")")
		})
	}
}

func TestAppDiagnostics_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "app.diagnostics.schema.json")
	rec := diagnostics.NewRecorder()
	ctx, open := rec.Begin(context.Background(), diagnostics.OpenInfo{Kind: "ssh", Key: "ssh:seed"})
	_ = diagnostics.Stage(ctx, diagnostics.StageSpawn)
	open.Finish(nil)
	ws, _, stop := newDiagnosticsWSServer(t, rec, nil)
	defer stop()

	resp := snippetCall(t, connectWS(t, ws), "app.diagnostics", map[string]any{}, 1)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "app.diagnostics result")
	var got diagnosticsResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, want := range []string{"nocx diagnostics", "outcome=opened", "stages: spawn", "helper: connected"} {
		if !strings.Contains(got.Text, want) {
			t.Fatalf("the text lacks %q:\n%s", want, got.Text)
		}
	}
}

// THE TEST THE OWNER ASKED FOR: work is blocked, and Copy diagnostics still
// answers — naming what is holding the resources that are occupied.
//
// Stage 8 has no capacity refusal to record, so the holders come from the
// LIVE admission views (`admissions:`) rather than from a refusal ring: the
// person's question is "what is stuck", and the answer is the same.
func TestAppDiagnostics_AnswersWhileWorkIsBlockedAndNamesTheHolders(t *testing.T) {
	blocker := newBlockingHelperOpener()
	rec := diagnostics.NewRecorder()
	ws, _, stop := newDiagnosticsWSServer(t, rec, blocker)
	defer stop()
	t.Cleanup(func() { close(blocker.release) })

	conn := connectWS(t, ws)
	sendCall(t, conn, "open", map[string]any{
		// A destination carrying every kind of secret the text must not
		// repeat: a user, a host, a path. The label is the only name that
		// may appear.
		"cols": 80, "rows": 24, "kind": "ssh",
		"profileId": "ssh:alice@db-7.internal.example:/home/alice/.ssh/id_ed25519",
	}, 1)
	// And a native picker left open: a capability that IS held for as long as
	// its adapter runs, which is exactly what the holders list is for.
	dlg := &blockingDialog{started: make(chan struct{}), release: make(chan struct{})}
	ws.SetDialogService(dlg)
	t.Cleanup(func() { close(dlg.release) })
	picker := connectWS(t, ws)
	sendCall(t, picker, "dialog.openFile", map[string]any{}, 2)
	select {
	case <-dlg.started:
	case <-time.After(5 * time.Second):
		t.Fatal("dialog adapter never invoked")
	}
	waittest.WaitFor(t, "the open to be in flight", func() bool {
		return len(rec.Snapshot().Live) == 1
	})

	// The diagnostics method itself, while the open holds the config and
	// session plans: it is answered on the read loop and takes no credit, so
	// it is available at exactly the moment somebody needs it.
	out := snippetCall(t, connectWS(t, ws), "app.diagnostics", map[string]any{}, 9)
	if out.Error != nil {
		t.Fatalf("app.diagnostics was refused while work was blocked: %+v", out.Error)
	}
	var got diagnosticsResult
	if err := json.Unmarshal(out.Result, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, want := range []string{
		"stage=dial", "dialog held=1", "dialog.openFile(",
	} {
		if !strings.Contains(got.Text, want) {
			t.Fatalf("the text lacks %q:\n%s", want, got.Text)
		}
	}
	if strings.Contains(got.Text, "alice") || strings.Contains(got.Text, "db-7.internal.example") ||
		strings.Contains(got.Text, "/home/") || strings.Contains(got.Text, "id_ed25519") {
		t.Fatalf("the text named a destination:\n%s", got.Text)
	}
}

// sendCall writes one request and does not read its answer: the open tests need
// the request to stay in flight.
func sendCall(t *testing.T, conn *websocket.Conn, method string, params any, id int) {
	t.Helper()
	req, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write %s: %v", method, err)
	}
}
