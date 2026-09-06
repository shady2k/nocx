package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	coordsock "github.com/shady2k/nocx/internal/coordinator"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panegrid"
	"github.com/shady2k/nocx/internal/peerpin"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/toolendpoint"
	"github.com/shady2k/nocx/internal/workers"
)

// The one dispatcher and its two callers (nocx-rowqt.14).
//
// What is real here, and it is the list that decides whether this test is
// evidence: the worker RECORD (internal/workers.Registrar over its store), the ONE
// assistant.ToolDispatcher both callers reach, the published unix socket, the
// kernel's own SO_PEERCRED stamp on it, the (pid, startTime) pin and its
// ancestry walk (peerpin.SystemPinner), the session registry, the pane grid,
// and the shipped authorizer — newToolAuthorizer, the same call app.New makes.
// The external caller is a REAL second process: this test binary re-executed,
// so its pid is genuinely not ours and the pin has something to walk.
//
// What is not real, and why that is the right cut: the three seams that fork
// and watch an OS process (Spawner, Enrolments, Supervisor). This test is
// about whether two callers move ONE ROW, not about whether claude starts, and
// a real fork would make it a test of the launcher instead.
//
// The claim being proved is design §6's, which no test made before this one:
// one dispatcher, two callers, and the external caller's mutation IS the
// in-process caller's row rather than an equal copy of it. Copies are what a
// fake proves, which is why the stub record in
// internal/toolendpoint/contract_test.go could not prove this and this test
// does not reuse it.

const workerExternalSocketEnv = "NOCX_TEST_WORKER_EXTERNAL_SOCKET"

// workerRPCDomainError is toolendpoint's -32000: the request was understood and
// refused, as against -32603, which says the backend failed.
const workerRPCDomainError = -32000

// workerTestWorkspace is the workspace a worker's pane lives in, as the
// composition root passes it.
const workerTestWorkspace = "workspace:default"

// TestGroupExternalCallerHelper is not a test. It is the external process: the
// go test binary re-executed with the socket named in the environment, so that
// the caller reaching tool.sock has a pid of its own for SO_PEERCRED to stamp
// and for the pin to walk. Without the environment variable it does nothing,
// which is what keeps it out of an ordinary run.
func TestGroupExternalCallerHelper(t *testing.T) {
	socket := os.Getenv(workerExternalSocketEnv)
	if socket == "" {
		t.Skip("not the external caller")
	}
	request, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read request: %v\n", err)
		os.Exit(1)
	}
	conn, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dial %s: %v\n", socket, err)
		os.Exit(1)
	}
	defer func() { _ = conn.Close() }()
	if _, writeErr := conn.Write(append(request, '\n')); writeErr != nil {
		fmt.Fprintf(os.Stderr, "write request: %v\n", writeErr)
		os.Exit(1)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintf(os.Stderr, "read response: %v\n", err)
		os.Exit(1)
	}
	_, _ = fmt.Fprint(os.Stdout, line)
}

// callExternally runs one JSON-RPC request from a second process and returns
// the response frame. It is a process and not a goroutine deliberately: a
// dialer inside this test would carry this test's pid, and admission would
// then be proving nothing about a caller that is genuinely elsewhere.
func callExternally(t *testing.T, socket, method, params string) workerExternalResponse {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	//nolint:gosec // binary is this test binary, from os.Executable
	cmd := exec.CommandContext(ctx, binary, "-test.run=TestGroupExternalCallerHelper", "-test.timeout=90s")
	cmd.Env = append(os.Environ(), workerExternalSocketEnv+"="+socket)
	cmd.Stdin = strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":` + params + `}`,
	)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("external caller %s: %v\nstderr: %s", method, err, stderr.String())
	}
	// The helper prints the response frame and go test prints its own PASS
	// lines around it. The frame is the one line that parses.
	var response workerExternalResponse
	found := false
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			continue
		}
		found = true
		break
	}
	if !found {
		t.Fatalf("external caller %s produced no response frame:\n%s\nstderr: %s", method, out, stderr.String())
	}
	return response
}

type workerExternalResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    any    `json:"data"`
	} `json:"error"`
}

// ── the three seams that would otherwise fork a process ────────────────────

type workerTwoCallersSpawner struct {
	mu   sync.Mutex
	live map[workers.ParticipantID]workers.Liveness
	// session is the session id a spawned participant runs in. Empty means
	// "the participant's own id", which is enough for a test that only needs
	// a distinct liveness; a test whose caller must RESOLVE from a session to
	// this participant sets the real one.
	session string
}

func (s *workerTwoCallersSpawner) Spawn(_ context.Context, req workers.SpawnRequest) (workers.Spawned, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.live == nil {
		s.live = make(map[workers.ParticipantID]workers.Liveness)
	}
	sessionID := s.session
	if sessionID == "" {
		sessionID = string(req.Participant)
	}
	live := workers.Liveness{
		BackendInstance: "test-instance",
		SessionID:       sessionID,
		Epoch:           1,
	}
	s.live[req.Participant] = live
	return workerTwoCallersSpawned{live: live}, nil
}

type workerTwoCallersSpawned struct{ live workers.Liveness }

func (s workerTwoCallersSpawned) Liveness() workers.Liveness { return s.live }
func (s workerTwoCallersSpawned) Kill(context.Context) error { return nil }

type workerTwoCallersEnrolments struct{ spawner *workerTwoCallersSpawner }

func (e workerTwoCallersEnrolments) Await(_ context.Context, p workers.ParticipantID) (workers.Liveness, error) {
	e.spawner.mu.Lock()
	defer e.spawner.mu.Unlock()
	live, ok := e.spawner.live[p]
	if !ok {
		return workers.Liveness{}, errors.New("no launcher enrolled")
	}
	return live, nil
}

func (workerTwoCallersEnrolments) Withdraw(context.Context, workers.ParticipantID) error { return nil }

type workerTwoCallersSupervisor struct{}

func (workerTwoCallersSupervisor) Attach(context.Context, workers.Participant) error { return nil }

// workerTwoCallersCloser ends the participant's process and then delivers the
// exit the way the composition root does: app.New wires the supervisor's
// exited callback to workerRecord.Exited, because ending a process writes no
// state — the terminal fact arrives when the exit is OBSERVED. A closer that
// terminalized the row itself would be a second writer of the same fact and
// would make this test assert a shape the product does not have.
type workerTwoCallersCloser struct {
	mu     sync.Mutex
	record *workers.Registrar
	ended  []workers.ParticipantID
}

func (c *workerTwoCallersCloser) Close(ctx context.Context, p workers.Participant) error {
	c.mu.Lock()
	c.ended = append(c.ended, p.ID)
	c.mu.Unlock()
	_, err := c.record.Exited(ctx, p.ID, p.Liveness, workers.Exit{
		Cause: "exited", Code: 0, At: time.Now(),
	})
	return err
}

func (c *workerTwoCallersCloser) endedParticipants() []workers.ParticipantID {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]workers.ParticipantID(nil), c.ended...)
}

// newGroupTwoCallersRecord builds the real record over the seams above.
func newGroupTwoCallersRecord() (*workers.Registrar, *workerTwoCallersCloser) {
	return newGroupTwoCallersRecordInSession("")
}

// newGroupTwoCallersRecordInSession is the same record, with every participant
// it registers running in one named session — what a caller from that pane
// must resolve to in order to be a participant rather than a coordinator.
func newGroupTwoCallersRecordInSession(sessionID string) (*workers.Registrar, *workerTwoCallersCloser) {
	spawner := &workerTwoCallersSpawner{session: sessionID}
	closer := &workerTwoCallersCloser{}
	record := workers.NewRegistrar(
		workers.NewMemoryStore(),
		spawner,
		workerTwoCallersEnrolments{spawner: spawner},
		workerTwoCallersSupervisor{},
		workers.WithCloser(closer),
		workers.WithEnrolmentDeadline(5*time.Second),
	)
	closer.record = record
	return record, closer
}

