package app

// A build on a remote host survives nocx being replaced (nocx-k6p18.30).
//
// WHAT A USER CAN DO THAT THEY COULD NOT BEFORE, and it is what these tests
// watch: start something long on a host where the helper is installed, quit
// nocx entirely, start it again, and the SAME shell is there — same host
// session id, same process, one shell and not two, listed in the registry
// `sessions.live` is served from and keyed to the pane it was the pipe of.
//
// THE HELPER OUTLIVES THE LANE HERE TOO, which is the whole point of the
// harness. `realHelperPeer` builds a fresh session service per exec lane, so a
// second lane meets a daemon that never spawned anything — a model of a
// coordinator restart in which the HOST also restarted, which is not the case
// under test. `sharedHelperPeer` registers ONE service on every lane instead,
// which is what the bridge actually does: the sessions live in a process on the
// host, and what rides the ssh channel is a connection to it.
//
// AND THE NEGATIVES ARE PER FAILURE MODE, for the reason nocx-k6p18.5 states:
// `absent` deletes a recording and closes a block, `unknown` costs a week of
// disk, and re-adoption adds four new ways to fail — an unreachable host, a
// generation that is gone, a connection whose stored route no longer leads
// there, and another nocx holding the keyboard. Not one of them may answer
// `absent`, and each is asserted separately rather than in a loop over one
// mechanism.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/git/hostsvc"
	localgit "github.com/shady2k/nocx/internal/git/local"
	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	helpersession "github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/vault"
)

// sharedHelperPeer serves every exec lane from ONE session service, which is
// what the bridge does on a real host: the daemon holds the sessions and the
// channel is a connection to it. Without this a "restart" would meet a daemon
// that had never spawned anything, and the tests below would be watching a
// different scenario than the one they name.
func sharedHelperPeer(svc *helpersession.Service) func(in io.Reader, out io.Writer) int {
	contentHash := syntheticArtifactHash
	return func(in io.Reader, out io.Writer) int {
		h := host.New(in, out, contentHash, "instance-1", discardLogger())
		h.Register(hostsvc.New(localgit.NewFactory()))
		h.Register(svc)
		// The CONNECTION is bound to the service and not the other way round,
		// which is cmd/nocx-helper's own accept loop verbatim: the sessions
		// outlive the connection, and that is the whole of D1. A harness that
		// forgot this line would be modelling a daemon per connection, which
		// is the thing this bead exists because we do not have.
		release := svc.Bind(h)
		defer release()
		if err := h.Serve(context.Background()); err != nil {
			return 1
		}
		return 0
	}
}

func sharedHelperService() *helpersession.Service {
	return helpersession.New(helpersession.Options{
		Generation: proto.GenerationID(syntheticArtifactHash),
		Spawner:    &scriptedSpawner{},
		Log:        discardLogger(),
	})
}

// scriptedSpawner is the helper's Spawner with nothing behind it: a process
// that keeps its pipe open and produces nothing until it is closed. The
// question these tests ask is whether a shell that is STILL THERE is found
// again, so what the shell prints is not part of it — and a real /bin/sh here
// would make the tests depend on this machine's shell, which is exactly the
// environmental coupling internal/app already pays for elsewhere.
type scriptedSpawner struct {
	mu      sync.Mutex
	spawned int
	procs   []*idleProcess
}

func (s *scriptedSpawner) Spawn(req helpersession.SpawnRequest) (helpersession.Process, error) {
	s.mu.Lock()
	s.spawned++
	pid := 4000 + s.spawned
	p := &idleProcess{done: make(chan struct{}), pid: pid, id: req.SessionID}
	s.procs = append(s.procs, p)
	s.mu.Unlock()
	return p, nil
}

// exitWith ends the one process this spawner started, with a status the helper
// will record — the shell finishing while nobody is watching.
func (s *scriptedSpawner) exitWith(t *testing.T, code int) {
	t.Helper()
	s.mu.Lock()
	procs := append([]*idleProcess(nil), s.procs...)
	s.mu.Unlock()
	if len(procs) != 1 {
		t.Fatalf("%d processes were spawned, want exactly 1", len(procs))
	}
	procs[0].exit(code)
}

type idleProcess struct {
	done chan struct{}
	pid  int
	id   string
	once sync.Once

	mu   sync.Mutex
	wait error
}

func (p *idleProcess) Read([]byte) (int, error) { <-p.done; return 0, io.EOF }
func (p *idleProcess) Write(b []byte) (int, error) {
	return len(b), nil
}

func (p *idleProcess) Close() error {
	p.once.Do(func() { close(p.done) })
	return nil
}

// exit ends the process with a status, in the order internal/pty guarantees:
// the wait result is written BEFORE Done closes, which is what lets the
// helper's watchExit observe it without further synchronisation.
func (p *idleProcess) exit(code int) {
	p.mu.Lock()
	p.wait = &scriptedExit{code: code}
	p.mu.Unlock()
	p.once.Do(func() { close(p.done) })
}

func (p *idleProcess) Resize(context.Context, uint16, uint16, uint16, uint16) error { return nil }
func (p *idleProcess) Done() <-chan struct{}                                        { return p.done }

func (p *idleProcess) WaitErr() (error, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.wait == nil {
		return nil, false
	}
	return p.wait, true
}

