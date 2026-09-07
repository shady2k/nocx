package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panegrid"
	"github.com/shady2k/nocx/internal/peerpin"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/toolendpoint"
	"github.com/shady2k/nocx/internal/workers"
)

type workerAuthPTYFactory struct {
	log log.Logger
}

func (f workerAuthPTYFactory) NewPTY(context.Context, pty.Config) (pty.Pty, error) {
	return pty.NewStub(f.log), nil
}

type workerAuthPinner struct {
	root       peerpin.Root
	pinPIDs    []int
	memberPIDs []int
	member     map[int]bool
}

func (p *workerAuthPinner) Pin(pid int) (peerpin.Root, error) {
	p.pinPIDs = append(p.pinPIDs, pid)
	if pid != p.root.PID {
		return peerpin.Root{}, peerpin.ErrGone
	}
	return p.root, nil
}

func (p *workerAuthPinner) Member(child int, root peerpin.Root) (bool, error) {
	p.memberPIDs = append(p.memberPIDs, child)
	if root != p.root {
		return false, peerpin.ErrGone
	}
	return p.member[child], nil
}

// emptyWorkerRecord is a real record holding nobody. The coordinator tests use
// it rather than a nil so they exercise the real "is this session a
// participant" lookup and take the coordinator branch because the ANSWER is
// no, not because the seam was missing.
func emptyWorkerRecord() *workers.Registrar {
	return workers.NewRegistrar(workers.NewMemoryStore(), nil, nil, nil)
}

func openWorkerAuthSession(t *testing.T) (*session.Reg, session.Session, *panegrid.Store) {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	reg := session.New(logger, workerAuthPTYFactory{log: logger})
	sess, err := reg.Open(context.Background(), session.Config{Kind: session.KindLocal, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close(sess.ID()) })
	grid := panegrid.New(logger)
	t.Cleanup(func() { grid.Withdraw(string(sess.ID())) })
	return reg, sess, grid
}

type workerAuthSessionOverride struct {
	session.Session
	kind session.Kind
	host string
}

func (s workerAuthSessionOverride) Kind() session.Kind { return s.kind }
func (s workerAuthSessionOverride) Host() string       { return s.host }

type workerAuthSessionSet struct {
	sessions []session.Session
	owned    map[session.ID]int
}

func (s workerAuthSessionSet) List() []session.Session { return s.sessions }

func (s workerAuthSessionSet) OwnedProcessPID(id session.ID) (int, bool) {
	pid, ok := s.owned[id]
	return pid, ok
}

func TestToolAuthorizerAdmitsEnrolledOwnedTreeThroughRealWorkerRecord(t *testing.T) {
	reg, sess, grid := openWorkerAuthSession(t)
	const ownedPID = 4242
	if err := reg.RecordOwnedProcessPID(sess.ID(), ownedPID); err != nil {
		t.Fatalf("record owned process pid: %v", err)
	}
	if err := grid.Enrol(string(sess.ID()), 80, 24); err != nil {
		t.Fatalf("enrol session grid: %v", err)
	}

	root := peerpin.Root{PID: ownedPID, StartTime: time.Unix(123, 0)}
	pinner := &workerAuthPinner{root: root, member: map[int]bool{9001: true}}
	auth := newToolAuthorizer(pinner, reg, grid, emptyWorkerRecord(), workerTestWorkspace)

	inv, _, err := auth.Admit(toolendpoint.Peer{UID: 1000, PID: 9001})
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
	workerStore := workers.NewRegistrar(workers.NewMemoryStore(), nil, nil, nil)
	dispatcher, err := assistant.NewToolDispatcher(registry, workerStore, localEnv)
	if err != nil {
		t.Fatalf("new worker dispatcher: %v", err)
	}
	inv.Method = "workers.holdings"
	inv.RawParams = []byte(`{}`)
	result, err := dispatcher.Dispatch(inv)
	if err != nil {
		t.Fatalf("dispatch admitted holdings: %v", err)
	}
	if result == "" {
		t.Fatal("dispatch returned an empty holdings result")
	}
}