// publishGroupEndpoint starts the shipped endpoint over the shipped authorizer
// with the real kernel-stamped peer credentials, and returns its socket path.
func publishGroupEndpoint(t *testing.T, reg *session.Reg, grid workerAuthEnrolments, record *workers.Registrar, dispatch assistant.ToolDispatcher) string {
	t.Helper()
	endpoint, err := toolendpoint.New(toolendpoint.Config{
		Dir:      t.TempDir(),
		Peers:    coordsock.SystemPeerCredentials{},
		Owner:    coordsock.SystemPathOwner{},
		SelfUID:  uint32(os.Getuid()), //nolint:gosec // a uid is not a signed quantity
		Auth:     newToolAuthorizer(peerpin.SystemPinner{}, reg, grid, record, workerTestWorkspace),
		Dispatch: dispatch,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("new worker endpoint: %v", err)
	}
	if err := endpoint.Start(); err != nil {
		t.Fatalf("start worker endpoint: %v", err)
	}
	t.Cleanup(func() { _ = endpoint.Close() })
	return endpoint.SocketPath()
}

// inProcessInvocation is the shape the assistant's own tool path hands the
// dispatcher: the run's session and the run's grant. It is built here rather
// than driven through agent.ask because what is under test is the DISPATCHER
// both callers share, and a model adapter between this test and it would only
// obscure which caller moved the row.
func inProcessInvocation(sid session.ID, method, params string) assistant.ToolInvocation {
	return assistant.ToolInvocation{
		Context:    context.Background(),
		RunContext: agenttools.RunContext{RunID: "run-in-process", Session: string(sid)},
		Grant:      callerGrant(sid, content.EnvironmentIDFor(content.EnvLocal, "")),
		Method:     method,
		RawParams:  []byte(params),
	}
}

func workerHoldingIDs(t *testing.T, raw string) []string {
	t.Helper()
	// Decoded against the contract's own field names. A decoder that names
	// them wrong returns an empty list and every assertion below it passes
	// for the wrong reason, so this refuses a payload with no participants
	// key rather than reading one as "no workers".
	var holdings struct {
		Participants *[]struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"participants"`
	}
	if err := json.Unmarshal([]byte(raw), &holdings); err != nil {
		t.Fatalf("decode holdings %q: %v", raw, err)
	}
	if holdings.Participants == nil {
		t.Fatalf("holdings result has no participants key, which its contract requires: %s", raw)
	}
	ids := make([]string, 0, len(*holdings.Participants))
	for _, worker := range *holdings.Participants {
		ids = append(ids, worker.ID)
	}
	return ids
}

// workerUndeliveredMail reads the count of messages this session has left for
// its workers that no worker has taken. It is the field an in-process
// workers.say moves and an external workers.holdings reports.
func workerUndeliveredMail(t *testing.T, raw string) int {
	t.Helper()
	var holdings struct {
		Undelivered *int `json:"undeliveredMail"`
	}
	if err := json.Unmarshal([]byte(raw), &holdings); err != nil {
		t.Fatalf("decode holdings %q: %v", raw, err)
	}
	if holdings.Undelivered == nil {
		return 0
	}
	return *holdings.Undelivered
}

// workerParticipantState returns the state the payload gives one participant,
// and fails if it does not name it at all — a missing row and a live row are
// different answers and must not both read as "".
func workerParticipantState(t *testing.T, raw, id string) string {
	t.Helper()
	var holdings struct {
		Participants []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"participants"`
	}
	if err := json.Unmarshal([]byte(raw), &holdings); err != nil {
		t.Fatalf("decode holdings %q: %v", raw, err)
	}
	for _, p := range holdings.Participants {
		if p.ID == id {
			return p.State
		}
	}
	t.Fatalf("holdings does not name participant %q at all: %s", id, raw)
	return ""
}

func contains(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// prepareGroupCaller opens a session, records this process as its
// backend-owned root and enrols its pane — the three facts the shipped
// authorizer requires before it will admit anything.
func prepareGroupCaller(t *testing.T) (*session.Reg, session.Session, workerAuthEnrolments) {
	t.Helper()
	reg, sess, grid := openWorkerAuthSession(t)
	// This process is the tree root, so a child of this process is genuinely
	// inside it and peerpin.SystemPinner has a real ancestry to walk.
	if err := reg.RecordOwnedProcessPID(sess.ID(), os.Getpid()); err != nil {
		t.Fatalf("record owned process pid: %v", err)
	}
	if err := grid.Enrol(string(sess.ID()), 80, 24); err != nil {
		t.Fatalf("enrol session grid: %v", err)
	}
	return reg, sess, grid
}

func toolRegistry(t *testing.T) agenttools.Registry {
	t.Helper()
	registry, err := agenttools.Assemble(os.DirFS("../../contracts/tools"))
	if err != nil {
		t.Fatalf("assemble tools: %v", err)
	}
	return registry
}

func newSharedToolDispatcher(t *testing.T, record assistant.WorkerRecord) assistant.ToolDispatcher {
	t.Helper()
	dispatcher, err := assistant.NewToolDispatcher(
		toolRegistry(t), record, content.EnvironmentIDFor(content.EnvLocal, ""),
	)
	if err != nil {
		t.Fatalf("new worker dispatcher: %v", err)
	}
	return dispatcher
}

// TestGroupExternalCallerMutationIsTheInProcessCallersRow is the epic's happy
// path: a second process starts a worker over the published socket, and the
// in-process caller — the one an assistant run reaches — sees THAT worker,
// then ends it, and the second process is told it is gone.
func TestGroupExternalCallerMutationIsTheInProcessCallersRow(t *testing.T) {
	reg, sess, grid := prepareGroupCaller(t)
	record, closer := newGroupTwoCallersRecord()
	dispatcher := newSharedToolDispatcher(t, record)
	socket := publishGroupEndpoint(t, reg, grid, record, dispatcher)

	// The external caller starts the worker.
	spawn := callExternally(t, socket, "workers.spawn",
		`{"command":"claude","task":"prove the row is one row"}`)
	if spawn.Error != nil {
		t.Fatalf("external workers.spawn: %+v", spawn.Error)
	}
	var spawnResult struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(spawn.Result, &spawnResult); err != nil {
		t.Fatalf("decode external spawn result %s: %v", spawn.Result, err)
	}
	if spawnResult.ID == "" {
		t.Fatalf("external spawn returned no participant id: %s", spawn.Result)
	}

	// The in-process caller sees that worker, through the same dispatcher.
	holdings, err := dispatcher.Dispatch(inProcessInvocation(sess.ID(), "workers.holdings", `{}`))
	if err != nil {
		t.Fatalf("in-process workers.holdings: %v", err)
	}
	if ids := workerHoldingIDs(t, holdings); !contains(ids, spawnResult.ID) {
		t.Fatalf("in-process holdings = %v, want the externally spawned %q", ids, spawnResult.ID)
	}

	// A mutation in the other direction, seen by the caller that did not make
	// it. workers.say leaves a message for a worker, and the external caller's
	// own holdings then reports one undelivered — which it can only do if the
	// message landed in the mailbox of the row it started.
	if _, err := dispatcher.Dispatch(inProcessInvocation(
		sess.ID(), "workers.say",
		`{"worker":"`+spawnResult.ID+`","message":"the row is one row"}`,
	)); err != nil {
		t.Fatalf("in-process workers.say to the external worker: %v", err)
	}
	afterSay := callExternally(t, socket, "workers.holdings", `{}`)
	if afterSay.Error != nil {
		t.Fatalf("external workers.holdings after the in-process say: %+v", afterSay.Error)
	}
	if got := workerUndeliveredMail(t, string(afterSay.Result)); got != 1 {
		t.Fatalf("external undeliveredMail = %d, want the 1 the in-process caller left", got)
	}

	// And the destructive one. The in-process caller ends the worker the
	// external caller started: the closer is handed THAT participant, and
	// once the exit it causes is observed the external caller is told the
	// worker is terminal. An equal copy would go on being live over there.
	if _, err := dispatcher.Dispatch(inProcessInvocation(
		sess.ID(), "workers.close", `{"worker":"`+spawnResult.ID+`"}`,
	)); err != nil {
		t.Fatalf("in-process workers.close of the external worker: %v", err)
	}
	ended := closer.endedParticipants()
	if len(ended) != 1 || string(ended[0]) != spawnResult.ID {
		t.Fatalf("closer ended %v, want only the externally spawned %q", ended, spawnResult.ID)
	}
	afterClose := callExternally(t, socket, "workers.holdings", `{}`)
	if afterClose.Error != nil {
		t.Fatalf("external workers.holdings after close: %+v", afterClose.Error)
	}
	if state := workerParticipantState(t, string(afterClose.Result), spawnResult.ID); state != "abandoned" {
		t.Fatalf("external view of %q is state %q, want the terminal state the in-process close caused",
			spawnResult.ID, state)
	}
}

// TestGroupExternalCallerCannotMoveARowItsCapabilityNeverHeld is the refusal
// clause. The external caller is admitted for ITS session; a worker belonging
// to another session's worker is not refused by a check placed in front of the
// record — the capability the caller was narrowed to never contained it.
func TestGroupExternalCallerCannotMoveARowItsCapabilityNeverHeld(t *testing.T) {
	reg, _, grid := prepareGroupCaller(t)
	record, _ := newGroupTwoCallersRecord()
	dispatcher := newSharedToolDispatcher(t, record)
	socket := publishGroupEndpoint(t, reg, grid, record, dispatcher)

	// A worker in a DIFFERENT session's worker, registered directly on the
	// record so that nothing about the caller's own path created it.
	stranger, err := record.Register(context.Background(), workers.RegisterRequest{
		CoordinatorSession: "another-session",
		Role:               workers.RoleWorker,
		Task:               "belongs to somebody else",
		Command:            "claude",
		Environment:        content.EnvironmentIDFor(content.EnvLocal, ""),
	})
	if err != nil {
		t.Fatalf("register the stranger's worker: %v", err)
	}

	closed := callExternally(t, socket, "workers.close", `{"worker":"`+string(stranger.ID)+`"}`)
	if closed.Error == nil {
		t.Fatalf("closing another session's worker succeeded: %s", closed.Result)
	}
	// And it says so as a REFUSAL. "internal error" would be indistinguishable
	// from the backend having fallen over, which is the one reading that would
	// let a caller retry a call it must never be allowed to make.
	if closed.Error.Code != workerRPCDomainError || closed.Error.Message != "worker request refused" {
		t.Fatalf("refusal answered %d %q, want the domain refusal %d %q",
			closed.Error.Code, closed.Error.Message, workerRPCDomainError, "worker request refused")
	}

	// And it is still live, which is the half a refused-looking error cannot
	// establish on its own.
	held, err := record.HeldBy(context.Background(), "another-session")
	if err != nil {
		t.Fatalf("holdings of the stranger's session: %v", err)
	}
	stillThere := false
	for _, p := range held {
		if p.ID == stranger.ID && p.State == workers.StateLive {
			stillThere = true
		}
	}
	if !stillThere {
		t.Fatalf("the stranger's worker did not survive the refused close: %+v", held)
	}
}

// TestGroupEndpointHasNoExecutionPathOfItsOwn is the falsifiability clause. If
// a second execution path is ever added beside the shared dispatcher, the
// endpoint will move a record the in-process caller cannot see, and this test
// is what breaks: the endpoint here is published over a dispatcher backed by a
// DIFFERENT record, and the in-process caller must then see nothing.
func TestGroupEndpointHasNoExecutionPathOfItsOwn(t *testing.T) {
	reg, sess, grid := prepareGroupCaller(t)
	endpointRecord, _ := newGroupTwoCallersRecord()
	inProcessRecord, _ := newGroupTwoCallersRecord()
	socket := publishGroupEndpoint(t, reg, grid, endpointRecord, newSharedToolDispatcher(t, endpointRecord))
	inProcess := newSharedToolDispatcher(t, inProcessRecord)

	spawn := callExternally(t, socket, "workers.spawn",
		`{"command":"claude","task":"a row on the other record"}`)
	if spawn.Error != nil {
		t.Fatalf("external workers.spawn: %+v", spawn.Error)
	}
	var spawnResult struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(spawn.Result, &spawnResult); err != nil {
		t.Fatalf("decode external spawn result %s: %v", spawn.Result, err)
	}

	holdings, err := inProcess.Dispatch(inProcessInvocation(sess.ID(), "workers.holdings", `{}`))
	if err != nil {
		t.Fatalf("in-process workers.holdings: %v", err)
	}
	if ids := workerHoldingIDs(t, holdings); contains(ids, spawnResult.ID) {
		t.Fatalf("two records shared a row %q: %v — the readback in the happy path proves nothing", spawnResult.ID, ids)
	}
}

// workerWorkerSetup is a worker whose enrolled session is the WORKER's, not the
// coordinator's. Only one session may be enrolled at a time here, and that is
// the product's rule rather than a test convenience: toolAuthorizer refuses a
// peer matching two live enrolled roots, because a pid inside both trees has
// no unambiguous session authority. The coordinator in these tests therefore
// speaks through the in-process path only, which is all it needs.
type workerWorkerSetup struct {
	coordinator session.ID
	participant workers.ParticipantID
	socket      string
	dispatcher  assistant.ToolDispatcher
}

func prepareGroupWorkerSetup(t *testing.T) workerWorkerSetup {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	reg := session.New(logger, workerAuthPTYFactory{log: logger})
	grid := panegrid.New(logger)

	// The coordinator's session exists and is deliberately NOT enrolled: it
	// reaches the record in process, and enrolling it would make the peer's
	// tree ambiguous between two roots.
	coordinator, err := reg.Open(context.Background(), session.Config{Kind: session.KindLocal, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("open coordinator session: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close(coordinator.ID()) })

	worker, err := reg.Open(context.Background(), session.Config{Kind: session.KindLocal, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("open worker session: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close(worker.ID()) })
	if pidErr := reg.RecordOwnedProcessPID(worker.ID(), os.Getpid()); pidErr != nil {
		t.Fatalf("record worker owned pid: %v", pidErr)
	}
	if enrolErr := grid.Enrol(string(worker.ID()), 80, 24); enrolErr != nil {
		t.Fatalf("enrol worker pane: %v", enrolErr)
	}
	t.Cleanup(func() { grid.Withdraw(string(worker.ID())) })

	// The participant's liveness carries the WORKER's session, which is what
	// ParticipantBySession resolves and therefore what makes the caller from
	// that pane a participant rather than a coordinator.
	record, _ := newGroupTwoCallersRecordInSession(string(worker.ID()))
	dispatcher := newSharedToolDispatcher(t, record)
	p, err := record.Register(context.Background(), workers.RegisterRequest{
		CoordinatorSession: string(coordinator.ID()),
		Role:               workers.RoleWorker,
		Task:               "read your own mail",
		Command:            "claude",
		Environment:        content.EnvironmentIDFor(content.EnvLocal, ""),
	})
	if err != nil {
		t.Fatalf("register the worker: %v", err)
	}
	if resolved, rerr := record.ParticipantOf(context.Background(), string(worker.ID())); rerr != nil {
		t.Logf("PROBE ParticipantOf(%s) failed: %v", worker.ID(), rerr)
	} else {
		t.Logf("PROBE ParticipantOf(%s) = %s", worker.ID(), resolved.ID)
	}
	return workerWorkerSetup{
		coordinator: coordinator.ID(),
		participant: p.ID,
		socket:      publishGroupEndpoint(t, reg, grid, record, dispatcher),
		dispatcher:  dispatcher,
	}
}

// TestGroupWorkerReadsTheMailItsCoordinatorLeft is nocx-rowqt.9's happy path:
// workers.say had a writer and no reader, so a coordinator could commit a message
// into a mailbox nothing could open. This watches a worker open it.
func TestGroupWorkerReadsTheMailItsCoordinatorLeft(t *testing.T) {
	w := prepareGroupWorkerSetup(t)

	// The coordinator says something, through the in-process path.
	if _, err := w.dispatcher.Dispatch(inProcessInvocation(
		w.coordinator, "workers.say",
		`{"worker":"`+string(w.participant)+`","message":"read this and report"}`,
	)); err != nil {
		t.Fatalf("coordinator workers.say: %v", err)
	}

	// And the WORKER reads it, over the socket, as itself.
	inbox := callExternally(t, w.socket, "workers.inbox", `{}`)
	if inbox.Error != nil {
		t.Fatalf("worker workers.inbox: %+v", inbox.Error)
	}
	var result struct {
		Messages []struct {
			From    string `json:"from"`
			Message string `json:"message"`
		} `json:"messages"`
		Cursor int64 `json:"cursor"`
	}
	if err := json.Unmarshal(inbox.Result, &result); err != nil {
		t.Fatalf("decode inbox result %s: %v", inbox.Result, err)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("worker read %d messages, want the 1 its coordinator left: %s",
			len(result.Messages), inbox.Result)
	}
	if result.Messages[0].Message != "read this and report" {
		t.Fatalf("worker read %q, want what the coordinator said", result.Messages[0].Message)
	}
	if result.Messages[0].From != string(w.coordinator) {
		t.Fatalf("message from = %q, want the coordinator session %q",
			result.Messages[0].From, w.coordinator)
	}
	if result.Cursor == 0 {
		t.Fatalf("cursor did not move: %s", inbox.Result)
	}
}

// TestGroupWorkerIsOfferedNoCoordinatorCall is the other half of the AC: a
// worker holds a capability that cannot perform an act reserved to the
// coordinator. It is refused because its grant never made the call reachable,
// not because an executor checked a role.
func TestGroupWorkerIsOfferedNoCoordinatorCall(t *testing.T) {
	w := prepareGroupWorkerSetup(t)
	for _, tc := range []struct{ method, params string }{
		{"workers.spawn", `{"command":"claude","task":"a worker starting a worker"}`},
		{"workers.holdings", `{}`},
		{"workers.close", `{"worker":"nobody"}`},
	} {
		t.Run(tc.method, func(t *testing.T) {
			response := callExternally(t, w.socket, tc.method, tc.params)
			if response.Error == nil {
				t.Fatalf("a worker performed %s: %s", tc.method, response.Result)
			}
			if response.Error.Code != workerRPCDomainError {
				t.Fatalf("%s answered %d %q, want the domain refusal %d",
					tc.method, response.Error.Code, response.Error.Message, workerRPCDomainError)
			}
		})
	}
}

// TestWorkerCoordinatorIsOfferedNoParticipantCall is its mirror, and the pair is
// what makes "disjoint by construction" checkable rather than asserted: if a
// later grant change made one set reach the other, exactly one of these two
// tests goes red.
func TestWorkerCoordinatorIsOfferedNoParticipantCall(t *testing.T) {
	reg, _, grid := prepareGroupCaller(t)
	record, _ := newGroupTwoCallersRecord()
	socket := publishGroupEndpoint(t, reg, grid, record, newSharedToolDispatcher(t, record))

	response := callExternally(t, socket, "workers.inbox", `{}`)
	if response.Error == nil {
		t.Fatalf("a coordinator read a participant's mailbox: %s", response.Result)
	}
	if response.Error.Code != workerRPCDomainError {
		t.Fatalf("workers.inbox answered %d %q, want the domain refusal %d",
			response.Error.Code, response.Error.Message, workerRPCDomainError)
	}
}
