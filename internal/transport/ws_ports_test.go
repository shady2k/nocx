package transport

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/discovery"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/nativeports"
	"github.com/shady2k/nocx/internal/portsprobe"
	"github.com/shady2k/nocx/internal/waittest"
)

func TestPortsStatus_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "ports.status.schema.json")

	// The two states that matter for the renderer: pending (no listeners,
	// no sample — the initial load) and available with mixed evidence.
	pending := portsStatusResult{
		PaneID: "pane-1",
		Discovery: portsDiscovery{
			State: "pending",
			// The wire path (portsDiscoveryFrom) emits these as [], never
			// null; the over-the-wire test asserts that on the real socket.
			Listeners:   []portsListener{},
			ProbesTried: []string{},
		},
		Forwards: []tunnelRecord{},
	}
	raw, err := json.Marshal(pending)
	if err != nil {
		t.Fatal(err)
	}
	validateJSON(t, schema, raw, "pending status")
	if string(raw) != `{"paneId":"pane-1","host":"","discovery":{"state":"pending","listeners":[],"probe":"","probesTried":[],"classification":"","stderr":"","lastSampleAt":null,"paused":false,"visible":false},"forwards":[]}` {
		t.Fatalf("pending status JSON mismatch:\n%s", raw)
	}

	known := "available"
	last := "2026-08-04T10:00:00Z"
	populated := portsStatusResult{
		PaneID: "pane-1",
		Host:   "host.example",
		Discovery: portsDiscovery{
			State:          known,
			Probe:          "ss",
			ProbesTried:    []string{"ss"},
			Classification: "",
			LastSampleAt:   &last,
			Listeners: []portsListener{
				{
					Family:  "ipv4",
					Address: "0.0.0.0",
					Port:    6768,
					Process: portsProcess{Evidence: "known", Name: "orca-ide", PID: 871},
				},
				{
					Family:  "ipv4",
					Address: "0.0.0.0",
					Port:    22,
					Process: portsProcess{Evidence: "permission-denied"},
				},
			},
		},
		Forwards: []tunnelRecord{
			{
				ID:            "fwd-1",
				Direction:     "local",
				RequestedBind: tunnelBind{Host: "127.0.0.1", Port: 6768},
				ActualBind:    tunnelBind{Host: "127.0.0.1", Port: 6768},
				Destination:   "host.example:6768",
				Scope:         "ports:pane-1",
				State:         "running",
			},
		},
	}
	raw, err = json.Marshal(populated)
	if err != nil {
		t.Fatal(err)
	}
	validateJSON(t, schema, raw, "populated status")
	if string(raw) != `{"paneId":"pane-1","host":"host.example","discovery":{"state":"available","listeners":[{"family":"ipv4","address":"0.0.0.0","port":6768,"process":{"evidence":"known","name":"orca-ide","pid":871}},{"family":"ipv4","address":"0.0.0.0","port":22,"process":{"evidence":"permission-denied","name":"","pid":0}}],"probe":"ss","probesTried":["ss"],"classification":"","stderr":"","lastSampleAt":"2026-08-04T10:00:00Z","paused":false,"visible":false},"forwards":[{"id":"fwd-1","direction":"local","requestedBind":{"host":"127.0.0.1","port":6768},"actualBind":{"host":"127.0.0.1","port":6768},"destination":"host.example:6768","scope":"ports:pane-1","caveat":"","state":"running","stopReason":null,"error":null}]}` {
		t.Fatalf("populated status JSON mismatch:\n%s", raw)
	}
}