func TestWorkerCallerGrantDerivesEnvironmentFromSession(t *testing.T) {
	_, local, _ := openWorkerAuthSession(t)
	remote := workerAuthSessionOverride{
		Session: local,
		kind:    session.KindRemote,
		host:    "build.example.com",
	}
	environmentID := workerEnvironmentForSession(remote)
	grant := callerGrant(remote.ID(), environmentID)
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

func TestToolAuthorizerRefusesCallerOutsideEveryEnrolledTree(t *testing.T) {
	reg, sess, grid := openWorkerAuthSession(t)
	const ownedPID = 4242
	if err := reg.RecordOwnedProcessPID(sess.ID(), ownedPID); err != nil {
		t.Fatalf("record owned process pid: %v", err)
	}
	if err := grid.Enrol(string(sess.ID()), 80, 24); err != nil {
		t.Fatalf("enrol session grid: %v", err)
	}

	pinner := &workerAuthPinner{
		root:   peerpin.Root{PID: ownedPID, StartTime: time.Unix(123, 0)},
		member: map[int]bool{9001: false},
	}
	auth := newToolAuthorizer(pinner, reg, grid, emptyWorkerRecord(), workerTestWorkspace)
	_, _, err := auth.Admit(toolendpoint.Peer{UID: 1000, PID: 9001})
	if !errors.Is(err, toolendpoint.ErrNotEnrolled) {
		t.Fatalf("outside-tree admission error = %v, want ErrNotEnrolled", err)
	}
}

func TestToolAuthorizerWithdrawClosesAdmissionInterval(t *testing.T) {
	reg, sess, grid := openWorkerAuthSession(t)
	const ownedPID = 4242
	if err := reg.RecordOwnedProcessPID(sess.ID(), ownedPID); err != nil {
		t.Fatalf("record owned process pid: %v", err)
	}
	if err := grid.Enrol(string(sess.ID()), 80, 24); err != nil {
		t.Fatalf("enrol session grid: %v", err)
	}

	pinner := &workerAuthPinner{
		root:   peerpin.Root{PID: ownedPID, StartTime: time.Unix(123, 0)},
		member: map[int]bool{9001: true},
	}
	auth := newToolAuthorizer(pinner, reg, grid, emptyWorkerRecord(), workerTestWorkspace)
	if _, _, err := auth.Admit(toolendpoint.Peer{UID: 1000, PID: 9001}); err != nil {
		t.Fatalf("admit before withdrawal: %v", err)
	}
	grid.Withdraw(string(sess.ID()))
	if _, _, err := auth.Admit(toolendpoint.Peer{UID: 1000, PID: 9001}); !errors.Is(err, toolendpoint.ErrNotEnrolled) {
		t.Fatalf("admit after withdrawal error = %v, want ErrNotEnrolled", err)
	}
}

type workerAuthEndpointPeers struct{}

func (workerAuthEndpointPeers) PeerUID(*net.UnixConn) (uint32, error) { return 1000, nil }
func (workerAuthEndpointPeers) PeerPID(*net.UnixConn) (int, error)    { return 9001, nil }

type workerAuthEndpointOwner struct{}

func (workerAuthEndpointOwner) OwnerUID(string) (uint32, error) { return 1000, nil }

func TestWorkerToolCallAfterLifecycleLossIsRefusedWithoutParticipant(t *testing.T) {
	reg, sess, grid := openWorkerAuthSession(t)
	const ownedPID = 4242
	if err := reg.RecordOwnedProcessPID(sess.ID(), ownedPID); err != nil {
		t.Fatalf("record owned process pid: %v", err)
	}
	if err := grid.Enrol(string(sess.ID()), 80, 24); err != nil {
		t.Fatalf("enrol session grid: %v", err)
	}

	workerStore := workers.NewMemoryStore()
	record := workers.NewRegistrar(workerStore, nil, nil, nil)
	pinner := &workerAuthPinner{
		root:   peerpin.Root{PID: ownedPID, StartTime: time.Unix(123, 0)},
		member: map[int]bool{9001: true},
	}
	auth := newToolAuthorizer(pinner, reg, grid, record, workerTestWorkspace)
	registry, err := agenttools.Assemble(os.DirFS("../../contracts/tools"))
	if err != nil {
		t.Fatalf("assemble tools: %v", err)
	}
	dispatcher, err := assistant.NewToolDispatcher(
		registry, record, content.EnvironmentIDFor(content.EnvLocal, ""),
	)
	if err != nil {
		t.Fatalf("new worker dispatcher: %v", err)
	}
	ep, err := toolendpoint.New(toolendpoint.Config{
		Dir:      t.TempDir(),
		Peers:    workerAuthEndpointPeers{},
		Owner:    workerAuthEndpointOwner{},
		SelfUID:  1000,
		Auth:     auth,
		Dispatch: dispatcher,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("new tool endpoint: %v", err)
	}
	if err := ep.Start(); err != nil {
		t.Fatalf("start tool endpoint: %v", err)
	}
	t.Cleanup(func() { _ = ep.Close() })

	call := func(id string, send bool) (int, string, string) {
		t.Helper()
		conn, err := net.Dial("unix", ep.SocketPath())
		if err != nil {
			t.Fatalf("dial tool endpoint: %v", err)
		}
		defer func() { _ = conn.Close() }()
		if send {
			if _, writeErr := io.WriteString(conn, `{"jsonrpc":"2.0","id":"`+id+`","method":"workers.holdings","params":{}}`+"\n"); writeErr != nil {
				t.Fatalf("write workers.holdings: %v", writeErr)
			}
		}
		if deadlineErr := conn.SetReadDeadline(time.Now().Add(time.Second)); deadlineErr != nil {
			t.Fatalf("set response deadline: %v", deadlineErr)
		}
		line, err := bufio.NewReader(conn).ReadBytes('\n')
		if err != nil {
			t.Fatalf("read workers.holdings response: %v", err)
		}
		var response struct {
			Error *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
				Data    struct {
					Reason string `json:"reason"`
				} `json:"data"`
			} `json:"error"`
		}
		if err := json.Unmarshal(line, &response); err != nil {
			t.Fatalf("decode workers.holdings response %q: %v", line, err)
		}
		if response.Error == nil {
			return 0, "", ""
		}
		return response.Error.Code, response.Error.Message, response.Error.Data.Reason
	}

	if code, message, reason := call("before-loss", true); code != 0 || message != "" || reason != "" {
		t.Fatalf("healthy workers.holdings = %d/%q/%q, want success", code, message, reason)
	}

	// This is the lifecycle channel's death while the caller process remains
	// alive: the pane's enrolment interval closes, but no worker record exists.
	grid.Withdraw(string(sess.ID()))
	code, message, reason := call("after-loss", false)
	if code != -32001 || message != "worker caller refused" ||
		reason != "caller is not in an enrolled process tree" {
		t.Fatalf("workers.holdings after lifecycle loss = %d/%q/%q, want the named refusal",
			code, message, reason)
	}
	if _, err := record.ParticipantOf(context.Background(), string(sess.ID())); !errors.Is(err, workers.ErrNoSuchParticipant) {
		t.Fatalf("lifecycle loss created a participant: err = %v", err)
	}
}

func TestToolAuthorizerRefusesEnrolledSessionWithoutOwnedProcess(t *testing.T) {
	reg, sess, grid := openWorkerAuthSession(t)
	if err := grid.Enrol(string(sess.ID()), 80, 24); err != nil {
		t.Fatalf("enrol session grid: %v", err)
	}
	pinner := &workerAuthPinner{
		root:   peerpin.Root{PID: 4242, StartTime: time.Unix(123, 0)},
		member: map[int]bool{9001: true},
	}
	auth := newToolAuthorizer(pinner, reg, grid, emptyWorkerRecord(), workerTestWorkspace)
	_, _, err := auth.Admit(toolendpoint.Peer{UID: 1000, PID: 9001})
	if !errors.Is(err, toolendpoint.ErrNotEnrolled) {
		t.Fatalf("unknown-owned-pid admission error = %v, want ErrNotEnrolled", err)
	}
}

func TestToolAuthorizerRefusesRemoteSessionWithoutOwnedProcess(t *testing.T) {
	_, local, grid := openWorkerAuthSession(t)
	remote := workerAuthSessionOverride{
		Session: local,
		kind:    session.KindRemote,
		host:    "build.example.com",
	}
	if err := grid.Enrol(string(remote.ID()), 80, 24); err != nil {
		t.Fatalf("enrol session grid: %v", err)
	}
	sessions := workerAuthSessionSet{
		sessions: []session.Session{remote},
		owned:    map[session.ID]int{},
	}
	pinner := &workerAuthPinner{
		root:   peerpin.Root{PID: 4242, StartTime: time.Unix(123, 0)},
		member: map[int]bool{9001: true},
	}
	auth := newToolAuthorizer(pinner, sessions, grid, emptyWorkerRecord(), workerTestWorkspace)
	_, _, err := auth.Admit(toolendpoint.Peer{UID: 1000, PID: 9001})
	if !errors.Is(err, toolendpoint.ErrNotEnrolled) {
		t.Fatalf("remote session admission error = %v, want ErrNotEnrolled", err)
	}
}

func TestToolDispatcherRefusesSpawnOutsideCoordinatorEnvironment(t *testing.T) {
	registry, err := agenttools.Assemble(os.DirFS("../../contracts/tools"))
	if err != nil {
		t.Fatalf("assemble tools: %v", err)
	}
	const (
		availableEnvironment = "env-available"
		askedEnvironment     = "env-asked"
	)
	dispatcher, err := assistant.NewToolDispatcher(
		registry,
		emptyWorkerRecord(),
		availableEnvironment,
	)
	if err != nil {
		t.Fatalf("new worker dispatcher: %v", err)
	}
	_, err = dispatcher.Dispatch(assistant.ToolInvocation{
		Context:    context.Background(),
		Method:     "workers.spawn",
		RunContext: agenttools.RunContext{Session: "sess-coordinator"},
		Grant:      callerGrant(session.ID("sess-coordinator"), askedEnvironment),
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

func TestRefusedToolInvocationOffersNoWorkerTools(t *testing.T) {
	reg, sess, grid := openWorkerAuthSession(t)
	if err := grid.Enrol(string(sess.ID()), 80, 24); err != nil {
		t.Fatalf("enrol session grid: %v", err)
	}
	pinner := &workerAuthPinner{
		root:   peerpin.Root{PID: 4242, StartTime: time.Unix(123, 0)},
		member: map[int]bool{9001: true},
	}
	auth := newToolAuthorizer(pinner, reg, grid, emptyWorkerRecord(), workerTestWorkspace)
	inv, _, err := auth.Admit(toolendpoint.Peer{UID: 1000, PID: 9001})
	if !errors.Is(err, toolendpoint.ErrNotEnrolled) {
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
		if strings.HasPrefix(tool.Name, "workers.") {
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