// scriptedExit is os/exec.ExitError's shape as far as anything here reads it.
type scriptedExit struct{ code int }

func (e *scriptedExit) Error() string                       { return "exit status " + strconv.Itoa(e.code) }
func (e *scriptedExit) ExitCode() int                       { return e.code }
func (p *idleProcess) Pid() int                             { return p.pid }
func (p *idleProcess) Shell() string                        { return "/bin/scripted" }
func (p *idleProcess) ForegroundProcessGroup() (int, error) { return p.pid, nil }

// stubRoutes is the connection resolver as a double: one saved connection, or
// a failure to resolve it.
type stubRoutes struct {
	host string
	cfg  *ssh.ConnectConfig
	err  error
	// asked records the profile ids it was consulted about, so a test can
	// prove a route was NOT looked up as well as that it was.
	mu    sync.Mutex
	asked []string
}

func (s *stubRoutes) Resolve(profileID string) (string, *ssh.ConnectConfig, error) {
	s.mu.Lock()
	s.asked = append(s.asked, profileID)
	s.mu.Unlock()
	if s.err != nil {
		return "", nil, s.err
	}
	return s.host, s.cfg, nil
}

func (s *stubRoutes) consulted() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.asked)
}

// stubAdopter stands in for the transport half. It supplies the resume offset
// the recording would have given and remembers what came back, so the app-side
// decision is observable without a WebSocket server.
type stubAdopter struct {
	from uint64
	// refuse is what the transport's own half fails with before it asks for a
	// re-attachment at all — the "server is already shutting down" case, and
	// the only failure the real one has after its receiver is reserved. It
	// refuses BEFORE rather than after deliberately: the real order is what
	// makes the rollback a removeRx and nothing else, and a double that failed
	// afterwards would be asserting against a state production cannot reach.
	refuse error
	// refuseAfter is the transport refusing a session the registry has ALREADY
	// been given — the shape its own id-mismatch guard produces. It is the only
	// state in which the app side has something to undo, so it is driven
	// explicitly rather than left to be reasoned about.
	refuseAfter error

	mu       sync.Mutex
	adopted  []session.ID
	attempts int
	lastErr  error
	open     transport.HostedSessionOpen
}

func (a *stubAdopter) ReadoptHostedSession(ctx context.Context, sid session.ID, reattach transport.HostedSessionReattach) error {
	a.mu.Lock()
	a.attempts++
	a.mu.Unlock()
	if a.refuse != nil {
		return a.refuse
	}
	hosted, err := reattach(ctx, a.from)
	if err != nil {
		a.mu.Lock()
		a.lastErr = err
		a.mu.Unlock()
		return err
	}
	if a.refuseAfter != nil {
		return a.refuseAfter
	}
	a.mu.Lock()
	a.adopted = append(a.adopted, hosted.Session.ID())
	a.open = hosted
	a.mu.Unlock()
	// The transport's own half starts the lifecycle bridge here (see
	// ws_readopt.go). The double does it too, because a re-adoption whose
	// bridge never starts is a channel that never delivers a frame — and a
	// test that skipped it would be watching a different mechanism.
	if hosted.StartLifecycle != nil {
		hosted.StartLifecycle()
	}
	return nil
}

// lastOpen is what the re-attachment handed the transport: the lane to
// register and the sentence the integration axis will say. It is read rather
// than re-derived, because those two fields are the whole of whether a
// returned pane says anything about itself.
func (a *stubAdopter) lastOpen() transport.HostedSessionOpen {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.open
}

func (a *stubAdopter) adoptedIDs() []session.ID {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]session.ID(nil), a.adopted...)
}

func (a *stubAdopter) failure() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastErr
}

// coordinator is one nocx process's helper half: its own session registry and
// its own helper registry over a shared lane provider. Building a second one
// against the same provider IS the coordinator replacement.
type coordinator struct {
	reg     *helperRegistry
	sess    *session.Reg
	consent *consent.Store
}

func newCoordinator(t *testing.T, provider *fakeLaneProvider) *coordinator {
	t.Helper()
	source := stubArtifacts(t)
	store, installs := testConsentStores(t)
	_, reg := helperGitFactory(provider, source, store, installs, discardLogger())
	sess := session.New(log.NewSlogAdapter(discardLogger()), nil)
	reg.registry = sess
	return &coordinator{reg: reg, sess: sess, consent: store}
}

// helperConnection is a saved connection whose destination mode is an explicit
// helper — the person said "use the helper on this machine", which is the
// consent for the binary (§4.3). The fake probe lease reports no host-key
// fingerprint, so an `auto` connection here would resolve to the consent ASK
// rather than to a helper, and there would be nothing to take back.
func helperConnection(user string) *ssh.ConnectConfig {
	return &ssh.ConnectConfig{User: user, DesiredMode: string(profile.DesiredHelper)}
}

// quit is this coordinator's process ending: every helper channel it holds is
// closed. On a real host that is what releases the session's write lease —
// nocx-k6p18.16 binds the lease to the CONNECTION that acquired it — which is
// what lets a replacement take the keyboard without arbitration. A test that
// skipped it would be modelling two nocx running at once, which is a different
// scenario with a different right answer (see the test that asserts it).
func (c *coordinator) quit() {
	c.reg.mu.Lock()
	helpers := make([]*hostHelper, 0, len(c.reg.hosts))
	for _, h := range c.reg.hosts {
		helpers = append(helpers, h)
	}
	c.reg.mu.Unlock()
	for _, h := range helpers {
		h.mu.Lock()
		h.closeLocked()
		h.mu.Unlock()
	}
}

