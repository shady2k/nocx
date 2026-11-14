package app

// THE THIRD WAY IN: A BEARER MINTED FOR ONE ASSISTANT TURN (nocx-a62cl.14.2).
//
// These tests stand the shipped endpoint and the shipped authorizer up over a
// real socket and drive the real MCP bridge (internal/mcpstdio) over pipes, the
// same bridge cmd/nocx-helper's "mcp" subcommand runs. What is asserted is the
// endpoint's own decision and the invocation the assistant chain receives: the
// turn's run id, its pane, its pane access and its grant. The bridge presents
// the bearer exactly as the product's agent does — from its environment, before
// any request — so a test that passed the token to the endpoint by hand would
// be asserting about a wire nobody writes.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	coordsock "github.com/shady2k/nocx/internal/coordinator"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/peerpin"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/toolendpoint"
	"github.com/shady2k/nocx/internal/toolendpoint/panebind"
	"github.com/shady2k/nocx/internal/toolendpoint/turnbind"
	"github.com/shady2k/nocx/internal/turnbearer"
	"github.com/shady2k/nocx/internal/waittest"
)

// turnPaneAccess is a stand-in for the run's DescendantPaneAccess. The turn
// arm carries whatever the mint was given, so the test can tell the turn's
// capability from the pane caller's (which has none).
type testTurnTools struct {
	ctx        agenttools.RunContext
	grant      content.Grant
	dispatcher assistant.ToolDispatcher
	admissions int
}

func (t *testTurnTools) RunContext() agenttools.RunContext { return t.ctx }
func (t *testTurnTools) Admit() (assistant.ToolInvocation, error) {
	t.admissions++
	return assistant.ToolInvocation{RunContext: t.ctx, Grant: t.grant, Dispatcher: t.dispatcher}, nil
}

type turnPaneAccess struct{ run string }

// turnDispatcher is a recording boundary fake. It records
// every invocation it is handed and can hold one open until its context ends,
// which is what makes a retirement's cancellation observable.
type turnDispatcher struct {
	mu    sync.Mutex
	calls []assistant.ToolInvocation

	hold   bool
	held   chan struct{}
	stop   chan struct{}
	once   sync.Once
	stopOn sync.Once
}

func (d *turnDispatcher) Dispatch(inv assistant.ToolInvocation) (string, error) {
	d.mu.Lock()
	d.calls = append(d.calls, inv)
	d.mu.Unlock()
	if !d.hold || inv.Method != "alpha.hold" {
		return `{"ok":true}`, nil
	}
	d.once.Do(func() { close(d.held) })
	<-inv.Context.Done()
	d.stopOn.Do(func() { close(d.stop) })
	return "", inv.Context.Err()
}

func (d *turnDispatcher) Catalogue(content.Grant) []agenttools.Tool {
	return []agenttools.Tool{
		{
			Declaration:  agenttools.Declaration{Name: "alpha.call", Description: "answers"},
			ParamsSchema: json.RawMessage(`{"type":"object"}`),
			ResultSchema: json.RawMessage(`{"type":"object"}`),
		},
		{
			Declaration:  agenttools.Declaration{Name: "alpha.hold", Description: "waits"},
			ParamsSchema: json.RawMessage(`{"type":"object"}`),
			ResultSchema: json.RawMessage(`{"type":"object"}`),
		},
	}
}

func (d *turnDispatcher) recorded() []assistant.ToolInvocation {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]assistant.ToolInvocation(nil), d.calls...)
}

// turnStand is the endpoint, the shipped authorizer, the minter and one real
// session this process is the backend-owned root of — the facts the pane arm
// needs, so the same stand can prove a pane's caller is admitted beside a turn.
type turnStand struct {
	endpoint   *toolendpoint.Endpoint
	minter     *turnbearer.Minter
	dispatcher *turnDispatcher
	sess       session.Session
	logger     *slog.Logger
}

