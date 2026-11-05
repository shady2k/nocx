package app

// A far-side open that never answers must be locatable while it waits, and the
// channel it loses must name who closed it (nocx-3an1s.1.8).
//
// This is the owner's shape, driven through the REAL transport and the real
// helper registry/daemon: the far helper answers everything up to
// `session.spawn` and then says nothing. The test reads the parked stage from
// the shipped-level LOG and from `app.diagnostics` over the real socket, then
// cancels the open and reads who closed the lifecycle channel.

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/diagnostics"
	"github.com/shady2k/nocx/internal/helper/proto"
	helpersession "github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/waittest"
)

// blockingSpawner is a far side that accepts the spawn request and never
// answers it: the exact silence the open record exists to name.
type blockingSpawner struct {
	release chan struct{}
}

func (s *blockingSpawner) Spawn(helpersession.SpawnRequest) (helpersession.Process, error) {
	<-s.release
	return nil, errors.New("the far shell was released by the test")
}

func (s *blockingSpawner) SpawnSSH(context.Context, helpersession.SSHSpawnRequest) (helpersession.Process, error) {
	return nil, errors.New("this stand hosts panes on the far helper's local spawn")
}

// registryOpener is the transport's seam answered by the real registry,
// exactly as hostedOpeners' remote arm answers it.
type registryOpener struct{ reg *helperRegistry }

func (o registryOpener) OpenHosted(ctx context.Context, cfg session.Config, claim string) (transport.HostedSessionOpen, bool, error) {
	opened, hold, selected, err := o.reg.openHoldingLease(ctx, cfg, claim)
	hold.release()
	return opened, selected, err
}

type traceResolver struct{ host string }

func (r traceResolver) Resolve(string) (string, *ssh.ConnectConfig, error) {
	return r.host, &ssh.ConnectConfig{User: "tester", DesiredMode: string(profile.DesiredHelper)}, nil
}

type traceProvider struct{}

func (traceProvider) HelperStatus() diagnostics.HelperStatus {
	return diagnostics.HelperStatus{Known: true, Connected: true, ConnectedFor: time.Minute}
}
func (traceProvider) LogSize() (int64, bool) { return 4096, true }

func TestAFarOpenThatNeverAnswersNamesItsStageAndWhoClosedIt(t *testing.T) {
	sl := logtest.Slog(t)
	appLog := log.NewSlogAdapter(sl)
	spawner := &blockingSpawner{release: make(chan struct{})}
	svc := helpersession.New(helpersession.Options{
		Generation: proto.GenerationID(syntheticArtifactHash),
		Spawner:    spawner,
		SSHSpawner: spawner,
		Log:        sl,
	})
	t.Cleanup(svc.Close)
	provider := &fakeLaneProvider{peer: sharedHelperPeer(t, svc), home: t.TempDir()}
	store, installs := testConsentStores(t)
	_, reg := helperGitFactory(provider, stubArtifacts(t), store, installs, sl)
	endStandOnCleanup(t, provider, reg)
	// AFTER endStandOnCleanup, so it runs FIRST (LIFO): the blocked far spawn
	// must be released before the stand waits for its helper peer to end.
	t.Cleanup(func() { close(spawner.release) })
	reg.registry = session.New(appLog, nil)
	pub := lifecyclepub.New(lifecycle.New(lifecycle.Options{}))
	reg.lifecycle = pub
	reg.tools = &fakeFarTools{}

	rec := diagnostics.NewRecorder()
	ws := transport.NewWSServer(appLog, reg.registry,
		transport.WithProfileResolver(traceResolver{host: "host.example:22"}),
		transport.WithHelperSessionOpener(registryOpener{reg: reg}),
		transport.WithLifecyclePublisher(pub),
		transport.WithDiagnostics(rec, traceProvider{}),
	)
	if err := ws.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(context.Background()) })

	// THE RENDERER'S OWN PATH, not the backend one: `open` over the socket is
	// the frame the dispatcher puts the record on. The request stays in
	// flight; closing its connection is the cancellation the rollback answers.
	openConn := dialTraceWSServer(t, ws)
	sendOpen(t, openConn, "ssh:trace", 1)

	// THE LOG, at the shipped level: the stage the open entered and has not
	// left.
	if !logtest.WaitForSlog(sl, 10*time.Second, func(r logtest.Record) bool {
		if r.Message != "open stage" {
			return false
		}
		stage, _ := attrString(r, "stage")
		phase, _ := attrString(r, "phase")
		return stage == diagnostics.StageSpawn && phase == "enter"
	}) {
		t.Fatal("no shipped-level line named the stage the open is parked in")
	}
	// AND THE PENDING LINE, at its bounded cadence.
	if !logtest.WaitForSlog(sl, 10*time.Second, func(r logtest.Record) bool {
		if r.Message != "open pending" {
			return false
		}
		stage, _ := attrString(r, "stage")
		return stage == diagnostics.StageSpawn
	}) {
		t.Fatal("no pending line named the parked stage")
	}
	waitForStage(t, rec, diagnostics.StageSpawn)

	// AND Copy diagnostics, over the real socket, while the open is parked.
	conn := dialTraceWSServer(t, ws)
	text := callAppDiagnostics(t, conn)
	t.Logf("COPY DIAGNOSTICS (pending open):\n%s", text)
	if !strings.Contains(text, "stage=spawn") {
		t.Fatalf("Copy diagnostics does not name the parked stage:\n%s", text)
	}

	// THE CLOSE. Cancelling the open is the coordinator's own rollback, and it
	// must say so rather than leaving a bare cause=closed.
	_ = openConn.Close()
	if !logtest.WaitForSlog(sl, 5*time.Second, func(r logtest.Record) bool {
		if r.Message != "lifecycle channel lost" {
			return false
		}
		by, _ := attrString(r, "closed_by")
		return by == "open-rollback:spawn"
	}) {
		t.Fatal("the lost channel did not name who closed it")
	}
	waitForClosedBy(t, rec, "open-rollback:spawn")
	text = callAppDiagnostics(t, conn)
	for _, want := range []string{"outcome=failed", "lifecycle_closed_by=open-rollback:spawn"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the record lacks %q:\n%s", want, text)
		}
	}
}

