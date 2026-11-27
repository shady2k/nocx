package hostsvc

// Scenario 9's tail, against a REAL process killed by SIGKILL: a write session
// that was streaming when its helper died leaves a temporary and a journal
// entry, and the next helper removes both before it serves anything.
//
// # Why a child process rather than a fake
//
// The mechanism being tested is the kernel's: a SIGKILL closes every descriptor
// of the killed process, which is what releases the journal's flock, and the
// successor's sweep decides what to delete from exactly that fact. A test that
// released the lock by hand would be testing the test — and the one thing a
// hand-released lock cannot show is that the kernel's own release is what the
// sweep reads. So the child re-executes this test binary in a mode where it
// opens the session, writes, prints READY and waits to be killed.
//
// Nothing here depends on a duration: the parent waits for READY on the pipe,
// kills, and waits for the exit. The child's `time.Sleep` loop is a park, not a
// timing assumption — SIGKILL ends it, and the sleep only keeps Go's deadlock
// detector from calling a parked goroutine a panic.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/helper/proto"
)

const (
	// crashChildEnv marks the re-executed binary as the process to kill.
	crashChildEnv = "NOCX_FS_JOURNAL_CRASH_CHILD"
	// crashRootEnv and crashTargetEnv name the journal root and the file the
	// child opens a session for.
	crashRootEnv   = "NOCX_FS_JOURNAL_CRASH_ROOT"
	crashTargetEnv = "NOCX_FS_JOURNAL_CRASH_TARGET"
	// crashChildInstance names the killed process's journal directory.
	crashChildInstance = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	// crashSessionID is the session the child opens and never finishes.
	crashSessionID = "0123456789abcdef0123456789abcdef"
)

func TestAKilledHelperLeavesNothingForItsSuccessor(t *testing.T) {
	if os.Getenv(crashChildEnv) == "1" {
		runKilledHelperChild()
		return
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(target, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()

	// #nosec G204 — the command is this test binary and the arguments are
	// constant; the values travel in the environment.
	cmd := exec.Command(os.Args[0], "-test.run=^TestAKilledHelperLeavesNothingForItsSuccessor$")
	cmd.Env = append(os.Environ(),
		crashChildEnv+"=1",
		crashRootEnv+"="+root,
		crashTargetEnv+"="+target,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the helper process: %v", err)
	}
	ready := false
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "READY" {
			ready = true
			break
		}
	}
	if !ready {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("the helper process never reported a live write session")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("killing the helper process: %v", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("the killed helper process reported a clean exit")
	}

	// The killed process's state: a temporary beside the target, and the entry
	// that names it in its own journal directory.
	temp := filepath.Join(dir, writeTempPrefix+crashSessionID)
	mustExist(t, temp)
	crashedDir := filepath.Join(root, journalDirName("test-host", crashChildInstance))
	mustExist(t, filepath.Join(crashedDir, crashSessionID+journalEntrySuffix))

	successor := journalRooted(t, local.New(local.WithRoot(dir)), root, successorInstance)
	mustNotExist(t, temp)
	mustNotExist(t, crashedDir)
	if got := inventoryOf(t, successor); len(got.Stranded) != 0 {
		t.Fatalf("the successor reported %+v after a clean sweep, want nothing stranded", got.Stranded)
	}
}

// runKilledHelperChild is the mode the killed process runs in: a real service,
// a real session, real bytes on disk, and then nothing until SIGKILL.
func runKilledHelperChild() {
	root := os.Getenv(crashRootEnv)
	target := os.Getenv(crashTargetEnv)
	svc, err := New(local.New(local.WithRoot(filepath.Dir(target))), JournalConfig{
		Root: root, Hostname: "test-host", InstanceID: crashChildInstance,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "child: new fs service: %v\n", err)
		os.Exit(1)
	}
	ctx := context.Background()
	for _, step := range []struct {
		op     string
		params any
	}{
		{proto.OpWriteOpen, proto.FSWriteOpenParams{ID: crashSessionID, Path: target}},
		{proto.OpWrite, proto.FSWriteParams{ID: crashSessionID, Offset: 0, Data: []byte("half a file\n")}},
	} {
		body, err := json.Marshal(step.params)
		if err != nil {
			fmt.Fprintf(os.Stderr, "child: marshal: %v\n", err)
			os.Exit(1)
		}
		if _, err := svc.Call(ctx, step.op, body); err != nil {
			fmt.Fprintf(os.Stderr, "child: %s: %v\n", step.op, err)
			os.Exit(1)
		}
	}
	fmt.Println("READY")
	for {
		time.Sleep(time.Hour)
	}
}
