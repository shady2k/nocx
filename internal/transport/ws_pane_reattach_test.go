package transport

// A dropped SSH connection comes back INTO THE SAME SHELL (nocx-oalo1.7.2,
// design §5.4, C16).
//
// WHAT A USER CAN DO THAT THEY COULD NOT BEFORE, and what these tests watch:
// run something on a host whose far helper holds the shell, lose the network,
// and be typing in the SAME process afterwards — no divider, no second shell —
// while a helper that cannot be asked costs a wait and never a shell beside a
// live one.
//
// THE TRANSPORT'S HALF IS THE DECISION, and that is what is exercised here: the
// coordinator's half (inventory, attach, adopt) is behind PaneShellReattacher
// and has its own tests in internal/app. So the double answers the same three
// things the real one does, and what is asserted is what the transport does
// with each: take the pane's shell back without ending the old one, wait, or
// replace.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/panedriver"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/waittest"
)

const (
	reattachPane  = "pane-reattach-0000-0000-000000000001"
	reattachShell = session.ID("77777777777777777777777777777771")
	replacementID = session.ID("77777777777777777777777777777772")
)

// reattachHostedOpener is the far-helper opener: every open it answers is a
// helper-held shell, which is what makes the pane one a reattach may ask about.
type reattachHostedOpener struct {
	reg    *session.Reg
	driver *panedriver.Driver

	mu      sync.Mutex
	calls   int
	chanels map[session.ID]*exitEndableChannel
}

func (o *reattachHostedOpener) OpenHosted(ctx context.Context, cfg session.Config, _ string) (HostedSessionOpen, bool, error) {
	o.mu.Lock()
	o.calls++
	n := o.calls
	id := reattachShell
	if n > 1 {
		id = replacementID
	}
	ch := newExitEndableChannel()
	if o.chanels == nil {
		o.chanels = make(map[session.ID]*exitEndableChannel)
	}
	o.chanels[id] = ch
	o.mu.Unlock()
	generation := o.driver.Bind(cfg.PaneID, "conn-1")
	BindPaneConnection(ctx, generation)
	s, err := o.reg.Adopt(ctx, cfg, id, ch)
	if err == nil {
		o.driver.AdoptSession(cfg.PaneID, "conn-1", string(id), "instance", generation)
	}
	return HostedSessionOpen{
		Session: s, Host: cfg.Host, Account: "alice",
		// Both are what makes this a FAR helper's shell rather than this
		// machine's: the generation names the id space and the command is the
		// binary on that host.
		Generation: "gen-far", HelperCommand: "/home/alice/.nocx/helper",
	}, true, err
}

func (o *reattachHostedOpener) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.calls
}

func (o *reattachHostedOpener) channel(t *testing.T, sid session.ID) *exitEndableChannel {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	ch := o.chanels[sid]
	if ch == nil {
		t.Fatalf("no channel was made for session %s", sid)
	}
	return ch
}

type reattachAnswer int

const (
	answerTaken reattachAnswer = iota
	answerAbsent
	answerUnavailable
	// answerTakenRecorded takes the shell back through the REAL half —
	// ReadoptHostedSession — so the receiver starts at the recording's end.
	answerTakenRecorded
)

// reattachDouble is the coordinator's half: three answers, counted, with the
// spec the transport asked about.
type reattachDouble struct {
	reg    *session.Reg
	driver *panedriver.Driver
	// ws is the transport the RECORDED answer adopts through: the real
	// re-attachment half, which installs the receiver, the pump and the
	// recorder at the offset this machine's recording ends at.
	ws *WSServer
	// chanels keeps the adopted channels so a test can feed the shell's own
	// output into the resumed stream.
	chanels map[session.ID]*readoptChannel
	// replayed is what the far end still has to print when the recorded
	// answer takes the shell back.
	replayed []byte
	mu       sync.Mutex
	mode     reattachAnswer

	asked atomic.Int32
	specs []OpenSpec
}

func (d *reattachDouble) set(mode reattachAnswer) {
	d.mu.Lock()
	d.mode = mode
	d.mu.Unlock()
}