func newTurnStand(t *testing.T, dispatcher *turnDispatcher) *turnStand {
	t.Helper()
	reg, sess, grid := openWorkerAuthSession(t)
	if err := reg.RecordOwnedProcessPID(sess.ID(), os.Getpid()); err != nil {
		t.Fatalf("record owned process pid: %v", err)
	}
	if err := grid.Watch(string(sess.ID()), 80, 24); err != nil {
		t.Fatalf("enrol session grid: %v", err)
	}
	auth, err := newToolAuthorizer(peerpin.SystemPinner{}, reg, grid, nil, workerTestWorkspace, allowWorkerApproval{})
	if err != nil {
		t.Fatalf("tool authorizer: %v", err)
	}
	logger := logtest.Slog(t)
	endpoint, err := toolendpoint.New(toolendpoint.Config{
		Dir:      shortWorkerSocketDir(t),
		Peers:    coordsock.SystemPeerCredentials{},
		Owner:    coordsock.SystemPathOwner{},
		SelfUID:  uint32(os.Getuid()), //nolint:gosec // a uid is not a signed quantity
		Auth:     auth,
		Dispatch: dispatcher,
		Logger:   logger,
	})
	if err != nil {
		t.Fatalf("new turn endpoint: %v", err)
	}
	// Bound BEFORE the socket accepts anything: the accept path asks the
	// authorizer whether it admits turn bearers, and that answer must not
	// change under a connection.
	minter := turnbearer.NewMinter(endpoint, "nocx-helper")
	auth.BindTurnGrants(minter)
	if err := endpoint.Start(); err != nil {
		t.Fatalf("start turn endpoint: %v", err)
	}
	t.Cleanup(func() { _ = endpoint.Close() })
	return &turnStand{endpoint: endpoint, minter: minter, dispatcher: dispatcher, sess: sess, logger: logger}
}

// turnGrantForTest is an authority nothing else in this stand mints: a grant
// scoped to a path no caller or pane arm produces, so "the call carried the
// turn's grant" cannot pass by both grants being the same zero value.
func turnGrantForTest() content.Grant {
	permit := content.EffectRow{Decision: content.DecisionPermit}
	return content.EffectPolicy{Observe: permit}.AsGrant([]content.GrantScope{
		{Kind: content.ResourcePath, ID: "/turn-only"},
	})
}

// envToken reads the bearer out of the environment the mint handed back.
func envToken(t *testing.T, env []string) string {
	t.Helper()
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, turnbind.TokenEnvVar+"="); ok {
			return value
		}
	}
	t.Fatalf("the minted environment carries no bearer: %v", env)
	return ""
}

// callWithToken drives one real bridge presenting token and returns its answer.
func callWithToken(t *testing.T, socket, token, method string, args any) toolResponse {
	t.Helper()
	client := newS14MCPClientWithToken(t, socket, token)
	return s14Await(t, client.startCall(t, method, args))
}

func TestATurnBearerIsAdmittedAndItsCallCarriesTheTurnsGrant(t *testing.T) {
	stand := newTurnStand(t, &turnDispatcher{})
	grant := turnGrantForTest()
	paneAccess := &turnPaneAccess{run: "run-turn-1"}
	bridge, _, err := stand.minter.Mint(turnbearer.Authority{
		Tools: &testTurnTools{ctx: agenttools.RunContext{RunID: "run-turn-1", Session: string(stand.sess.ID()), PaneAccess: paneAccess}, grant: grant, dispatcher: stand.dispatcher},
	})
	if err != nil {
		t.Fatalf("mint a turn bearer: %v", err)
	}
	command := strings.Join(bridge.Command, " ")
	if want := "nocx-helper mcp --socket " + stand.endpoint.SocketPath(); command != want {
		t.Fatalf("the bridge argv is %q, want %q", command, want)
	}
	token := envToken(t, bridge.Env)
	if strings.Contains(command, token) {
		t.Fatal("the bearer travelled in the bridge argv, where every process can read it")
	}
	if len(token) != panebind.TokenHexLen {
		t.Fatalf("the bearer is %d characters, want %d", len(token), panebind.TokenHexLen)
	}
	if _, err = hex.DecodeString(token); err != nil {
		t.Fatalf("invalid bearer hex: %v", err)
	}

	second, _, err := stand.minter.Mint(turnbearer.Authority{
		Tools: &testTurnTools{ctx: agenttools.RunContext{RunID: "run-turn-2", Session: string(stand.sess.ID())}, grant: grant, dispatcher: stand.dispatcher},
	})
	if err != nil {
		t.Fatalf("mint a second turn bearer: %v", err)
	}
	if envToken(t, second.Env) == token {
		t.Fatal("two turns minted the same bearer")
	}

	if resp := callWithToken(t, stand.endpoint.SocketPath(), token, "alpha.call", map[string]any{"n": 1}); resp.failed != "" {
		t.Fatalf("a live turn's call was refused: %s", resp.failed)
	}
	calls := stand.dispatcher.recorded()
	if len(calls) != 1 {
		t.Fatalf("the assistant chain received %d calls, want the turn's one: %+v", len(calls), calls)
	}
	call := calls[0]
	if call.RunContext.RunID != "run-turn-1" {
		t.Fatalf("the call's run id is %q, want the turn's", call.RunContext.RunID)
	}
	if call.RunContext.Session != string(stand.sess.ID()) {
		t.Fatalf("the call's session is %q, want the turn's pane %q", call.RunContext.Session, stand.sess.ID())
	}
	if call.RunContext.PaneAccess != paneAccess {
		t.Fatalf("the call's pane access is %v, want the turn's %s", call.RunContext.PaneAccess, paneAccess.run)
	}
	if !reflect.DeepEqual(call.Grant, grant) {
		t.Fatalf("the call's grant is %+v, want the turn's %+v", call.Grant, grant)
	}
	assertNotLogged(t, stand.logger, token)
}

