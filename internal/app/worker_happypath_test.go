package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agentcalib"
	"github.com/shady2k/nocx/internal/agentcapture"
	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/agenttyping"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	coordsock "github.com/shady2k/nocx/internal/coordinator"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panegrid"
	"github.com/shady2k/nocx/internal/paneobserve"
	"github.com/shady2k/nocx/internal/peerpin"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/toolendpoint"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/waittest"
	"github.com/shady2k/nocx/internal/workers"
	"github.com/shady2k/nocx/internal/workspace"
)

const (
	happyExternalEnv    = "NOCX_TEST_WORKER_HAPPY_EXTERNAL"
	happyCommandEnv     = "NOCX_TEST_WORKER_HAPPY_COMMAND"
	happyWaitSecondsEnv = "NOCX_TEST_WORKER_HAPPY_WAIT_SECONDS"
	happyMutationEnv    = "NOCX_TEST_WORKER_HAPPY_MUTATION"
	happyExpectedReport = "read AGENTS.md and reported from an external worker\n"
)

// The skip-report mutation models a worker that never declares what it
// produced; wrong-summary models a declaration for the wrong work. Either
// regression must make the external cycle fail instead of claiming completion.

// TestWorkerHappyExternalCoordinator is a REAL second process. It is the
// external coordinator for TestExternalClaudeDrivesAWorkerEndToEnd, not
// nocx's assistant engine. The four calls are deliberately direct JSON-RPC
// worker calls: spawn a real pane, wait for the declaration, read its summary,
// and close the participant.
func TestWorkerHappyExternalCoordinator(t *testing.T) {
	socket := os.Getenv(happyExternalEnv)
	if socket == "" {
		t.Skip("not the external coordinator")
	}

	methods := []string{"workers.spawn", "workers.wait", "workers.holdings", "workers.close"}
	client, err := openHappyExternalClient(socket)
	if err != nil {
		t.Fatalf("connect external coordinator: %v", err)
	}
	defer client.Close()
	command := os.Getenv(happyCommandEnv)
	if command == "" {
		command = "claude"
	}
	spawnParams, err := json.Marshal(struct {
		Command string `json:"command"`
		Task    string `json:"task"`
	}{Command: command, Task: "read AGENTS.md and report"})
	if err != nil {
		t.Fatalf("marshal spawn params: %v", err)
	}
	spawn := client.Call("workers.spawn", string(spawnParams))
	if spawn.Error != nil {
		t.Fatalf("workers.spawn: %+v", spawn.Error)
	}
	var spawned struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	mustDecodeHappyResult(t, spawn.Result, &spawned)
	if spawned.ID == "" || spawned.State != string(workers.StateLive) {
		t.Fatalf("spawn result = %s, want a live participant id", spawn.Result)
	}

	waitSeconds := os.Getenv(happyWaitSecondsEnv)
	if waitSeconds == "" {
		waitSeconds = "10"
	}
	wait := client.Call("workers.wait", `{"seconds":`+waitSeconds+`}`)
	if wait.Error != nil {
		t.Fatalf("workers.wait: %+v", wait.Error)
	}
	var waited happyWorkerHoldingsResult
	mustDecodeHappyResult(t, wait.Result, &waited)
	assertHappyDeclaration(t, waited, spawned.ID)

	holdings := client.Call("workers.holdings", `{}`)
	if holdings.Error != nil {
		t.Fatalf("workers.holdings: %+v", holdings.Error)
	}
	var readback happyWorkerHoldingsResult
	mustDecodeHappyResult(t, holdings.Result, &readback)
	assertHappyDeclaration(t, readback, spawned.ID)

	closed := client.Call("workers.close", `{"worker":"`+spawned.ID+`"}`)
	if closed.Error != nil {
		t.Fatalf("workers.close: %+v", closed.Error)
	}
	var closeResult struct {
		ID    string `json:"id"`
		Ended bool   `json:"ended"`
	}
	mustDecodeHappyResult(t, closed.Result, &closeResult)
	if closeResult.ID != spawned.ID || !closeResult.Ended {
		t.Fatalf("close result = %s, want ended participant %q", closed.Result, spawned.ID)
	}

	payload, err := json.Marshal(struct {
		Methods  []string        `json:"methods"`
		Spawn    json.RawMessage `json:"spawn"`
		Wait     json.RawMessage `json:"wait"`
		Holdings json.RawMessage `json:"holdings"`
		Close    json.RawMessage `json:"close"`
	}{methods, spawn.Result, wait.Result, holdings.Result, closed.Result})
	if err != nil {
		t.Fatalf("marshal external cycle: %v", err)
	}
	_, _ = os.Stdout.Write(append(payload, '\n'))
}