func (d *reattachDouble) ReattachPaneShell(ctx context.Context, spec OpenSpec, cfg session.Config) (PaneShellReattach, bool, error) {
	d.asked.Add(1)
	d.mu.Lock()
	d.specs = append(d.specs, spec)
	mode := d.mode
	d.mu.Unlock()
	switch mode {
	case answerAbsent:
		return PaneShellReattach{}, false, nil
	case answerUnavailable:
		return PaneShellReattach{}, false, errors.New("the helper did not answer")
	}
	// THE REATTACHER BINDS THE PANE'S RESTORED CONNECTION, exactly as an open
	// does: the generation it answers is the one pane.opened carries.
	BindPaneConnection(ctx, d.driver.Bind(cfg.PaneID, "conn-1"))
	if mode == answerTakenRecorded {
		return d.takeRecorded(ctx, spec, cfg)
	}
	s, err := d.reg.Adopt(ctx, cfg, spec.Reattach.Session, newExitEndableChannel())
	if err != nil {
		return PaneShellReattach{}, false, err
	}
	return PaneShellReattach{
		Session: s, Config: cfg,
		Hosted: HostedSessionOpen{
			Session: s, Host: cfg.Host, Account: "alice",
			Generation: "gen-far", HelperCommand: "/home/alice/.nocx/helper",
		},
	}, true, nil
}

// takeRecorded adopts through the transport's own re-attachment half, the way
// the app's reattacher does, and answers with what it produced.
func (d *reattachDouble) takeRecorded(ctx context.Context, spec OpenSpec, cfg session.Config) (PaneShellReattach, bool, error) {
	if d.ws == nil {
		return PaneShellReattach{}, false, errors.New("the recorded answer needs a transport to adopt through")
	}
	sid := spec.Reattach.Session
	d.mu.Lock()
	replayed := d.replayed
	d.mu.Unlock()
	ch := newReadoptChannel(replayed)
	d.mu.Lock()
	if d.chanels == nil {
		d.chanels = make(map[session.ID]*readoptChannel)
	}
	d.chanels[sid] = ch
	d.mu.Unlock()
	var taken HostedSessionOpen
	err := d.ws.ReadoptHostedSession(ctx, sid, func(ctx context.Context, _ uint64) (HostedSessionOpen, error) {
		s, adoptErr := d.reg.Adopt(ctx, cfg, sid, ch)
		if adoptErr != nil {
			return HostedSessionOpen{}, adoptErr
		}
		taken = HostedSessionOpen{
			Session: s, Host: cfg.Host, Account: "alice",
			Generation: "gen-far", HelperCommand: "/home/alice/.nocx/helper",
		}
		return taken, nil
	})
	if err != nil {
		return PaneShellReattach{}, false, err
	}
	return PaneShellReattach{Session: taken.Session, Config: cfg, Hosted: taken}, true, nil
}

// setReplayed is what the far end prints once the recorded answer takes the
// shell back.
func (d *reattachDouble) setReplayed(data []byte) {
	d.mu.Lock()
	d.replayed = data
	d.mu.Unlock()
}

func (d *reattachDouble) lastSpec(t *testing.T) OpenSpec {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.specs) == 0 {
		t.Fatal("the reattacher was never asked")
	}
	return d.specs[len(d.specs)-1]
}

type reattachEnv struct {
	ws      *WSServer
	reg     *session.Reg
	driver  *panedriver.Driver
	opener  *reattachHostedOpener
	double  *reattachDouble
	socket  *websocket.Conn
	wc      *wsConn
	state   *connState
	params  openParams
	binding uint64
}

func newReattachEnv(t *testing.T, serverOpts ...WSServerOption) *reattachEnv {
	t.Helper()
	ctx, logger := logtest.New(t)
	reg := session.New(logger, nil)
	driver := panedriver.New("instance", logger)
	opener := &reattachHostedOpener{reg: reg, driver: driver}
	double := &reattachDouble{reg: reg, driver: driver}
	serverOpts = append([]WSServerOption{
		WithPaneConnectionPolicy(driver),
		WithHelperSessionOpener(opener),
		WithProfileResolver(&fakeResolver{resolveFn: func(string) (string, *ssh.ConnectConfig, error) {
			return "remote.example", &ssh.ConnectConfig{User: "alice"}, nil
		}}),
	}, serverOpts...)
	ws := NewWSServer(logger, reg, serverOpts...)
	double.ws = ws
	ws.SetPaneShellReattacher(double)
	if err := ws.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	socket := connectWS(t, ws)
	t.Cleanup(func() { _ = socket.Close() })
	wc := soleWSConn(t, ws)
	params := openParams{PaneID: reattachPane, Kind: "ssh", ProfileID: "saved", Cols: 80, Rows: 24}
	state := newConnState()
	// The supervisor's binding, which is what a reconnect press carries.
	binding := driver.Bind(reattachPane, "conn-1")
	ws.bindPane(reattachPane, wc, state, params)
	ws.paneRecordOf(reattachPane).bindingGeneration = binding
	return &reattachEnv{
		ws: ws, reg: reg, driver: driver, opener: opener, double: double,
		socket: socket, wc: wc, state: state, params: params, binding: binding,
	}
}

