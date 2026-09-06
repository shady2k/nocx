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
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/wave"
	"github.com/shady2k/nocx/internal/waveendpoint"
	"github.com/shady2k/nocx/internal/wavepin"
)

// The one dispatcher and its two callers (nocx-rowqt.14).
//
// What is real here, and it is the list that decides whether this test is
// evidence: the wave RECORD (internal/wave.Registrar over its store), the ONE
// assistant.WaveDispatcher both callers reach, the published unix socket, the
// kernel's own SO_PEERCRED stamp on it, the (pid, startTime) pin and its
// ancestry walk (wavepin.SystemPinner), the session registry, the pane grid,
// and the shipped authorizer — newWaveAuthorizer, the same call app.New makes.
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
// internal/waveendpoint/contract_test.go could not prove this and this test
// does not reuse it.

const waveExternalSocketEnv = "NOCX_TEST_WAVE_EXTERNAL_SOCKET"

// waveRPCDomainError is waveendpoint's -32000: the request was understood and
// refused, as against -32603, which says the backend failed.
const waveRPCDomainError = -32000

// TestWaveExternalCallerHelper is not a test. It is the external process: the
// go test binary re-executed with the socket named in the environment, so that
// the caller reaching wave.sock has a pid of its own for SO_PEERCRED to stamp
// and for the pin to walk. Without the environment variable it does nothing,
// which is what keeps it out of an ordinary run.
func TestWaveExternalCallerHelper(t *testing.T) {
	socket := os.Getenv(waveExternalSocketEnv)
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
func callExternally(t *testing.T, socket, method, params string) waveExternalResponse {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	//nolint:gosec // binary is this test binary, from os.Executable
	cmd := exec.CommandContext(ctx, binary, "-test.run=TestWaveExternalCallerHelper", "-test.timeout=90s")
	cmd.Env = append(os.Environ(), waveExternalSocketEnv+"="+socket)
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
	var response waveExternalResponse
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

type waveExternalResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    any    `json:"data"`
	} `json:"error"`
}

// ── the three seams that would otherwise fork a process ────────────────────

type waveTwoCallersSpawner struct {
	mu   sync.Mutex
	live map[wave.ParticipantID]wave.Liveness
}

func (s *waveTwoCallersSpawner) Spawn(_ context.Context, req wave.SpawnRequest) (wave.Spawned, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.live == nil {
		s.live = make(map[wave.ParticipantID]wave.Liveness)
	}
	live := wave.Liveness{
		BackendInstance: "test-instance",
		SessionID:       string(req.Participant),
		Epoch:           1,
	}
	s.live[req.Participant] = live
	return waveTwoCallersSpawned{live: live}, nil
}

type waveTwoCallersSpawned struct{ live wave.Liveness }

func (s waveTwoCallersSpawned) Liveness() wave.Liveness    { return s.live }
func (s waveTwoCallersSpawned) Kill(context.Context) error { return nil }

type waveTwoCallersEnrolments struct{ spawner *waveTwoCallersSpawner }

func (e waveTwoCallersEnrolments) Await(_ context.Context, p wave.ParticipantID) (wave.Liveness, error) {
	e.spawner.mu.Lock()
	defer e.spawner.mu.Unlock()
	live, ok := e.spawner.live[p]
	if !ok {
		return wave.Liveness{}, errors.New("no launcher enrolled")
	}
	return live, nil
}

func (waveTwoCallersEnrolments) Withdraw(context.Context, wave.ParticipantID) error { return nil }

type waveTwoCallersSupervisor struct{}

func (waveTwoCallersSupervisor) Attach(context.Context, wave.Participant) error { return nil }

// waveTwoCallersCloser ends the participant's process and then delivers the
// exit the way the composition root does: app.New wires the supervisor's
// exited callback to waveRecord.Exited, because ending a process writes no
// state — the terminal fact arrives when the exit is OBSERVED. A closer that
// terminalized the row itself would be a second writer of the same fact and
// would make this test assert a shape the product does not have.
type waveTwoCallersCloser struct {
	mu     sync.Mutex
	record *wave.Registrar
	ended  []wave.ParticipantID
}