func sendOpen(t *testing.T, conn *websocket.Conn, profileID string, id int) {
	t.Helper()
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "open",
		"params": map[string]any{"cols": 80, "rows": 24, "kind": "ssh", "profileId": profileID},
	})
	if err != nil {
		t.Fatalf("marshal open: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write open: %v", err)
	}
}

func dialTraceWSServer(t *testing.T, ws *transport.WSServer) *websocket.Conn {
	t.Helper()
	u := url.URL{Scheme: "ws", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(ws.Port())), Path: "/session"}
	d := websocket.Dialer{Subprotocols: []string{"nocx.token." + ws.Token()}}
	conn, _, err := d.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("dial test WSServer: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func callAppDiagnostics(t *testing.T, conn *websocket.Conn) string {
	t.Helper()
	req, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 41, "method": "app.diagnostics", "params": map[string]any{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write app.diagnostics: %v", err)
	}
	for {
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		_, raw, rerr := conn.ReadMessage()
		if rerr != nil {
			t.Fatalf("read app.diagnostics: %v", rerr)
		}
		var resp struct {
			ID    *json.RawMessage `json:"id"`
			Error *struct {
				Code int `json:"code"`
			} `json:"error"`
			Result *struct {
				Text string `json:"text"`
			} `json:"result"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			continue
		}
		if resp.ID == nil || string(*resp.ID) != "41" {
			continue
		}
		if resp.Error != nil {
			t.Fatalf("app.diagnostics refused: %+v", resp.Error)
		}
		if resp.Result == nil {
			t.Fatal("app.diagnostics answered no result")
		}
		return resp.Result.Text
	}
}

func attrString(r logtest.Record, key string) (string, bool) {
	for _, a := range r.Attrs {
		if a.Key == key {
			return a.Value.String(), true
		}
	}
	return "", false
}

func waitForStage(t *testing.T, rec *diagnostics.Recorder, stage string) {
	t.Helper()
	waittest.WaitFor(t, "the record to name the parked stage", func() bool {
		snap := rec.Snapshot()
		return len(snap.Live) == 1 && snap.Live[0].Current == stage
	})
}

func waitForClosedBy(t *testing.T, rec *diagnostics.Recorder, closer string) {
	t.Helper()
	waittest.WaitFor(t, "the record to name the channel's closer", func() bool {
		snap := rec.Snapshot()
		if len(snap.Recent) == 0 {
			return false
		}
		last := snap.Recent[len(snap.Recent)-1]
		return last.ClosedBy == closer
	})
}
