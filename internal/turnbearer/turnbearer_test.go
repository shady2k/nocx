package turnbearer

import (
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/toolendpoint"
)

type testEndpoint struct {
	pending bool
	entered chan string
	release chan struct{}
}

func (*testEndpoint) SocketPath() string { return "/test.sock" }
func (e *testEndpoint) SettleAdmissions(scope string, _ toolendpoint.AdmissionEpoch) {
	if e.entered != nil {
		e.entered <- scope
	}
	if e.release != nil {
		<-e.release
	}
}

func (e *testEndpoint) PendingAdmissions(string, toolendpoint.AdmissionEpoch) bool { return e.pending }

type testRunTools string

func (r testRunTools) RunContext() agenttools.RunContext {
	return agenttools.RunContext{RunID: string(r), Session: "pane"}
}

func (testRunTools) Admit() (assistant.ToolInvocation, error) { return assistant.ToolInvocation{}, nil }

func mint(t *testing.T, m *Minter, session string) (string, func()) {
	t.Helper()
	b, retire, err := m.Mint(session)
	if err != nil {
		t.Fatal(err)
	}
	return strings.SplitN(b.Env[0], "=", 2)[1], retire
}

func begin(t *testing.T, m *Minter, session, run string) {
	t.Helper()
	if err := m.BeginTurn(session, Authority{Tools: testRunTools(run)}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionBearerSurvivesTurnsWithoutRebindingAuthority(t *testing.T) {
	m := NewMinter(&testEndpoint{}, "helper")
	token, retire := mint(t, m, "session")
	defer retire()
	if _, ok := m.Lookup(token); ok {
		t.Fatal("idle bearer admitted")
	}
	begin(t, m, "session", "first")
	first, ok := m.Lookup(token)
	if !ok {
		t.Fatal("active bearer refused")
	}
	m.EndTurn("session")
	if _, admitted := m.Lookup(token); admitted {
		t.Fatal("idle bearer admitted after end")
	}
	begin(t, m, "session", "second")
	second, ok := m.Lookup(token)
	if !ok || second.Tools.RunContext().RunID != "second" {
		t.Fatal("same bearer did not admit next turn")
	}
	if first.Tools.RunContext().RunID != "first" {
		t.Fatal("old authority rebound")
	}
	if first.Publish(func() bool { t.Fatal("old authority published"); return true }) {
		t.Fatal("ended authority live")
	}
	if !second.Publish(func() bool { return true }) {
		t.Fatal("new authority refused")
	}
	retire()
	if _, ok := m.Lookup(token); ok {
		t.Fatal("retired bearer admitted")
	}
	if second.Publish(func() bool { return true }) {
		t.Fatal("retired authority live")
	}
}

func TestEndTurnWaitsForSettlementAndRejectsOverlap(t *testing.T) {
	e := &testEndpoint{entered: make(chan string, 1), release: make(chan struct{})}
	m := NewMinter(e, "helper")
	token, retire := mint(t, m, "session")
	begin(t, m, "session", "first")
	if err := m.BeginTurn("session", Authority{Tools: testRunTools("overlap")}); err == nil {
		t.Fatal("overlap admitted")
	}
	a, _ := m.Lookup(token)
	done := make(chan struct{})
	go func() { m.EndTurn("session"); close(done) }()
	if scope := <-e.entered; scope != toolendpoint.TurnScope("first") {
		t.Fatal(scope)
	}
	if _, ok := m.Lookup(token); ok {
		t.Fatal("settling bearer admitted")
	}
	if a.Publish(func() bool { return true }) {
		t.Fatal("settling authority published")
	}
	if err := m.BeginTurn("session", Authority{Tools: testRunTools("second")}); err == nil {
		t.Fatal("turn admitted before settlement")
	}
	select {
	case <-done:
		t.Fatal("EndTurn returned before settlement")
	default:
	}
	close(e.release)
	<-done
	begin(t, m, "session", "second")
	retire()
}

func TestPendingTracksAcceptedCalls(t *testing.T) {
	e := &testEndpoint{pending: true}
	m := NewMinter(e, "helper")
	_, retire := mint(t, m, "session")
	defer retire()
	if m.Pending("session") {
		t.Fatal("idle session pending")
	}
	begin(t, m, "session", "run")
	if !m.Pending("session") {
		t.Fatal("pending endpoint activity lost")
	}
	e.pending = false
	if m.Pending("session") {
		t.Fatal("settled calls pending")
	}
}

func TestSessionValidationAndRetirement(t *testing.T) {
	m := NewMinter(&testEndpoint{}, "helper")
	if _, _, err := m.Mint(""); err == nil {
		t.Fatal("empty session admitted")
	}
	_, retire := mint(t, m, "session")
	if _, _, err := m.Mint("session"); err == nil {
		t.Fatal("duplicate session admitted")
	}
	if err := m.BeginTurn("missing", Authority{Tools: testRunTools("run")}); err == nil {
		t.Fatal("unknown session admitted")
	}
	if err := m.BeginTurn("session", Authority{}); err == nil {
		t.Fatal("nil tools admitted")
	}
	if err := m.BeginTurn("session", Authority{Tools: testRunTools("")}); err == nil {
		t.Fatal("empty run admitted")
	}
	retire()
	retire()
	if err := m.BeginTurn("session", Authority{Tools: testRunTools("run")}); err == nil {
		t.Fatal("retired session admitted")
	}
	m.EndTurn("missing")
	_, newRetire := mint(t, m, "session")
	newRetire()
}

func TestRetirementSerializesWithPublication(t *testing.T) {
	m := NewMinter(&testEndpoint{}, "helper")
	token, retire := mint(t, m, "session")
	begin(t, m, "session", "run")
	a, _ := m.Lookup(token)
	entered, release := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); a.Publish(func() bool { close(entered); <-release; return true }) }()
	<-entered
	go func() { defer wg.Done(); retire() }()
	close(release)
	wg.Wait()
	if a.Publish(func() bool { return true }) {
		t.Fatal("retired publication allowed")
	}
}

func TestRetireWaitsForAcceptedCallsAndRemovesBearerImmediately(t *testing.T) {
	e := &testEndpoint{entered: make(chan string, 1), release: make(chan struct{})}
	m := NewMinter(e, "helper")
	token, retire := mint(t, m, "session")
	begin(t, m, "session", "run")
	authority, _ := m.Lookup(token)
	done := make(chan struct{})
	go func() { retire(); close(done) }()
	<-e.entered
	if _, ok := m.Lookup(token); ok {
		t.Fatal("retiring bearer admitted")
	}
	if authority.Publish(func() bool { return true }) {
		t.Fatal("retiring authority published")
	}
	if err := m.BeginTurn("session", Authority{Tools: testRunTools("next")}); err == nil {
		t.Fatal("retiring session admitted turn")
	}
	select {
	case <-done:
		t.Fatal("retirement returned before calls settled")
	default:
	}
	close(e.release)
	<-done
	retire()
}

func TestRetireClearsAuthorityAndSecret(t *testing.T) {
	m := NewMinter(&testEndpoint{}, "helper")
	_, retire := mint(t, m, "session")
	begin(t, m, "session", "run")
	s := m.sessions["session"]
	retire()
	if s.token != "" || s.authority.Tools != nil {
		t.Fatal("retirement retained secret or authority")
	}
}
