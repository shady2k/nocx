package app

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"
)

// TestMain fails the package when a goroutine a test started outlives it.
//
// Both halves are one rule: a goroutine lives exactly as long as the thing
// that owns it, so a worker, a session or a lane still running after every
// test has ended is a test that never ended what it opened. The downlink half
// was nocx-2v80t.3.32; the stand half is nocx-1ebrs.66.1.29, where the same
// shape reached 867 goroutines at the end of a full run — a helper's Serve and
// its sessions' row pumps, a pane's write loop, a dialled lane's loss watcher
// — and the package's own dump at the ten-minute panic in CI is what showed
// it. The check is the only place this is visible: a leaked goroutine changes
// no test's result, which is why it grew for weeks, and the cost it adds is
// paid by every later test on a slower runner.
func TestMain(m *testing.M) {
	code := m.Run()
	if code == 0 {
		if left := downlinkWorkersAfterTheirSessions(); left > 0 {
			fmt.Fprintf(os.Stderr, "FAIL: %d completion downlink workers are still running after every test ended; "+
				"a test opened a session it never closed\n", left)
			code = 1
		}
		if left := standGoroutinesAfterTheirTests(); len(left) > 0 {
			fmt.Fprintf(os.Stderr, "FAIL: %d goroutines a test's stand started are still running after every test ended; "+
				"a test never ended what it opened:\n", countStandGoroutines(left))
			for _, g := range left {
				fmt.Fprintf(os.Stderr, "  %3d × %s (ends when %s)\n", g.count, g.frame, g.ends)
			}
			code = 1
		}
	}
	os.Exit(code)
}

// downlinkWorkersAfterTheirSessions counts the downlink workers still running
// once each has had the chance to see its session's end: a worker stops on
// its context's done, asynchronously, so the count is read until it reaches
// zero or the bound passes — a wait on the state, never a fixed pause.
func downlinkWorkersAfterTheirSessions() int {
	deadline := time.Now().Add(5 * time.Second)
	for {
		n := liveDownlinkWorkers()
		if n == 0 || !time.Now().Before(deadline) {
			return n
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func liveDownlinkWorkers() int {
	return bytes.Count(goroutineDump(), []byte("client.(*CompletionDownlink).run("))
}

// standGoroutine is one goroutine a TEST's stand owns, named by the frame that
// can only be reached from such a stand, and the event that ends it — the
// sentence a reader needs to know which fixture forgot which teardown.
type standGoroutine struct {
	frame string
	ends  string
	count int
}

// standGoroutines are the four shapes nocx-1ebrs.66.1.29 found at the end of a
// full run. Each is started once per process, session or lane and ends on an
// event its owner controls, so the count may only be zero.
var standGoroutines = []standGoroutine{
	{
		frame: "internal/helper/session.(*hostSession).serveRows(",
		ends:  "the helper session's stop, which helpersession.Service.Close runs when its daemon ends",
	},
	{
		frame: "internal/session.(*realSession).startWriteLoop.func1(",
		ends:  "the session's Close, which closes writeDone",
	},
	{
		frame: "internal/helper/client.Dial.func5(",
		ends:  "the lane's Done, which closes when its connection dies",
	},
	{
		frame: "internal/app.(*fakeLaneConn).Wait(",
		ends:  "the fake helper process exiting, which is the peer function returning",
	},
}

// standGoroutinesAfterTheirTests reads the dump until it holds none of the
// four, or the bound passes: a session that was just stopped unwinds
// asynchronously, exactly like a downlink worker, so the wait is on the state
// and never on a fixed pause.
func standGoroutinesAfterTheirTests() []standGoroutine {
	deadline := time.Now().Add(5 * time.Second)
	for {
		left := liveStandGoroutines()
		if len(left) == 0 || !time.Now().Before(deadline) {
			return left
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func liveStandGoroutines() []standGoroutine {
	dump := goroutineDump()
	var left []standGoroutine
	for _, g := range standGoroutines {
		if n := bytes.Count(dump, []byte(g.frame)); n > 0 {
			g.count = n
			left = append(left, g)
		}
	}
	return left
}

func countStandGoroutines(left []standGoroutine) int {
	n := 0
	for _, g := range left {
		n += g.count
	}
	return n
}

// goroutineDump is every goroutine's stack, in one buffer that grows until the
// dump fits — runtime.Stack truncates silently when it does not.
func goroutineDump() []byte {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return buf[:n]
		}
		buf = make([]byte, 2*len(buf))
	}
}