func (c *waveTwoCallersCloser) Close(ctx context.Context, p wave.Participant) error {
	c.mu.Lock()
	c.ended = append(c.ended, p.ID)
	c.mu.Unlock()
	_, err := c.record.Exited(ctx, p.ID, p.Liveness, wave.Exit{
		Cause: "exited", Code: 0, At: time.Now(),
	})
	return err
}

func (c *waveTwoCallersCloser) endedParticipants() []wave.ParticipantID {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]wave.ParticipantID(nil), c.ended...)
}

// newWaveTwoCallersRecord builds the real record over the seams above.
func newWaveTwoCallersRecord() (*wave.Registrar, *waveTwoCallersCloser) {
	spawner := &waveTwoCallersSpawner{}
	closer := &waveTwoCallersCloser{}
	record := wave.NewRegistrar(
		wave.NewMemoryStore(),
		spawner,
		waveTwoCallersEnrolments{spawner: spawner},
		waveTwoCallersSupervisor{},
		wave.WithCloser(closer),
		wave.WithEnrolmentDeadline(5*time.Second),
	)
	closer.record = record
	return record, closer
}

// publishWaveEndpoint starts the shipped endpoint over the shipped authorizer
// with the real kernel-stamped peer credentials, and returns its socket path.
func publishWaveEndpoint(t *testing.T, reg *session.Reg, grid waveAuthEnrolments, dispatch assistant.WaveDispatcher) string {
	t.Helper()
	endpoint, err := waveendpoint.New(waveendpoint.Config{
		Dir:      t.TempDir(),
		Peers:    coordsock.SystemPeerCredentials{},
		Owner:    coordsock.SystemPathOwner{},
		SelfUID:  uint32(os.Getuid()), //nolint:gosec // a uid is not a signed quantity
		Auth:     newWaveAuthorizer(wavepin.SystemPinner{}, reg, grid),
		Dispatch: dispatch,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("new wave endpoint: %v", err)
	}
	if err := endpoint.Start(); err != nil {
		t.Fatalf("start wave endpoint: %v", err)
	}
	t.Cleanup(func() { _ = endpoint.Close() })
	return endpoint.SocketPath()
}

// inProcessInvocation is the shape the assistant's own tool path hands the
// dispatcher: the run's session and the run's grant. It is built here rather
// than driven through agent.ask because what is under test is the DISPATCHER
// both callers share, and a model adapter between this test and it would only
// obscure which caller moved the row.
func inProcessInvocation(sid session.ID, method, params string) assistant.WaveInvocation {
	return assistant.WaveInvocation{
		Context:    context.Background(),
		RunContext: agenttools.RunContext{RunID: "run-in-process", Session: string(sid)},
		Grant:      waveCallerGrant(sid),
		Method:     method,
		RawParams:  []byte(params),
	}
}

func waveHoldingIDs(t *testing.T, raw string) []string {
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

// waveUndeliveredMail reads the count of messages this session has left for
// its workers that no worker has taken. It is the field an in-process
// wave.say moves and an external wave.holdings reports.
func waveUndeliveredMail(t *testing.T, raw string) int {
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

// waveParticipantState returns the state the payload gives one participant,
// and fails if it does not name it at all — a missing row and a live row are
// different answers and must not both read as "".
func waveParticipantState(t *testing.T, raw, id string) string {
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

// prepareWaveCaller opens a session, records this process as its
// backend-owned root and enrols its pane — the three facts the shipped
// authorizer requires before it will admit anything.
func prepareWaveCaller(t *testing.T) (*session.Reg, session.Session, waveAuthEnrolments) {
	t.Helper()
	reg, sess, grid := openWaveAuthSession(t)
	// This process is the tree root, so a child of this process is genuinely
	// inside it and wavepin.SystemPinner has a real ancestry to walk.
	if err := reg.RecordOwnedProcessPID(sess.ID(), os.Getpid()); err != nil {
		t.Fatalf("record owned process pid: %v", err)
	}
	if err := grid.Enrol(string(sess.ID()), 80, 24); err != nil {
		t.Fatalf("enrol session grid: %v", err)
	}
	return reg, sess, grid
}

func waveToolRegistry(t *testing.T) agenttools.Registry {
	t.Helper()
	registry, err := agenttools.Assemble(os.DirFS("../../contracts/tools"))
	if err != nil {
		t.Fatalf("assemble tools: %v", err)
	}
	return registry
}

func newSharedWaveDispatcher(t *testing.T, record assistant.WaveRecord) assistant.WaveDispatcher {
	t.Helper()
	dispatcher, err := assistant.NewWaveDispatcher(
		waveToolRegistry(t), record, content.EnvironmentIDFor(content.EnvLocal, ""),
	)
	if err != nil {
		t.Fatalf("new wave dispatcher: %v", err)
	}
	return dispatcher
}

// TestWaveExternalCallerMutationIsTheInProcessCallersRow is the epic's happy
// path: a second process starts a worker over the published socket, and the
// in-process caller — the one an assistant run reaches — sees THAT worker,
// then ends it, and the second process is told it is gone.
func TestWaveExternalCallerMutationIsTheInProcessCallersRow(t *testing.T) {
	reg, sess, grid := prepareWaveCaller(t)
	record, closer := newWaveTwoCallersRecord()
	dispatcher := newSharedWaveDispatcher(t, record)
	socket := publishWaveEndpoint(t, reg, grid, dispatcher)

	// The external caller starts the worker.
	spawn := callExternally(t, socket, "wave.spawn",
		`{"command":"claude","task":"prove the row is one row"}`)
	if spawn.Error != nil {
		t.Fatalf("external wave.spawn: %+v", spawn.Error)
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
	holdings, err := dispatcher.Dispatch(inProcessInvocation(sess.ID(), "wave.holdings", `{}`))
	if err != nil {
		t.Fatalf("in-process wave.holdings: %v", err)
	}
	if ids := waveHoldingIDs(t, holdings); !contains(ids, spawnResult.ID) {
		t.Fatalf("in-process holdings = %v, want the externally spawned %q", ids, spawnResult.ID)
	}

	// A mutation in the other direction, seen by the caller that did not make
	// it. wave.say leaves a message for a worker, and the external caller's
	// own holdings then reports one undelivered — which it can only do if the
	// message landed in the mailbox of the row it started.
	if _, err := dispatcher.Dispatch(inProcessInvocation(
		sess.ID(), "wave.say",
		`{"worker":"`+spawnResult.ID+`","message":"the row is one row"}`,
	)); err != nil {
		t.Fatalf("in-process wave.say to the external worker: %v", err)
	}
	afterSay := callExternally(t, socket, "wave.holdings", `{}`)
	if afterSay.Error != nil {
		t.Fatalf("external wave.holdings after the in-process say: %+v", afterSay.Error)
	}
	if got := waveUndeliveredMail(t, string(afterSay.Result)); got != 1 {
		t.Fatalf("external undeliveredMail = %d, want the 1 the in-process caller left", got)
	}

	// And the destructive one. The in-process caller ends the worker the
	// external caller started: the closer is handed THAT participant, and
	// once the exit it causes is observed the external caller is told the
	// worker is terminal. An equal copy would go on being live over there.
	if _, err := dispatcher.Dispatch(inProcessInvocation(
		sess.ID(), "wave.close", `{"worker":"`+spawnResult.ID+`"}`,
	)); err != nil {
		t.Fatalf("in-process wave.close of the external worker: %v", err)
	}
	ended := closer.endedParticipants()
	if len(ended) != 1 || string(ended[0]) != spawnResult.ID {
		t.Fatalf("closer ended %v, want only the externally spawned %q", ended, spawnResult.ID)
	}
	afterClose := callExternally(t, socket, "wave.holdings", `{}`)
	if afterClose.Error != nil {
		t.Fatalf("external wave.holdings after close: %+v", afterClose.Error)
	}
	if state := waveParticipantState(t, string(afterClose.Result), spawnResult.ID); state != "abandoned" {
		t.Fatalf("external view of %q is state %q, want the terminal state the in-process close caused",
			spawnResult.ID, state)
	}
}

// TestWaveExternalCallerCannotMoveARowItsCapabilityNeverHeld is the refusal
// clause. The external caller is admitted for ITS session; a worker belonging
// to another session's wave is not refused by a check placed in front of the
// record — the capability the caller was narrowed to never contained it.
func TestWaveExternalCallerCannotMoveARowItsCapabilityNeverHeld(t *testing.T) {
	reg, _, grid := prepareWaveCaller(t)
	record, _ := newWaveTwoCallersRecord()
	dispatcher := newSharedWaveDispatcher(t, record)
	socket := publishWaveEndpoint(t, reg, grid, dispatcher)

	// A worker in a DIFFERENT session's wave, registered directly on the
	// record so that nothing about the caller's own path created it.
	stranger, err := record.Register(context.Background(), wave.RegisterRequest{
		CoordinatorSession: "another-session",
		Role:               wave.RoleWorker,
		Task:               "belongs to somebody else",
		Command:            "claude",
		Environment:        content.EnvironmentIDFor(content.EnvLocal, ""),
	})
	if err != nil {
		t.Fatalf("register the stranger's worker: %v", err)
	}

	closed := callExternally(t, socket, "wave.close", `{"worker":"`+string(stranger.ID)+`"}`)
	if closed.Error == nil {
		t.Fatalf("closing another session's worker succeeded: %s", closed.Result)
	}
	// And it says so as a REFUSAL. "internal error" would be indistinguishable
	// from the backend having fallen over, which is the one reading that would
	// let a caller retry a call it must never be allowed to make.
	if closed.Error.Code != waveRPCDomainError || closed.Error.Message != "wave request refused" {
		t.Fatalf("refusal answered %d %q, want the domain refusal %d %q",
			closed.Error.Code, closed.Error.Message, waveRPCDomainError, "wave request refused")
	}

	// And it is still live, which is the half a refused-looking error cannot
	// establish on its own.
	held, err := record.HeldBy(context.Background(), "another-session")
	if err != nil {
		t.Fatalf("holdings of the stranger's session: %v", err)
	}
	stillThere := false
	for _, p := range held {
		if p.ID == stranger.ID && p.State == wave.StateLive {
			stillThere = true
		}
	}
	if !stillThere {
		t.Fatalf("the stranger's worker did not survive the refused close: %+v", held)
	}
}

// TestWaveEndpointHasNoExecutionPathOfItsOwn is the falsifiability clause. If
// a second execution path is ever added beside the shared dispatcher, the
// endpoint will move a record the in-process caller cannot see, and this test
// is what breaks: the endpoint here is published over a dispatcher backed by a
// DIFFERENT record, and the in-process caller must then see nothing.
func TestWaveEndpointHasNoExecutionPathOfItsOwn(t *testing.T) {
	reg, sess, grid := prepareWaveCaller(t)
	endpointRecord, _ := newWaveTwoCallersRecord()
	inProcessRecord, _ := newWaveTwoCallersRecord()
	socket := publishWaveEndpoint(t, reg, grid, newSharedWaveDispatcher(t, endpointRecord))
	inProcess := newSharedWaveDispatcher(t, inProcessRecord)

	spawn := callExternally(t, socket, "wave.spawn",
		`{"command":"claude","task":"a row on the other record"}`)
	if spawn.Error != nil {
		t.Fatalf("external wave.spawn: %+v", spawn.Error)
	}
	var spawnResult struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(spawn.Result, &spawnResult); err != nil {
		t.Fatalf("decode external spawn result %s: %v", spawn.Result, err)
	}

	holdings, err := inProcess.Dispatch(inProcessInvocation(sess.ID(), "wave.holdings", `{}`))
	if err != nil {
		t.Fatalf("in-process wave.holdings: %v", err)
	}
	if ids := waveHoldingIDs(t, holdings); contains(ids, spawnResult.ID) {
		t.Fatalf("two records shared a row %q: %v — the readback in the happy path proves nothing", spawnResult.ID, ids)
	}
}
