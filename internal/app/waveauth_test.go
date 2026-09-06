package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panegrid"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/wave"
	"github.com/shady2k/nocx/internal/waveendpoint"
	"github.com/shady2k/nocx/internal/wavepin"
)

type waveAuthPTYFactory struct {
	log log.Logger
}

func (f waveAuthPTYFactory) NewPTY(context.Context, pty.Config) (pty.Pty, error) {
	return pty.NewStub(f.log), nil
}

type waveAuthPinner struct {
	root       wavepin.Root
	pinPIDs    []int
	memberPIDs []int
	member     map[int]bool
}

func (p *waveAuthPinner) Pin(pid int) (wavepin.Root, error) {
	p.pinPIDs = append(p.pinPIDs, pid)
	if pid != p.root.PID {
		return wavepin.Root{}, wavepin.ErrGone
	}
	return p.root, nil
}

func (p *waveAuthPinner) Member(child int, root wavepin.Root) (bool, error) {
	p.memberPIDs = append(p.memberPIDs, child)
	if root != p.root {
		return false, wavepin.ErrGone
	}
	return p.member[child], nil
}

// emptyWaveRecord is a real record holding nobody. The coordinator tests use
// it rather than a nil so they exercise the real "is this session a
// participant" lookup and take the coordinator branch because the ANSWER is
// no, not because the seam was missing.
func emptyWaveRecord() *wave.Registrar {
	return wave.NewRegistrar(wave.NewMemoryStore(), nil, nil, nil)
}

func openWaveAuthSession(t *testing.T) (*session.Reg, session.Session, *panegrid.Store) {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	reg := session.New(logger, waveAuthPTYFactory{log: logger})
	sess, err := reg.Open(context.Background(), session.Config{Kind: session.KindLocal, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close(sess.ID()) })
	grid := panegrid.New(logger)
	t.Cleanup(func() { grid.Withdraw(string(sess.ID())) })
	return reg, sess, grid
}

type waveAuthSessionOverride struct {
	session.Session
	kind session.Kind
	host string
}

func (s waveAuthSessionOverride) Kind() session.Kind { return s.kind }
func (s waveAuthSessionOverride) Host() string       { return s.host }

type waveAuthSessionSet struct {
	sessions []session.Session
	owned    map[session.ID]int
}

func (s waveAuthSessionSet) List() []session.Session { return s.sessions }

func (s waveAuthSessionSet) OwnedProcessPID(id session.ID) (int, bool) {
	pid, ok := s.owned[id]
	return pid, ok
}

func TestWaveAuthorizerAdmitsEnrolledOwnedTreeThroughRealWaveRecord(t *testing.T) {
	reg, sess, grid := openWaveAuthSession(t)
	const ownedPID = 4242
	if err := reg.RecordOwnedProcessPID(sess.ID(), ownedPID); err != nil {
		t.Fatalf("record owned process pid: %v", err)
	}
	if err := grid.Enrol(string(sess.ID()), 80, 24); err != nil {
		t.Fatalf("enrol session grid: %v", err)
	}

	root := wavepin.Root{PID: ownedPID, StartTime: time.Unix(123, 0)}
	pinner := &waveAuthPinner{root: root, member: map[int]bool{9001: true}}
	auth := newWaveAuthorizer(pinner, reg, grid, emptyWaveRecord(), waveTestWorkspace)

	inv, _, err := auth.Admit(waveendpoint.Peer{UID: 1000, PID: 9001})
	if err != nil {
		t.Fatalf("admit enrolled tree: %v", err)
	}
	if inv.Context == nil {
		t.Fatal("admitted invocation has nil context")
	}
	if got := inv.RunContext.Session; got != string(sess.ID()) {
		t.Fatalf("invocation session = %q, want %q", got, sess.ID())
	}
	if len(pinner.pinPIDs) != 1 || pinner.pinPIDs[0] != ownedPID {
		t.Fatalf("pinner pinned %v, want only backend-owned pid %d", pinner.pinPIDs, ownedPID)
	}
	if len(pinner.memberPIDs) != 1 || pinner.memberPIDs[0] != 9001 {
		t.Fatalf("pinner checked members %v, want caller pid 9001", pinner.memberPIDs)
	}

	localEnv := content.EnvironmentIDFor(content.EnvLocal, "")
	var environmentScopes []content.GrantScope
	for _, scope := range inv.Grant.Scopes {
		if scope.Kind == content.ResourceEnvironment {
			environmentScopes = append(environmentScopes, scope)
		}
	}
	if len(environmentScopes) != 1 || environmentScopes[0].ID != localEnv {
		t.Fatalf("environment scopes = %+v, want exactly one local scope %q", environmentScopes, localEnv)
	}
	if !containsGrantScope(inv.Grant, content.ResourceSession, string(sess.ID())) {
		t.Fatalf("grant has no session scope: %+v", inv.Grant.Scopes)
	}
	if inv.Grant.Policy.DecisionFor(content.EffectDelegate) != content.DecisionPermit {
		t.Fatalf("delegate effect = %q, want permit", inv.Grant.Policy.DecisionFor(content.EffectDelegate))
	}
	if inv.Grant.Policy.DecisionFor(content.EffectCrossBoundary) != content.DecisionRefuse {
		t.Fatalf("cross-boundary effect = %q, want refuse", inv.Grant.Policy.DecisionFor(content.EffectCrossBoundary))
	}

	registry, err := agenttools.Assemble(os.DirFS("../../contracts/tools"))
	if err != nil {
		t.Fatalf("assemble tools: %v", err)
	}
	waves := wave.NewRegistrar(wave.NewMemoryStore(), nil, nil, nil)
	dispatcher, err := assistant.NewWaveDispatcher(registry, waves, localEnv)
	if err != nil {
		t.Fatalf("new wave dispatcher: %v", err)
	}
	inv.Method = "wave.holdings"
	inv.RawParams = []byte(`{}`)
	result, err := dispatcher.Dispatch(inv)
	if err != nil {
		t.Fatalf("dispatch admitted holdings: %v", err)
	}
	if result == "" {
		t.Fatal("dispatch returned an empty holdings result")
	}
}

