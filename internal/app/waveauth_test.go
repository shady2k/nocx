package app

import (
	"context"
	"errors"
	"os"
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
	auth := newWaveAuthorizer(pinner, reg, grid)

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
	if !containsGrantScope(inv.Grant, content.ResourceSession, string(sess.ID())) {
		t.Fatalf("grant has no session scope: %+v", inv.Grant.Scopes)
	}
	if !containsGrantScope(inv.Grant, content.ResourceEnvironment, localEnv) {
		t.Fatalf("grant has no local environment scope: %+v", inv.Grant.Scopes)
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
	auth := newWaveAuthorizer(pinner, reg, grid)
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
	auth := newWaveAuthorizer(pinner, reg, grid)
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
	auth := newWaveAuthorizer(pinner, reg, grid)
	_, _, err := auth.Admit(waveendpoint.Peer{UID: 1000, PID: 9001})
	if !errors.Is(err, waveendpoint.ErrNotEnrolled) {
		t.Fatalf("unknown-owned-pid admission error = %v, want ErrNotEnrolled", err)
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
