package transport

// Client-host tests (nocx-uo1k6, design D3). The coordinator has no window,
// so every native-host capability is an ask of an attached client. What has
// to be true, and is asserted per capability rather than once for the group:
//
//   - with a client attached, each capability's ask reaches it and its answer
//     comes back;
//   - with NO client attached, each capability answers with its own typed
//     "no UI host attached" — never a hang, never a silent success, never a
//     value invented on the client's behalf;
//   - a client that disconnects mid-ask terminalizes it and leaves nothing
//     pending;
//   - which client serves an ask is ADR-0026 §16's rule, unchanged: the ask
//     reaches every attached client and the FIRST to answer consumes it;
//   - every outcome the far side can report — cancelled, failed, malformed —
//     has a test, and each is paired with the success it is the failure of.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/waittest"
)

// newHostServer is a started server with NO connection attached. The
// no-UI-host assertions need exactly that, and every other test dials into it
// explicitly so the recipient set is what the test says it is.
func newHostServer(t *testing.T) *WSServer {
	t.Helper()
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	return ws
}

// attachClient dials in AND waits until the server has registered the
// connection. The wait is not decoration: RequestHost snapshots the attached
// connections, so an ask issued in the window between the dial and the
// registration is answered "no UI host attached" — correctly, and not what a
// test about an attached client means.
func attachClient(t *testing.T, ws *WSServer) *websocket.Conn {
	t.Helper()
	want := len(ws.rendererConns()) + 1
	conn := connectWS(t, ws)
	waittest.WaitFor(t, "the server to register the client", func() bool {
		return len(ws.rendererConns()) >= want
	})
	return conn
}

// awaitHostRequest reads the next host.request off conn and returns its
// decoded params.
func awaitHostRequest(t *testing.T, conn *websocket.Conn) hostRequestWire {
	t.Helper()
	raw := readNotification(t, conn, "host.request", 5*time.Second)
	var p hostRequestWire
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("host.request decode: %v\nraw: %s", err, raw)
	}
	if p.RequestID == "" {
		t.Fatalf("host.request carried no requestId: %s", raw)
	}
	return p
}