// openHostedFixture opens one helper-hosted session and returns the durable
// binding the transport would have written for it. It is the SHIPPED opener —
// helperRegistry.OpenHosted — so what is taken back below is what the product
// actually creates.
func openHostedFixture(t *testing.T, c *coordinator, paneID string) content.PendingSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	opened, selected, err := c.reg.OpenHosted(ctx, session.Config{
		Kind: session.KindRemote, Host: "host.example", Cwd: t.TempDir(),
		PaneID: paneID, ProfileID: "profile-1",
		Remote: helperConnection("u"),
	})
	if err != nil {
		t.Fatalf("open a helper-hosted session: %v", err)
	}
	if !selected {
		t.Fatal("the helper was not selected for a consented machine, so there is nothing to take back")
	}
	// The transport starts the lifecycle bridge after the open ack; without
	// it the shell's hello never reaches the adapter and the handshake bound
	// expires, which would leave every test below watching a session that
	// never integrated in the first place.
	if opened.StartLifecycle != nil {
		opened.StartLifecycle()
	}
	return content.PendingSession{
		SessionID: string(opened.Session.ID()), Host: opened.Host,
		Account: opened.Account, Generation: opened.Generation,
		PaneID: paneID, ProfileID: "profile-1",
		HelperCommand: opened.HelperCommand, Fingerprint: opened.Fingerprint,
	}
}

func readoptFixture(t *testing.T, c *coordinator, routes *stubRoutes, adopter *stubAdopter) *readoptPass {
	t.Helper()
	return &readoptPass{registry: c.reg, routes: routes, adopter: adopter}
}

func routesFor(p content.PendingSession) *stubRoutes {
	return &stubRoutes{host: p.Host, cfg: helperConnection(p.Account)}
}

// ── THE HAPPY PATH ────────────────────────────────────────────────────────

// A fresh coordinator finds the shell the previous one left running, attaches
// to the SAME host session, and puts it in the registry `sessions.live` reads —
// keyed to the pane it was the pipe of, and without spawning a second shell.
func TestAFreshCoordinatorTakesBackTheSessionStillRunningOnItsHost(t *testing.T) {
	svc := sharedHelperService()
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc)}

	first := newCoordinator(t, provider)
	binding := openHostedFixture(t, first, "pane-1")

	// nocx is replaced: the first coordinator's channels go with its process,
	// and what comes up is a new session registry and a new helper registry.
	// Nothing of the first survives except the binding it wrote — which is
	// exactly what a restart leaves behind.
	first.quit()
	second := newCoordinator(t, provider)
	if _, ok := second.sess.Get(session.ID(binding.SessionID)); ok == nil {
		t.Fatal("the replacement coordinator already held the session; the restart was not modelled")
	}

	rec := &recordingReconciler{pending: []content.PendingSession{binding}}
	adopter := &stubAdopter{}
	reconcileSessions(context.Background(), rec, second.reg.inventories(),
		readoptFixture(t, second, routesFor(binding), adopter), time.Hour, quietLogger())

	if len(rec.applied) != 1 || rec.applied[0].Verdict != content.VerdictLive {
		t.Fatalf("verdict = %+v, want exactly one live — the helper holds the session", rec.applied)
	}
	adopted := adopter.adoptedIDs()
	if len(adopted) != 1 || string(adopted[0]) != binding.SessionID {
		t.Fatalf("adopted = %v, want exactly [%s] — the SAME host session, not a new shell (%v)",
			adopted, binding.SessionID, adopter.failure())
	}

	// The registry the live list is served from now holds it, with its pane.
	sess, err := second.sess.Get(session.ID(binding.SessionID))
	if err != nil {
		t.Fatalf("the replacement coordinator does not hold the session it took back: %v", err)
	}
	if sess.PaneID() != "pane-1" {
		t.Fatalf("pane = %q, want pane-1 — an entry no restored pane can claim is no better than no entry",
			sess.PaneID())
	}
	if sess.Identity().InstanceID != second.sess.InstanceID() {
		t.Fatal("the re-adopted session carries the OLD instance id; a fresh instance is what lets " +
			"the renderer claim it without judgeClaim being weakened")
	}

	// One shell on the host, not two. This is the assertion the whole bead is
	// about: the alternative failure is silent and looks like success.
	entries := helperSessionsOnHost(t, svc)
	if len(entries) != 1 {
		t.Fatalf("the host holds %d sessions, want 1 — taking a session back must not spawn a second shell", len(entries))
	}

	// And the helper channel is registered, so sessions.inventory answers on a
	// cold start instead of "no active helper".
	inv, err := second.reg.sessions(context.Background())
	if err != nil {
		t.Fatalf("sessions.inventory on a cold start: %v", err)
	}
	if len(inv) != 1 || inv[0].HostSessionID.Session != binding.SessionID {
		t.Fatalf("inventory = %+v, want the re-adopted session", inv)
	}
}

