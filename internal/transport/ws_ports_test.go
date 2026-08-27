package transport

// ports.* contract tests (nocx-wzc4.2). The DTO cases pin the Go wire
// structs against contracts/ports.*.schema.json — field tags, the
// never-null listeners/forwards slices, enum spelling. The over-the-wire
// case drives the REAL handlers through the REAL socket with a REAL
// scheduler over a scripted connector, and validates the result bytes: a
// test that validates a payload it built itself proves the struct is
// well-formed, not that the server sends it (AGENTS.md rule 5).

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/discovery"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/nativeports"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/waittest"
)

// ---------------------------------------------------------------------------
// Scripted connector — the scheduler's lease surface, standing in for
// ssh.RealClient. The Exec responses are real probe-shaped frames; the
// parsing happens in the real detector.
// ---------------------------------------------------------------------------

type portsFakeConn struct {
	mu     sync.Mutex
	done   chan struct{}
	closed bool
	resps  []*ssh.ExecResult
}

func (f *portsFakeConn) Exec(_ context.Context, _ string) (*ssh.ExecResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, ssh.ErrExecClosed
	}
	if len(f.resps) == 0 {
		return nil, ssh.ErrExecLost
	}
	r := f.resps[0]
	f.resps = f.resps[1:]
	return r, nil
}

func (f *portsFakeConn) Done() <-chan struct{} { return f.done }
func (f *portsFakeConn) LostErr() error        { return nil }
func (f *portsFakeConn) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

func (f *portsFakeConn) queue(resps ...*ssh.ExecResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resps = append(f.resps, resps...)
}

type portsFakeConnector struct {
	mu    sync.Mutex
	conns []*portsFakeConn
	resp  *ssh.ExecResult // queued into every fresh conn (the settle sample)
}

func (c *portsFakeConnector) DiscoveryConn(_ context.Context, _ string, _ ...ssh.ConnectOption) (ssh.DiscoveryConn, error) {
	f := &portsFakeConn{done: make(chan struct{})}
	c.mu.Lock()
	if c.resp != nil {
		f.resps = append(f.resps, c.resp)
	}
	c.conns = append(c.conns, f)
	c.mu.Unlock()
	return f, nil
}

func (c *portsFakeConnector) conn() *portsFakeConn {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.conns) == 0 {
		return nil
	}
	return c.conns[0]
}

// portsFramed wraps probe output in the fixed version sentinel the discovery
// package requires; a body without it is rejected whole.
func portsFramed(body string) *ssh.ExecResult {
	return &ssh.ExecResult{Stdout: []byte("NOCX-PD/1\n" + body + "\nNOCX-PD/1\n"), ExitStatus: 0}
}

// portsSSMixed is the measured shape: three rows with process evidence and
// six without — the "run as root to see owners" case.
const portsSSMixed = `LISTEN 0      4096   127.0.0.53%lo:53    0.0.0.0:*
LISTEN 0      511          0.0.0.0:6768  0.0.0.0:* users:(("orca-ide",pid=871,fd=81))
LISTEN 0      4096         0.0.0.0:5355  0.0.0.0:*
LISTEN 0      511        127.0.0.1:40721 0.0.0.0:* users:(("MainThread",pid=1184,fd=22))
LISTEN 0      128          0.0.0.0:22    0.0.0.0:*
LISTEN 0      4096            [::]:5355     [::]:*
LISTEN 0      128             [::]:22       [::]:*`

// ---------------------------------------------------------------------------
// DTO conformance
// ---------------------------------------------------------------------------

func TestPortsStatus_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "ports.status.schema.json")

	// The two states that matter for the renderer: pending (no listeners,
	// no sample — the initial load) and available with mixed evidence.
	pending := portsStatusResult{
		ProfileID: "ssh:p1:1",
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
	if string(raw) != `{"profileId":"ssh:p1:1","host":"","discovery":{"state":"pending","listeners":[],"probe":"","probesTried":[],"classification":"","stderr":"","lastSampleAt":null,"paused":false,"visible":false,"connLost":false},"forwards":[]}` {
		t.Fatalf("pending status JSON mismatch:\n%s", raw)
	}

	known := "available"
	last := "2026-08-04T10:00:00Z"
	populated := portsStatusResult{
		ProfileID: "ssh:p1:1",
		Host:      "host.example",
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
				Scope:         "ports:ssh:p1:1",
				State:         "running",
			},
		},
	}
	raw, err = json.Marshal(populated)
	if err != nil {
		t.Fatal(err)
	}
	validateJSON(t, schema, raw, "populated status")
	if string(raw) != `{"profileId":"ssh:p1:1","host":"host.example","discovery":{"state":"available","listeners":[{"family":"ipv4","address":"0.0.0.0","port":6768,"process":{"evidence":"known","name":"orca-ide","pid":871}},{"family":"ipv4","address":"0.0.0.0","port":22,"process":{"evidence":"permission-denied","name":"","pid":0}}],"probe":"ss","probesTried":["ss"],"classification":"","stderr":"","lastSampleAt":"2026-08-04T10:00:00Z","paused":false,"visible":false,"connLost":false},"forwards":[{"id":"fwd-1","direction":"local","requestedBind":{"host":"127.0.0.1","port":6768},"actualBind":{"host":"127.0.0.1","port":6768},"destination":"host.example:6768","scope":"ports:ssh:p1:1","caveat":"","state":"running","stopReason":null,"error":null}]}` {
		t.Fatalf("populated status JSON mismatch:\n%s", raw)
	}
}