func TestWaveCallerGrantDerivesEnvironmentFromSession(t *testing.T) {
	_, local, _ := openWaveAuthSession(t)
	remote := waveAuthSessionOverride{
		Session: local,
		kind:    session.KindRemote,
		host:    "build.example.com",
	}
	environmentID := waveEnvironmentForSession(remote)
	grant := waveCallerGrant(remote.ID(), environmentID)
	want := content.EnvironmentIDFor(content.EnvSSH, remote.Host())
	var environments []content.GrantScope
	for _, scope := range grant.Scopes {
		if scope.Kind == content.ResourceEnvironment {
			environments = append(environments, scope)
		}
	}
	if len(environments) != 1 {
		t.Fatalf("environment scopes = %+v, want exactly one", environments)
	}
	if environments[0].ID != want {
		t.Fatalf("environment scope = %q, want session environment %q", environments[0].ID, want)
	}
}

func TestWaveAuthorizerRefusesCallerOutsideEveryEnrolledTree(t *testing.T) {
	reg, sess, grid := openWaveAuthSession(t)
	const ownedPID = 4242
	if err := reg.RecordOwnedProcessPID(sess.ID(), ownedPID); err != nil {
		t.Fatalf("record owned process pid: %v", err)
	}
	if err := grid.Enrol(string(sess.ID()), 80, 24); err != nil {
		t.Fatalf("enrol session grid: %v", err)
	}

	pinner := &waveAuthPinner{
		root:   wavepin.Root{PID: ownedPID, StartTime: time.Unix(123, 0)},
		member: map[int]bool{9001: false},
	}
	auth := newWaveAuthorizer(pinner, reg, grid, emptyWaveRecord(), waveTestWorkspace)
	_, _, err := auth.Admit(waveendpoint.Peer{UID: 1000, PID: 9001})
	if !errors.Is(err, waveendpoint.ErrNotEnrolled) {
		t.Fatalf("outside-tree admission error = %v, want ErrNotEnrolled", err)
	}
}

func TestWaveAuthorizerWithdrawClosesAdmissionInterval(t *testing.T) {
	reg, sess, grid := openWaveAuthSession(t)
	const ownedPID = 4242
	if err := reg.RecordOwnedProcessPID(sess.ID(), ownedPID); err != nil {
		t.Fatalf("record owned process pid: %v", err)
	}
	if err := grid.Enrol(string(sess.ID()), 80, 24); err != nil {
		t.Fatalf("enrol session grid: %v", err)
	}

	pinner := &waveAuthPinner{
		root:   wavepin.Root{PID: ownedPID, StartTime: time.Unix(123, 0)},
		member: map[int]bool{9001: true},
	}
	auth := newWaveAuthorizer(pinner, reg, grid, emptyWaveRecord(), waveTestWorkspace)
	if _, _, err := auth.Admit(waveendpoint.Peer{UID: 1000, PID: 9001}); err != nil {
		t.Fatalf("admit before withdrawal: %v", err)
	}
	grid.Withdraw(string(sess.ID()))
	if _, _, err := auth.Admit(waveendpoint.Peer{UID: 1000, PID: 9001}); !errors.Is(err, waveendpoint.ErrNotEnrolled) {
		t.Fatalf("admit after withdrawal error = %v, want ErrNotEnrolled", err)
	}
}