// helperSessionsOnHost asks the daemon directly what it holds — the count that
// says whether a second shell was spawned. Straight at the service rather than
// over a lane, deliberately: this is the test's own instrument and must not be
// the mechanism under test.
func helperSessionsOnHost(t *testing.T, svc *helpersession.Service) []proto.SessionEntry {
	t.Helper()
	raw, err := svc.Call(context.Background(), proto.OpSessions, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("ask the helper what it holds: %v", err)
	}
	result, ok := raw.(proto.SessionsResult)
	if !ok {
		t.Fatalf("the helper answered %T, want a sessions result", raw)
	}
	return result.Sessions
}

// closeHelperSession ends a session on the host the way the daemon's own
// close-session operation does, so a later ask gets a truthful "I do not hold
// that" rather than a fabricated one.
func closeHelperSession(t *testing.T, svc *helpersession.Service, p content.PendingSession) {
	t.Helper()
	params, err := json.Marshal(proto.CloseSessionParams{Session: proto.HostSessionID{
		Generation: proto.GenerationID(p.Generation), Session: p.SessionID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Call(context.Background(), proto.OpCloseSession, params); err != nil {
		t.Fatalf("close the session on the host: %v", err)
	}
}

// ── THE FAILURE PATHS, one test each ──────────────────────────────────────

// The host is unreachable at start. The verdict must be exactly the one the
// code would have produced without re-adoption — `unknown`, never `absent` —
// because that verdict is what stops a recording being deleted.
func TestAnUnreachableHostNeverReadsAsTheSessionBeingGone(t *testing.T) {
	svc := sharedHelperService()
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc)}
	first := newCoordinator(t, provider)
	binding := openHostedFixture(t, first, "pane-1")

	first.quit()
	second := newCoordinator(t, provider)
	// Every lane from here on fails, which is what a host that is switched
	// off, unplugged or firewalled looks like from this side.
	provider.laneErr = errors.New("dial tcp 10.0.0.9:22: connect: no route to host")

	rec := &recordingReconciler{pending: []content.PendingSession{binding}}
	adopter := &stubAdopter{}
	reconcileSessions(context.Background(), rec, second.reg.inventories(),
		readoptFixture(t, second, routesFor(binding), adopter), time.Hour, quietLogger())

	if len(rec.applied) != 1 {
		t.Fatalf("judgements = %+v, want exactly one", rec.applied)
	}
	got := rec.applied[0]
	if got.Verdict == content.VerdictAbsent {
		t.Fatal("an unreachable host produced ABSENT — that deletes the recording of a build " +
			"that is still running and closes its blocks as finished")
	}
	if got.Verdict != content.VerdictUnknown || got.Cause != content.CauseHostUnreachable {
		t.Fatalf("verdict/cause = %q/%q, want unknown/hostUnreachable", got.Verdict, got.Cause)
	}
	if len(adopter.adoptedIDs()) != 0 {
		t.Fatal("a session was adopted although its host was never reached")
	}
	if _, err := second.sess.Get(session.ID(binding.SessionID)); err == nil {
		t.Fatal("a failed re-adoption left the session in the registry; sessions.live would list " +
			"a session that was never taken back")
	}
}

// The helper is reachable and the generation that held the session is gone: the
// bridge finds no daemon serving it. Nothing ANSWERED about the session, so the
// verdict is unknown — "that generation is not running" is not "the session
// does not exist".
func TestAGenerationThatIsGoneIsNotTheSessionBeingGone(t *testing.T) {
	svc := sharedHelperService()
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc)}
	first := newCoordinator(t, provider)
	binding := openHostedFixture(t, first, "pane-1")

	first.quit()
	second := newCoordinator(t, provider)
	// The daemon on the far side answers for the generation it is, and the
	// binding names another one. That is the shape of "the install was
	// replaced and the old generation's process is gone".
	binding.Generation = "0000000000000000000000000000000000000000000000000000000000000000"

	rec := &recordingReconciler{pending: []content.PendingSession{binding}}
	adopter := &stubAdopter{}
	reconcileSessions(context.Background(), rec, second.reg.inventories(),
		readoptFixture(t, second, routesFor(binding), adopter), time.Hour, quietLogger())

	if len(rec.applied) != 1 {
		t.Fatalf("judgements = %+v, want exactly one", rec.applied)
	}
	if got := rec.applied[0]; got.Verdict != content.VerdictUnknown {
		t.Fatalf("verdict = %q, want unknown — nothing answered about this session", got.Verdict)
	}
	if len(adopter.adoptedIDs()) != 0 {
		t.Fatal("a session was adopted from a generation that is not serving")
	}
}

// The helper is reachable, the generation is serving, and it does not hold the
// session: it ended and was closed while nobody was watching. THIS is the one
// path to absent, and it must still work — without it the recording of every
// finished session would be kept for the retention age.
func TestAHelperThatAnswersAndDoesNotHoldItStillProducesAbsent(t *testing.T) {
	svc := sharedHelperService()
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc)}
	first := newCoordinator(t, provider)
	binding := openHostedFixture(t, first, "pane-1")

	// The session is closed on the host — the shell exited and the daemon
	// released it — so a later ask gets a truthful "I do not hold that".
	closeHelperSession(t, svc, binding)

	first.quit()
	second := newCoordinator(t, provider)
	rec := &recordingReconciler{pending: []content.PendingSession{binding}}
	adopter := &stubAdopter{}
	reconcileSessions(context.Background(), rec, second.reg.inventories(),
		readoptFixture(t, second, routesFor(binding), adopter), time.Hour, quietLogger())

	if len(rec.applied) != 1 {
		t.Fatalf("judgements = %+v, want exactly one", rec.applied)
	}
	if got := rec.applied[0]; got.Verdict != content.VerdictAbsent {
		t.Fatalf("verdict = %q, want absent — the exact generation was asked and answered", got.Verdict)
	}
	if len(adopter.adoptedIDs()) != 0 {
		t.Fatal("a session the host does not hold was adopted anyway")
	}
}

