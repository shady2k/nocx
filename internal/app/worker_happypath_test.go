package app

import (
	"bufio"
	"bytes"
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

	"github.com/shady2k/nocx/internal/log/logtest"

	"github.com/shady2k/nocx/internal/agentcalib"
	"github.com/shady2k/nocx/internal/agentcapture"
	"github.com/shady2k/nocx/internal/agentcapture/replaylocal"
	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/agenttyping"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	coordsock "github.com/shady2k/nocx/internal/coordinator"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/paneobserve"
	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/paneview/paneviewtest"
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
	happyExternalEnv = "NOCX_TEST_WORKER_HAPPY_EXTERNAL"
	happyCommandEnv  = "NOCX_TEST_WORKER_HAPPY_COMMAND"
)

// TestWorkerHappyExternalCoordinator is a REAL second process. It is the
// external coordinator for TestExternalClaudeDrivesAWorkerEndToEnd, not
// nocx's assistant engine. The three calls are deliberately direct JSON-RPC
// worker calls: spawn a real pane, read what the session holds, and close the
// participant.
//
// THE WAIT AND THE DECLARATION LEFT THIS CYCLE with ADR-0070: an interactive
// worker never exits, so a wait on its exit held to its deadline, and the
// verdict a wrapper used to send is not a fact nocx records. What a
// coordinator has instead is its MAILBOX — the worker's own report and the
// states nocx saw — which is worker_report_test.go's and
// worker_observed_inbox_test.go's subject.
func TestWorkerHappyExternalCoordinator(t *testing.T) {
	socket := os.Getenv(happyExternalEnv)
	if socket == "" {
		t.Skip("not the external coordinator")
	}

	methods := []string{"workers.spawn", "workers.holdings", "workers.close"}
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

	holdings := client.Call("workers.holdings", `{}`)
	if holdings.Error != nil {
		t.Fatalf("workers.holdings: %+v", holdings.Error)
	}
	var readback happyWorkerHoldingsResult
	mustDecodeHappyResult(t, holdings.Result, &readback)
	assertHappyWorker(t, readback, spawned.ID, string(workers.StateLive))

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
		Holdings json.RawMessage `json:"holdings"`
		Close    json.RawMessage `json:"close"`
	}{methods, spawn.Result, holdings.Result, closed.Result})
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
		ID    string `json:"id"`
		State string `json:"state"`
		Task  string `json:"task"`
	} `json:"participants"`
}

