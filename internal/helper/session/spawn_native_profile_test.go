//go:build linux || darwin

package session

import (
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/sandbox"
	"golang.org/x/sys/unix"
)

func TestNativeProfileRelayRefusesAnUnconsumedPipeBeforeStartupCanHang(t *testing.T) {
	source, err := os.CreateTemp(t.TempDir(), "profile-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	if removeErr := os.Remove(source.Name()); removeErr != nil {
		t.Fatal(removeErr)
	}
	if truncateErr := source.Truncate(sandbox.MaxRunnerPlanBytes); truncateErr != nil {
		t.Fatal(truncateErr)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	sourceFD, err := unix.FcntlInt(source.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	writerFD, err := unix.FcntlInt(writer.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		_ = unix.Close(sourceFD)
		t.Fatal(err)
	}
	started := time.Now()
	if err := relaySeatbeltProfile([]int{sourceFD, writerFD}, started.Add(50*time.Millisecond)); !errors.Is(err, errNativeLaunch) {
		t.Fatalf("unconsumed profile pipe = %v, want native launch refusal", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("private profile relay exceeded its startup deadline: %s", elapsed)
	}
	for _, fd := range []int{sourceFD, writerFD} {
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); !errors.Is(err, syscall.EBADF) {
			t.Fatalf("failed profile relay retained a private capability: %v", err)
		}
	}
}