// The saved connection now points somewhere else. Asking THAT machine about
// this session's id would be asking a stranger about somebody else's id space,
// and its truthful "I do not hold that" would delete live work — which is the
// exact hazard nocx-k6p18.15's ordering exists to prevent.
func TestAConnectionThatNowLeadsElsewhereIsNotAsked(t *testing.T) {
	svc := sharedHelperService()
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc)}
	first := newCoordinator(t, provider)
	binding := openHostedFixture(t, first, "pane-1")

	first.quit()
	second := newCoordinator(t, provider)
	routes := &stubRoutes{host: "somebody.else.example", cfg: helperConnection(binding.Account)}

	rec := &recordingReconciler{pending: []content.PendingSession{binding}}
	adopter := &stubAdopter{}
	reconcileSessions(context.Background(), rec, second.reg.inventories(),
		readoptFixture(t, second, routes, adopter), time.Hour, quietLogger())

	if len(rec.applied) != 1 {
		t.Fatalf("judgements = %+v, want exactly one", rec.applied)
	}
	got := rec.applied[0]
	if got.Verdict == content.VerdictAbsent {
		t.Fatal("a connection that now leads to a different host produced ABSENT — the machine this " +
			"session is on was never asked")
	}
	if got.Verdict != content.VerdictUnknown {
		t.Fatalf("verdict = %q, want unknown", got.Verdict)
	}
	if provider.laneCount() != 1 {
		t.Fatalf("%d lanes were opened, want 1 (the original open) — a lane to the wrong host was dialled",
			provider.laneCount())
	}
}

// A sealed vault cannot resolve the connection. It is the one cause on the
// unreconciled list a person clears in one gesture, so it must arrive as
// itself and not as a generic unreachable host.
func TestASealedVaultIsReportedAsItselfAndNotAsAbsent(t *testing.T) {
	svc := sharedHelperService()
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc)}
	first := newCoordinator(t, provider)
	binding := openHostedFixture(t, first, "pane-1")

	first.quit()
	second := newCoordinator(t, provider)
	routes := &stubRoutes{err: vault.ErrVaultSealed}

	rec := &recordingReconciler{pending: []content.PendingSession{binding}}
	adopter := &stubAdopter{}
	reconcileSessions(context.Background(), rec, second.reg.inventories(),
		readoptFixture(t, second, routes, adopter), time.Hour, quietLogger())

	if len(rec.applied) != 1 {
		t.Fatalf("judgements = %+v, want exactly one", rec.applied)
	}
	got := rec.applied[0]
	if got.Verdict != content.VerdictUnknown || got.Cause != content.CauseVaultSealed {
		t.Fatalf("verdict/cause = %q/%q, want unknown/vaultSealed", got.Verdict, got.Cause)
	}
}

// A session with no route recorded — a direct-host open, which carries no
// saved connection — is left exactly where it was: unknown/noInventory, the
// behaviour that existed before re-adoption. Nothing is guessed, and no
// connection is dialled to guess with.
func TestASessionWithNoRouteIsLeftExactlyWhereItWas(t *testing.T) {
	svc := sharedHelperService()
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc)}
	first := newCoordinator(t, provider)
	binding := openHostedFixture(t, first, "pane-1")
	binding.ProfileID = ""

	first.quit()
	second := newCoordinator(t, provider)
	routes := routesFor(binding)
	rec := &recordingReconciler{pending: []content.PendingSession{binding}}
	adopter := &stubAdopter{}
	reconcileSessions(context.Background(), rec, second.reg.inventories(),
		readoptFixture(t, second, routes, adopter), time.Hour, quietLogger())

	if len(rec.applied) != 1 {
		t.Fatalf("judgements = %+v, want exactly one", rec.applied)
	}
	got := rec.applied[0]
	if got.Verdict != content.VerdictUnknown || got.Cause != content.CauseNoInventory {
		t.Fatalf("verdict/cause = %q/%q, want unknown/noInventory", got.Verdict, got.Cause)
	}
	if routes.consulted() != 0 {
		t.Fatal("a connection was resolved for a session that recorded no route")
	}
}