func assertHappyWorker(t *testing.T, got happyWorkerHoldingsResult, id, wantState string) {
	t.Helper()
	for _, participant := range got.Participants {
		if participant.ID == id {
			if participant.State != wantState {
				t.Fatalf("worker %q state = %q, want %q", id, participant.State, wantState)
			}
			if participant.Task == "" {
				t.Fatalf("worker %q is listed with no task", id)
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
	// views is the stand's stand-in for the runtime beside the PTY: every byte
	// a pane prints is fed to it, from the same fd the session reads. In
	// production that reader IS the session's one handler (the transport's
	// pump) and the emulator sits behind it; here the pump belongs to the
	// product, so the stand tees the PTY itself rather than deriving a screen
	// from a stream the coordinator no longer reads.
	views *paneviewtest.Views
	// transcript holds the raw bytes every pane this factory opened actually
	// printed — nocx.bash's own stderr included, which is where "nocx: tool
	// surface unavailable" or a shell's own error about a failed redirect
	// would land. Nothing before nocx-xn63t.6.1 kept this: a failure inside
	// the worker's pane (as opposed to the external coordinator process,
	// whose own stdout/stderr runHappyExternalCoordinator already prints on
	// failure) left no trace at all — the CI dump for the mac run that
	// motivated this had nothing between "agent enrolled" and "agent
	// withdrawn" to say why no agent_report ever followed. Read on test
	// failure only (happyPaneTranscripts), so a passing run pays nothing.
	transcript *happyPaneTranscripts
}

// happyPaneTranscripts records every byte each pane produced, keyed by pane
// id, so a failing test can print exactly what a person watching that pane
// would have seen — including whatever the shell wrote to its own stderr
// before nocx ever gets a structured fact about it.
type happyPaneTranscripts struct {
	mu   sync.Mutex
	byID map[string]*bytes.Buffer
}

func newHappyPaneTranscripts() *happyPaneTranscripts {
	return &happyPaneTranscripts{byID: make(map[string]*bytes.Buffer)}
}

func (h *happyPaneTranscripts) feed(paneID string, b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	buf, ok := h.byID[paneID]
	if !ok {
		buf = &bytes.Buffer{}
		h.byID[paneID] = buf
	}
	buf.Write(b)
}

// dump renders every pane's transcript so far, for a t.Cleanup that only
// calls it once the test has already failed.
func (h *happyPaneTranscripts) dump() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out strings.Builder
	for paneID, buf := range h.byID {
		fmt.Fprintf(&out, "--- pane %s ---\n%s\n", paneID, buf.String())
	}
	return out.String()
}

// teePTY hands every byte a pane produced to the stand's pane source as well as
// to whoever read it. That order is the product's: the runtime is fed before
// anything downstream is served, so a frame read never describes a stream that
// had not reached the emulator yet.
type teePTY struct {
	pty.Pty
	feed func([]byte)
}

func (t teePTY) Read(b []byte) (int, error) {
	n, err := t.Pty.Read(b)
	if n > 0 && t.feed != nil {
		t.feed(b[:n])
	}
	return n, err
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
	paneID := cfg.SessionID
	transcript := f.transcript
	if f.views == nil {
		if transcript == nil {
			return lp, nil
		}
		return teePTY{Pty: lp, feed: func(b []byte) { transcript.feed(paneID, b) }}, nil
	}
	views := f.views
	return teePTY{Pty: lp, feed: func(b []byte) {
		views.Feed(paneID, b)
		if transcript != nil {
			transcript.feed(paneID, b)
		}
	}}, nil
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
	// views is the stand's pane source. A session this opener starts is a
	// session the product will be asked to WATCH (the agent wrapper enrols),
	// and the source has to know the pane before that question arrives — in
	// production the helper holds the runtime from spawn, and in a stand this
	// is what stands in for that.
	views *paneviewtest.Views
}

func (o *happyRealHelperOpener) OpenHosted(ctx context.Context, cfg session.Config, _ string) (transport.HostedSessionOpen, bool, error) {
	sess, err := o.reg.Open(ctx, cfg)
	if err != nil {
		return transport.HostedSessionOpen{}, true, err
	}
	if o.views != nil {
		o.views.Size(string(sess.ID()), int(cfg.Cols), int(cfg.Rows))
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
	grid     *paneviewtest.Views
	watch    *happyPaneWatch
	record   *workers.Registrar
	endpoint *toolendpoint.Endpoint
	coord    session.Session
	// paneWatch and typist are set only under withHappyStandRealTyping — the
	// same paneobserve.Watcher and agenttyping.Typist the composition root
	// builds, wired into workerSpawner's readiness seam and happyTaskQueue
	// instead of leaving them nil (nocx-66gd0). nil for every other test,
	// which keeps happyPaneWatch's cheaper recording in place for them.
	paneWatch *paneobserve.Watcher
	typist    *agenttyping.Typist
}

// happyTaskQueue is newHappyStand's own workers.TaskQueue (Task 11), reaching
// the SAME real readiness/typing stack withHappyStandRealTyping already
// wires — never a second, faked answer to "may nocx write into this pane".
// It is not the shipped internal/app.paneMessages: that one delivers through
// a real helper's session.intent (design §8), and this stand runs no helper
// at all (Task 14's own happypath extends this same newHappyStand with one).
// What this stand's tests need from a TaskQueue is only "the rules the
// coordinator's worker reports under, and then the task it was given,
// eventually reach the pane once it is free", and awaitFreeText plus the real
// Typist already answer that faithfully.
type happyTaskQueue struct {
	readiness paneReadiness
	typist    paneTypist
	log       log.Logger
}

// EnqueueBriefing types the briefing's one message (nocx-xn63t.4.16). It types
// ONE text and not the two halves separately: the rules and the task travel
// joined, rules first, which is what tells the worker both what it is and what
// to do without leaving the rules alone as a turn of their own.
func (q *happyTaskQueue) EnqueueBriefing(_ context.Context, _ string, participant workers.Participant, briefing workers.Briefing) error {
	go func() {
		paneID := participant.Liveness.SessionID
		state, err := awaitFreeText(context.Background(), q.readiness, paneID, q.log, "worker spawn (happy stand)")
		if err != nil || state != agentdriver.StateFreeText {
			return
		}
		q.typist.Submit(context.Background(), paneID, briefing.Text())
	}()
	return nil
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
	// readiness seam and the record's TaskQueue (happyTaskQueue, below),
	// instead of leaving them nil. Only a test about nocx-66gd0's delivery
	// gate needs this; it costs a real corpus replay and a background sweep
	// goroutine that every other happy-path test has no reason to pay for.
	realTyping bool
}

// logger is the stand's own logger, and endpointSlog is the same sink the tool
// endpoint writes to — ONE sink, which is the property the product now has and
// the reason a test can assert about a whole exchange from a single buffer.
func (c happyStandConfig) logger() log.Logger { return log.NewSlogAdapter(c.slogger) }

func (c happyStandConfig) endpointSlog(t testing.TB) *slog.Logger {
	if c.slogger != nil {
		return c.slogger
	}
	return logtest.Slog(t)
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
// a real agentdriver.Registry classifying a real *paneview.Store, a real
// paneobserve.Watcher, an agentcalib.Calibrations verified against real
// corpus captures, and the real agenttyping.Typist every consumer of this
// package's typing seam goes through. Nothing about the pane, the channel or
// the enrolment changes; this only stops the spawner from treating the
// absence of an observer as licence to skip typing.
func withHappyStandRealTyping() happyStandOption {
	return func(c *happyStandConfig) { c.realTyping = true }
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
// *paneview.Store rather than a bare emulator (agentcapture's own package doc
// names why that distinction matters: ADR-0041's column geometry).
func happyReplayCapture(t *testing.T, name string, atMs int64) paneview.Frame {
	t.Helper()
	path := filepath.Join("..", "agentdriver", "testdata", "captures", name+".jsonl")
	header, chunks, err := agentcapture.Read(path)
	if err != nil {
		t.Fatalf("read capture %s: %v", name, err)
	}
	moments, err := agentcapture.Frames(context.Background(), replaylocal.Replayer{}, header, chunks, []int64{atMs})
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
	grid := paneviewtest.NewViews(logger)
	factory := &happyRealPTYFactory{
		log: logger, lanes: lanes, lanesBySession: make(map[string]lifecycle.LaneID),
		views: grid, transcript: newHappyPaneTranscripts(),
	}
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
		realWatch = paneobserve.New(logger, grid.Store, paneDrivers, paneobserve.Config{})
		paneWatcherSeam = realWatch
		calibStore := newHappyVerifiedClaudeCalibration(t)
		// Screens is nil deliberately, exactly as agentdriver's own
		// verify_corpus_test.go leaves it: verification replays a stored
		// set and never reads a live pane, so passing one would suggest it
		// could.
		paneCalibration := agentcalib.New(logger, nil, calibStore, paneDrivers, replaylocal.Replayer{})
		paneTyping = newPaneTypist(logger, grid.Store, paneDrivers, paneCalibration, realWatch, reg)
	} else {
		watch = &happyPaneWatch{}
		paneWatcherSeam = watch
	}

	paneEnrol, err := newPaneEnroller(logger, lanes, grid.Store, paneWatcherSeam, allowPaneApproval{})
	if err != nil {
		t.Fatalf("pane enroller: %v", err)
	}
	paneEnrol = enrol.hookInto(paneEnrol)
	kernel := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(kernel,
		lifecyclepub.WithAgentEnroller(paneEnrol),
	)
	pub.SetEmitter(happyLifecycleEmitter{})
	factory.kernel = pub
	opener := &happyRealHelperOpener{reg: reg, factory: factory, views: grid}
	tpOpts := []transport.WSServerOption{transport.WithHelperSessionOpener(opener)}
	if cfg.realTyping {
		tpOpts = append(tpOpts, transport.WithPaneScreens(grid.Store), transport.WithPaneObserver(realWatch))
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
	// seats is WHICH TAB each participant's pane was minted in
	// (nocx-xn63t.4.6), wired into the spawner and the closer exactly as
	// app.go wires it — this stand is where the external-coordinator journey
	// runs, and that journey is where "closing a worker closes its tab" is
	// measured end to end.
	seats := newWorkerTabs()
	spawner := &workerSpawner{
		layout: db.Layout(), opener: tp, sessions: reg, enrolments: enrol,
		workspace: string(workspace.Default),
		// The product's own push (nocx-ui8q6.3) and its tab-close counterpart
		// (nocx-xn63t.4.6): this stand talks to the server in-process and no
		// window is connected to it, so both answer their "nobody is
		// connected" branch and log the drop — the same thing production does
		// with no renderer attached.
		announce: tp, tabs: seats, log: logger,
	}
	if cfg.realTyping {
		spawner.readiness = realWatch
	}
	recordOpts := []workers.Option{
		workers.WithEnrolmentDeadline(cfg.deadline),
		workers.WithLogger(logger),
		workers.WithCloser(&workerCloser{
			sessions: reg, layout: db.Layout(), tabs: seats, announce: tp, log: logger,
		}),
	}
	record := workers.NewRegistrar(store, spawner, enrol, sup, recordOpts...)
	if cfg.realTyping {
		// Task 11's own delivery seam (design §9): the task is enqueued once
		// this participant is live, exactly as app.go's own SetTaskQueue
		// wires it, over a queue built for this stand rather than the full
		// helper-backed session.message stack — this stand has no real
		// helper runtime behind its panes at all (that is Task 14's own
		// happypath, which extends this same newHappyStand for exactly that
		// reason). happyTaskQueue reaches the SAME real typing gate
		// this stand already wires for readiness, so
		// TestACoordinatorSpawnsAWorkerAndTypesItsTask's own assertion — the
		// briefing lands in the pane as a bracketed paste and a submit key —
		// still exercises a real gate rather than a double that only
		// records a call.
		record.SetTaskQueue(&happyTaskQueue{readiness: realWatch, typist: paneTyping, log: logger})
	}
	sup.exited = func(ctx context.Context, id workers.ParticipantID, l workers.Liveness, e workers.Exit) {
		_, _ = record.Exited(ctx, id, l, e)
	}

	registry := toolRegistry(t)
	auth := mustToolAuthorizer(t, peerpin.SystemPinner{}, reg, grid, record, workerTestWorkspace, allowWorkerApproval{})
	dispatcher, err := assistant.NewToolDispatcher(registry, workerRecordForTools{record}, content.EnvironmentIDFor(content.EnvLocal, ""))
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
		Logger:   cfg.endpointSlog(t),
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
	if err := grid.Watch(string(coord.ID()), 80, 24); err != nil {
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
			Holdings json.RawMessage `json:"holdings"`
			Close    json.RawMessage `json:"close"`
		}
		if json.Unmarshal([]byte(line), &result) == nil {
			if len(result.Methods) != 3 {
				t.Fatalf("external cycle methods = %v, want three direct workers.* calls", result.Methods)
			}
			return map[string]json.RawMessage{"spawn": result.Spawn, "holdings": result.Holdings, "close": result.Close}
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
		script := "#!/bin/sh\nprintf 'FAKE-CLAUDE-RAN\\n'\n"
		if err := os.WriteFile(fakeClaude, []byte(script), 0o700); err != nil { //nolint:gosec // the test launcher must be executable
			t.Fatalf("write fake claude: %v", err)
		}
		t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	} else {
		t.Logf("using external worker command %q", command)
	}
	stand := newHappyStand(t)
	// A TAB OF THE PERSON'S OWN, minted before anything is spawned. Without
	// it the worker's tab is the ONLY tab in the window, and the close would
	// then be answered by content's own replacement — a tab minted so that the
	// application is never left with none — which would make "the tab is gone"
	// indistinguishable from "a different tab is there". A person with one tab
	// open is the ordinary case for a coordinator anyway.
	if _, err := stand.db.Layout().CreateTab(context.Background(),
		content.Tab{ID: happyPersonTab, WorkspaceID: workerTestWorkspace, Layout: content.LayoutRow},
		content.Pane{ID: happyPersonPane, TabID: happyPersonTab, Kind: content.PaneLocal, SizeShare: 1},
	); err != nil {
		t.Fatalf("the person's own tab: %v", err)
	}
	// Nothing before nocx-xn63t.6.1 printed what the worker's OWN pane saw —
	// runHappyExternalCoordinator already surfaces the external coordinator
	// process's stdout/stderr on failure, but a report that never arrived
	// could be the shell's fault (an nocx.bash refusal, a fake claude that
	// never ran, a write that failed) with nothing to say so. t.Cleanup, not
	// a defer near the assertions, so it also catches a t.Fatalf raised by a
	// helper this test calls.
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("worker pane transcript(s):\n%s", stand.factory.transcript.dump())
		}
	})
	cycle := runHappyExternalCoordinator(t, stand.endpoint.SocketPath())

	var spawned struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	mustDecodeHappyResult(t, cycle["spawn"], &spawned)
	if spawned.ID == "" || spawned.State != string(workers.StateLive) {
		t.Fatalf("external spawn = %s, want a live worker", cycle["spawn"])
	}

	var readback happyWorkerHoldingsResult
	mustDecodeHappyResult(t, cycle["holdings"], &readback)
	assertHappyWorker(t, readback, spawned.ID, string(workers.StateLive))

	var closed struct {
		ID    string `json:"id"`
		Ended bool   `json:"ended"`
	}
	mustDecodeHappyResult(t, cycle["close"], &closed)
	if closed.ID != spawned.ID || !closed.Ended {
		t.Fatalf("external close = %s, want ended worker %q", cycle["close"], spawned.ID)
	}
	var stored workers.Participant
	waittest.WaitFor(t, "the close to reach the record", func() bool {
		var err error
		stored, err = stand.store.Participant(context.Background(), workers.ParticipantID(spawned.ID))
		return err == nil && stored.State == workers.StateClosed
	})
	if stored.Group != workers.ID(stand.coord.ID()) {
		t.Fatalf("stored worker = %+v, want coordinator %q", stored, stand.coord.ID())
	}
	t.Logf("worker record: id=%s group=%s state=%s session=%s", stored.ID, stored.Group, stored.State, stored.Liveness.SessionID)
	if stored.Liveness.SessionID == "" {
		t.Fatalf("stored worker has no worker session: %+v", stored)
	}
	watches, exits := stand.watch.counts()
	paneIDs := stand.watch.paneIDs()
	if watches != 1 || len(paneIDs) != 1 || paneIDs[0] == "" {
		t.Fatalf("pane observation = watches %d, exits %d, pane ids %v; want one watch for a real pane", watches, exits, paneIDs)
	}

	// AND THE WORKER'S TAB IS GONE (nocx-xn63t.4.6) — stage 4's own promise to
	// the owner, measured here because this is the journey where the whole path
	// is real: the external coordinator's workers.close is what took it out,
	// and what is read is the CONTENT STORE a restart reads back rather than a
	// fact either side asserted about itself.
	//
	// THE PERSON'S TAB IS STILL THERE AND ALONE, which is the pair of facts
	// that makes this an assertion rather than a count: the window holds the
	// one tab it held before the spawn, at seat 0, and nothing else. A close
	// that left the worker's tab behind fails the first half; a close that
	// took the strip with it — or that the store answered with a replacement
	// tab — fails the second.
	tabs, err := stand.db.Layout().Tabs(context.Background(), workerTestWorkspace)
	if err != nil {
		t.Fatalf("the strip after the close: %v", err)
	}
	if len(tabs) != 1 || tabs[0].ID != happyPersonTab {
		t.Fatalf("the strip after the close = %+v, want exactly the person's own tab %q: workers.close left the worker's tab standing",
			tabs, happyPersonTab)
	}
	if tabs[0].Position != 0 {
		t.Fatalf("the person's tab sits at position %d, want 0 — the strip is not dense after the close", tabs[0].Position)
	}
	snap, err := stand.db.Layout().Snapshot(context.Background())
	if err != nil {
		t.Fatalf("layout snapshot after the close: %v", err)
	}
	if len(snap.Panes) != 1 || snap.Panes[0].ID != happyPersonPane {
		t.Fatalf("the window's panes after the close = %+v, want exactly the person's own pane %q — the worker's pane is still in the chain",
			snap.Panes, happyPersonPane)
	}
}

// The two rows the journey's own tab is made of, named rather than minted so
// that "the worker's tab is gone" can be read as "this one is what is left".
const (
	happyPersonTab  = "tab-the-person-had-open"
	happyPersonPane = "pane-the-person-had-open"
)

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
1. Call workers.spawn exactly once with command ` + "`claude -p 'Read AGENTS.md.'`" + ` and task ` + "`read AGENTS.md and report`" + `.
2. Call workers.holdings and read what your session holds.
3. Call workers.close for the worker id returned by workers.spawn.
Do not use Bash or any other tool. After the cycle, answer with the exact ordered tool names and the complete JSON results from all three calls.`
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
		return err == nil && len(held) == 1 && held[0].State == workers.StateClosed
	})
	if len(held) != 1 || held[0].Group != workers.ID(stand.coord.ID()) || held[0].Liveness.SessionID == "" {
		t.Fatalf("manual worker record = %+v, want one closed external worker for coordinator %q", held, stand.coord.ID())
	}
	t.Logf("manual worker record: id=%s group=%s state=%s session=%s", held[0].ID, held[0].Group, held[0].State, held[0].Liveness.SessionID)
}