func TestPortsSample_DTOConformsToContract(t *testing.T) {
	// ports.sample returns the same shape as ports.status; the check that
	// matters is that a nil forward ledger and a nil probe list marshal as
	// [] never null.
	schema := loadSchema(t, "ports.sample.schema.json")
	raw, err := json.Marshal(portsStatusResult{
		PaneID: "pane-1",
		Discovery: portsDiscovery{
			State:          "unavailable",
			Classification: "no probe tool usable on this host",
			Listeners:      []portsListener{},
			ProbesTried:    []string{},
		},
		Forwards: []tunnelRecord{},
	})
	if err != nil {
		t.Fatal(err)
	}
	validateJSON(t, schema, raw, "sample result")
	if string(raw) != `{"paneId":"pane-1","host":"","discovery":{"state":"unavailable","listeners":[],"probe":"","probesTried":[],"classification":"no probe tool usable on this host","stderr":"","lastSampleAt":null,"paused":false,"visible":false},"forwards":[]}` {
		t.Fatalf("sample JSON mismatch:\n%s", raw)
	}
}

func TestPortsPauseAndVisible_DTOConformToContract(t *testing.T) {
	for _, name := range []string{"ports.pause.schema.json", "ports.visible.schema.json"} {
		schema := loadSchema(t, name)
		raw, err := json.Marshal(struct{}{})
		if err != nil {
			t.Fatal(err)
		}
		validateJSON(t, schema, raw, name)
		if string(raw) != `{}` {
			t.Fatalf("%s result = %s, want {}", name, raw)
		}
	}
}

func TestPortsError_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "ports.error.schema.json")
	for _, reason := range []string{portsUnknownPane, portsHelperUnavailable} {
		validateJSON(t, schema, mustMarshal(portsErrorData{Reason: reason}), reason)
	}
}

func newPortsHarness(t *testing.T, provider func(log.Logger) discovery.LocalProvider) (*WSServer, *discovery.ConnectionScheduler) {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	sched := discovery.NewConnectionScheduler(nil, logger, provider)
	t.Cleanup(func() { _ = sched.Close() })
	ws := NewWSServer(logger, newRegWithStub(logger), WithDiscoveryScheduler(sched))
	if err := ws.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Stop(context.Background()) })
	return ws, sched
}

