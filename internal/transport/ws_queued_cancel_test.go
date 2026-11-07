package transport

// nocx-3an1s.2.17, findings 2 and 3. Both are about a request that is
// decided before its handler runs: a queued task its owner withdrew, and a
// queued mutation whose declared turn ended. The first must leave nothing
// registered (an entry left in requestCancels, or a permit never given back
// to the inflight set, is memory that grows and a shutdown that waits its
// whole drain bound); the second must stay "did not start", because no work
// ran and the same identity may be asked again.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/operation/catalog"
	"github.com/shady2k/nocx/internal/transport/control"
	"github.com/shady2k/nocx/internal/waittest"
)

// heldSubmission is a Submission that keeps every task until the test starts
// it. It is the queue both findings need: a task exists, its owner can act,
// and nothing runs until this test says so — no scheduling luck is involved.
type heldSubmission struct {
	mu     sync.Mutex
	tasks  []heldTask
	queued chan struct{}
}

type heldTask struct {
	ctx  context.Context
	task control.Task
}

func newHeldSubmission() *heldSubmission {
	return &heldSubmission{queued: make(chan struct{}, 8)}
}

func (s *heldSubmission) TrySubmit(ctx context.Context, task control.Task) *control.Rejection {
	s.mu.Lock()
	s.tasks = append(s.tasks, heldTask{ctx: ctx, task: task})
	s.mu.Unlock()
	s.queued <- struct{}{}
	return nil
}

// next waits for the next queued task and reports it. A deadline is the
// bound of a negative claim ("the replay never queued"), not a pacing sleep.
func (s *heldSubmission) next(t *testing.T, within time.Duration) heldTask {
	t.Helper()
	select {
	case <-s.queued:
	case <-time.After(within):
		t.Fatal("no task reached the held submission")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	task := s.tasks[0]
	s.tasks = s.tasks[1:]
	return task
}

// start applies the one-step gate exactly as a worker does: Start decides
// whether Run is called.
func (s *heldSubmission) start(held heldTask) {
	if held.task.Start == nil || held.task.Start() {
		held.task.Run(held.ctx)
	}
}

// onlyConn returns the single registered connection, for reading the
// per-connection registries the findings name.
func onlyConn(t *testing.T, srv *WSServer) *wsConn {
	t.Helper()
	srv.connsMu.Lock()
	defer srv.connsMu.Unlock()
	if len(srv.conns) != 1 {
		t.Fatalf("registered connections = %d, want 1", len(srv.conns))
	}
	for conn := range srv.conns {
		return conn
	}
	return nil
}

func requestCancelEntries(w *wsConn) int {
	w.requestCancels.mu.Lock()
	defer w.requestCancels.mu.Unlock()
	return len(w.requestCancels.running)
}

func inflightEntries(srv *WSServer) int {
	srv.inflight.mu.Lock()
	defer srv.inflight.mu.Unlock()
	return len(srv.inflight.cancels)
}

// TestACancelledQueuedTaskLeavesNoRegistrationAndShutdownDoesNotWait is
// finding 2. A task skipped because its owner withdrew it while queued never
// calls Run, and Run is where the dispatcher drops its cancellation entry and
// where the inflight wrapper gives the permit back. Before the fix both
// survived: the maps grew with every withdrawn queued call and Stop waited
// out the whole control drain bound for tasks that had never started.
func TestACancelledQueuedTaskLeavesNoRegistrationAndShutdownDoesNotWait(t *testing.T) {
	_, logger := logtest.New(t)
	held := newHeldSubmission()
	srv := NewWSServer(logger, newRegWithStub(logger))
	srv.controlDrainTimeout = 3 * time.Second
	srv.probeSub = &inflightSubmission{inflight: &srv.inflight, inner: held}
	srv.methods["test.queued"] = reg(srv.probeSub, catalog.OpConnectionsTest, noParams(),
		func(_ *wsConn, _ *connState, r Responder) handlerFunc {
			return func(_ context.Context, req jsonrpcRequest) {
				_ = r.TryResult(req.ID, json.RawMessage(`{}`))
			}
		})
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck
	wc := onlyConn(t, srv)

	writeRequest(t, conn, "test.queued", map[string]any{}, 1)
	queued := held.next(t, wantWithin)
	if got := inflightEntries(srv); got != 1 {
		t.Fatalf("admitted tasks before the cancel = %d, want 1", got)
	}
	if got := requestCancelEntries(wc); got != 1 {
		t.Fatalf("request cancellation entries before the cancel = %d, want 1", got)
	}

	// Withdraw it while it is still queued.
	if response := jsonrpcCallWithID(t, conn, "rpc.cancel", map[string]any{"id": 1}, 2); len(response) == 0 {
		t.Fatal("rpc.cancel answered nothing")
	}

	// The worker now reaches the start gate; it must refuse, and the refusal
	// must clean up after itself.
	held.start(queued)

	waittest.WaitFor(t, "the withdrawn task's registrations to be released", func() bool {
		return requestCancelEntries(wc) == 0 && inflightEntries(srv) == 0
	})

	// Shutdown must not wait its control drain bound for a task that never
	// ran. Three seconds is the configured bound; a correct server returns in
	// a fraction of one.
	began := time.Now()
	if err := srv.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if elapsed := time.Since(began); elapsed > time.Second {
		t.Fatalf("Stop took %v with no work in flight; the control drain bound is %v", elapsed, srv.controlDrainTimeout)
	}
}

// TestAnExpiredNeverStartedMutationReadsNotStartedAndMayBeReplayed is
// finding 3. A queued mutation whose turn ended records "not started", then
// the responder records the expiry error as "the handler answered". The
// second write used to win, so operation.outcome said done and the retry
// the record sanctions was refused as a replay of work that never ran.
//
// The method uses an internal declaration whose turn is one second, so the
// expiry is the real dispatcher timer rather than a test-invented deadline.
func TestAnExpiredNeverStartedMutationReadsNotStartedAndMayBeReplayed(t *testing.T) {
	_, logger := logtest.New(t)
	held := newHeldSubmission()
	srv := NewWSServer(logger, newRegWithStub(logger))
	srv.methods["test.expiredMutation"] = reg(held, catalog.OpInternalFilesUpload, noParams(),
		func(_ *wsConn, _ *connState, r Responder) handlerFunc {
			return func(_ context.Context, req jsonrpcRequest) {
				_ = r.TryResult(req.ID, json.RawMessage(`{"ran":true}`))
			}
		})
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })
	conn := connectWS(t, srv)
	t.Cleanup(func() { _ = conn.Close() })

	sendMutation(t, conn, 1, "test.expiredMutation", map[string]any{}, "m-expired")
	queued := held.next(t, wantWithin)

	frame, err := awaitFrame(conn, time.Now().Add(5*time.Second), isResponseTo(1))
	if err != nil {
		t.Fatalf("the never-started mutation answered nothing: %v", err)
	}
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(frame, &envelope); err != nil {
		t.Fatalf("decode expiry answer: %v", err)
	}
	if envelope.Error == nil || envelope.Error.Code != OperationExpiredCode {
		t.Fatalf("the expired call's answer = %+v, want code %d", envelope.Error, OperationExpiredCode)
	}

	if outcome, operation := askOutcome(t, conn, 2, "m-expired"); outcome != string(catalog.AnswerNotStarted) || operation != string(catalog.OpInternalFilesUpload.ID()) {
		t.Fatalf("outcome = %q for %q, want not-started for the operation", outcome, operation)
	}

	// Let the worker reach the start gate, so the skipped task's own cleanup
	// runs before the replay.
	held.start(queued)

	// The replay the record sanctions: nothing ran under this identity, so the
	// same identity must be admitted and run.
	sendMutation(t, conn, 3, "test.expiredMutation", map[string]any{}, "m-expired")
	replayed := held.next(t, wantWithin)
	held.start(replayed)

	if outcome, _ := askOutcome(t, conn, 4, "m-expired"); outcome != string(catalog.AnswerDone) {
		t.Fatalf("outcome after the sanctioned replay = %q, want done", outcome)
	}
}