// openPane adopts the pane's first (held) shell through the ordinary open
// path, which is what records the shell on the pane.
func (e *reattachEnv) openPane(t *testing.T) session.Session {
	t.Helper()
	h := openHandlers{opener: e.ws.opener, sess: e.ws}
	opened, err := h.openInto(context.Background(), e.wc, e.state, e.params, func(openResult) error { return nil })
	if err != nil {
		t.Fatalf("the pane's first open: %v", err)
	}
	sess, err := e.reg.Get(session.ID(opened.SessionID))
	if err != nil {
		t.Fatalf("the pane's first shell is not in the registry: %v", err)
	}
	// The open binds the pane's connection through the opener, so the binding a
	// press carries is the one that open produced.
	e.binding = e.ws.paneRecordOf(reattachPane).bindingGeneration
	return sess
}

// drop takes the shell's channel away the way a lost transport does — a
// failure the session layer reads as a LOSS, never as the shell's own exit —
// and waits until the registry has let go of it exactly as monitorExit does.
func (e *reattachEnv) drop(t *testing.T, sid session.ID) {
	t.Helper()
	e.opener.channel(t, sid).exitAt(errors.New("helper carrier lost"))
	waittest.WaitFor(t, "the dropped shell left the registry", func() bool {
		_, err := e.reg.Get(sid)
		return err != nil
	})
}

// assertPaneOpenedContract validates the RAW notification the socket carried
// against its schema (AGENTS.md rule 5): a test that validates a payload it
// built itself proves the struct is well-formed, not that the server sends it.
func assertPaneOpenedContract(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("pane.opened is not a JSON document: %v", err)
	}
	if err := loadSchema(t, "pane.opened.schema.json").Validate(doc); err != nil {
		t.Fatalf("the raw pane.opened notification does not conform to its contract: %v\n%s", err, raw)
	}
}

func TestReconnectPaneTakesTheSameShellBackWithoutASecondOne(t *testing.T) {
	env := newReattachEnv(t)
	sess := env.openPane(t)
	before := sess.ID()
	env.drop(t, before)
	env.double.set(answerTaken)

	if err := env.ws.ReconnectPane(context.Background(), reattachPane, env.binding); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if got := env.opener.count(); got != 1 {
		t.Fatalf("the opener spawned %d shells, want 1: a reattach must not open one", got)
	}
	spec := env.double.lastSpec(t)
	if spec.Reattach == nil || spec.Reattach.Session != before {
		t.Fatalf("the reattach asked about %v, want the pane's shell %q", spec.Reattach, before)
	}
	if spec.Reattach.Generation != "gen-far" {
		t.Fatalf("the reattach asked about generation %q, want the one that qualifies the shell's id space", spec.Reattach.Generation)
	}
	if spec.PaneID != reattachPane || spec.ProfileID != "saved" {
		t.Fatalf("the reattach asked with the wrong route: %+v", spec)
	}
	back, err := env.reg.Get(before)
	if err != nil {
		t.Fatalf("the same shell is not in the registry after the reconnect: %v", err)
	}
	if back.ID() != before {
		t.Fatalf("reconnect adopted %q, want the same shell %q", back.ID(), before)
	}
	var fact paneOpened
	raw := readNotification(t, env.socket, "pane.opened", wantWithin)
	assertPaneOpenedContract(t, raw)
	if err := json.Unmarshal(raw, &fact); err != nil {
		t.Fatalf("decode pane.opened: %v", err)
	}
	if fact.NewShell {
		t.Fatal("a reattached shell reported newShell true: the renderer would draw a divider over a shell that never ended")
	}
	// EXPLICIT, not merely false in Go: the field is required by the schema and
	// a renderer that cannot tell "no divider" from "nobody said" draws one.
	if !bytes.Contains(raw, []byte(`"newShell":false`)) {
		t.Fatalf("the reattachment did not send an explicit newShell false: %s", raw)
	}
	if fact.Open.SessionID != string(before) {
		t.Fatalf("pane.opened names %q, want the same shell %q", fact.Open.SessionID, before)
	}
}

