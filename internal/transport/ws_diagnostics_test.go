package transport

// app.diagnostics — the contract, the record, and the one property that makes
// it useful: it answers while the control plane is saturated
// (nocx-3an1s.1.8).
//
// The tests here drive the REAL socket. The saturation case holds every lane
// permit with real (blocked) opens driven through the real session opener, then
// asks a method that needs the lane and must be refused, and then asks
// app.diagnostics and must NOT be refused. That is the whole design in one
// test: a diagnostics method behind the same admission it diagnoses is a
// method that is missing exactly when it is needed.

import (
	"context"
	"encoding/json"
	"errors"
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
		WithControlLaneCapacity(2),
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

// THE TEST THE OWNER ASKED FOR: the control plane is saturated, and Copy
// diagnostics still answers — with the holders that saturated it.
func TestAppDiagnostics_AnswersWhileTheLaneIsSaturatedAndNamesTheHolders(t *testing.T) {
	const laneCapacity = 2
	blocker := newBlockingHelperOpener()
	rec := diagnostics.NewRecorder()
	ws, sl, stop := newDiagnosticsWSServer(t, rec, blocker)
	defer stop()
	t.Cleanup(func() { close(blocker.release) })

	for i := 0; i < laneCapacity; i++ {
		conn := connectWS(t, ws)
		sendCall(t, conn, "open", map[string]any{
			// A destination carrying every kind of secret the text must not
			// repeat: a user, a host, a path. The label is the only name that
			// may appear.
			"cols": 80, "rows": 24, "kind": "ssh",
			"profileId": "ssh:alice@db-7.internal.example:/home/alice/.ssh/id_ed25519",
		}, 1)
	}
	waittest.WaitFor(t, "every lane permit to be held by a pending open", func() bool {
		return len(rec.Snapshot().Live) == laneCapacity
	})

	// A method that needs the lane: the session gate is free, the lane is not.
	probe := connectWS(t, ws)
	resp := snippetCall(t, probe, "sessions.live", map[string]any{}, 7)
	if resp.Error == nil || resp.Error.Code != SaturationErrorCode {
		t.Fatalf("sessions.live answered %+v, want the saturation refusal while the lane is held", resp)
	}
	data, ok := resp.Error.Data.(map[string]any)
	if !ok || data["scope"] != "control" {
		t.Fatalf("refusal data = %#v, want scope control", resp.Error.Data)
	}

	// The line the owner's 300 MB log did not have.
	if !logtest.WaitForSlog(sl, 2*time.Second, func(r logtest.Record) bool {
		if r.Message != "control action refused" {
			return false
		}
		scope, _ := recordAttr(r, "scope")
		holders, _ := recordAttr(r, "holders")
		return scope == "control" && strings.Contains(holders, "open(")
	}) {
		t.Fatal("no shipped-level line recorded the refusal with its holders")
	}

	// AND THE DIAGNOSTICS METHOD ITSELF, on the same saturated server.
	out := snippetCall(t, connectWS(t, ws), "app.diagnostics", map[string]any{}, 9)
	if out.Error != nil {
		t.Fatalf("app.diagnostics was refused by the saturation it exists to report: %+v", out.Error)
	}
	var got diagnosticsResult
	if err := json.Unmarshal(out.Result, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, want := range []string{
		"stage=dial", "scope=control", "method=sessions.live", "holders=open(",
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

// A working open whose PUBLISH failed must read as working. The owner's fresh
// server did exactly this: the agent refused to sign for a hardware-backed key,
// the bundle did not land, and the pane still worked. That must not look like
// the stuck case.
func TestAppDiagnostics_AWorkingOpenWithAFailedPublishIsNotAStuckOpen(t *testing.T) {
	rec := diagnostics.NewRecorder()
	ctx, open := rec.Begin(context.Background(), diagnostics.OpenInfo{Kind: "ssh", Key: "ssh:publish"})
	diagnostics.Stage(ctx, diagnostics.StagePublish)(nil)
	diagnostics.NotePublish(ctx, "failed:connection-failed")
	open.Finish(nil)

	text := diagnostics.Render(rec.Snapshot(), diagnostics.Facts{ServerStarted: time.Now()})
	if !strings.Contains(text, "outcome=opened") || !strings.Contains(text, "publish=failed:connection-failed") {
		t.Fatalf("a working open with a failed publish did not read as working:\n%s", text)
	}
}

// failingHelperOpener refuses the open outright, which is the path that used
// to say `failed to open session` with no request id.
type failingHelperOpener struct{}

func (failingHelperOpener) OpenHosted(context.Context, session.Config, string) (HostedSessionOpen, bool, error) {
	return HostedSessionOpen{}, true, errors.New("the far side refused the pane")
}

func TestAFailedOpenLineCarriesItsRequestAndTrace(t *testing.T) {
	ws, sl, stop := newDiagnosticsWSServer(t, diagnostics.NewRecorder(), failingHelperOpener{})
	defer stop()
	conn := connectWS(t, ws)
	resp := snippetCall(t, conn, "open", map[string]any{
		"cols": 80, "rows": 24, "kind": "ssh", "profileId": "ssh:failed",
	}, 5)
	if resp.Error == nil {
		t.Fatal("the failing open answered success")
	}
	if !logtest.WaitForSlog(sl, 2*time.Second, func(r logtest.Record) bool {
		if r.Message != "failed to open session" {
			return false
		}
		rid, _ := recordAttr(r, "request_id")
		trace, _ := recordAttr(r, "trace_id")
		return rid != "" && len(trace) == 32
	}) {
		t.Fatal("the failed-open line carried no request and trace id")
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