// The transport half refuses — the server is already shutting down. The
// session is LIVE and it was not taken back, and both halves of that must
// hold: the verdict stays live so the recording survives, and nothing is left
// in the registry pretending it was adopted.
func TestATransportThatRefusesLeavesTheSessionLiveAndUnadopted(t *testing.T) {
	svc := sharedHelperService()
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc)}
	first := newCoordinator(t, provider)
	binding := openHostedFixture(t, first, "pane-1")

	first.quit()
	second := newCoordinator(t, provider)
	adopter := &stubAdopter{refuse: errors.New("server is shutting down")}
	rec := &recordingReconciler{pending: []content.PendingSession{binding}}
	reconcileSessions(context.Background(), rec, second.reg.inventories(),
		readoptFixture(t, second, routesFor(binding), adopter), time.Hour, quietLogger())

	if len(rec.applied) != 1 {
		t.Fatalf("judgements = %+v, want exactly one", rec.applied)
	}
	if got := rec.applied[0]; got.Verdict != content.VerdictLive {
		t.Fatalf("verdict = %q, want live — the helper answered that the session exists, and a "+
			"coordinator that could not attach is not evidence that a build stopped", got.Verdict)
	}
	if len(adopter.adoptedIDs()) != 0 {
		t.Fatal("the adopter reported an adoption it refused")
	}
	if _, err := second.sess.Get(session.ID(binding.SessionID)); err == nil {
		t.Fatal("a refused re-adoption left the session in the registry; sessions.live would offer " +
			"a restored pane a session that was never taken back")
	}
}

// Two coordinators against one host, which D12 SERVES rather than refuses. The
// first still holds the session's one write capability (nocx-k6p18.16 binds the
// lease to the connection that acquired it), so the second declines the session
// rather than putting a pane on screen whose keystrokes go nowhere — and the
// verdict stays LIVE, because the session exists and its recording must not be
// deleted by the arrival of a second nocx.
func TestASessionAnotherCoordinatorIsHoldingIsLeftToIt(t *testing.T) {
	svc := sharedHelperService()
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc)}
	first := newCoordinator(t, provider)
	binding := openHostedFixture(t, first, "pane-1")
	// first does NOT quit: it is still running, still attached, still holding
	// the keyboard.

	second := newCoordinator(t, provider)
	rec := &recordingReconciler{pending: []content.PendingSession{binding}}
	adopter := &stubAdopter{}
	reconcileSessions(context.Background(), rec, second.reg.inventories(),
		readoptFixture(t, second, routesFor(binding), adopter), time.Hour, quietLogger())

	if len(rec.applied) != 1 {
		t.Fatalf("judgements = %+v, want exactly one", rec.applied)
	}
	if got := rec.applied[0]; got.Verdict != content.VerdictLive {
		t.Fatalf("verdict = %q, want live — the session exists and another nocx is using it", got.Verdict)
	}
	if len(adopter.adoptedIDs()) != 0 {
		t.Fatal("a session whose keyboard another coordinator holds was adopted anyway")
	}
	if _, err := second.sess.Get(session.ID(binding.SessionID)); err == nil {
		t.Fatal("the declined session is in the registry, so a pane would be handed a session " +
			"it cannot type into")
	}
	// And the one shell is untouched: the second coordinator neither spawned
	// one nor took the first's keyboard away.
	if entries := helperSessionsOnHost(t, svc); len(entries) != 1 {
		t.Fatalf("the host holds %d sessions, want 1", len(entries))
	}
	if entries := helperSessionsOnHost(t, svc); entries[0].Writer == nil {
		t.Fatal("the first coordinator lost the write capability to a coordinator that declined the session")
	}
}

// The build FINISHED while nocx was away, and the host knows how. The helper
// recorded the status when the process died and has been holding it since; the
// coordinator that would have heard the notification is gone. A re-adoption
// that did not carry the status would reach EOF and the product would say the
// session "was interrupted" — the unknown outcome this epic exists to end
// (nocx-k6p18.23) — about a build whose exit code was available the whole time.
func TestASessionThatEndedWhileNocxWasAwayCarriesTheHostsExitStatus(t *testing.T) {
	const exitCode = 7
	// The spawner is held by the test rather than made inside the fixture,
	// because ending the process is what this test is about.
	spawner := &scriptedSpawner{}
	svc := helpersession.New(helpersession.Options{
		Generation: proto.GenerationID(syntheticArtifactHash),
		Spawner:    spawner,
		Log:        discardLogger(),
	})
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc)}
	first := newCoordinator(t, provider)
	binding := openHostedFixture(t, first, "pane-1")
	first.quit()

	// The shell exits with nobody attached. Waited on by observing the
	// helper's own inventory rather than by a duration: the exit is recorded
	// before the entry can report it, so the entry reporting it IS the event.
	spawner.exitWith(t, exitCode)
	waitForHelperExit(t, svc, binding)

	second := newCoordinator(t, provider)
	rec := &recordingReconciler{pending: []content.PendingSession{binding}}
	adopter := &stubAdopter{}
	reconcileSessions(context.Background(), rec, second.reg.inventories(),
		readoptFixture(t, second, routesFor(binding), adopter), time.Hour, quietLogger())

	// The helper still HOLDS the session — an exited session is not a closed
	// one — so the verdict is live and the recording survives to be read.
	if len(rec.applied) != 1 || rec.applied[0].Verdict != content.VerdictLive {
		t.Fatalf("verdict = %+v, want live: the helper still holds this session", rec.applied)
	}
	sess, err := second.sess.Get(session.ID(binding.SessionID))
	if err != nil {
		t.Fatalf("the finished session was not taken back: %v (%v)", err, adopter.failure())
	}
	cause, status := sess.ExitOutcome()
	if cause == session.ExitInterrupted {
		t.Fatal("a session the host has an exit status for reads as INTERRUPTED — that is the " +
			"unknown outcome this epic set out to end")
	}
	if status != exitCode {
		t.Fatalf("exit status = %d, want %d — the host's own number, not a substitute", status, exitCode)
	}
}

