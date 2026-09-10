package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	nocxlog "github.com/shady2k/nocx/internal/log"
)

// THE EPIC'S OWN CHECK (nocx-4l2a5): one failure, one trace, every stage.
//
// On 2026-09-09 a coordinator's workers.spawn answered an error and the
// evidence for it was scattered across two files under three unrelated
// identifiers — the MCP call, the endpoint dispatch and the registration steps
// had nothing in common but their timestamps, and the middle thirty seconds
// were blank. This drives the REAL endpoint socket with a real pane behind it,
// makes the spawn fail the way it failed that night (a launcher that never
// enrols), and then reads the log the way a person would: one trace id, and
// every stage of the exchange under it.
func TestAFailingSpawnIsOneReadableTrace(t *testing.T) {
	var logs safeBuffer
	stand := newHappyStand(t,
		withHappyStandLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))),
		withHappyStandEnrolmentDeadline(1500*time.Millisecond),
	)

	// The caller's frame, as nocx-helper would have opened it before asking
	// this socket. What the endpoint opens is a child of it.
	_, caller := nocxlog.StartSpan(context.Background())

	conn, err := net.Dial("unix", stand.endpoint.SocketPath())
	if err != nil {
		t.Fatalf("dial the tool socket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// `true` is a launcher that runs and never enrols — the shape of the
	// failure, without needing an agent that hangs.
	params, err := json.Marshal(map[string]string{"command": "true", "task": "never enrol"})
	if err != nil {
		t.Fatalf("marshal spawn params: %v", err)
	}
	request, err := json.Marshal(map[string]any{
		"jsonrpc":     "2.0",
		"id":          "spawn-trace-1",
		"method":      "workers.spawn",
		"params":      json.RawMessage(params),
		"traceparent": caller.Traceparent(),
	})
	if err != nil {
		t.Fatalf("marshal spawn request: %v", err)
	}
	if _, err := conn.Write(append(request, '\n')); err != nil {
		t.Fatalf("write spawn request: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	var response struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&response); err != nil {
		t.Fatalf("decode spawn response: %v", err)
	}
	if response.Error == nil {
		t.Fatal("the spawn succeeded; this test needs the failure it is about")
	}

	written := logs.String()
	// EVERY STAGE, and each one is a question the 2026-09-09 log could not
	// answer: what was asked, what was built, what was being waited for, how
	// long it waited, why it gave up, what it undid, and what the caller was
	// told.
	for _, stage := range []string{
		"workers.spawn: start",
		"worker.spawn: start",
		"worker spawn: the participant's session is open",
		"worker participant spawned",
		"worker: the participant's budget opens",
		"worker: the enrolment never arrived",
		"worker: compensating a failed registration",
		"tool endpoint: unclassified dispatch failure",
	} {
		if !strings.Contains(written, stage) {
			t.Fatalf("the log does not carry the stage %q:\n%s", stage, written)
		}
	}

	// ONE TRACE. This is the property that makes the stages above readable as
	// a single failure rather than as eight events that happened to be near
	// each other, and it is the one a grep actually uses.
	underTrace := 0
	for _, line := range strings.Split(strings.TrimSpace(written), "\n") {
		if strings.Contains(line, "trace_id="+caller.TraceID) {
			underTrace++
		}
	}
	if underTrace < 8 {
		t.Fatalf("only %d lines belong to the caller's trace %s, want every stage:\n%s",
			underTrace, caller.TraceID, written)
	}
	// And the endpoint's frame is a CHILD of the caller's, not the caller's.
	if !strings.Contains(written, "parent_span_id="+caller.SpanID) {
		t.Fatalf("the endpoint's frame is not a child of the caller's:\n%s", written)
	}
}

// safeBuffer is a bytes.Buffer a test may read while the code under test is
// still writing to it from its own goroutines.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

var _ io.Writer = (*safeBuffer)(nil)

// A PANE'S WHOLE LIFE BELONGS TO THE SPAWN THAT OPENED IT (nocx-n14oo.3).
//
// A session is written about from timers, pumps and compensations that hold no
// context, so the exchange is bound onto its own logger once, where the session
// is constructed. That is what puts its open, its every line and its close
// thirty seconds later under one trace.
//
// What this stand cannot prove is the same binding on the lifecycle adapter,
// because happyRealPTYFactory builds the lifecycle socketpair itself and never
// reaches internal/app/helper_hosted.go, where the adapter a real pane gets is
// constructed. That one is bound at the same seam and by construction; it is
// read back from a live run, not from here.
func TestAPanesWholeLifeBelongsToTheSpawnThatOpenedIt(t *testing.T) {
	var logs safeBuffer
	stand := newHappyStand(t,
		withHappyStandLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))),
		withHappyStandEnrolmentDeadline(1500*time.Millisecond),
	)
	_, caller := nocxlog.StartSpan(context.Background())

	conn, err := net.Dial("unix", stand.endpoint.SocketPath())
	if err != nil {
		t.Fatalf("dial the tool socket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	params, err := json.Marshal(map[string]string{"command": "true", "task": "never enrol"})
	if err != nil {
		t.Fatalf("marshal spawn params: %v", err)
	}
	request, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "spawn-cause-1", "method": "workers.spawn",
		"params": json.RawMessage(params), "traceparent": caller.Traceparent(),
	})
	if err != nil {
		t.Fatalf("marshal spawn request: %v", err)
	}
	if _, err := conn.Write(append(request, '\n')); err != nil {
		t.Fatalf("write spawn request: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	var response struct {
		Error *struct{} `json:"error"`
	}
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&response); err != nil {
		t.Fatalf("decode spawn response: %v", err)
	}

	// THE PANE'S WHOLE LIFE, not only the call that made it: the session this
	// spawn opened writes under the spawn's trace, and so does its close
	// thirty seconds later, from a compensation that holds no context.
	//
	// Only the lines UNDER THIS TRACE are read. The stand opens a coordinator
	// session of its own before the spawn, under no exchange at all, and
	// asserting over every "session opened" in the buffer would be asserting
	// about that one too.
	written := logs.String()
	var opened, closed bool
	for _, line := range strings.Split(strings.TrimSpace(written), "\n") {
		if !strings.Contains(line, "trace_id="+caller.TraceID) {
			continue
		}
		if strings.Contains(line, `msg="session opened"`) || strings.Contains(line, `msg="helper session adopted"`) {
			opened = true
		}
		if strings.Contains(line, `msg="session closed"`) {
			closed = true
		}
	}
	if !opened {
		t.Fatalf("the session this spawn opened is outside its trace:\n%s", written)
	}
	if !closed {
		t.Fatalf("the close that compensated this spawn is outside its trace:\n%s", written)
	}
}