// hostRequestWire is the test's own view of the notification — deliberately
// not the production struct, so a test decodes what the wire carries rather
// than agreeing with the sender by construction.
type hostRequestWire struct {
	RequestID  string `json:"requestId"`
	Capability string `json:"capability"`
	URL        string `json:"url"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	SessionID  string `json:"sessionId"`
	Count      *int   `json:"count"`
}

// askAsync issues one RequestHost on a goroutine and hands back its outcome.
func askAsync(ws *WSServer, ctx context.Context, ask HostAsk) <-chan hostOutcome {
	out := make(chan hostOutcome, 1)
	go func() {
		answer, err := ws.RequestHost(ctx, ask)
		out <- hostOutcome{answer: answer, err: err}
	}()
	return out
}

type hostOutcome struct {
	answer HostAnswer
	err    error
}

func settled(t *testing.T, out <-chan hostOutcome) hostOutcome {
	t.Helper()
	select {
	case o := <-out:
		return o
	case <-time.After(10 * time.Second):
		t.Fatal("RequestHost never settled")
		return hostOutcome{}
	}
}

// resolveHost answers a pending ask over the wire and asserts the server
// accepted the resolution.
func resolveHost(t *testing.T, conn *websocket.Conn, params map[string]any) {
	t.Helper()
	resp := jsonrpcCall(t, conn, "host.resolved", params)
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("host.resolved decode: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("host.resolved refused: %+v", env.Error)
	}
}

// hostCapabilityCases is every capability with the arguments it carries and
// the answer a client gives. One row per capability so every assertion below
// is per capability, not once for the group.
var hostCapabilityCases = []struct {
	name    string
	ask     HostAsk
	noHost  error
	wantURL string
	// answer is what the client sends back; wantPath is what RequestHost
	// must then report.
	answer   map[string]any
	wantPath string
}{
	{
		name:     "file picker",
		ask:      HostAsk{Capability: HostCapOpenFile},
		noHost:   ErrNoDialogHost,
		answer:   map[string]any{"outcome": "ok", "path": "/home/dev/id_ed25519"},
		wantPath: "/home/dev/id_ed25519",
	},
	{
		name:     "directory picker",
		ask:      HostAsk{Capability: HostCapOpenDirectory},
		noHost:   ErrNoDialogHost,
		answer:   map[string]any{"outcome": "ok", "path": "/home/dev/projects"},
		wantPath: "/home/dev/projects",
	},
	{
		name:    "open url",
		ask:     HostAsk{Capability: HostCapOpenURL, URL: "https://example.com/x"},
		noHost:  ErrNoURLHost,
		wantURL: "https://example.com/x",
		answer:  map[string]any{"outcome": "ok"},
	},
	{
		name:   "banner",
		ask:    HostAsk{Capability: HostCapBanner, Title: "done", Body: "the build finished", SessionID: "s-1"},
		noHost: ErrNoAttentionHost,
		answer: map[string]any{"outcome": "ok"},
	},
	{
		name:   "badge",
		ask:    HostAsk{Capability: HostCapBadge, Count: 3},
		noHost: ErrNoAttentionHost,
		answer: map[string]any{"outcome": "ok"},
	},
	{
		name:   "bounce",
		ask:    HostAsk{Capability: HostCapBounce},
		noHost: ErrNoAttentionHost,
		answer: map[string]any{"outcome": "ok"},
	},
	{
		name:   "focus window",
		ask:    HostAsk{Capability: HostCapFocusWindow},
		noHost: ErrNoWindowHost,
		answer: map[string]any{"outcome": "ok"},
	},
}

// TestClientHost_EachCapabilityReachesAnAttachedClient — the happy path, once
// per capability: the ask leaves the coordinator carrying that capability's
// arguments, the client answers, and the answer comes back to the caller.
func TestClientHost_EachCapabilityReachesAnAttachedClient(t *testing.T) {
	for _, tc := range hostCapabilityCases {
		t.Run(tc.name, func(t *testing.T) {
			ws := newHostServer(t)
			conn := attachClient(t, ws)
			defer conn.Close() //nolint:errcheck

			out := askAsync(ws, t.Context(), tc.ask)
			req := awaitHostRequest(t, conn)
			if req.Capability != string(tc.ask.Capability) {
				t.Fatalf("capability = %q, want %q", req.Capability, tc.ask.Capability)
			}
			if req.URL != tc.wantURL {
				t.Errorf("url = %q, want %q", req.URL, tc.wantURL)
			}
			if tc.ask.Capability == HostCapBanner {
				if req.Title != "done" || req.Body != "the build finished" || req.SessionID != "s-1" {
					t.Errorf("banner args = (%q,%q,%q), want (done, the build finished, s-1)",
						req.Title, req.Body, req.SessionID)
				}
			}
			if tc.ask.Capability == HostCapBadge {
				if req.Count == nil || *req.Count != 3 {
					t.Errorf("badge count = %v, want 3", req.Count)
				}
			} else if req.Count != nil {
				// A badge of zero clears the badge, so count cannot be
				// omitempty — which makes "sent only where it means
				// something" a property worth pinning.
				t.Errorf("count = %v on %s, want it absent", *req.Count, tc.ask.Capability)
			}

			answer := map[string]any{"requestId": req.RequestID}
			for k, v := range tc.answer {
				answer[k] = v
			}
			resolveHost(t, conn, answer)

			o := settled(t, out)
			if o.err != nil {
				t.Fatalf("RequestHost: %v", o.err)
			}
			if o.answer.Path != tc.wantPath {
				t.Errorf("path = %q, want %q", o.answer.Path, tc.wantPath)
			}
			if o.answer.Cancelled {
				t.Error("an ok outcome must not report cancelled")
			}
			if n := ws.broker.Pending(); n != 0 {
				t.Errorf("pending after the answer = %d, want 0", n)
			}
		})
	}
}

// TestClientHost_NoUIHostAnswersPerCapability — the regression this whole
// bead exists for, stated as the honest degrade. With no client attached each
// capability answers with ITS OWN error, immediately: never a hang, never a
// silent success, never a value invented on the client's behalf.
func TestClientHost_NoUIHostAnswersPerCapability(t *testing.T) {
	ws := newHostServer(t)
	waittest.WaitFor(t, "the server to have no client attached", func() bool {
		return len(ws.rendererConns()) == 0
	})
	for _, tc := range hostCapabilityCases {
		t.Run(tc.name, func(t *testing.T) {
			out := askAsync(ws, t.Context(), tc.ask)
			o := settled(t, out)
			if !errors.Is(o.err, tc.noHost) {
				t.Fatalf("error = %v, want %v", o.err, tc.noHost)
			}
			if !errors.Is(o.err, ErrNoUIHost) {
				t.Errorf("error = %v, want it to wrap ErrNoUIHost", o.err)
			}
			if o.answer != (HostAnswer{}) {
				t.Errorf("answer = %+v on a refused ask, want the zero value", o.answer)
			}
			if n := ws.broker.Pending(); n != 0 {
				t.Errorf("pending after the refusal = %d, want 0", n)
			}
		})
	}
}

// TestClientHost_DisconnectMidAskCancelsIt — a client that receives an ask
// and dies without answering can never answer it. The ask terminalizes rather
// than waiting out a timeout that, for a picker, does not exist at all, and
// nothing is left pending.
func TestClientHost_DisconnectMidAskCancelsIt(t *testing.T) {
	ws := newHostServer(t)
	conn := attachClient(t, ws)

	out := askAsync(ws, t.Context(), HostAsk{Capability: HostCapOpenFile})
	_ = awaitHostRequest(t, conn)
	_ = conn.Close()

	o := settled(t, out)
	if !errors.Is(o.err, ErrRequestDisconnected) {
		t.Fatalf("error = %v, want ErrRequestDisconnected", o.err)
	}
	waittest.WaitFor(t, "the pending ask to be cleared", func() bool {
		return ws.broker.Pending() == 0
	})
}

// TestClientHost_FirstConsumerWins — which client serves an ask is ADR-0026
// §16's rule and is REUSED, not re-invented: the ask reaches every attached
// client, and `consume` guarantees exactly one answer. The second client's
// answer to the same id is the unknown-id case, and the caller is woken once.
func TestClientHost_FirstConsumerWins(t *testing.T) {
	ws := newHostServer(t)
	a := attachClient(t, ws)
	defer a.Close() //nolint:errcheck
	b := attachClient(t, ws)
	defer b.Close() //nolint:errcheck
	waittest.WaitFor(t, "both clients registered", func() bool {
		return len(ws.rendererConns()) == 2
	})

	out := askAsync(ws, t.Context(), HostAsk{Capability: HostCapOpenFile})
	reqA := awaitHostRequest(t, a)
	reqB := awaitHostRequest(t, b)
	if reqA.RequestID != reqB.RequestID {
		t.Fatalf("the two clients received different ids (%q, %q): an ask is ONE request broadcast",
			reqA.RequestID, reqB.RequestID)
	}

	resolveHost(t, a, map[string]any{"requestId": reqA.RequestID, "outcome": "ok", "path": "/from/a"})
	o := settled(t, out)
	if o.err != nil {
		t.Fatalf("RequestHost: %v", o.err)
	}
	if o.answer.Path != "/from/a" {
		t.Fatalf("path = %q, want the first answer /from/a", o.answer.Path)
	}

	// The second answer cannot wake anybody: the ask was consumed.
	resp := jsonrpcCall(t, b, "host.resolved", map[string]any{
		"requestId": reqB.RequestID, "outcome": "ok", "path": "/from/b",
	})
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error == nil || !strings.Contains(env.Error.Message, "Unknown request id") {
		t.Fatalf("the second answer = %+v, want 'Unknown request id'", env.Error)
	}
}

// TestClientHost_CancelledPickerIsAnOutcome — a person dismissing a picker is
// not a failure. The caller gets a result, and it can tell a dismissal from a
// completion without inspecting the path. Paired with the success above.
func TestClientHost_CancelledPickerIsAnOutcome(t *testing.T) {
	ws := newHostServer(t)
	conn := attachClient(t, ws)
	defer conn.Close() //nolint:errcheck

	out := askAsync(ws, t.Context(), HostAsk{Capability: HostCapOpenFile})
	req := awaitHostRequest(t, conn)
	resolveHost(t, conn, map[string]any{"requestId": req.RequestID, "outcome": "cancelled"})

	o := settled(t, out)
	if o.err != nil {
		t.Fatalf("a dismissal must not be an error, got %v", o.err)
	}
	if !o.answer.Cancelled {
		t.Error("answer.Cancelled = false, want true")
	}
	if o.answer.Path != "" {
		t.Errorf("path = %q on a dismissal, want empty", o.answer.Path)
	}
}

// TestClientHost_FailedOutcomeIsATypedError — the failure path of every
// external call this seam makes: the client could not perform the effect and
// says why. The caller receives an error naming the capability and carrying
// the client's sentence, never a zero value that reads like success.
func TestClientHost_FailedOutcomeIsATypedError(t *testing.T) {
	for _, tc := range hostCapabilityCases {
		t.Run(tc.name, func(t *testing.T) {
			ws := newHostServer(t)
			conn := attachClient(t, ws)
			defer conn.Close() //nolint:errcheck

			out := askAsync(ws, t.Context(), tc.ask)
			req := awaitHostRequest(t, conn)
			resolveHost(t, conn, map[string]any{
				"requestId": req.RequestID, "outcome": "failed", "error": "no D-Bus session",
			})

			o := settled(t, out)
			if o.err == nil {
				t.Fatal("a failed outcome returned no error")
			}
			if !strings.Contains(o.err.Error(), string(tc.ask.Capability)) {
				t.Errorf("error %q does not name the capability %q", o.err, tc.ask.Capability)
			}
			if !strings.Contains(o.err.Error(), "no D-Bus session") {
				t.Errorf("error %q does not carry the client's sentence", o.err)
			}
			if errors.Is(o.err, ErrNoUIHost) {
				t.Error("a client that answered is not a missing client")
			}
		})
	}
}

// TestClientHost_UnavailableOutcomeIsAbsenceNotFailure — the other half of
// the failure path above, and the distinction the notification centre turns
// on. A client that ANSWERS "I have no such surface" is not a client that
// tried and failed: a plain browser has no OS banner and never will, so the
// coordinator has nobody who can present one — exactly what ErrNoUIHost
// means. It therefore resolves to the SAME per-capability error as no client
// at all, which is what keeps notify's one exemption from the failure feed
// (a channel that does not exist is not a channel that lost a message)
// working without a second spelling of absence (AD-8).
func TestClientHost_UnavailableOutcomeIsAbsenceNotFailure(t *testing.T) {
	for _, tc := range hostCapabilityCases {
		t.Run(tc.name, func(t *testing.T) {
			ws := newHostServer(t)
			conn := attachClient(t, ws)
			defer conn.Close() //nolint:errcheck

			out := askAsync(ws, t.Context(), tc.ask)
			req := awaitHostRequest(t, conn)
			resolveHost(t, conn, map[string]any{
				"requestId": req.RequestID, "outcome": "unavailable",
				"error": "this client has no native host",
			})

			o := settled(t, out)
			if !errors.Is(o.err, tc.noHost) {
				t.Fatalf("error = %v, want %v", o.err, tc.noHost)
			}
			if !errors.Is(o.err, ErrNoUIHost) {
				t.Errorf("error = %v, want it to wrap ErrNoUIHost", o.err)
			}
			if !strings.Contains(o.err.Error(), "this client has no native host") {
				t.Errorf("error %q does not carry the client's sentence", o.err)
			}
			if o.answer != (HostAnswer{}) {
				t.Errorf("answer = %+v on an unavailable surface, want the zero value", o.answer)
			}
			if n := ws.broker.Pending(); n != 0 {
				t.Errorf("pending after the answer = %d, want 0", n)
			}
		})
	}
}

// TestClientHost_UnavailableWithoutASentenceIsStillAbsence — the sentence is
// the client's courtesy, not the meaning. An unavailable outcome that carries
// none still answers, and still answers absence.
func TestClientHost_UnavailableWithoutASentenceIsStillAbsence(t *testing.T) {
	ws := newHostServer(t)
	conn := attachClient(t, ws)
	defer conn.Close() //nolint:errcheck

	out := askAsync(ws, t.Context(), HostAsk{Capability: HostCapBanner, Title: "done"})
	req := awaitHostRequest(t, conn)
	resolveHost(t, conn, map[string]any{"requestId": req.RequestID, "outcome": "unavailable"})

	o := settled(t, out)
	if !errors.Is(o.err, ErrNoAttentionHost) {
		t.Fatalf("error = %v, want ErrNoAttentionHost", o.err)
	}
}

// TestClientHost_UnavailableCarriesNoPath — the shape check, stated the way
// the other outcomes state theirs: a surface that does not exist produced
// nothing, so a path on it is a broken client and the resolution is refused
// with the ask left pending for a corrected retry.
func TestClientHost_UnavailableCarriesNoPath(t *testing.T) {
	ws := newHostServer(t)
	conn := attachClient(t, ws)
	defer conn.Close() //nolint:errcheck

	out := askAsync(ws, t.Context(), HostAsk{Capability: HostCapOpenFile})
	req := awaitHostRequest(t, conn)
	resp := jsonrpcCall(t, conn, "host.resolved", map[string]any{
		"requestId": req.RequestID, "outcome": "unavailable", "path": "/home/dev/key",
	})
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("host.resolved decode: %v", err)
	}
	if env.Error == nil {
		t.Fatal("an unavailable outcome carrying a path was accepted")
	}
	if ws.broker.Pending() != 1 {
		t.Fatalf("pending after the refusal = %d, want the ask left for a retry", ws.broker.Pending())
	}
	resolveHost(t, conn, map[string]any{"requestId": req.RequestID, "outcome": "unavailable"})
	if o := settled(t, out); !errors.Is(o.err, ErrNoDialogHost) {
		t.Fatalf("error after the corrected retry = %v, want ErrNoDialogHost", o.err)
	}
}

// TestClientHost_MalformedResolutionLeavesTheAskPending — a refused
// resolution must not consume the ask: a broken client cannot turn a garbage
// outcome into a silent ask failure, and a corrected retry still answers.
func TestClientHost_MalformedResolutionLeavesTheAskPending(t *testing.T) {
	ws := newHostServer(t)
	conn := attachClient(t, ws)
	defer conn.Close() //nolint:errcheck

	out := askAsync(ws, t.Context(), HostAsk{Capability: HostCapOpenFile})
	req := awaitHostRequest(t, conn)

	for _, bad := range []map[string]any{
		{"requestId": req.RequestID, "outcome": "maybe"},
		{"requestId": req.RequestID, "outcome": "failed"},
		{"requestId": req.RequestID, "outcome": "ok", "error": "both"},
		{"requestId": req.RequestID, "outcome": "cancelled", "path": "/x"},
		{"requestId": req.RequestID, "outcome": "failed", "error": "why", "path": "/x"},
	} {
		resp := jsonrpcCall(t, conn, "host.resolved", bad)
		var env struct {
			Error *jsonrpcErrorObj `json:"error"`
		}
		if err := json.Unmarshal(resp, &env); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if env.Error == nil || env.Error.Code != -32602 {
			t.Fatalf("resolution %v = %+v, want -32602", bad, env.Error)
		}
		select {
		case o := <-out:
			t.Fatalf("a refused resolution settled the ask: %+v", o)
		default:
		}
	}

	// The corrected retry still resolves the ask that was never consumed.
	resolveHost(t, conn, map[string]any{"requestId": req.RequestID, "outcome": "ok", "path": "/ok"})
	o := settled(t, out)
	if o.err != nil || o.answer.Path != "/ok" {
		t.Fatalf("corrected retry = (%+v, %v), want /ok", o.answer, o.err)
	}
}

// TestClientHost_CallerCancellationTerminalizes — the request context is the
// caller's (ADR-0026 item 10). A caller that gives up leaves nothing pending,
// so a late answer cannot wake somebody who is gone.
func TestClientHost_CallerCancellationTerminalizes(t *testing.T) {
	ws := newHostServer(t)
	conn := attachClient(t, ws)
	defer conn.Close() //nolint:errcheck

	ctx, cancel := context.WithCancel(t.Context())
	out := askAsync(ws, ctx, HostAsk{Capability: HostCapOpenFile})
	_ = awaitHostRequest(t, conn)
	cancel()

	o := settled(t, out)
	if !errors.Is(o.err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", o.err)
	}
	waittest.WaitFor(t, "the pending ask to be dropped", func() bool {
		return ws.broker.Pending() == 0
	})
}

// TestClientHost_AlreadyCancelledCallerAsksNobody — a caller that is already
// terminal must not cause an effect: nobody should be shown a picker for a
// request nobody is waiting for.
func TestClientHost_AlreadyCancelledCallerAsksNobody(t *testing.T) {
	ws := newHostServer(t)
	conn := attachClient(t, ws)
	defer conn.Close() //nolint:errcheck

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ws.RequestHost(ctx, HostAsk{Capability: HostCapOpenFile}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if _, err := awaitNotification(conn, "host.request", 300*time.Millisecond); err == nil {
		t.Fatal("a cancelled caller still delivered a host.request")
	}
}

// TestClientHost_NoCapabilityIsRefused — a wiring bug, refused at the seam
// rather than sent to a client as an empty capability it cannot answer.
func TestClientHost_NoCapabilityIsRefused(t *testing.T) {
	ws := newHostServer(t)
	if _, err := ws.RequestHost(t.Context(), HostAsk{}); err == nil {
		t.Fatal("an ask with no capability was accepted")
	}
}

// ── the activation half ───────────────────────────────────────────────────

// recordingActivation records the clicks the transport hands it.
type recordingActivation struct {
	mu       sync.Mutex
	sessions []string
	done     chan struct{}
	once     sync.Once
}

func newRecordingActivation() *recordingActivation {
	return &recordingActivation{done: make(chan struct{})}
}

func (r *recordingActivation) Activated(_ context.Context, sessionID string) {
	r.mu.Lock()
	r.sessions = append(r.sessions, sessionID)
	r.mu.Unlock()
	r.once.Do(func() { close(r.done) })
}

func (r *recordingActivation) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.sessions...)
}

// TestClientHostAttentionActivated_ReachesTheSink — a person clicks a banner
// the client presented; the click reaches the coordinator, which is where the
// decision about it lives (AD-3). The client is told nothing back, because
// where the focus lands is not its business.
func TestClientHostAttentionActivated_ReachesTheSink(t *testing.T) {
	ws := newHostServer(t)
	sink := newRecordingActivation()
	ws.SetAttentionActivation(sink)
	conn := attachClient(t, ws)
	defer conn.Close() //nolint:errcheck

	if err := conn.WriteJSON(map[string]any{
		"jsonrpc": "2.0",
		"method":  "host.attentionActivated",
		"params":  map[string]any{"sessionId": "s-7"},
	}); err != nil {
		t.Fatalf("notify: %v", err)
	}
	select {
	case <-sink.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the click never reached the activation sink")
	}
	if got := sink.seen(); len(got) != 1 || got[0] != "s-7" {
		t.Fatalf("sink saw %v, want [s-7]", got)
	}
}

// TestClientHostAttentionActivated_WithNoSinkIsDropped — the failure path of
// the activation seam: a coordinator that never bound one logs the click and
// carries on. The socket must stay usable, which is what a subsequent call
// proves.
func TestClientHostAttentionActivated_WithNoSinkIsDropped(t *testing.T) {
	ws := newHostServer(t)
	conn := attachClient(t, ws)
	defer conn.Close() //nolint:errcheck

	if err := conn.WriteJSON(map[string]any{
		"jsonrpc": "2.0",
		"method":  "host.attentionActivated",
		"params":  map[string]any{"sessionId": "s-7"},
	}); err != nil {
		t.Fatalf("notify: %v", err)
	}
	// The socket is still answering: the drop cost nothing else.
	resp := jsonrpcCall(t, conn, "host.resolved", map[string]any{
		"requestId": "0123456789abcdef", "outcome": "ok",
	})
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error == nil || !strings.Contains(env.Error.Message, "Unknown request id") {
		t.Fatalf("after the dropped click the socket answered %+v, want the unknown-id refusal", env.Error)
	}
}

// TestClientHostAttentionActivated_RefusesAnEmptySession — the shape check on
// the ingress. A click that cannot name a session is not a click the
// coordinator can route.
func TestClientHostAttentionActivated_RefusesAnEmptySession(t *testing.T) {
	if msg := validateHostAttentionActivatedRaw(json.RawMessage(`{}`)); msg == "" {
		t.Error("an empty sessionId was accepted")
	}
	if msg := validateHostAttentionActivatedRaw(json.RawMessage(`{"sessionId":"s-1"}`)); msg != "" {
		t.Errorf("a valid activation was refused: %s", msg)
	}
}

// TestClientHost_UnknownResolutionIsRefused — a resolution for an id that was
// never minted cannot affect a later ask (ADR-0026 §16).
func TestClientHost_UnknownResolutionIsRefused(t *testing.T) {
	ws := newHostServer(t)
	conn := attachClient(t, ws)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "host.resolved", map[string]any{
		"requestId": "deadbeefdeadbeef", "outcome": "ok",
	})
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error == nil || !strings.Contains(env.Error.Message, "Unknown request id") {
		t.Fatalf("unknown id = %+v, want 'Unknown request id'", env.Error)
	}
}

// TestClientHostResolved_IsIngressCritical — the disposition, asserted rather
// than assumed. The asking task blocks on the answer while holding a lane
// permit, so an answer queued behind a full lane would deadlock the ask; the
// registration validator enforces the closed set in both directions, and this
// is the entry that says host.resolved belongs to it.
func TestClientHostResolved_IsIngressCritical(t *testing.T) {
	if _, ok := ingressCriticalMethods["host.resolved"]; !ok {
		t.Fatal("host.resolved is not in the ingress-critical set: an answer could queue behind the ask it unblocks")
	}
	if _, ok := ingressCriticalMethods["host.attentionActivated"]; ok {
		t.Fatal("host.attentionActivated must NOT be ingress-critical: acting on a click issues further asks and blocks")
	}
}