func TestReconnectPaneKeepsWaitingWhenTheHelperCannotBeAsked(t *testing.T) {
	env := newReattachEnv(t)
	sess := env.openPane(t)
	env.drop(t, sess.ID())
	env.double.set(answerUnavailable)

	if err := env.ws.ReconnectPane(context.Background(), reattachPane, env.binding); err == nil {
		t.Fatal("a helper that could not be asked produced a replacement shell")
	}
	if got := env.opener.count(); got != 1 {
		t.Fatalf("the opener spawned %d shells, want 1: C16 forbids a second shell while the inventory is unknown", got)
	}
	if _, err := env.reg.Get(sess.ID()); err == nil {
		t.Fatal("a refused reconnect left a shell in the registry")
	}
}

func TestReconnectPaneOpensAReplacementOnlyAfterConfirmedAbsence(t *testing.T) {
	env := newReattachEnv(t)
	sess := env.openPane(t)
	env.drop(t, sess.ID())
	env.double.set(answerAbsent)

	if err := env.ws.ReconnectPane(context.Background(), reattachPane, env.binding); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if got := env.opener.count(); got != 2 {
		t.Fatalf("the opener spawned %d shells, want 2: a confirmed absence is the one answer that opens one", got)
	}
	var fact paneOpened
	raw := readNotification(t, env.socket, "pane.opened", wantWithin)
	assertPaneOpenedContract(t, raw)
	if err := json.Unmarshal(raw, &fact); err != nil {
		t.Fatalf("decode pane.opened: %v", err)
	}
	if !fact.NewShell {
		t.Fatal("a replacement shell reported newShell false: nothing would say the old shell may still be running")
	}
	if !bytes.Contains(raw, []byte(`"newShell":true`)) {
		t.Fatalf("the replacement did not send newShell explicitly: %s", raw)
	}
	if fact.Open.SessionID == string(sess.ID()) {
		t.Fatal("the replacement kept the old session id, so it is not a replacement")
	}
}

// TestAPaneWhoseTransportNeverDroppedNeverAsks is the paired normal case: a
// pane opened and left alone asks nothing, because the only caller of a
// reattach is a reconnect.
func TestAPaneWhoseTransportNeverDroppedNeverAsks(t *testing.T) {
	env := newReattachEnv(t)
	env.openPane(t)
	if got := env.double.asked.Load(); got != 0 {
		t.Fatalf("an undropped pane asked about a reattach %d time(s)", got)
	}
	if got := env.opener.count(); got != 1 {
		t.Fatalf("the opener spawned %d shells, want 1", got)
	}
}

// ═══════════════════════════════════════════════════════════════════════════

// A RE-ATTACHMENT'S STREAM CONTINUES WHERE ITS RECORDING ENDS (AD-9), and it
// has ONE recorder.
//
// The transport half of the reconnect is ReadoptHostedSession: it builds the
// receiver at the offset this machine's recording ends at, starts the read pump
// and starts the recorder. The open that follows must deliver from THAT offset —
// not from zero, which a ring with a nonzero base cannot serve (snapshot(0)
// needs a reset the ring never gives, so the subscriber's stream would stop
// before its first byte) — and it must not install a second recorder or a
// second read pump for a session that already has both.
func TestAReconnectionDeliversFromItsResumePointWithOneRecorder(t *testing.T) {
	const recorded = "the stretch this machine already recorded"
	const next = "and what the shell says now"
	rec := newFakeRecorder()
	env := newReattachEnv(t, WithSessionOutputRecorder(rec))
	sess := env.openPane(t)
	// The previous incarnation's recording of THIS session, as the store has it.
	if _, err := rec.Append(context.Background(), content.SessionOutputAppend{
		SessionID: string(sess.ID()), Offset: 0, Body: []byte(recorded),
	}); err != nil {
		t.Fatal(err)
	}
	env.drop(t, sess.ID())
	env.double.setReplayed([]byte(next))
	env.double.set(answerTakenRecorded)

	if err := env.ws.ReconnectPane(context.Background(), reattachPane, env.binding); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	var fact paneOpened
	raw := readNotification(t, env.socket, "pane.opened", wantWithin)
	assertPaneOpenedContract(t, raw)
	if err := json.Unmarshal(raw, &fact); err != nil {
		t.Fatalf("decode pane.opened: %v", err)
	}
	if fact.NewShell {
		t.Fatal("a re-attachment reported a new shell")
	}
	if fact.ReplayFrom != uint64(len(recorded)) {
		t.Fatalf("pane.opened resumes at %d, want %d — the offset the recording ends at", fact.ReplayFrom, len(recorded))
	}
	// THE BYTES REACH THE PANE, from that offset: a pump started at zero over a
	// ring based at len(recorded) delivers nothing at all.
	if got := readBinaryBytes(t, env.socket, len(next)); got != len(next) {
		t.Fatalf("the pane's socket received %d resumed byte(s), want %d", got, len(next))
	}
	// ONE RECORDER: a second one would append the same bytes a second time.
	waitFor(t, "the resumed bytes to be recorded once", func() bool {
		return string(rec.recorded()) == recorded+next
	})
	if got := string(rec.recorded()); got != recorded+next {
		t.Fatalf("recording = %q, want %q exactly once", got, recorded+next)
	}
}

