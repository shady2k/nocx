package toolendpoint

import (
	"io"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/waittest"
)

func TestSettleAdmissionsWaitsForCallsAndRelease(t *testing.T) {
	started, cancelled, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	released, finishRelease := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-finish:
		default:
			close(finish)
		}
		select {
		case <-finishRelease:
		default:
			close(finishRelease)
		}
	}()
	dispatch := &testDispatcher{out: `{}`, started: started, cancelled: cancelled, release: finish}
	auth := &testAuthorizer{inv: assistant.ToolInvocation{RunContext: agenttools.RunContext{Session: TurnScope("run")}}, release: func() { close(released); <-finishRelease }}
	cfg := endpointConfig(t, auth, dispatch)
	cfg.Logger = logtest.Slog(t)
	ep := startEndpoint(t, cfg)
	conn := dialEndpoint(t, ep)
	call := `{"jsonrpc":"2.0","id":1,"method":"workers.holdings","params":{}}` + "\n"
	if _, err := io.WriteString(conn, call); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("call not started")
	}
	if !ep.PendingAdmissions(TurnScope("run"), 1) {
		t.Fatal("accepted call not pending")
	}
	if ep.PendingAdmissions(TurnScope("other"), 1) {
		t.Fatal("other scope pending")
	}
	settled := make(chan struct{})
	go func() { ep.SettleAdmissions(TurnScope("run"), 1); close(settled) }()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("call not cancelled")
	}
	select {
	case <-settled:
		t.Fatal("settled before dispatch returned")
	default:
	}
	if !ep.PendingAdmissions(TurnScope("run"), 1) {
		t.Fatal("cancelled but unfinished call not pending")
	}
	close(finish)
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("release not reached")
	}
	select {
	case <-settled:
		t.Fatal("settled before release completed")
	default:
	}
	close(finishRelease)
	select {
	case <-settled:
	case <-time.After(5 * time.Second):
		t.Fatal("settlement did not finish")
	}
	if ep.PendingAdmissions(TurnScope("run"), 1) {
		t.Fatal("settled call pending")
	}
}

func TestIdleAdmissionIsNotPendingAndSettlementLeavesNewerEpoch(t *testing.T) {
	auth := &sessionAuthorizer{sessions: []string{"session", "session"}, epoch: 1}
	cfg := endpointConfig(t, &testAuthorizer{}, &testDispatcher{out: `{}`})
	cfg.Auth, cfg.Logger = auth, logtest.Slog(t)
	ep := startEndpoint(t, cfg)
	first := dialEndpoint(t, ep)
	callOver(t, first, "first")
	waittest.WaitFor(t, "answered call is no longer pending", func() bool {
		return !ep.PendingAdmissions("session", 1)
	})
	auth.mu.Lock()
	auth.epoch = 2
	auth.mu.Unlock()
	second := dialEndpoint(t, ep)
	callOver(t, second, "second")
	ep.SettleAdmissions("session", 1)
	expectClosed(t, first)
	callOver(t, second, "newer")
}

// delayedAdmission holds the decision after publication, without adding a test
// hook to the endpoint. A request already in the socket must not run after its
// turn was retired, even when Admit returns only after the next turn starts.
type delayedAdmission struct {
	first     Authorizer
	next      Authorizer
	published chan struct{}
	resume    chan struct{}
	mu        sync.Mutex
	count     int
}

func (a *delayedAdmission) Admit(peer Peer, publish func(string, AdmissionEpoch) bool) (assistant.ToolInvocation, func(), error) {
	a.mu.Lock()
	first := a.count == 0
	a.count++
	a.mu.Unlock()
	if !first {
		return a.next.Admit(peer, publish)
	}
	inv, release, err := a.first.Admit(peer, publish)
	close(a.published)
	<-a.resume
	return inv, release, err
}

func TestDelayedRequestCannotReachNextTurnsAuthority(t *testing.T) {
	auth := &delayedAdmission{
		first:     &sessionAuthorizer{sessions: []string{TurnScope("old")}},
		next:      &sessionAuthorizer{sessions: []string{TurnScope("new")}},
		published: make(chan struct{}), resume: make(chan struct{}),
	}
	defer func() {
		select {
		case <-auth.resume:
		default:
			close(auth.resume)
		}
	}()
	dispatch := &testDispatcher{out: `{}`}
	cfg := endpointConfig(t, &testAuthorizer{}, dispatch)
	cfg.Auth, cfg.Logger = auth, logtest.Slog(t)
	ep := startEndpoint(t, cfg)
	old := dialEndpoint(t, ep)
	if _, err := io.WriteString(old, `{"jsonrpc":"2.0","id":1,"method":"workers.holdings","params":{}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-auth.published:
	case <-time.After(5 * time.Second):
		t.Fatal("old admission not published")
	}
	ep.RetireAdmissions(TurnScope("old"), 1)
	next := dialEndpoint(t, ep)
	callOver(t, next, "new-turn")
	close(auth.resume)
	ep.SettleAdmissions(TurnScope("old"), 1)
	if got := dispatch.callCount(); got != 1 {
		t.Fatalf("dispatch calls = %d, want only next turn's call", got)
	}
	if got := dispatch.lastInvocation().RunContext.Session; got != TurnScope("new") {
		t.Fatalf("dispatch authority = %q", got)
	}
	callOver(t, next, "still-new")
}
