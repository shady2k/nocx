//go:build darwin

package endpoint_test

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/helper/endpoint"
	"golang.org/x/sys/unix"
)

// The real daemon must own its responsible code, not just its responsible PID:
// an orphaned daemon already had its own PID while retaining the app's path.
func TestStartedHelperOwnsItsResponsibleExecutable(t *testing.T) {
	bin, generation := helperBinary(t)
	// An ordinary spawn must reproduce inherited attribution here. Otherwise
	// this launch context cannot distinguish the old launcher from the fix.
	control := startPeer(t, bin, helperHome(t), generation)
	controlPath := responsiblePath(t, control.cmd.Process.Pid)
	want, err := filepath.EvalSymlinks(bin)
	if err != nil {
		t.Fatal(err)
	}
	if controlPath == want {
		t.Fatalf("ordinary-spawn control already owns its code (%q); this fixture cannot prove disclaimed responsibility", controlPath)
	}
	home := helperHome(t)
	t.Setenv("HOME", home)
	conn, err := endpoint.Ensure(t.Context(), endpoint.Dir(home), generation, generation, bin)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	raw, err := conn.(*net.UnixConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	var peerErr error
	if err := raw.Control(func(fd uintptr) {
		pid, peerErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); err != nil {
		t.Fatal(err)
	}
	if peerErr != nil {
		t.Fatal(peerErr)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proc.Signal(os.Interrupt) }()
	got := responsiblePath(t, pid)
	if got != want {
		t.Fatalf("helper responsible path = %q, want its own executable %q (ordinary spawn retained %q)", got, want, controlPath)
	}
}

func responsiblePath(t *testing.T, pid int) string {
	t.Helper()
	// procinfo needs root. ci-mac's hosted runner has passwordless sudo; failure
	// is a failed assertion, not a skip that silently loses Darwin coverage.
	output, err := exec.Command("sudo", "-n", "/bin/launchctl", "procinfo", strconv.Itoa(pid)).CombinedOutput()
	if err != nil {
		t.Fatalf("procinfo: %v: %s", err, output)
	}
	match := regexp.MustCompile(`(?m)^\s*responsible path\s*=\s*(.+)$`).FindStringSubmatch(string(output))
	if len(match) != 2 {
		t.Fatalf("no responsible path in procinfo: %s", output)
	}
	return strings.TrimSpace(match[1])
}
