package pty

import (
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/waittest"
)

// TestClose_EndsAnInteractiveShell is the closing end of a session's lifetime,
// asserted for every shell nocx starts locally rather than for the one that
// happened to work.
//
// Closing a tab must end its shell. The mechanism used to be implicit and it
// was wrong twice over: the signal sent was SIGTERM, which an INTERACTIVE
// shell ignores by design, so what actually ended the session was the EOF the
// shell read once the pty master closed. bash exits on that EOF. zsh, measured
// on macOS 15, does not — it sits at its prompt indefinitely, because the
// kernel does not hang up the foreground group here the way Linux's vhangup
// does. That went unnoticed for as long as bash was the only shell a local
// session ever ran (nocx-wwz0), and would have leaked one process per closed
// tab on the platform this product ships to.
//
// A table over the shells, because "the tab closed" is one property and a
// shell that keeps running is the same defect whichever shell it is.
func TestClose_EndsAnInteractiveShell(t *testing.T) {
	for _, name := range []string{"bash", "zsh"} {
		t.Run(name, func(t *testing.T) {
			path, err := exec.LookPath(name)
			if err != nil {
				t.Fatalf("%s is not installed: %v — the shells a local session can run must be "+
					"present, or this reports a leaked process as a skip", name, err)
			}
			lp, err := NewLocal(log.NewSlogAdapter(slog.Default()), Config{
				Command: path,
				// Login AND interactive: the shape the enhanced zsh tier
				// starts, and the one in which SIGTERM is ignored.
				Args: []string{"-l", "-i"},
				Cols: 80, Rows: 24,
			})
			if err != nil {
				t.Fatalf("NewLocal: %v", err)
			}

			// ONE reader, draining continuously: the shell must be idle at its
			// prompt rather than blocked writing into a full master, because a
			// blocked writer dies of the close itself and would hide exactly
			// the case under test.
			var mu sync.Mutex
			var seen strings.Builder
			go func() {
				b := make([]byte, 4096)
				for {
					n, rerr := lp.Read(b)
					if n > 0 {
						mu.Lock()
						seen.Write(b[:n])
						mu.Unlock()
					}
					if rerr != nil {
						return
					}
				}
			}()

			// Reaching the prompt is waited for, never slept for: the shell's
			// own answer is the observable state change. The echoed command
			// line carries the marker too, so the answer is its second
			// occurrence.
			const marker = "NOCX_PTY_ALIVE"
			if _, werr := lp.Write([]byte("echo " + marker + "\n")); werr != nil {
				t.Fatalf("write to pty: %v", werr)
			}
			waittest.WaitFor(t, "the shell to reach a prompt", func() bool {
				mu.Lock()
				defer mu.Unlock()
				return strings.Count(seen.String(), marker) >= 2
			})

			if cerr := lp.Close(); cerr != nil && !strings.Contains(cerr.Error(), "file already closed") {
				t.Fatalf("Close: %v", cerr)
			}
			select {
			case <-lp.Done():
			case <-time.After(20 * time.Second):
				t.Fatalf("%s survived Close — a closed tab leaks a shell", name)
			}
		})
	}
}