func TestWaveAuthorizerRefusesEnrolledSessionWithoutOwnedProcess(t *testing.T) {
	reg, sess, grid := openWaveAuthSession(t)
	if err := grid.Enrol(string(sess.ID()), 80, 24); err != nil {
		t.Fatalf("enrol session grid: %v", err)
	}
	pinner := &waveAuthPinner{
		root:   wavepin.Root{PID: 4242, StartTime: time.Unix(123, 0)},
		member: map[int]bool{9001: true},
	}
	auth := newWaveAuthorizer(pinner, reg, grid, emptyWaveRecord(), waveTestWorkspace)
	_, _, err := auth.Admit(waveendpoint.Peer{UID: 1000, PID: 9001})
	if !errors.Is(err, waveendpoint.ErrNotEnrolled) {
		t.Fatalf("unknown-owned-pid admission error = %v, want ErrNotEnrolled", err)
	}
}

func TestWaveAuthorizerRefusesRemoteSessionWithoutOwnedProcess(t *testing.T) {
	_, local, grid := openWaveAuthSession(t)
	remote := waveAuthSessionOverride{
		Session: local,
		kind:    session.KindRemote,
		host:    "build.example.com",
	}
	if err := grid.Enrol(string(remote.ID()), 80, 24); err != nil {
		t.Fatalf("enrol session grid: %v", err)
	}
	sessions := waveAuthSessionSet{
		sessions: []session.Session{remote},
		owned:    map[session.ID]int{},
	}
	pinner := &waveAuthPinner{
		root:   wavepin.Root{PID: 4242, StartTime: time.Unix(123, 0)},
		member: map[int]bool{9001: true},
	}
	auth := newWaveAuthorizer(pinner, sessions, grid, emptyWaveRecord(), waveTestWorkspace)
	_, _, err := auth.Admit(waveendpoint.Peer{UID: 1000, PID: 9001})
	if !errors.Is(err, waveendpoint.ErrNotEnrolled) {
		t.Fatalf("remote session admission error = %v, want ErrNotEnrolled", err)
	}
}

func TestWaveDispatcherRefusesSpawnOutsideCoordinatorEnvironment(t *testing.T) {
	registry, err := agenttools.Assemble(os.DirFS("../../contracts/tools"))
	if err != nil {
		t.Fatalf("assemble tools: %v", err)
	}
	const (
		availableEnvironment = "env-available"
		askedEnvironment     = "env-asked"
	)
	dispatcher, err := assistant.NewWaveDispatcher(
		registry,
		emptyWaveRecord(),
		availableEnvironment,
	)
	if err != nil {
		t.Fatalf("new wave dispatcher: %v", err)
	}
	_, err = dispatcher.Dispatch(assistant.WaveInvocation{
		Context:    context.Background(),
		Method:     "wave.spawn",
		RunContext: agenttools.RunContext{Session: "sess-coordinator"},
		Grant:      waveCallerGrant(session.ID("sess-coordinator"), askedEnvironment),
		RawParams:  []byte(`{"command":"claude","task":"read it"}`),
	})
	if err == nil {
		t.Fatal("a spawn outside the coordinator environment was accepted")
	}
	if !strings.Contains(err.Error(), availableEnvironment) ||
		!strings.Contains(err.Error(), askedEnvironment) {
		t.Fatalf("err = %v, want it to name asked %q and available %q",
			err, askedEnvironment, availableEnvironment)
	}
}

func TestRefusedWaveInvocationOffersNoWaveTools(t *testing.T) {
	reg, sess, grid := openWaveAuthSession(t)
	if err := grid.Enrol(string(sess.ID()), 80, 24); err != nil {
		t.Fatalf("enrol session grid: %v", err)
	}
	pinner := &waveAuthPinner{
		root:   wavepin.Root{PID: 4242, StartTime: time.Unix(123, 0)},
		member: map[int]bool{9001: true},
	}
	auth := newWaveAuthorizer(pinner, reg, grid, emptyWaveRecord(), waveTestWorkspace)
	inv, _, err := auth.Admit(waveendpoint.Peer{UID: 1000, PID: 9001})
	if !errors.Is(err, waveendpoint.ErrNotEnrolled) {
		t.Fatalf("unadmitted peer error = %v, want ErrNotEnrolled", err)
	}
	if inv.Context != nil || inv.Method != "" || inv.RunContext.RunID != "" ||
		inv.RunContext.Workspace != "" || inv.RunContext.Session != "" ||
		inv.RunContext.Participant != "" || len(inv.RawParams) != 0 ||
		len(inv.Grant.Effects) != 0 || len(inv.Grant.Scopes) != 0 {
		t.Fatalf("refused admission returned non-zero invocation: %+v", inv)
	}

	registry, err := agenttools.Assemble(os.DirFS("../../contracts/tools"))
	if err != nil {
		t.Fatalf("assemble tools: %v", err)
	}
	for _, tool := range registry.ForGrant(inv.Grant) {
		if strings.HasPrefix(tool.Name, "wave.") {
			t.Fatalf("unadmitted grant offered %q", tool.Name)
		}
	}
}

func containsGrantScope(grant content.Grant, kind content.ResourceKind, id string) bool {
	for _, scope := range grant.Scopes {
		if scope.Kind == kind && scope.ID == id {
			return true
		}
	}
	return false
}