func TestPortsSample_DTOConformsToContract(t *testing.T) {
	// ports.sample returns the same shape as ports.status; the check that
	// matters is that a nil forward ledger and a nil probe list marshal as
	// [] never null.
	schema := loadSchema(t, "ports.sample.schema.json")
	raw, err := json.Marshal(portsStatusResult{
		ProfileID: "ssh:p1:1",
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
	if string(raw) != `{"profileId":"ssh:p1:1","host":"","discovery":{"state":"unavailable","listeners":[],"probe":"","probesTried":[],"classification":"no probe tool usable on this host","stderr":"","lastSampleAt":null,"paused":false,"visible":false,"connLost":false},"forwards":[]}` {
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

// ---------------------------------------------------------------------------
// Over the wire
// ---------------------------------------------------------------------------

// portsRPCResult mirrors the JSON-RPC envelope; result is the handler's own
// bytes, which is what the over-the-wire rule validates.
type portsRPCResult struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *vaultRPCError  `json:"error,omitempty"`
}

func portsCall(t *testing.T, conn *websocket.Conn, method string, params map[string]any, id int) json.RawMessage {
	t.Helper()
	req, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write: %v", err)
	}
	for {
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var msg portsRPCResult
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.ID != id {
			continue
		}
		if msg.Error != nil {
			t.Fatalf("%s: JSON-RPC error %d: %s", method, msg.Error.Code, msg.Error.Message)
		}
		return msg.Result
	}
}

func newPortsHarness(t *testing.T, sched *discovery.Scheduler) (*WSServer, func()) {
	t.Helper()
	ws := NewWSServer(
		log.NewSlogAdapter(nil),
		newRegWithStub(log.NewSlogAdapter(nil)),
		WithDiscoveryScheduler(sched),
	)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return ws, func() { _ = ws.Stop(ctx) }
}

// The real handlers through the real socket, with a real scheduler over a
// scripted connector: the result bytes are the handler's own, and nothing
// here names a field, so nothing here can omit one (AGENTS.md rule 5).
func TestPorts_OverTheWireConformsToContract(t *testing.T) {
	statusSchema := loadSchema(t, "ports.status.schema.json")
	sampleSchema := loadSchema(t, "ports.sample.schema.json")
	pauseSchema := loadSchema(t, "ports.pause.schema.json")
	visibleSchema := loadSchema(t, "ports.visible.schema.json")

	connector := &portsFakeConnector{}
	sched := discovery.NewScheduler(
		connector, log.NewSlogAdapter(nil),
		discovery.WithSettleDelay(5*time.Millisecond),
		discovery.WithPromptDebounce(5*time.Millisecond),
		discovery.WithSampleInterval(0),
	)
	ws, stop := newPortsHarness(t, sched)
	defer stop()
	conn := connectWS(t, ws)

	// A profile with no connection yet: pending, EMPTY listeners and
	// forwards arrays — the honest pre-first-sample state.
	raw := portsCall(t, conn, "ports.status", map[string]any{"profileId": "ssh:p1:1"}, 1)
	validateJSON(t, statusSchema, raw, "ports.status pending")
	var st portsStatusResult
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if st.Discovery.State != "pending" {
		t.Fatalf("state = %q, want pending", st.Discovery.State)
	}
	if st.Discovery.Listeners == nil || st.Forwards == nil {
		t.Fatal("listeners/forwards must be [] on the wire, never null")
	}

	// The target comes up; the settle sample runs against the scripted
	// host and produces mixed evidence.
	connector.resp = portsFramed(portsSSMixed)
	sched.ConnectionUp("ssh:p1:1", "host.example", testPortsOption())
	waitPortsFor(t, "settle sample", func() bool {
		return sched.Status("ssh:p1:1").Sample.State == "available"
	})

	raw = portsCall(t, conn, "ports.status", map[string]any{"profileId": "ssh:p1:1"}, 2)
	validateJSON(t, statusSchema, raw, "ports.status settled")
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Discovery.Listeners) != 7 {
		t.Fatalf("listeners = %d, want 7", len(st.Discovery.Listeners))
	}
	if st.Host != "host.example" {
		t.Fatalf("host = %q, want host.example", st.Host)
	}
	denied := 0
	for _, l := range st.Discovery.Listeners {
		if l.Process.Evidence == "permission-denied" {
			denied++
		}
	}
	if denied != 5 {
		t.Fatalf("permission-denied rows = %d, want 5 (two rows carry process evidence)", denied)
	}

	// Retry: ports.sample returns the FRESH state, not the pre-retry one.
	connector.conn().queue(portsFramed(portsSSMixed))
	raw = portsCall(t, conn, "ports.sample", map[string]any{"profileId": "ssh:p1:1"}, 3)
	validateJSON(t, sampleSchema, raw, "ports.sample")
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if st.Discovery.State != "available" {
		t.Fatalf("state after sample = %q, want available", st.Discovery.State)
	}

	// Pause and visible are empty acknowledgements, and the next status
	// carries the flags.
	raw = portsCall(t, conn, "ports.pause", map[string]any{"profileId": "ssh:p1:1", "paused": true}, 4)
	validateJSON(t, pauseSchema, raw, "ports.pause")
	raw = portsCall(t, conn, "ports.visible", map[string]any{"profileId": "ssh:p1:1", "visible": true}, 5)
	validateJSON(t, visibleSchema, raw, "ports.visible")
	raw = portsCall(t, conn, "ports.status", map[string]any{"profileId": "ssh:p1:1"}, 6)
	validateJSON(t, statusSchema, raw, "ports.status after pause")
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if !st.Discovery.Paused || !st.Discovery.Visible {
		t.Fatalf("paused/visible flags not echoed: %+v", st.Discovery)
	}

	// An unknown profile is a valid status, never an error: pending.
	raw = portsCall(t, conn, "ports.status", map[string]any{"profileId": "ssh:never:1"}, 7)
	validateJSON(t, statusSchema, raw, "ports.status unknown profile")
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if st.Discovery.State != "pending" {
		t.Fatalf("unknown profile state = %q, want pending", st.Discovery.State)
	}

	// Missing profileId is invalid params.
	bad := portsCallParams(t, conn, "ports.status", map[string]any{}, 8)
	if bad == nil || bad.Error == nil || bad.Error.Code != -32602 {
		t.Fatalf("missing profileId: got %+v, want -32602", bad)
	}
}

// portsCallParams is portsCall for the error path: it returns the envelope
// instead of failing on a JSON-RPC error.
func portsCallParams(t *testing.T, conn *websocket.Conn, method string, params map[string]any, id int) *portsRPCResult {
	t.Helper()
	req, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write: %v", err)
	}
	for {
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var msg portsRPCResult
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.ID == id {
			return &msg
		}
	}
}

func waitPortsFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	waittest.WaitForTimeout(t, what, wantWithin, cond)
}

func testPortsOption() ssh.ConnectOption {
	return func(c *ssh.ConnectConfig) { c.User = "test" }
}

// TestPorts_LocalTarget_OverTheWire: the reserved "local" identity serves
// the machine's own ports through the real handlers — forwards stay empty,
// the host is the machine's name, and the sample comes from the real local
// probe, never the scripted SSH connector. Opening a local tab through the
// real socket is what creates the target; closing the last local tab tears
// it down while an SSH target keeps sampling (re-scopes both directions).
func TestPorts_LocalTarget_OverTheWire(t *testing.T) {
	statusSchema := loadSchema(t, "ports.status.schema.json")
	connector := &portsFakeConnector{}
	sched := discovery.NewScheduler(
		connector, log.NewSlogAdapter(nil),
		discovery.WithLocalProvider(func(l log.Logger) discovery.Provider {
			return nativeports.NewProvider(l)
		}),
		discovery.WithSettleDelay(5*time.Millisecond),
		discovery.WithPromptDebounce(5*time.Millisecond),
		discovery.WithSampleInterval(0),
	)
	ws, stop := newPortsHarness(t, sched)
	defer stop()
	conn := connectWS(t, ws)
	// Before any local tab: pending, like any target before its first
	// sample — never an error and never "no connection".
	raw := portsCall(t, conn, "ports.status", map[string]any{"profileId": discovery.LocalTargetID}, 1)
	validateJSON(t, statusSchema, raw, "ports.status local pending")
	var st portsStatusResult
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if st.Discovery.State != "pending" {
		t.Fatalf("local state before any tab = %q, want pending", st.Discovery.State)
	}

	// A local tab opens through the real socket (handleOpen's local branch
	// fires the hook): the settle sample probes the real machine.
	openResp := jsonrpcCallWithID(t, conn, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 2)
	var open struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	if err := json.Unmarshal(openResp, &open); err != nil {
		t.Fatal(err)
	}
	if open.Result.SessionID == "" {
		t.Fatal("open returned no sessionId")
	}

	waitPortsFor(t, "local settle sample", func() bool {
		st := sched.Status(discovery.LocalTargetID).Sample.State
		return st == "available" || st == "available-limited"
	})
	raw = portsCall(t, conn, "ports.status", map[string]any{"profileId": discovery.LocalTargetID}, 3)
	validateJSON(t, statusSchema, raw, "ports.status local settled")
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if st.ProfileID != discovery.LocalTargetID {
		t.Fatalf("profileId = %q, want %q", st.ProfileID, discovery.LocalTargetID)
	}
	if st.Discovery.Probe == "" {
		t.Fatal("local probe = \"\", want a real probe dialect (the ladder ran on this machine)")
	}
	if st.Host == "" {
		t.Fatal("local host empty, want the machine hostname")
	}
	if len(st.Forwards) != 0 {
		t.Fatalf("local forwards = %d, want 0 (nothing to forward from the machine you are on)", len(st.Forwards))
	}
	// The scripted SSH connector must never be touched by the local target.
	if got := len(connector.conns); got != 0 {
		t.Fatalf("ssh connector leases = %d, want 0 (local never dials)", got)
	}

	// Re-scope both directions: an SSH target on top of the local one...
	connector.resp = portsFramed(portsSSMixed)
	sched.ConnectionUp("ssh:p1:1", "host.example", testPortsOption())
	waitPortsFor(t, "ssh settle sample", func() bool {
		return sched.Status("ssh:p1:1").Sample.State == "available"
	})
	raw = portsCall(t, conn, "ports.status", map[string]any{"profileId": "ssh:p1:1"}, 4)
	validateJSON(t, statusSchema, raw, "ports.status ssh")
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if st.Host != "host.example" || len(st.Discovery.Listeners) != 7 {
		t.Fatalf("ssh status = host %q with %d listeners, want host.example with 7", st.Host, len(st.Discovery.Listeners))
	}
	if got := sched.Status(discovery.LocalTargetID).Sample.State; got != "available" && got != "available-limited" {
		t.Fatalf("local state while ssh tab is up = %q, want still sampling", got)
	}

	// ...and closing the local tab tears down the local target only.
	_ = jsonrpcCallWithID(t, conn, "close", map[string]string{"sessionId": open.Result.SessionID}, 5)
	waitPortsFor(t, "local target down", func() bool {
		return sched.Status(discovery.LocalTargetID).Sample.State == "pending"
	})
	if st := sched.Status("ssh:p1:1"); st.Sample.State != "available" {
		t.Fatalf("ssh state after local close = %q, want still available", st.Sample.State)
	}
}