// assertNotLogged is the bearer's own rule: it is a secret that admits, so no
// log line this endpoint writes may carry it.
func assertNotLogged(t *testing.T, logger *slog.Logger, token string) {
	t.Helper()
	for _, record := range logtest.RecordsSlog(logger) {
		if strings.Contains(record.Message, token) {
			t.Fatalf("the bearer reached a log line: %q", record.Message)
		}
		for _, attr := range record.Attrs {
			if strings.Contains(attr.Value.String(), token) {
				t.Fatalf("the bearer reached a log attribute %q: %q", attr.Key, attr.Value.String())
			}
		}
	}
}

func TestAWrongOrRetiredTurnBearerIsRefused(t *testing.T) {
	stand := newTurnStand(t, &turnDispatcher{})
	grant := turnGrantForTest()
	bridge, retire, err := stand.minter.Mint(turnbearer.Authority{
		Tools: &testTurnTools{ctx: agenttools.RunContext{RunID: "run-retired", Session: string(stand.sess.ID())}, grant: grant, dispatcher: stand.dispatcher},
	})
	if err != nil {
		t.Fatalf("mint a turn bearer: %v", err)
	}
	retire()

	cases := map[string]string{
		"wrong":   strings.Repeat("ab", 32),
		"retired": envToken(t, bridge.Env),
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			resp := callWithToken(t, stand.endpoint.SocketPath(), token, "alpha.call", map[string]any{})
			if resp.failed == "" {
				t.Fatal("the call was admitted under a bearer no live turn minted")
			}
			if !strings.Contains(resp.failed, "live assistant turn") {
				t.Fatalf("the refusal reads %q, want the turn bearer's own sentence", resp.failed)
			}
			if calls := stand.dispatcher.recorded(); len(calls) != 0 {
				t.Fatalf("a refused connection reached the assistant chain: %+v", calls)
			}
		})
	}
}

func TestRetiringATurnCancelsItsBlockedCallAndClosesItsConnection(t *testing.T) {
	dispatcher := &turnDispatcher{hold: true, held: make(chan struct{}), stop: make(chan struct{})}
	stand := newTurnStand(t, dispatcher)
	bridge, retire, err := stand.minter.Mint(turnbearer.Authority{
		Tools: &testTurnTools{ctx: agenttools.RunContext{RunID: "run-held", Session: string(stand.sess.ID())}, grant: turnGrantForTest(), dispatcher: stand.dispatcher},
	})
	if err != nil {
		t.Fatalf("mint a turn bearer: %v", err)
	}
	client := newS14MCPClientWithToken(t, stand.endpoint.SocketPath(), envToken(t, bridge.Env))
	held := client.startCall(t, "alpha.hold", map[string]any{})
	select {
	case <-dispatcher.held:
	case <-time.After(10 * time.Second):
		t.Fatal("the held call never reached the assistant chain")
	}

	retire()

	select {
	case <-dispatcher.stop:
	case <-time.After(10 * time.Second):
		t.Fatal("retiring the turn did not cancel its blocked call")
	}
	if resp := s14Await(t, held); resp.failed == "" {
		t.Fatalf("the blocked call answered %+v after its turn was retired, want a failure", resp)
	}
	// The connection is gone, and the bearer it would present is retired: the
	// same bridge cannot go on working by reconnecting.
	later := s14Await(t, client.startCall(t, "alpha.call", map[string]any{}))
	if !strings.Contains(later.failed, "live assistant turn") {
		t.Fatalf("a call after the turn was retired answered %q, want the refusal", later.failed)
	}
	waittest.WaitFor(t, "the turn's connection to be gone", func() bool {
		for _, admitted := range stand.endpoint.AdmittedSessions() {
			if admitted == toolendpoint.TurnScope("run-held") {
				return false
			}
		}
		return true
	})
}