// waitForHelperExit polls the daemon's own inventory until it reports the
// status. Not a sleep: the entry carrying an exit is the observable event.
func waitForHelperExit(t *testing.T, svc *helpersession.Service, p content.PendingSession) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range helperSessionsOnHost(t, svc) {
			if e.Session.Session == p.SessionID && e.Exit != nil {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the helper never recorded the process's exit")
}

// Consent is re-asked and not assumed to have survived. Opening a helper
// channel to a machine is the act consent governs (D8), and a person who
// withdrew it between two runs must not have one opened silently on their
// behalf — least of all by a startup pass they never triggered. The session
// stays UNKNOWN, because nothing was asked about it.
func TestConsentWithdrawnBetweenRunsStopsTheHelperBeingReached(t *testing.T) {
	const machine = "SHA256:test-host"
	svc := sharedHelperService()
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc)}
	first := newCoordinator(t, provider)
	binding := openHostedFixture(t, first, "pane-1")
	first.quit()

	// The binding records the machine the session runs on. A route whose mode
	// is `auto` is the case where the STORE decides — an explicit helper mode
	// is itself the consent (§4.3) and would not consult it.
	binding.Fingerprint = machine
	routes := &stubRoutes{host: binding.Host, cfg: &ssh.ConnectConfig{User: binding.Account}}

	second := newCoordinator(t, provider)
	if err := second.consent.Revoke(machine); err != nil {
		t.Fatalf("revoke consent: %v", err)
	}
	lanesBefore := provider.laneCount()

	rec := &recordingReconciler{pending: []content.PendingSession{binding}}
	adopter := &stubAdopter{}
	reconcileSessions(context.Background(), rec, second.reg.inventories(),
		readoptFixture(t, second, routes, adopter), time.Hour, quietLogger())

	if len(rec.applied) != 1 {
		t.Fatalf("judgements = %+v, want exactly one", rec.applied)
	}
	got := rec.applied[0]
	if got.Verdict == content.VerdictAbsent {
		t.Fatal("a machine nobody may connect to produced ABSENT — nothing was asked")
	}
	if got.Verdict != content.VerdictUnknown {
		t.Fatalf("verdict = %q, want unknown", got.Verdict)
	}
	if provider.laneCount() != lanesBefore {
		t.Fatalf("%d lanes were opened to a machine whose consent was withdrawn, want none",
			provider.laneCount()-lanesBefore)
	}
}

// The paired positive, without which the test above is satisfiable by never
// connecting at all: the SAME binding, with consent still standing, reaches
// the helper and takes the session back.
func TestConsentStandingLetsTheSameBindingBeTakenBack(t *testing.T) {
	const machine = "SHA256:test-host"
	svc := sharedHelperService()
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc)}
	first := newCoordinator(t, provider)
	binding := openHostedFixture(t, first, "pane-1")
	first.quit()

	binding.Fingerprint = machine
	routes := &stubRoutes{host: binding.Host, cfg: &ssh.ConnectConfig{User: binding.Account}}

	second := newCoordinator(t, provider)
	rec := &recordingReconciler{pending: []content.PendingSession{binding}}
	adopter := &stubAdopter{}
	reconcileSessions(context.Background(), rec, second.reg.inventories(),
		readoptFixture(t, second, routes, adopter), time.Hour, quietLogger())

	if len(rec.applied) != 1 || rec.applied[0].Verdict != content.VerdictLive {
		t.Fatalf("verdict = %+v, want live", rec.applied)
	}
	if ids := adopter.adoptedIDs(); len(ids) != 1 || string(ids[0]) != binding.SessionID {
		t.Fatalf("adopted = %v, want [%s] (%v)", ids, binding.SessionID, adopter.failure())
	}
}

// A transport that refuses a session the REGISTRY has already been given must
// leave nothing behind. The interval this closes has both ends: the session is
// in the registry from Adopt until either the transport takes ownership of it
// or this path takes it back out. Left in, it would be a `sessions.live` row a
// restored pane claims and then finds silent — no ring, no pump, nothing
// reading the host.
func TestATransportRefusingAfterTheAdoptTakesTheSessionBackOut(t *testing.T) {
	svc := sharedHelperService()
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc)}
	first := newCoordinator(t, provider)
	binding := openHostedFixture(t, first, "pane-1")
	first.quit()

	second := newCoordinator(t, provider)
	adopter := &stubAdopter{refuseAfter: errors.New("re-attaching answered for another session")}
	rec := &recordingReconciler{pending: []content.PendingSession{binding}}
	reconcileSessions(context.Background(), rec, second.reg.inventories(),
		readoptFixture(t, second, routesFor(binding), adopter), time.Hour, quietLogger())

	if len(rec.applied) != 1 || rec.applied[0].Verdict != content.VerdictLive {
		t.Fatalf("verdict = %+v, want live — the helper answered that the session exists", rec.applied)
	}
	if _, err := second.sess.Get(session.ID(binding.SessionID)); err == nil {
		t.Fatal("the refused session is still in the registry: sessions.live would offer a restored " +
			"pane a session with no ring behind it")
	}
	second.reg.mu.Lock()
	_, held := second.reg.hosts[session.ID(binding.SessionID)]
	second.reg.mu.Unlock()
	if held {
		t.Fatal("the refused session still holds a helper channel on the far host")
	}
}

