package mcpstdio

// A LONG CALL REPORTS PROGRESS WHILE IT WAITS (nocx-a62cl.4.2, ADR-anntnxdj §3).
//
// pi cuts an MCP request at 60 s by default, and the measurement behind that
// number was a 90 s call that survived because it sent progress every 20 s
// (ADR-anntnxdj, measured on pi 1.4.0). So the adapter sends
// notifications/progress for a call whose client asked for them, at an
// interval under that default, and stops when the call ends — for any reason,
// including the caller's cancellation and the call's own declared lifetime.
//
// Every test here runs over the REAL endpoint: the request's context, the
// cancellation message and the dispatch are the product's, and only the two
// seams a test cannot do without are doubles — the tool surface and the clock.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/toolendpoint"
	"github.com/shady2k/nocx/internal/workers"
)

// progressClock is the clock a long call's progress is sent on, in the test's
// hands. A tick is an EVENT and not a duration: a test that waited for a real
// interval would be measuring the machine, and the interval itself is a
// product value stated once, in progressInterval.
//
// stopped is the clock's OWN stop, and it is the second observable this file
// needs: the adapter stops the ticker when the goroutine that reports progress
// ends, so a closed stopped says that reporting is over — the fact "a
// cancelled call stops sending progress at once" is about, read without
// waiting for a duration and without hoping a write lost a race.
type progressClock struct {
	ticks    chan time.Time
	used     chan struct{}
	stopped  chan struct{}
	usedOnce sync.Once
	stopOnce sync.Once
	interval time.Duration
}