func TestATurnBearerIsAdmittedAgainAfterItsBridgeReconnects(t *testing.T) {
	stand := newTurnStand(t, &turnDispatcher{})
	bridge, _, err := stand.minter.Mint(turnbearer.Authority{
		Tools: &testTurnTools{ctx: agenttools.RunContext{RunID: "run-reconnect", Session: string(stand.sess.ID())}, grant: turnGrantForTest(), dispatcher: stand.dispatcher},
	})
	if err != nil {
		t.Fatalf("mint a turn bearer: %v", err)
	}
	token := envToken(t, bridge.Env)
	first := newS14MCPClientWithToken(t, stand.endpoint.SocketPath(), token)
	if resp := s14Await(t, first.startCall(t, "alpha.call", map[string]any{})); resp.failed != "" {
		t.Fatalf("the turn's first call was refused: %s", resp.failed)
	}
	// The bridge's own session ends — the same thing that happens when the
	// agent's MCP client drops the pipe — and the turn goes on.
	if err := first.writer.Close(); err != nil {
		t.Fatalf("close the first bridge: %v", err)
	}
	waittest.WaitFor(t, "the turn's first connection to end", func() bool {
		for _, admitted := range stand.endpoint.AdmittedSessions() {
			if admitted == toolendpoint.TurnScope("run-reconnect") {
				return false
			}
		}
		return true
	})
	second := newS14MCPClientWithToken(t, stand.endpoint.SocketPath(), token)
	if resp := s14Await(t, second.startCall(t, "alpha.call", map[string]any{})); resp.failed != "" {
		t.Fatalf("a reconnect within the turn was refused: %s", resp.failed)
	}
}

func TestAPanesCallerIsAdmittedAlongsideATurnBearer(t *testing.T) {
	dispatcher := &turnDispatcher{}
	stand := newTurnStand(t, dispatcher)
	bridge, _, err := stand.minter.Mint(turnbearer.Authority{
		Tools: &testTurnTools{ctx: agenttools.RunContext{RunID: "run-beside", Session: string(stand.sess.ID())}, grant: turnGrantForTest(), dispatcher: stand.dispatcher},
	})
	if err != nil {
		t.Fatalf("mint a turn bearer: %v", err)
	}
	turnClient := newS14MCPClientWithToken(t, stand.endpoint.SocketPath(), envToken(t, bridge.Env))
	if resp := s14Await(t, turnClient.startCall(t, "alpha.call", map[string]any{})); resp.failed != "" {
		t.Fatalf("the turn's call was refused: %s", resp.failed)
	}
	// A pane's own caller presents no bearer; it is admitted by the process
	// tree this test is the root of, on the slot the pane already owns.
	paneClient := newS14MCPClient(t, stand.endpoint.SocketPath())
	if resp := s14Await(t, paneClient.startCall(t, "alpha.call", map[string]any{})); resp.failed != "" {
		t.Fatalf("the pane caller's call was refused while a turn held its own slot: %s", resp.failed)
	}
	waittest.WaitFor(t, "both the turn and the pane caller to be admitted", func() bool {
		seen := map[string]bool{}
		for _, admitted := range stand.endpoint.AdmittedSessions() {
			seen[admitted] = true
		}
		return seen[toolendpoint.TurnScope("run-beside")] && seen[string(stand.sess.ID())]
	})
	calls := dispatcher.recorded()
	if len(calls) != 2 {
		t.Fatalf("the assistant chain received %d calls, want the turn's and the pane caller's: %+v", len(calls), calls)
	}
	var turn, pane bool
	for _, call := range calls {
		switch {
		case call.RunContext.RunID == "run-beside":
			turn = true
		case call.RunContext.RunID == "" && call.RunContext.Session == string(stand.sess.ID()):
			pane = true
		}
	}
	if !turn || !pane {
		t.Fatalf("the chain saw turn=%v pane=%v, want both: %+v", turn, pane, calls)
	}
}

