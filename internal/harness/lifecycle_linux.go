//go:build linux

package harness

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// observeExit reports that the agent's leader exited without reaping it, so
// the session keeps the process identity until its group signals finish
// (ADR-sr71cdyt).
func observeExit(pid int) error {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return err
	}
}

func groupRunning(pgid int) (bool, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		raw, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ESRCH) {
			continue
		}
		if err != nil {
			return false, err
		}
		end := strings.LastIndexByte(string(raw), ')')
		if end < 0 {
			return false, fmt.Errorf("invalid process stat for %s", entry.Name())
		}
		fields := strings.Fields(string(raw[end+1:]))
		if len(fields) < 3 {
			return false, fmt.Errorf("short process stat for %s", entry.Name())
		}
		group, err := strconv.Atoi(fields[2])
		if err != nil {
			return false, err
		}
		if group == pgid && fields[0] != "Z" && fields[0] != "X" {
			return true, nil
		}
	}
	return false, nil
}
