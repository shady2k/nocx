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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
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
func (c *progressClock) seam(time.Duration) (<-chan time.Time, func()) {
	c.usedOnce.Do(func() { close(c.used) })
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
	return startStdioServing(t, func(input io.Reader, output io.Writer) error {
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

// lifetimeDispatcher holds a call until the lifetime its catalogue entry
// declares runs out, which is the rule the product applies to every tool's
// declaration (`runDeclaredTool`, ADR-8eej3e66's non-renewable `max`). It is a
// double because no shipped tool declares a lifetime short enough to watch.
type lifetimeDispatcher struct {
	lifetime time.Duration
	started  chan struct{}
	armed    chan struct{}
	ended    chan error
}

func (d *lifetimeDispatcher) Dispatch(inv assistant.ToolInvocation) (string, error) {
	if inv.Method != "alpha.lifetime" {
		return `{"screen":true}`, nil
	}
	d.started <- struct{}{}
	// The lifetime begins once the test has watched the progress it asserts,
	// so "progress first, then the bound" is an order rather than a race. The
	// context is the way out of a test that failed before arming it: the
	// endpoint closes the connection, and this call must not hold the
	// endpoint's own teardown open.
	select {
	case <-d.armed:
	case <-inv.Context.Done():
		d.ended <- inv.Context.Err()
		return "", inv.Context.Err()
	}
	ctx, cancel := context.WithTimeout(inv.Context, d.lifetime)
	defer cancel()
	<-ctx.Done()
	d.ended <- ctx.Err()
	return "", ctx.Err()
}

func (d *lifetimeDispatcher) Catalogue(content.Grant) []agenttools.Tool {
	return []agenttools.Tool{
		{
			Declaration:  agenttools.Declaration{Name: "alpha.lifetime", Description: "runs until its declared lifetime ends"},
			ParamsSchema: json.RawMessage(`{"type":"object"}`),
			ResultSchema: json.RawMessage(`{"type":"object"}`),
		},
		{
			Declaration:  agenttools.Declaration{Name: "alpha.screen", Description: "answers at once"},
			ParamsSchema: json.RawMessage(`{"type":"object"}`),
			ResultSchema: json.RawMessage(`{"type":"object"}`),
		},
	}
}

// TestACallEndingAtItsDeclaredLifetimeKeepsItsBoundUnderProgress is the fourth
// assertion, ADR-8eej3e66's "progress never renews a lifetime": the call's own
// bound still ends it while progress is being sent, the adapter reports that
// ending rather than a result, and nothing is written for the call afterwards.
func TestACallEndingAtItsDeclaredLifetimeKeepsItsBoundUnderProgress(t *testing.T) {
	dispatcher := &lifetimeDispatcher{
		lifetime: 50 * time.Millisecond,
		started:  make(chan struct{}, 1),
		armed:    make(chan struct{}),
		ended:    make(chan error, 1),
	}
	endpoint := startRealEndpoint(t, &slotAuthorizer{}, dispatcher)
	clock := newProgressClock()
	driver := startStdioWithProgressClock(t, endpoint.SocketPath(), clock)
	initialization(driver)

	driver.send(2, "tools/call", `{"name":"alpha.lifetime","_meta":{"progressToken":7}}`)
	<-dispatcher.started
	clock.tick()
	if count := progressFrame(t, driver, "7"); count != 1 {
		t.Fatalf("the first interval reported progress %d, want 1", count)
	}
	clock.tick()
	if count := progressFrame(t, driver, "7"); count != 2 {
		t.Fatalf("the second interval reported progress %d, want 2", count)
	}

	arm := sync.OnceFunc(func() { close(dispatcher.armed) })
	t.Cleanup(arm)
	arm()
	select {
	case err := <-dispatcher.ended:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("the call ended with %v, want its own declared lifetime", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call never ended at its declared lifetime: progress held it open")
	}
	// THE ADAPTER REPORTS THE ENDING, not a result: a call whose bound ran out
	// was abandoned, and the sentence says so (the endpoint's own
	// classification of a deadline).
	reason := toolRefusal(t, driver.nextID(2))
	if !strings.Contains(reason, "ran past its own bound") {
		t.Fatalf("the ended call reached the caller as %q, want its bound named", reason)
	}

	// AND PROGRESS STOPPED WITH THE CALL. A tick after the answer may write
	// nothing, and the fence proves it.
	clock.waitStopped(t)
	clock.tick()
	driver.send(3, "tools/call", `{"name":"alpha.screen"}`)
	if text := toolText(t, driver.nextID(3)); text != `{"screen":true}` {
		t.Fatalf("the fence call answered %s, want nothing between it and the ended call", text)
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