func TestTurnAdmissionUsesBoundContextAndMintsOnEveryConnection(t *testing.T) {
	stand := newTurnStand(t, &turnDispatcher{})
	bound := agenttools.RunContext{
		RunID: "bound", Session: string(stand.sess.ID()),
		AutomaticSessionItems: []string{"attachment"},
		ControllerIdentity:    session.Identity{},
		PaneAccess:            &turnPaneAccess{run: "bound"},
	}
	tools := &testTurnTools{ctx: bound, grant: turnGrantForTest(), dispatcher: stand.dispatcher}
	bridge, _, err := stand.minter.Mint(turnbearer.Authority{Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	if tools.admissions != 0 {
		t.Fatal("Mint minted a grant before admission")
	}
	first := newS14MCPClientWithToken(t, stand.endpoint.SocketPath(), envToken(t, bridge.Env))
	if resp := s14Await(t, first.startCall(t, "alpha.call", map[string]any{})); resp.failed != "" {
		t.Fatal(resp.failed)
	}
	if calls := stand.dispatcher.recorded(); len(calls) != 1 || !reflect.DeepEqual(calls[0].RunContext, bound) {
		t.Fatalf("admission reconstructed the bound run context: %+v", calls)
	}
	second := newS14MCPClientWithToken(t, stand.endpoint.SocketPath(), envToken(t, bridge.Env))
	if resp := s14Await(t, second.startCall(t, "alpha.call", map[string]any{})); resp.failed == "" {
		t.Fatal("simultaneous second connection was admitted")
	}
	if err := first.writer.Close(); err != nil {
		t.Fatal(err)
	}
	waittest.WaitFor(t, "first connection release", func() bool { return len(stand.endpoint.AdmittedSessions()) == 0 })
	third := newS14MCPClientWithToken(t, stand.endpoint.SocketPath(), envToken(t, bridge.Env))
	if resp := s14Await(t, third.startCall(t, "alpha.call", map[string]any{})); resp.failed != "" {
		t.Fatal(resp.failed)
	}
	if tools.admissions != 2 {
		t.Fatalf("grant owner called %d times, want admission and reconnect", tools.admissions)
	}
}

type turnSecret struct{ value string }

func (s turnSecret) FindKnown(_ context.Context, text string) ([]assistant.KnownMatch, error) {
	if at := strings.Index(text, s.value); at >= 0 {
		return []assistant.KnownMatch{{Start: at, End: at + len(s.value), SecretName: "test-secret"}}, nil
	}
	return nil, nil
}

type turnPerson struct {
	store  *assistant.ApprovalStore
	asked  chan *assistant.ApprovalRequest
	answer chan struct{}
	egress chan struct{}
}

func (q *turnPerson) Ask(ctx context.Context, req *assistant.ApprovalRequest) assistant.Answer {
	q.asked <- req
	select {
	case <-q.answer:
	case <-ctx.Done():
		return assistant.Unanswered{Err: ctx.Err()}
	}
	q.store.Approve(assistant.Approval{RunID: req.RunID, Attempt: req.Attempt, Tool: req.Tool, CallID: req.CallID, ArgHash: req.ArgHash, Effect: req.Effect})
	return assistant.Approved{}
}

func (q *turnPerson) AskEgress(_ context.Context, req *assistant.EgressRequest) assistant.Answer {
	close(q.egress)
	return assistant.Declined{Reason: "keep the secret local"}
}

func TestTurnBearerSelectsRealAssistantApprovalAndEgressChain(t *testing.T) {
	stand := newTurnStand(t, &turnDispatcher{})
	client, _, err := assistant.NewClientAndRegistry(log.NewSlogAdapter(nil), nil, content.Floor{})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.txt")
	secret := "a-bearer-test-secret-not-for-the-agent" //nolint:gosec // synthetic vault material for the egress test
	if err = os.WriteFile(path, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	grant := content.EffectPolicy{Observe: content.EffectRow{Decision: content.DecisionAsk}}.AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: dir}})
	store := assistant.NewApprovalStore()
	person := &turnPerson{store: store, asked: make(chan *assistant.ApprovalRequest, 1), answer: make(chan struct{}), egress: make(chan struct{})}
	tools, err := assistant.BindRunTools(client, assistant.AskParams{
		Grant: &grant, FreshGrant: func() *content.Grant { return &grant },
		RunID: "real-chain", SessionID: string(stand.sess.ID()), Attempt: 1,
		AttemptLedger: attemptLedgerForTest(t), KnownMaterial: turnSecret{value: secret}, Approvals: store, Questioner: person,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	bridge, _, err := stand.minter.Mint(turnbearer.Authority{Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	bridgeClient := newS14MCPClientWithToken(t, stand.endpoint.SocketPath(), envToken(t, bridge.Env))
	result := bridgeClient.startCall(t, "files.read", map[string]any{"path": path})
	select {
	case <-person.asked:
	case early := <-result:
		t.Fatalf("call answered without reaching assistant approval: %s", early.raw)
	case <-time.After(10 * time.Second):
		t.Fatal("assistant ask did not reach approval service")
	}
	select {
	case <-result:
		t.Fatal("call answered before approval")
	default:
	}
	close(person.answer)
	resp := s14Await(t, result)
	select {
	case <-person.egress:
	default:
		t.Fatal("assistant egress screen was bypassed")
	}
	if strings.Contains(string(resp.result), secret) || strings.Contains(resp.failed, secret) {
		t.Fatal("declined secret reached bridge")
	}
	if len(stand.dispatcher.recorded()) != 0 {
		t.Fatal("turn routed through pane dispatcher")
	}
}