// TWO SESSIONS ON ONE HOST ARE TWO RE-ADOPTIONS. The natural way to write the
// pass is to let the first session's answer judge the second — one dial, one
// inventory — and it is wrong in a way that reads as success: the second
// session is judged LIVE, its recording is kept, and it is never taken back, so
// its pane quietly opens a second shell. Both are taken back, and neither
// spawns anything.
func TestTwoSessionsOnOneHostAreBothTakenBack(t *testing.T) {
	svc := sharedHelperService()
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc)}
	first := newCoordinator(t, provider)
	one := openHostedFixture(t, first, "pane-1")
	two := openHostedFixture(t, first, "pane-2")
	if one.SessionID == two.SessionID {
		t.Fatal("the fixture opened one session twice")
	}
	first.quit()

	second := newCoordinator(t, provider)
	rec := &recordingReconciler{pending: []content.PendingSession{one, two}}
	adopter := &stubAdopter{}
	reconcileSessions(context.Background(), rec, second.reg.inventories(),
		readoptFixture(t, second, routesFor(one), adopter), time.Hour, quietLogger())

	if len(rec.applied) != 2 {
		t.Fatalf("judgements = %+v, want one per session", rec.applied)
	}
	for _, j := range rec.applied {
		if j.Verdict != content.VerdictLive {
			t.Fatalf("%s = %q/%q, want live", j.SessionID, j.Verdict, j.Cause)
		}
	}
	for _, want := range []string{one.SessionID, two.SessionID} {
		sess, err := second.sess.Get(session.ID(want))
		if err != nil {
			t.Fatalf("session %s was judged live and never taken back (%v); its pane will open a "+
				"second shell to a host that is already running one", want, adopter.failure())
		}
		if sess.PaneID() == "" {
			t.Fatalf("session %s came back with no pane", want)
		}
	}
	if entries := helperSessionsOnHost(t, svc); len(entries) != 2 {
		t.Fatalf("the host holds %d sessions, want the same 2 it started with", len(entries))
	}
}

// A HOST THAT NEITHER ANSWERS NOR REFUSES MUST NOT HANG THE START. The pass is
// synchronous — it runs before the WebSocket server listens, so a client cannot
// ask `sessions.live` while it is still deciding — and that is only safe
// because one attempt is bounded. Without the bound a machine behind a
// black-holing firewall is a nocx that never finishes starting.
//
// The verdict is `timedOut`, which is a sentence a person can act on, and it is
// emphatically not `absent`.
func TestAHostThatNeverAnswersIsBoundedAndNeverReadsAsGone(t *testing.T) {
	svc := sharedHelperService()
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc)}
	first := newCoordinator(t, provider)
	binding := openHostedFixture(t, first, "pane-1")
	first.quit()

	// Nothing ever closes this: the lane parks until the attempt's own
	// deadline cancels it.
	provider.laneBlock = make(chan struct{})

	second := newCoordinator(t, provider)
	pass := readoptFixture(t, second, routesFor(binding), &stubAdopter{})
	pass.timeout = 50 * time.Millisecond

	rec := &recordingReconciler{pending: []content.PendingSession{binding}}
	done := make(chan struct{})
	go func() {
		reconcileSessions(context.Background(), rec, second.reg.inventories(), pass, time.Hour, quietLogger())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the pass never returned against a host that neither answers nor refuses; a " +
			"synchronous pass that cannot end is a nocx that never finishes starting")
	}

	if len(rec.applied) != 1 {
		t.Fatalf("judgements = %+v, want exactly one", rec.applied)
	}
	got := rec.applied[0]
	if got.Verdict == content.VerdictAbsent {
		t.Fatal("a host that never answered produced ABSENT — a deadline is not an answer")
	}
	if got.Verdict != content.VerdictUnknown || got.Cause != content.CauseTimedOut {
		t.Fatalf("verdict/cause = %q/%q, want unknown/timedOut", got.Verdict, got.Cause)
	}
}

// The paired positive for the bound: with the same field set, a host that DOES
// answer is taken back rather than being cut off by its own guard.
func TestTheBoundDoesNotCutOffAHostThatAnswers(t *testing.T) {
	svc := sharedHelperService()
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc)}
	first := newCoordinator(t, provider)
	binding := openHostedFixture(t, first, "pane-1")
	first.quit()

	second := newCoordinator(t, provider)
	adopter := &stubAdopter{}
	pass := readoptFixture(t, second, routesFor(binding), adopter)
	pass.timeout = 30 * time.Second

	rec := &recordingReconciler{pending: []content.PendingSession{binding}}
	reconcileSessions(context.Background(), rec, second.reg.inventories(), pass, time.Hour, quietLogger())

	if len(rec.applied) != 1 || rec.applied[0].Verdict != content.VerdictLive {
		t.Fatalf("verdict = %+v, want live", rec.applied)
	}
	if ids := adopter.adoptedIDs(); len(ids) != 1 {
		t.Fatalf("adopted = %v, want the session back (%v)", ids, adopter.failure())
	}
}
