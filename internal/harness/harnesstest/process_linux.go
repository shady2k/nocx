//go:build linux

package harnesstest

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

func processRunning(pid int) bool {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		return true
	}
	end := strings.LastIndexByte(string(raw), ')')
	fields := strings.Fields(string(raw[end+1:]))
	return len(fields) == 0 || (fields[0] != "Z" && fields[0] != "X")
}