// The window the over-the-wire test above jumps straight over: it asks once
// with no target at all, then waits for "available" before asking again, so
// the state BETWEEN ConnectionUp and the first sample was never validated.
// That is exactly where State went out as the zero string — accepted by no
// enum in ports.status.schema.json, matched by no arm of the renderer's
// switch, and drawn as a section with a heading and nothing under it
// (owner, 2026-08-04).
func TestPortsStatus_AfterConnectionUpBeforeSettle_OverTheWireConformsToContract(t *testing.T) {
	statusSchema := loadSchema(t, "ports.status.schema.json")

	connector := &portsFakeConnector{}
	sched := discovery.NewScheduler(
		connector, log.NewSlogAdapter(nil),
		// Long enough that the settle sample cannot land during the call:
		// the point of the test is the state before it does.
		discovery.WithSettleDelay(time.Hour),
		discovery.WithPromptDebounce(time.Hour),
		discovery.WithSampleInterval(0),
	)
	ws, stop := newPortsHarness(t, sched)
	defer stop()
	conn := connectWS(t, ws)

	sched.ConnectionUp("ssh:p1:1", "host.example", testPortsOption())

	raw := portsCall(t, conn, "ports.status", map[string]any{"profileId": "ssh:p1:1"}, 1)
	validateJSON(t, statusSchema, raw, "ports.status after ConnectionUp, before settle")

	var st portsStatusResult
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if st.Discovery.State != "pending" {
		t.Fatalf("state = %q, want pending — a connection that has come up and not yet sampled is pending, not blank", st.Discovery.State)
	}
}