// readBinaryBytes reads the socket's data frames until want bytes have arrived
// and answers how many did before its deadline.
func readBinaryBytes(t *testing.T, socket *websocket.Conn, want int) int {
	t.Helper()
	deadline := time.Now().Add(wantWithin)
	_ = socket.SetReadDeadline(deadline)
	got := 0
	for got < want {
		mt, raw, err := socket.ReadMessage()
		if err != nil {
			if time.Now().After(deadline) {
				return got
			}
			t.Fatalf("reading the resumed stream: %v", err)
		}
		if mt != websocket.BinaryMessage {
			continue
		}
		frame, err := DecodeFrame(raw)
		if err != nil || frame.MsgType != MsgTypeData {
			continue
		}
		got += len(frame.Payload)
	}
	return got
}

// AN OLD INCARNATION'S TEARDOWN CANNOT TAKE THE NEW RECEIVER (AD-9).
//
// A re-attachment adopts the same session id under a new epoch, and it can
// begin in the interval between the previous incarnation's monitorExit taking
// the registry entry and that monitor removing its receiver. The receiver is
// the claim every id-keyed teardown hangs off — the ring, the subscriber slot,
// the lanes, the files and git bindings — so a delete by ID alone would tear a
// live pane down from its predecessor's teardown.
func TestAnOldIncarnationsTeardownCannotTakeTheNewReceiver(t *testing.T) {
	env := newReattachEnv(t)
	old := env.openPane(t)
	oldRx := env.ws.getRx(old.ID())
	if oldRx == nil {
		t.Fatal("the first open left no receiver")
	}
	// The old monitor's first step: the registry entry is retired, which is
	// what makes the same id adoptable again.
	retired, finish, err := env.ws.registry.PrepareEndSession(old.ID())
	if err != nil {
		t.Fatalf("retire the old incarnation: %v", err)
	}
	if err := finish(); err != nil {
		t.Fatalf("end the old incarnation: %v", err)
	}
	// The re-attachment's first step: the same id adopted under a new epoch,
	// and the receiver taken for it.
	reborn, err := env.reg.Adopt(context.Background(), session.Config{Kind: session.KindRemote, Host: "remote.example"}, old.ID(), newExitEndableChannel())
	if err != nil {
		t.Fatalf("adopt the same shell back: %v", err)
	}
	if reborn.Identity().Epoch == retired.Identity().Epoch {
		t.Fatal("the re-attachment reused the old incarnation's epoch, so nothing names the new one")
	}
	if rx := env.ws.getOrCreateRx(reborn); rx == nil {
		t.Fatal("the new incarnation got no receiver")
	}

	// The old monitor now reaches its own removal. It must not claim the id.
	removed, present := env.ws.removeRxFor(old.ID(), retired.Identity())
	if removed != nil {
		t.Fatal("the old incarnation removed the new incarnation's receiver")
	}
	if !present {
		t.Fatal("the old incarnation's removal answered \"no receiver\", so a newer one was not seen as taking the id")
	}
	if env.ws.getRx(old.ID()) == nil {
		t.Fatal("the new incarnation's receiver was deleted by the old incarnation's teardown")
	}
	if !env.ws.getRx(old.ID()).sameIncarnation(reborn.Identity()) {
		t.Fatal("the receiver does not belong to the new incarnation")
	}
}