func openPortsPane(t *testing.T, conn *websocket.Conn, pane string) string {
	t.Helper()
	raw := jsonrpcCallWithID(t, conn, "open", openParams{PaneID: pane, Cols: 80, Rows: 24}, 1)
	var reply struct {
		Result openResult
		Error  *vaultRPCError
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Error != nil || reply.Result.SessionID == "" {
		t.Fatalf("open pane %s: %s", pane, raw)
	}
	return string(reply.Result.SessionID)
}

type portsLocalFixture struct{ release <-chan struct{} }

func (p portsLocalFixture) Sample(ctx context.Context) portsprobe.Sample {
	select {
	case <-p.release:
	case <-ctx.Done():
		return portsprobe.Sample{Canceled: true}
	}
	listeners, ok := portsprobe.ParseSS([]byte(`LISTEN 0 511 0.0.0.0:6768 0.0.0.0:* users:(("test",pid=871,fd=81))
LISTEN 0 128 [::]:22 [::]:*`))
	if !ok {
		panic("cannot parse port fixture")
	}
	return portsprobe.Sample{State: portsprobe.SampleState(listeners), Listeners: listeners, Probe: "ss"}
}

func TestPorts_OverTheWireConformsToContract(t *testing.T) {
	statusSchema := loadSchema(t, "ports.status.schema.json")
	errorSchema := loadSchema(t, "ports.error.schema.json")
	release := make(chan struct{})
	ws, _ := newPortsHarness(t, func(log.Logger) discovery.LocalProvider {
		return portsLocalFixture{release: release}
	})
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	const pane = "0190d3a0-1c2b-7def-8abc-0123456789ab"
	params := map[string]any{"paneId": pane}
	openPortsPane(t, conn, pane)

	pending := vaultCall(t, conn, "ports.status", params, 2)
	validateJSON(t, statusSchema, pending.Result, "pending ports status")
	var st portsStatusResult
	if err := json.Unmarshal(pending.Result, &st); err != nil {
		t.Fatal(err)
	}
	if st.PaneID != pane || st.Discovery.State != "pending" || st.Discovery.Listeners == nil || st.Forwards == nil {
		t.Fatalf("wrong pending result: %s", pending.Result)
	}
	close(release)
	sampled := vaultCall(t, conn, "ports.sample", params, 3)
	validateJSON(t, loadSchema(t, "ports.sample.schema.json"), sampled.Result, "ports sample")
	if err := json.Unmarshal(sampled.Result, &st); err != nil {
		t.Fatal(err)
	}
	if st.Discovery.State != "available" || len(st.Discovery.Listeners) != 2 ||
		st.Discovery.Listeners[0].Process.Evidence != "known" ||
		st.Discovery.Listeners[1].Process.Evidence != "permission-denied" {
		t.Fatalf("wrong sample: %s", sampled.Result)
	}

	paused := vaultCall(t, conn, "ports.pause", map[string]any{"paneId": pane, "paused": true}, 4)
	validateJSON(t, loadSchema(t, "ports.pause.schema.json"), paused.Result, "ports pause")
	visible := vaultCall(t, conn, "ports.visible", map[string]any{"paneId": pane, "visible": true}, 5)
	validateJSON(t, loadSchema(t, "ports.visible.schema.json"), visible.Result, "ports visible")
	flags := vaultCall(t, conn, "ports.status", params, 6)
	validateJSON(t, statusSchema, flags.Result, "ports cadence flags")
	if err := json.Unmarshal(flags.Result, &st); err != nil {
		t.Fatal(err)
	}
	if !st.Discovery.Paused || !st.Discovery.Visible {
		t.Fatalf("wrong cadence flags: %s", flags.Result)
	}

	other := connectWS(t, ws)
	t.Cleanup(func() { _ = other.Close() })
	for _, tc := range []struct {
		conn *websocket.Conn
		pane string
	}{
		{conn: conn, pane: "absent-pane"},
		{conn: other, pane: pane},
	} {
		reply := vaultCall(t, tc.conn, "ports.status", map[string]any{"paneId": tc.pane}, 7)
		if reply.Error == nil || reply.Error.Code != -32602 {
			t.Fatalf("expected invalid params for %q, got %+v", tc.pane, reply)
		}
		validateJSON(t, errorSchema, reply.Error.Data, "unknown-pane")
		if string(reply.Error.Data) != `{"reason":"unknown-pane"}` {
			t.Fatalf("unknown-pane detail = %s", reply.Error.Data)
		}
	}
	reply := vaultCall(t, conn, "ports.status", map[string]any{"profileId": pane}, 8)
	if reply.Error == nil || reply.Error.Code != -32602 {
		t.Fatalf("accepted profileId: %+v", reply)
	}
}

func TestPorts_LocalTarget_OverTheWire(t *testing.T) {
	ws, sched := newPortsHarness(t, func(logger log.Logger) discovery.LocalProvider {
		return nativeports.NewProvider(logger)
	})
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	const pane = "0190d3a0-1c2b-7def-8abc-0123456789ac"
	sid := openPortsPane(t, conn, pane)
	params := map[string]any{"paneId": pane}
	reply := vaultCall(t, conn, "ports.sample", params, 2)
	validateJSON(t, loadSchema(t, "ports.sample.schema.json"), reply.Result, "local sample")
	var st portsStatusResult
	if err := json.Unmarshal(reply.Result, &st); err != nil {
		t.Fatal(err)
	}
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if st.PaneID != pane || st.Host != host || st.Discovery.Probe == "" || len(st.Forwards) != 0 {
		t.Fatalf("wrong local target: %s", reply.Result)
	}
	_ = jsonrpcCallWithID(t, conn, "close", map[string]string{"sessionId": sid}, 3)
	waittest.WaitFor(t, "local target removed with its last session", func() bool {
		return sched.Status(discovery.LocalTargetID).Host == ""
	})
}
