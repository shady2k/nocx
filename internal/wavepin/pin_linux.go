//go:build linux

package wavepin

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The process inspection package has the same platform split, but its source
// is unexported and its broader diagnostic contract does not fit this small
// identity seam. Keep this answer local while preserving its two safety rules:
// parse stat after the LAST ')' and cache Linux's boot instant once.
var (
	linuxBootOnce sync.Once
	linuxBoot     time.Time
)

const (
	linuxStatParentIndex    = 1  // field 4 after fields 1 and 2 are removed
	linuxStatStartTimeIndex = 19 // field 22 after fields 1 and 2 are removed
	linuxClockTick          = 10 * time.Millisecond
)

var errLinuxStatMalformed = errors.New("wavepin: malformed /proc process stat")

func readProcess(pid int) (processSnapshot, error) {
	if pid <= 0 {
		return processSnapshot{}, os.ErrProcessDone
	}
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat")) // #nosec G304 — pid is kernel-validated input
	if err != nil {
		return processSnapshot{}, err
	}
	return parseLinuxStat(raw, linuxBootTime())
}

func linuxBootTime() time.Time {
	linuxBootOnce.Do(func() {
		raw, err := os.ReadFile("/proc/stat") // #nosec G304 — fixed procfs path
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[0] == "btime" {
				seconds, err := strconv.ParseInt(fields[1], 10, 64)
				if err == nil && seconds > 0 {
					linuxBoot = time.Unix(seconds, 0)
				}
				return
			}
		}
	})
	return linuxBoot
}

func parseLinuxStat(raw []byte, boot time.Time) (processSnapshot, error) {
	line := string(raw)
	end := strings.LastIndexByte(line, ')')
	if end < 0 {
		return processSnapshot{}, errLinuxStatMalformed
	}
	fields := strings.Fields(line[end+1:])
	if len(fields) <= linuxStatStartTimeIndex {
		return processSnapshot{}, errLinuxStatMalformed
	}
	parent, err := strconv.Atoi(fields[linuxStatParentIndex])
	if err != nil || parent <= 0 {
		return processSnapshot{}, errLinuxStatMalformed
	}
	ticks, err := strconv.ParseInt(fields[linuxStatStartTimeIndex], 10, 64)
	if err != nil || ticks < 0 || boot.IsZero() {
		return processSnapshot{}, errLinuxStatMalformed
	}
	return processSnapshot{
		Parent:    parent,
		StartTime: boot.Add(time.Duration(ticks) * linuxClockTick),
	}, nil
}