func newProgressClock() *progressClock {
	return &progressClock{
		ticks:   make(chan time.Time, 8),
		used:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
}

func (c *progressClock) tick() { c.ticks <- time.Now() }

// seam has the shape Server.progressTicks expects.
func (c *progressClock) seam(interval time.Duration) (<-chan time.Time, func()) {
	c.usedOnce.Do(func() { c.interval = interval; close(c.used) })
	return c.ticks, func() { c.stopOnce.Do(func() { close(c.stopped) }) }
}

// reported is whether a ticker was ever built: a call without a progress token
// must not start one at all.
func (c *progressClock) reported() bool {
	select {
	case <-c.used:
		return true
	default:
		return false
	}
}

// waitStopped requires that the reporting loop has ended.
func (c *progressClock) waitStopped(t *testing.T) {
	t.Helper()
	select {
	case <-c.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("progress reporting never stopped: it outlived its call")
	}
}

// startStdioWithProgressClock serves the adapter over the socket given, with
// this test's clock driving progress.
func startStdioWithProgressClock(t *testing.T, socket string, clock *progressClock) *stdioDriver {
	t.Helper()
	server, err := New(Config{Socket: socket, Logger: discardLogger})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	server.progressTicks = clock.seam
	return startStdioServing(t, func(input io.Reader, output io.WriteCloser) error {
		return server.Serve(context.Background(), input, output)
	})
}

// progressFrame reads the adapter's next line and requires it to be a
// notifications/progress for the token the call carried, returning the count
// it reports.
func progressFrame(t *testing.T, driver *stdioDriver, token string) int64 {
	t.Helper()
	envelope := driver.next()
	var method string
	if err := json.Unmarshal(envelope["method"], &method); err != nil || method != "notifications/progress" {
		t.Fatalf("the adapter wrote %v, want a progress notification for %s", envelope, token)
	}
	var params struct {
		Token    json.RawMessage `json:"progressToken"`
		Progress int64           `json:"progress"`
	}
	if err := json.Unmarshal(envelope["params"], &params); err != nil {
		t.Fatalf("decode progress params %s: %v", envelope["params"], err)
	}
	if got := strings.Trim(string(params.Token), `"`); got != token {
		t.Fatalf("the progress notification names token %s, want %s", params.Token, token)
	}
	return params.Progress
}

// TestALongCallReportsProgressAtTheDeclaredInterval is the DONE WHEN's first
// assertion: a call carrying a progressToken that takes longer than two
// intervals receives a notification at each one, and then its result.
func TestALongCallReportsProgressAtTheDeclaredInterval(t *testing.T) {
	dispatcher := &heldDispatcher{held: make(chan struct{}), release: make(chan struct{})}
	endpoint := startRealEndpoint(t, &slotAuthorizer{}, dispatcher)
	clock := newProgressClock()
	driver := startStdioWithProgressClock(t, endpoint.SocketPath(), clock)
	initialization(driver)

	driver.send(2, "tools/call", `{"name":"alpha.hold","_meta":{"progressToken":"tok-1"}}`)
	<-dispatcher.held
	<-clock.used
	if clock.interval != 20*time.Second {
		t.Fatalf("progress interval %v, want 20s below client 60s timeout", clock.interval)
	}
	clock.tick()
	if count := progressFrame(t, driver, "tok-1"); count != 1 {
		t.Fatalf("the first interval reported progress %d, want 1", count)
	}
	clock.tick()
	if count := progressFrame(t, driver, "tok-1"); count != 2 {
		t.Fatalf("the second interval reported progress %d, want 2", count)
	}

	close(dispatcher.release)
	if text := toolText(t, driver.nextID(2)); text != `{"held":true}` {
		t.Fatalf("the call answered %s after its progress", text)
	}
}

// TestACallWithoutAProgressTokenReportsNone is the second assertion: no token,
// no notification — and the call still answers. The fence call is what makes
// "none" readable: its answer can only follow whatever the held call wrote, so
// a progress notification would arrive first.
func TestACallWithoutAProgressTokenReportsNone(t *testing.T) {
	dispatcher := &heldDispatcher{held: make(chan struct{}), release: make(chan struct{})}
	endpoint := startRealEndpoint(t, &slotAuthorizer{}, dispatcher)
	clock := newProgressClock()
	driver := startStdioWithProgressClock(t, endpoint.SocketPath(), clock)
	initialization(driver)

	driver.send(2, "tools/call", `{"name":"alpha.hold"}`)
	<-dispatcher.held
	clock.tick()
	clock.tick()

	driver.send(3, "tools/call", `{"name":"alpha.screen"}`)
	if text := toolText(t, driver.nextID(3)); text != `{"screen":true}` {
		t.Fatalf("the fence call answered %s", text)
	}

	close(dispatcher.release)
	if text := toolText(t, driver.nextID(2)); text != `{"held":true}` {
		t.Fatalf("the tokenless call answered %s", text)
	}
	if clock.reported() {
		t.Fatal("a call without a progress token started reporting: MCP progress is opt-in per request")
	}
}

// TestACancelledCallStopsProgressAtOnce is the third assertion. The cancel is
// known to have reached the backend before the next tick (the executor reports
// its context ended), so a notification after that point is a notification for
// a call nobody is waiting for — which is what the fence then proves absent.
func TestACancelledCallStopsProgressAtOnce(t *testing.T) {
	dispatcher := &heldDispatcher{
		held:    make(chan struct{}),
		release: make(chan struct{}),
		stopped: make(chan struct{}),
	}
	endpoint := startRealEndpoint(t, &slotAuthorizer{}, dispatcher)
	clock := newProgressClock()
	driver := startStdioWithProgressClock(t, endpoint.SocketPath(), clock)
	initialization(driver)

	driver.send(2, "tools/call", `{"name":"alpha.hold","_meta":{"progressToken":"tok-1"}}`)
	<-dispatcher.held
	clock.tick()
	if count := progressFrame(t, driver, "tok-1"); count != 1 {
		t.Fatalf("the first interval reported progress %d, want 1", count)
	}

	driver.notify("notifications/cancelled", `{"requestId":2,"reason":"no longer needed"}`)
	select {
	case <-dispatcher.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled call's executor was never told: the cancel did not reach the call")
	}

	// THE REPORTING IS OVER, which is what "at once" means and what a tick
	// sent now can no longer become. A loop that outlived its call would still
	// be holding the ticker, and this wait would say so.
	clock.waitStopped(t)

	// A TICK THE CALL WILL NEVER SEE. Nothing may be written for it: the call
	// is over, and progress for a call nobody waits for is the noise this
	// assertion exists to keep out of the client's stream.
	clock.tick()

	driver.send(3, "tools/call", `{"name":"alpha.screen"}`)
	if text := toolText(t, driver.nextID(3)); text != `{"screen":true}` {
		t.Fatalf("the fence call answered %s, want nothing between it and the cancel", text)
	}
}

// lifetimeWorkerRecord blocks only at the real holdings executor's storage
// boundary. The dispatcher, capability, and execution deadline stay production.
type lifetimeWorkerRecord struct {
	assistant.WorkerRecord
	started chan context.Context
	ended   chan error
}

func (r *lifetimeWorkerRecord) Undispatched() []workers.Fact { return nil }

func (r *lifetimeWorkerRecord) HeldBy(ctx context.Context, _ string) ([]workers.Participant, error) {
	r.started <- ctx
	<-ctx.Done()
	r.ended <- ctx.Err()
	return nil, ctx.Err()
}

type lifetimeAuthorizer struct{ slotAuthorizer }

func (a *lifetimeAuthorizer) Admit(peer toolendpoint.Peer, publish func(string, toolendpoint.AdmissionEpoch) bool) (assistant.ToolInvocation, func(), error) {
	inv, release, err := a.slotAuthorizer.Admit(peer, publish)
	inv.Grant = content.EffectPolicy{
		Observe: content.EffectRow{Decision: content.DecisionPermit},
	}.AsGrant([]content.GrantScope{{Kind: content.ResourceSession, ID: "session"}})
	return inv, release, err
}

// TestACallEndingAtItsDeclaredLifetimeKeepsItsBoundUnderProgress exercises
// runDeclaredTool's execution deadline through the real endpoint and dispatcher.
// Only the declared duration is shortened; progress ticks cannot renew it.
func TestACallEndingAtItsDeclaredLifetimeKeepsItsBoundUnderProgress(t *testing.T) {
	registry, err := agenttools.Assemble(os.DirFS("../../contracts/tools"))
	if err != nil {
		t.Fatalf("assemble tools: %v", err)
	}
	const lifetime = time.Second
	// All returns this registry's declaration rows. Change only this test's
	// assembled copy, retaining the shipped schema and capability constructor.
	allTools := registry.All()
	for i := range allTools {
		if allTools[i].Name == "workers.holdings" {
			allTools[i].Deadline = lifetime
		}
	}
	record := &lifetimeWorkerRecord{started: make(chan context.Context, 1), ended: make(chan error, 1)}
	dispatcher, err := assistant.NewToolDispatcher(registry, record, content.EnvironmentIDFor(content.EnvLocal, ""))
	if err != nil {
		t.Fatalf("new tool dispatcher: %v", err)
	}
	endpoint := startRealEndpoint(t, &lifetimeAuthorizer{}, dispatcher)
	clock := newProgressClock()
	driver := startStdioWithProgressClock(t, endpoint.SocketPath(), clock)
	initialization(driver)

	driver.send(2, "tools/call", `{"name":"workers.holdings","arguments":{},"_meta":{"progressToken":7}}`)
	var execution context.Context
	select {
	case execution = <-record.started:
	case <-time.After(5 * time.Second):
		t.Fatal("holdings never reached its storage boundary")
	}
	originalDeadline, bounded := execution.Deadline()
	if !bounded {
		t.Fatal("the production executor context has no declared deadline")
	}
	for count := int64(1); count <= 2; count++ {
		if err := execution.Err(); err != nil {
			t.Fatalf("execution ended before progress event %d: %v", count, err)
		}
		clock.tick()
		if got := progressFrame(t, driver, "7"); got != count {
			t.Fatalf("progress = %d, want %d", got, count)
		}
		if deadline, ok := execution.Deadline(); !ok || !deadline.Equal(originalDeadline) {
			t.Fatal("progress renewed the execution deadline")
		}
	}
	select {
	case err := <-record.ended:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("the executor ended with %v, want its own declared lifetime", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call never ended at its declared lifetime: progress held it open")
	}
	reason := toolRefusal(t, driver.nextID(2))
	if !strings.Contains(reason, "ran past its own bound") {
		t.Fatalf("the ended call reached the caller as %q, want its bound named", reason)
	}

	clock.waitStopped(t)
	clock.tick()
	// A catalogue request fences the output without another blocking execution.
	driver.send(3, "tools/list", `{}`)
	if response := driver.nextID(3); response["error"] != nil || response["result"] == nil {
		t.Fatalf("catalogue fence failed: %v", response)
	}
}

// TestProgressForACancelledCallWritesNothing pins the window the end-to-end
// test above cannot: a tick may arrive between the client's cancellation and
// the call's answer. The sender's select would pick between two ready channels
// by chance, so the context decides — and here nothing else ends the loop, so
// a loop that ignores its context writes the frame.
func TestProgressForACancelledCallWritesNothing(t *testing.T) {
	clock := newProgressClock()
	output := &recordingWriter{written: make(chan struct{}, 4)}
	server := &Server{logger: discardLogger, progressTicks: clock.seam}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	server.startProgress(ctx, &lineWriter{output: output}, json.RawMessage(`"tok-1"`))
	clock.tick()
	clock.waitStopped(t)

	if written := output.String(); written != "" {
		t.Fatalf("a cancelled call reported %q, want nothing", written)
	}
}
