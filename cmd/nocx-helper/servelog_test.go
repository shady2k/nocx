package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE DAEMON WRITES TO A FILE, and the assertion is on the file (nocx-n14oo.1).
//
// It wrote to a stderr that internal/helper/endpoint/bridge.go sets to
// /dev/null, so the process that opens every local pane said nothing anywhere.
// A spy on the logger would have passed the whole time that was true, which is
// why this reads the bytes back off disk.
func TestTheServeDaemonWritesToAFileAPersonCanName(t *testing.T) {
	home := t.TempDir()

	lg, file := openServeLog(home, "gen-abc")
	if file == nil {
		t.Fatal("no log file was opened; the daemon would be writing to /dev/null again")
	}
	lg.Info("helper session adopted", "id", "s-1")
	if err := file.Close(); err != nil {
		t.Fatalf("close log file: %v", err)
	}

	path := ServeLogPath(home)
	if path != filepath.Join(home, ".nocx", "helper.log") {
		t.Fatalf("log path = %q, want it under the helper's own directory", path)
	}
	written, err := os.ReadFile(path) //nolint:gosec // the path is this test's temp home
	if err != nil {
		t.Fatalf("read the helper log: %v", err)
	}
	out := string(written)
	for _, want := range []string{
		// It names itself first, so a running daemon can say where it writes.
		"helper log file",
		"path=" + path,
		// The line that was logged, with the generation on it — several
		// generations serve at once and one file holds them all.
		"helper session adopted",
		"generation=gen-abc",
		// AddSource, like the backend's: a line names the module that wrote it.
		"servelog_test.go:",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the helper log does not carry %q:\n%s", want, out)
		}
	}
}

// A LOG THAT CANNOT BE OPENED DOES NOT STOP THE DAEMON. Refusing to open panes
// because a file could not be created would be the diagnostic costing more than
// what it diagnoses.
func TestAnUnopenableLogStillServes(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	// A regular file where the .nocx directory has to go: MkdirAll fails, and
	// so would the open under it.
	if err := os.WriteFile(home, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write the blocking file: %v", err)
	}

	lg, file := openServeLog(home, "gen-abc")
	if lg == nil {
		t.Fatal("no logger at all; the daemon would have nothing to write to")
	}
	if file != nil {
		_ = file.Close()
		t.Fatal("a file was opened where none could be")
	}
	// Usable, not merely non-nil.
	lg.Info("still serving")
}
