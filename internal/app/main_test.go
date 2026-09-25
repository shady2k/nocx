package app

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"
)

// TestMain fails the package when a completion downlink's worker outlives the
// run (nocx-2v80t.3.32). A worker lives exactly as long as the session it
// delivers for, so one still running here is a test that never ended the
// session it opened — which under -race kept workers alive for minutes and
// ran the package up against CI's ten-minute default.
func TestMain(m *testing.M) {
	code := m.Run()
	if code == 0 {
		if left := downlinkWorkersAfterTheirSessions(); left > 0 {
			fmt.Fprintf(os.Stderr, "FAIL: %d completion downlink workers are still running after every test ended; "+
				"a test opened a session it never closed\n", left)
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
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return bytes.Count(buf[:n], []byte("client.(*CompletionDownlink).run("))
		}
		buf = make([]byte, 2*len(buf))
	}
}