func mustDecodeHappyResult(t *testing.T, raw json.RawMessage, dst any) {
	t.Helper()
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("decode result %s: %v", raw, err)
	}
}

type happyRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}

type happyRPCResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *happyRPCError  `json:"error"`
}

type happyExternalClient struct {
	conn   net.Conn
	reader *bufio.Reader
}

func openHappyExternalClient(socket string) (*happyExternalClient, error) {
	conn, err := net.DialTimeout("unix", socket, 10*time.Second)
	if err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Now().Add(120 * time.Second)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &happyExternalClient{conn: conn, reader: bufio.NewReader(conn)}, nil
}

func (c *happyExternalClient) Close() {
	_ = c.conn.Close()
}

func (c *happyExternalClient) Call(method, params string) happyRPCResponse {
	request := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q,"params":%s}`+"\n", method, params)
	if _, err := io.WriteString(c.conn, request); err != nil {
		return happyRPCResponse{Error: &happyRPCError{Message: err.Error()}}
	}
	line, err := c.reader.ReadBytes('\n')
	if err != nil {
		return happyRPCResponse{Error: &happyRPCError{Message: err.Error()}}
	}
	var response happyRPCResponse
	if err := json.Unmarshal(line, &response); err != nil {
		return happyRPCResponse{Error: &happyRPCError{Message: err.Error()}}
	}
	return response
}

type happyWorkerHoldingsResult struct {
	Participants []struct {
		ID      string `json:"id"`
		State   string `json:"state"`
		Summary string `json:"summary"`
	} `json:"participants"`
}

func assertHappyDeclaration(t *testing.T, got happyWorkerHoldingsResult, id string) {
	t.Helper()
	for _, participant := range got.Participants {
		if participant.ID == id {
			if participant.State != string(workers.StateLive) {
				t.Fatalf("worker %q state = %q, want live while its pane is open", id, participant.State)
			}
			if participant.Summary != happyExpectedReport {
				t.Fatalf("worker %q summary = %q, want %q", id, participant.Summary, happyExpectedReport)
			}
			return
		}
	}
	t.Fatalf("worker holdings did not contain %q: %+v", id, got.Participants)
}

// happyPaneWatch is intentionally only an observation recorder. The real
// panegrid and paneEnroller remain in the path; this recorder proves the two
// lifecycle ends without pretending that a screen classification is a worker
// fact.
type happyPaneWatch struct {
	mu      sync.Mutex
	watches []string
	exited  []string
}

func (w *happyPaneWatch) Watch(paneID, agent string) {
	w.mu.Lock()
	w.watches = append(w.watches, paneID+":"+agent)
	w.mu.Unlock()
}

func (w *happyPaneWatch) Exited(paneID string) {
	w.mu.Lock()
	w.exited = append(w.exited, paneID)
	w.mu.Unlock()
}

func (w *happyPaneWatch) counts() (int, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.watches), len(w.exited)
}

func (w *happyPaneWatch) paneIDs() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	ids := make([]string, 0, len(w.watches))
	for _, watch := range w.watches {
		if paneID, _, ok := strings.Cut(watch, ":"); ok {
			ids = append(ids, paneID)
		}
	}
	return ids
}

type happyRealPTYFactory struct {
	log            log.Logger
	kernel         lifecyclechannel.Kernel
	lanes          *sessionRegistry
	mu             sync.Mutex
	lanesBySession map[string]lifecycle.LaneID
	adapters       []*lifecyclechannel.Adapter
}

func (f *happyRealPTYFactory) NewPTY(_ context.Context, cfg pty.Config) (pty.Pty, error) {
	parent, child, err := lifecyclechannel.NewSocketPair()
	if err != nil {
		return nil, err
	}
	adapter, err := lifecyclechannel.NewStream(
		log.NewSlogAdapter(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))),
		f.kernel,
		parent,
		lifecyclechannel.WithHelloTimeout(5*time.Second),
	)
	if err != nil {
		_ = child.Close()
		return nil, err
	}
	launch := adapter.Launch()
	f.lanes.register(launch.Lane, cfg.SessionID)
	f.mu.Lock()
	f.lanesBySession[cfg.SessionID] = launch.Lane
	f.adapters = append(f.adapters, adapter)
	f.mu.Unlock()

	opts := shellintegration.LaunchOptions{
		SessionID:   cfg.SessionID,
		Enhanced:    cfg.Enhanced,
		Capability:  launch.Capability,
		Recovery:    launch.Recovery,
		Lane:        string(launch.Lane),
		Domain:      string(launch.Domain),
		Epoch:       launch.Epoch,
		LifecycleFD: 3,
	}
	shellPath, err := exec.LookPath("bash")
	if err != nil {
		_ = adapter.Close()
		_ = child.Close()
		return nil, err
	}
	rc, err := shellintegration.LocalBashRcfile(opts)
	if err != nil {
		_ = adapter.Close()
		_ = child.Close()
		return nil, err
	}
	rcFile, err := os.CreateTemp("", "nocx-worker-happy-rc-*")
	if err != nil {
		_ = adapter.Close()
		_ = child.Close()
		return nil, err
	}
	rcPath := rcFile.Name()
	if _, writeErr := rcFile.WriteString(rc); writeErr != nil {
		_ = rcFile.Close()
		_ = os.Remove(rcPath)
		_ = adapter.Close()
		_ = child.Close()
		return nil, writeErr
	}
	if closeErr := rcFile.Close(); closeErr != nil {
		_ = os.Remove(rcPath)
		_ = adapter.Close()
		_ = child.Close()
		return nil, closeErr
	}
	lp, err := pty.NewLocal(f.log, pty.Config{
		Command: shellPath,
		Args:    []string{"--rcfile", rcPath, "-i"},
		Cols:    cfg.Cols,
		// Rows was missing here: every pane this stand ever opened got a
		// real kernel window size of 0 rows regardless of cfg.Rows, silent
		// because no test before nocx-ui8q6.5 read the geometry back —
		// nocx.bash's own __nocx_agent_geometry falls to `stty size`
		// whenever $LINES is unset, and 0 fails that call's own sanity
		// check and falls further to a hardcoded 24, which is a real
		// geometry mismatch against a 120x40 corpus capture rather than a
		// missing field.
		Rows:       cfg.Rows,
		XPixel:     cfg.XPixel,
		YPixel:     cfg.YPixel,
		Enhanced:   cfg.Enhanced,
		SessionID:  cfg.SessionID,
		ExtraFiles: []*os.File{child},
	})
	_ = child.Close()
	if err != nil {
		_ = adapter.Close()
		_ = os.Remove(rcPath)
		return nil, err
	}
	go func() {
		<-lp.Done()
		_ = os.Remove(rcPath)
	}()
	return lp, nil
}

func (f *happyRealPTYFactory) closeAdapters() {
	f.mu.Lock()
	adapters := append([]*lifecyclechannel.Adapter(nil), f.adapters...)
	f.mu.Unlock()
	for _, adapter := range adapters {
		_ = adapter.Close()
	}
}

type happyRealHelperOpener struct {
	reg     *session.Reg
	factory *happyRealPTYFactory
}

func (o *happyRealHelperOpener) OpenHosted(ctx context.Context, cfg session.Config, _ string) (transport.HostedSessionOpen, bool, error) {
	sess, err := o.reg.Open(ctx, cfg)
	if err != nil {
		return transport.HostedSessionOpen{}, true, err
	}
	o.factory.mu.Lock()
	lane := o.factory.lanesBySession[string(sess.ID())]
	o.factory.mu.Unlock()
	return transport.HostedSessionOpen{
		Session:        sess,
		Generation:     "happy-test-generation",
		LifecycleLane:  lane,
		StartLifecycle: func() {},
	}, true, nil
}

type happyStand struct {
	db       content.ContentDB
	store    *workers.MemoryStore
	reg      *session.Reg
	tp       *transport.WSServer
	factory  *happyRealPTYFactory
	grid     *panegrid.Store
	watch    *happyPaneWatch
	record   *workers.Registrar
	endpoint *toolendpoint.Endpoint
	coord    session.Session
	// paneWatch and typist are set only under withHappyStandRealTyping — the
	// same paneobserve.Watcher and agenttyping.Typist the composition root
	// builds, wired into workerSpawner's readiness/typist seams instead of
	// leaving them nil (nocx-66gd0). nil for every other test, which keeps
	// happyPaneWatch's cheaper recording in place for them.
	paneWatch *paneobserve.Watcher
	typist    *agenttyping.Typist
}

type happyEndpointOwner struct{}

func (happyEndpointOwner) OwnerUID(string) (uint32, error) {
	return uint32(os.Getuid()), nil //nolint:gosec // test owner must match the current process uid
}

// happyLifecycleEmitter used to acknowledge every published establishment
// immediately; ADR-0062 removed that step, since the accept now flushes on
// the backend's own authority as soon as the kernel mints it. It stays only
// as the Emitter the publisher requires.
type happyLifecycleEmitter struct{}

func (happyLifecycleEmitter) PublishLifecycle(lifecyclepub.Fact) {}

// happyStandOption tunes the stand for a test that needs something other than
// the happy path's own values — a logger it can read back, or a deadline it can
// afford to wait out.
type happyStandOption func(*happyStandConfig)

type happyStandConfig struct {
	slogger  *slog.Logger
	deadline time.Duration
	// realTyping asks the stand to wire the real pane-observation and
	// typing stack (paneobserve.Watcher, agentdriver.Registry,
	// agentcalib.Calibrations, agenttyping.Typist) into workerSpawner's
	// readiness and typist seams, instead of leaving them nil. Only a test
	// about nocx-66gd0's delivery gate needs this; it costs a real corpus
	// replay and a background sweep goroutine that every other happy-path
	// test has no reason to pay for.
	realTyping bool
	// answering additionally wires workers.screen and workers.answer's own
	// composition-root seams (workerScreener, workerAnswerer) onto the same
	// real observation/typing stack realTyping builds, plus the shared
	// owed-task debt workerSpawner and workerAnswerer both need to hand a
	// spawn's untyped task to whichever answer finally frees it
	// (nocx-f545a.7's shape, app.go's own wiring) — see
	// withHappyStandAnswering. Implies realTyping: a screen or an answer
	// with nothing real behind them would test nothing.
	answering bool
}

// logger is the stand's own logger, and endpointSlog is the same sink the tool
// endpoint writes to — ONE sink, which is the property the product now has and
// the reason a test can assert about a whole exchange from a single buffer.
func (c happyStandConfig) logger() log.Logger { return log.NewSlogAdapter(c.slogger) }

func (c happyStandConfig) endpointSlog() *slog.Logger {
	if c.slogger != nil {
		return c.slogger
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// withHappyStandLogger makes the stand's log READABLE, which is what a test
// about what a person can read has to assert on.
func withHappyStandLogger(sl *slog.Logger) happyStandOption {
	return func(c *happyStandConfig) { c.slogger = sl }
}

// withHappyStandEnrolmentDeadline shortens the wait a test spends proving that
// a launcher which never enrols is given up on.
func withHappyStandEnrolmentDeadline(d time.Duration) happyStandOption {
	return func(c *happyStandConfig) { c.deadline = d }
}

// withHappyStandRealTyping wires workerSpawner's readiness and typist seams
// to the real orchestration stack instead of leaving them nil (nocx-66gd0):
// a real agentdriver.Registry classifying a real panegrid.Store, a real
// paneobserve.Watcher, an agentcalib.Calibrations verified against real
// corpus captures, and the real agenttyping.Typist every consumer of this
// package's typing seam goes through. Nothing about the pane, the channel or
// the enrolment changes; this only stops the spawner from treating the
// absence of an observer as licence to skip typing.
func withHappyStandRealTyping() happyStandOption {
	return func(c *happyStandConfig) { c.realTyping = true }
}

// withHappyStandAnswering wires workers.screen and workers.answer through the
// same composition-root seams app.go builds — workerScreener and
// workerAnswerer, over the real grid, watcher and typist realTyping already
// wires in, plus one shared *owedTasks so a question workers.spawn left owed
// is the SAME debt workerAnswerer pays once its answer is confirmed
// (nocx-f545a.7). nocx-f545a.5's real-Claude check is the first caller: it
// reads a stuck worker's screen and answers its folder-trust question through
// exactly the tool surface a coordinator reaches.
func withHappyStandAnswering() happyStandOption {
	return func(c *happyStandConfig) { c.realTyping = true; c.answering = true }
}

// happyCalibStore is a calibration store holding one pre-verified set. It
// exists because agentdriver's own verify_corpus_test.go builds the
// equivalent (memSet) to prove the shipped claude rule earns its typing
// authority — but that helper is unexported in a different package, and a
// _test.go symbol cannot be imported across packages. Reimplementing the
// same few lines here is cheaper and more honest than exporting a helper
// whose only caller would be a test.
type happyCalibStore struct {
	set   agentcalib.Set
	found bool
}

func (m *happyCalibStore) Load(agent string) (agentcalib.Set, bool, error) {
	if !m.found || m.set.Agent != agent {
		return agentcalib.Set{}, false, nil
	}
	return m.set, true, nil
}

func (m *happyCalibStore) Save(set agentcalib.Set) error {
	m.set, m.found = set, true
	return nil
}

// happyCorpusMoment names one labelled frame from the real corpus in
// internal/agentdriver/testdata/captures, at a mark that package's own
// claude_test.go and verify_corpus_test.go already assert a state for — this
// file introduces no new claim about what the corpus contains.
type happyCorpusMoment struct {
	label   agentcalib.Label
	capture string
	atMs    int64
}

// happyReplayCapture replays a real capture from internal/agentdriver's own
// corpus up to atMs, through agentcapture.Frames — the same replay path
// agentdriver's own tests use, and the one that goes through a real
// panegrid.Store rather than a bare emulator (agentcapture's own package doc
// names why that distinction matters: ADR-0041's column geometry).
func happyReplayCapture(t *testing.T, name string, atMs int64) panegrid.Frame {
	t.Helper()
	path := filepath.Join("..", "agentdriver", "testdata", "captures", name+".jsonl")
	header, chunks, err := agentcapture.Read(path)
	if err != nil {
		t.Fatalf("read capture %s: %v", name, err)
	}
	moments, err := agentcapture.Frames(log.NewSlogAdapter(nil), header, chunks, []int64{atMs})
	if err != nil {
		t.Fatalf("replay capture %s at %dms: %v", name, atMs, err)
	}
	return moments[0].Frame
}

// newHappyVerifiedClaudeCalibration builds a labelled set for "claude" out of
// the real corpus, exactly as a calibration walk would write one — a
// capture, and one mark per label (agentcalib's own Set doc) — using
// agentcapture.Paint so the set holds the BYTES that reproduce a frame,
// never the frame itself, which is the round trip
// TestPaintingAndReplayingAFrameDoesNotMoveTheVerdict already proves is
// sound. Only the three REQUIRED labels are given (idle, working, asks-you);
// the three optional ones are left uncalibrated on purpose, which is what
// nocx-jse6x's design allows. The marks are the exact ones
// internal/agentdriver/verify_corpus_test.go already verifies the shipped
// claude rule against, so this is not a new claim about the rule either.
func newHappyVerifiedClaudeCalibration(t *testing.T) agentcalib.Store {
	t.Helper()
	moments := []happyCorpusMoment{
		{agentcalib.LabelIdle, "claude-idle", 11000},
		{agentcalib.LabelWorking, "claude-working", 17000},
		{agentcalib.LabelAsksYou, "claude-permission", 49000},
	}
	set := agentcalib.Set{
		Agent:  "claude",
		Header: agentcapture.Header{Agent: "claude", Argv: []string{"claude"}, Cols: 120, Rows: 40},
	}
	for _, m := range moments {
		frame := happyReplayCapture(t, m.capture, m.atMs)
		mark := int64(len(set.Chunks))
		set.Chunks = append(set.Chunks, agentcapture.Chunk{
			AtMs:   mark,
			Offset: agentcapture.EndOffset(set.Chunks, len(set.Chunks)),
			Data:   string(agentcapture.Paint(frame)),
		})
		at := mark
		set.Labels = append(set.Labels, agentcalib.Record{Label: m.label, AtMs: &at})
	}
	store := &happyCalibStore{}
	if err := store.Save(set); err != nil {
		t.Fatalf("save calibration set: %v", err)
	}
	return store
}

func newHappyStand(t *testing.T, opts ...happyStandOption) *happyStand {
	t.Helper()
	ctx := context.Background()
	cfg := happyStandConfig{deadline: 10 * time.Second}
	for _, o := range opts {
		o(&cfg)
	}
	logger := cfg.logger()
	stand := &happyStand{}

	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	db, err := content.Open(ctx, content.Config{
		Path: filepath.Join(dir, "content.db"), Key: key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger: logger,
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	stand.db = db

	lanes := newSessionRegistry()
	var watch *happyPaneWatch
	var paneWatcherSeam paneWatcher
	grid := panegrid.New(logger)
	factory := &happyRealPTYFactory{log: logger, lanes: lanes, lanesBySession: make(map[string]lifecycle.LaneID)}
	reg := session.New(logger, factory)
	enrol := newWorkerEnrolments(logger, reg)

	// The real observation and typing stack (nocx-66gd0), built before the
	// enroller so its watcher can be the one paneEnrol.Enrol calls Watch on.
	// Nil until proven otherwise, so the ordinary stand keeps costing nothing.
	var (
		realWatch   *paneobserve.Watcher
		paneTyping  *agenttyping.Typist
		stopSweep   context.CancelFunc
		sweepStopCh chan struct{}
	)
	if cfg.realTyping {
		paneDrivers, driversErr := agentdriver.NewRegistry(agentdriver.Claude())
		if driversErr != nil {
			t.Fatalf("pane drivers: %v", driversErr)
		}
		realWatch = paneobserve.New(logger, grid, paneDrivers, paneobserve.Config{})
		paneWatcherSeam = realWatch
		calibStore := newHappyVerifiedClaudeCalibration(t)
		// Screens is nil deliberately, exactly as agentdriver's own
		// verify_corpus_test.go leaves it: verification replays a stored
		// set and never reads a live pane, so passing one would suggest it
		// could.
		paneCalibration := agentcalib.New(logger, nil, calibStore, paneDrivers)
		paneTyping = newPaneTypist(logger, grid, paneDrivers, paneCalibration, realWatch, reg)
	} else {
		watch = &happyPaneWatch{}
		paneWatcherSeam = watch
	}

	paneEnrol, err := newPaneEnroller(logger, lanes, grid, paneWatcherSeam, allowPaneApproval{})
	if err != nil {
		t.Fatalf("pane enroller: %v", err)
	}
	paneEnrol = enrol.hookInto(paneEnrol)
	report := &workerReporter{lanes: lanes, enrol: enrol, log: logger, now: time.Now}
	kernel := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(kernel,
		lifecyclepub.WithAgentEnroller(paneEnrol),
		lifecyclepub.WithAgentReporter(report),
	)
	pub.SetEmitter(happyLifecycleEmitter{})
	factory.kernel = pub
	opener := &happyRealHelperOpener{reg: reg, factory: factory}
	tpOpts := []transport.WSServerOption{transport.WithHelperSessionOpener(opener)}
	if cfg.realTyping {
		tpOpts = append(tpOpts, transport.WithPaneGrid(grid), transport.WithPaneObserver(realWatch))
	}
	tp := transport.NewWSServer(logger, reg, tpOpts...)
	if cfg.realTyping {
		// Bound post-construction, exactly as the composition root binds it
		// (app.go): the server is built after the things that enrol into
		// it. Sweep does nothing at all until this is set (paneobserve's own
		// doc), so without it readiness would never answer.
		realWatch.SetEmitter(tp.EmitPaneObservation)
		// Production drives Sweep from transport's own coalescing ticker,
		// started by WSServer.Start — which this stand never calls, since it
		// talks to the server in-process rather than over its listener. So
		// this test drives Sweep directly, exactly as paneobserve's own
		// package doc prescribes for a test ("a test drives it directly, and
		// therefore asserts on a state change rather than on a duration").
		// The classification itself is the real Sweep method against the
		// real grid; only the clock that calls it is smaller than
		// production's.
		sweepCtx, cancel := context.WithCancel(context.Background())
		stopSweep = cancel
		sweepStopCh = make(chan struct{})
		go func() {
			defer close(sweepStopCh)
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-sweepCtx.Done():
					return
				case <-ticker.C:
					realWatch.Sweep()
				}
			}
		}()
	}

	store := workers.NewMemoryStore()
	sup := &workerSupervisor{sessions: reg, log: logger}
	spawner := &workerSpawner{layout: db.Layout(), opener: tp, sessions: reg, enrolments: enrol, workspace: string(workspace.Default), log: logger}
	if cfg.realTyping {
		spawner.readiness = realWatch
		spawner.typist = paneTyping
	}
	recordOpts := []workers.Option{
		workers.WithEnrolmentDeadline(cfg.deadline),
		workers.WithLogger(logger),
		workers.WithCloser(&workerCloser{sessions: reg, log: logger}),
	}
	if cfg.answering {
		// The shared debt spawner marks and workerAnswerer pays, exactly as
		// app.go's own workerOwed is shared between the two (nocx-f545a.7):
		// two separate instances would let a question workers.spawn left
		// owed go unpaid forever, because the answerer would be checking a
		// debt nobody had marked on ITS OWN set.
		owed := newOwedTasks()
		spawner.owed = owed
		recordOpts = append(recordOpts,
			workers.WithScreener(&workerScreener{grid: grid, watch: realWatch}),
			workers.WithAnswerer(&workerAnswerer{
				grid: grid, typist: paneTyping,
				owed: owed, classify: realWatch, typing: paneTyping, log: logger,
			}),
		)
	}
	record := workers.NewRegistrar(store, spawner, enrol, sup, recordOpts...)
	report.declare = func(ctx context.Context, id workers.ParticipantID, l workers.Liveness, d workers.Declaration) error {
		_, declareErr := record.Declared(ctx, id, l, d)
		return declareErr
	}
	sup.exited = func(ctx context.Context, id workers.ParticipantID, l workers.Liveness, e workers.Exit) {
		_, _ = record.Exited(ctx, id, l, e)
	}

	registry := toolRegistry(t)
	auth := mustToolAuthorizer(t, peerpin.SystemPinner{}, reg, grid, record, workerTestWorkspace, allowWorkerApproval{})
	dispatcher, err := assistant.NewToolDispatcher(registry, record, content.EnvironmentIDFor(content.EnvLocal, ""))
	if err != nil {
		t.Fatalf("new worker dispatcher: %v", err)
	}
	dispatcher, err = assistant.NewAttemptRecordingDispatcher(registry, dispatcher, db.Ledger())
	if err != nil {
		t.Fatalf("wrap worker dispatcher with ledger: %v", err)
	}
	endpoint, err := toolendpoint.New(toolendpoint.Config{
		Dir:      filepath.Join(shortWorkerSocketDir(t), "runtime"),
		Peers:    coordsock.SystemPeerCredentials{},
		SelfUID:  uint32(os.Getuid()), //nolint:gosec // uid is not a signed quantity
		Owner:    happyEndpointOwner{},
		Logger:   cfg.endpointSlog(),
		Auth:     auth,
		Dispatch: dispatcher,
	})
	if err != nil {
		t.Fatalf("new worker endpoint: %v", err)
	}
	if startErr := endpoint.Start(); startErr != nil {
		t.Fatalf("start worker endpoint: %v", startErr)
	}

	coordOpened, err := tp.OpenSession(ctx, transport.OpenSpec{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("open coordinator session: %v", err)
	}
	coord := coordOpened.Session
	if err := reg.RecordOwnedProcessPID(coord.ID(), os.Getpid()); err != nil {
		t.Fatalf("record coordinator root: %v", err)
	}
	if err := grid.Enrol(string(coord.ID()), 80, 24); err != nil {
		t.Fatalf("enrol coordinator pane: %v", err)
	}

	stand.reg, stand.tp, stand.factory, stand.grid, stand.watch = reg, tp, factory, grid, watch
	stand.store, stand.record, stand.endpoint, stand.coord = store, record, endpoint, coord
	stand.paneWatch, stand.typist = realWatch, paneTyping
	t.Cleanup(func() {
		_ = endpoint.Close()
		for _, sess := range reg.List() {
			_ = reg.Close(sess.ID())
		}
		waittest.WaitFor(t, "worker record to settle", func() bool {
			open, err := store.NonTerminal(context.Background(), workers.ID(coord.ID()))
			return err == nil && len(open) == 0
		})
		factory.closeAdapters()
		grid.Withdraw(string(coord.ID()))
		_ = tp.Stop(context.Background())
		_ = db.Close()
	})
	if cfg.realTyping {
		// Stops the sweep goroutine BEFORE the cleanup above tears the grid
		// and the sessions down — registered after, so LIFO runs it first.
		t.Cleanup(func() {
			stopSweep()
			<-sweepStopCh
		})
	}
	return stand
}

func runHappyExternalCoordinator(t *testing.T, socket string) map[string]json.RawMessage {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := execCommandContext(ctx, binary, "-test.run=TestWorkerHappyExternalCoordinator", "-test.timeout=90s")
	cmd.Env = append(os.Environ(), happyExternalEnv+"="+socket)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("external coordinator: %v\nstdout: %s\nstderr: %s", err, out, stderr.String())
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var result struct {
			Methods  []string        `json:"methods"`
			Spawn    json.RawMessage `json:"spawn"`
			Wait     json.RawMessage `json:"wait"`
			Holdings json.RawMessage `json:"holdings"`
			Close    json.RawMessage `json:"close"`
		}
		if json.Unmarshal([]byte(line), &result) == nil {
			if len(result.Methods) != 4 {
				t.Fatalf("external cycle methods = %v, want four direct workers.* calls", result.Methods)
			}
			return map[string]json.RawMessage{"spawn": result.Spawn, "wait": result.Wait, "holdings": result.Holdings, "close": result.Close}
		}
	}
	t.Fatalf("external coordinator produced no cycle result: %s", out)
	return nil
}

// TestExternalClaudeDrivesAWorkerEndToEnd is the epic's DONE WHEN. It uses a
// real bash PTY, the authenticated lifecycle channel, the real pane grid, the
// real worker record, the shipped peer authorizer, the published unix socket,
// and a real second process as the coordinator. The fake claude binary only
// speaks the staged declaration surface; CI cannot require a subscription CLI,
// so this check does not prove the vendor's model chose these calls. The
// recorded manual run under .internal/ is the separate evidence for real
// Claude. No assistant engine or model loop is constructed here: the external
// process calls workers.* directly, and only the shared worker dispatcher is
// used to reach the record.
func TestExternalClaudeDrivesAWorkerEndToEnd(t *testing.T) {
	command := os.Getenv(happyCommandEnv)
	if command == "" {
		fakeDir := t.TempDir()
		fakeClaude := filepath.Join(fakeDir, "claude")
		mutation := os.Getenv(happyMutationEnv)
		script := "#!/bin/sh\nprintf 'FAKE-CLAUDE-RAN\\n'\n"
		if mutation != "skip-report" {
			summary := happyExpectedReport
			if mutation == "wrong-summary" {
				summary = "a different result"
			}
			script += "printf 'ok\\n%s' '" + summary + "' > \"$NOCX_AGENT_REPORT\"\n"
		}
		if err := os.WriteFile(fakeClaude, []byte(script), 0o700); err != nil { //nolint:gosec // the test launcher must be executable
			t.Fatalf("write fake claude: %v", err)
		}
		t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	} else {
		t.Logf("using external worker command %q", command)
	}
	stand := newHappyStand(t)
	cycle := runHappyExternalCoordinator(t, stand.endpoint.SocketPath())

	var spawned struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	mustDecodeHappyResult(t, cycle["spawn"], &spawned)
	if spawned.ID == "" || spawned.State != string(workers.StateLive) {
		t.Fatalf("external spawn = %s, want a live worker", cycle["spawn"])
	}

	var waited happyWorkerHoldingsResult
	mustDecodeHappyResult(t, cycle["wait"], &waited)
	assertHappyDeclaration(t, waited, spawned.ID)
	var readback happyWorkerHoldingsResult
	mustDecodeHappyResult(t, cycle["holdings"], &readback)
	assertHappyDeclaration(t, readback, spawned.ID)

	var closed struct {
		ID    string `json:"id"`
		Ended bool   `json:"ended"`
	}
	mustDecodeHappyResult(t, cycle["close"], &closed)
	if closed.ID != spawned.ID || !closed.Ended {
		t.Fatalf("external close = %s, want ended worker %q", cycle["close"], spawned.ID)
	}
	var stored workers.Participant
	waittest.WaitFor(t, "worker declaration and close to reach the record", func() bool {
		var err error
		stored, err = stand.store.Participant(context.Background(), workers.ParticipantID(spawned.ID))
		return err == nil && stored.State == workers.StateCompleted
	})
	if stored.Group != workers.ID(stand.coord.ID()) || stored.Declared == nil || stored.Declared.Summary != happyExpectedReport {
		t.Fatalf("stored worker = %+v, want coordinator %q and declaration %q", stored, stand.coord.ID(), happyExpectedReport)
	}
	t.Logf("worker record: id=%s group=%s state=%s session=%s summary=%q", stored.ID, stored.Group, stored.State, stored.Liveness.SessionID, stored.Declared.Summary)
	if stored.Liveness.SessionID == "" {
		t.Fatalf("stored worker has no worker session: %+v", stored)
	}
	watches, exits := stand.watch.counts()
	paneIDs := stand.watch.paneIDs()
	if watches != 1 || len(paneIDs) != 1 || paneIDs[0] == "" {
		t.Fatalf("pane observation = watches %d, exits %d, pane ids %v; want one watch for a real pane", watches, exits, paneIDs)
	}
}

// execCommandContext is kept local so the external process is visibly the
// test binary re-executed, not an assistant/model subprocess or herdr.
func execCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

func TestManualRealClaudeCoordinator(t *testing.T) {
	if os.Getenv("NOCX_MANUAL_REAL_COORDINATOR") == "" {
		t.Skip("manual real coordinator run")
	}
	stand := newHappyStand(t)
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	workDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Dir(filepath.Dir(workDir))
	configPath := filepath.Join(t.TempDir(), "mcp.json")
	config, err := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			"nocx": map[string]any{
				"type":    "stdio",
				"command": goBinary,
				"args":    []string{"run", "./cmd/nocx-helper", "mcp", "--socket", stand.endpoint.SocketPath()},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	prompt := `Act as the coordinator. Use only the nocx workers tools and perform this exact cycle:
1. Call workers.spawn exactly once with command ` + "`claude -p 'Read AGENTS.md. Then write exactly two lines to \\\"$NOCX_AGENT_REPORT\\\": first ok, second read AGENTS.md and reported from an external worker.'`" + ` and task ` + "`read AGENTS.md and report`" + `.
2. Call workers.wait with seconds 60.
3. Call workers.holdings and inspect the declaration.
4. Call workers.close for the worker id returned by workers.spawn.
Do not use Bash or any other tool. After the cycle, answer with the exact ordered tool names and the complete JSON results from all four calls.`
	cmd := exec.Command("claude", "--print", "--no-session-persistence", "--dangerously-skip-permissions", "--strict-mcp-config", "--mcp-config", configPath, "--output-format", "stream-json", "--verbose", "-p", prompt) //nolint:gosec // manual test intentionally launches the installed Claude CLI
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	var output strings.Builder
	cmd.Stdout = &output
	cmd.Stderr = &output
	runErr := cmd.Run()
	t.Logf("claude output:\n%s", output.String())
	if runErr != nil {
		t.Fatal(runErr)
	}
	var held []workers.Participant
	waittest.WaitFor(t, "manual worker record to settle", func() bool {
		var err error
		held, err = stand.store.HeldBy(context.Background(), string(stand.coord.ID()))
		return err == nil && len(held) == 1 && held[0].State == workers.StateCompleted && held[0].Declared != nil
	})
	if len(held) != 1 || held[0].Group != workers.ID(stand.coord.ID()) || held[0].Liveness.SessionID == "" || held[0].Declared.Summary != happyExpectedReport {
		t.Fatalf("manual worker record = %+v, want one completed external worker for coordinator %q with declaration %q", held, stand.coord.ID(), happyExpectedReport)
	}
	t.Logf("manual worker record: id=%s group=%s state=%s session=%s summary=%q", held[0].ID, held[0].Group, held[0].State, held[0].Liveness.SessionID, held[0].Declared.Summary)
}