// TestAQueuedTaskThatStartsStillReleasesItsRegistration is the paired
// ordinary case: the cleanup added for a skipped task must not take the
// running task's release away from it.
func TestAQueuedTaskThatStartsStillReleasesItsRegistration(t *testing.T) {
	_, logger := logtest.New(t)
	held := newHeldSubmission()
	srv := NewWSServer(logger, newRegWithStub(logger))
	srv.probeSub = &inflightSubmission{inflight: &srv.inflight, inner: held}
	srv.methods["test.queued"] = reg(srv.probeSub, catalog.OpConnectionsTest, noParams(),
		func(_ *wsConn, _ *connState, r Responder) handlerFunc {
			return func(_ context.Context, req jsonrpcRequest) {
				_ = r.TryResult(req.ID, json.RawMessage(`{}`))
			}
		})
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })
	conn := connectWS(t, srv)
	t.Cleanup(func() { _ = conn.Close() })
	wc := onlyConn(t, srv)

	writeRequest(t, conn, "test.queued", map[string]any{}, 1)
	queued := held.next(t, wantWithin)
	held.start(queued)
	if frame, err := awaitFrame(conn, time.Now().Add(wantWithin), isResponseTo(1)); err != nil || len(frame) == 0 {
		t.Fatalf("the running task answered nothing: %v", err)
	}
	waittest.WaitFor(t, "the running task's registration to be released", func() bool {
		return requestCancelEntries(wc) == 0 && inflightEntries(srv) == 0
	})
}

// TestAReplayRefusalLeavesNoCancellationRegistration is the same early end in
// the dispatch path that rejects a replayed mutation before a task exists:
// the registration is made before the mutation record is consulted, and the
// refusal must drop it as every other end does.
func TestAReplayRefusalLeavesNoCancellationRegistration(t *testing.T) {
	_, logger := logtest.New(t)
	srv := NewWSServer(logger, newRegWithStub(logger))
	srv.methods["test.mutation"] = reg(control.AsyncSubmission{}, catalog.OpSettingsSet, noParams(),
		func(_ *wsConn, _ *connState, r Responder) handlerFunc {
			return func(_ context.Context, req jsonrpcRequest) {
				_ = r.TryResult(req.ID, json.RawMessage(`{}`))
			}
		})
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })
	conn := connectWS(t, srv)
	t.Cleanup(func() { _ = conn.Close() })
	wc := onlyConn(t, srv)

	if envelope := mutationCall(t, conn, 1, "test.mutation", map[string]any{}, "m-done"); envelope["error"] != nil {
		t.Fatalf("the first mutation was refused: %+v", envelope["error"])
	}
	replay := mutationCall(t, conn, 2, "test.mutation", map[string]any{}, "m-done")
	if replay["error"] == nil {
		t.Fatal("the replay was accepted")
	}
	waittest.WaitFor(t, "the replay refusal's registration to be dropped", func() bool {
		return requestCancelEntries(wc) == 0
	})
}
