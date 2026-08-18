//go:build darwin

package sandbox

import (
	"regexp"
	"strings"
)

var seatbeltDenialPattern = regexp.MustCompile(`Sandbox:\s+([^\s(]+)\([0-9]+\)\s+deny(?:\([0-9]+\))?\s+(file-[^\s]+)\s+(/.*)$`)

func parseSeatbeltDenial(message string) (program, operation, path string, access AccessClass, ok bool) {
	match := seatbeltDenialPattern.FindStringSubmatch(strings.TrimSpace(message))
	if len(match) != 4 {
		return "", "", "", "", false
	}
	program = match[1]
	operation = match[2]
	path = strings.TrimSpace(match[3])
	if path == "" || !strings.HasPrefix(path, "/") {
		return "", "", "", "", false
	}
	access = AccessReadOnly
	if strings.HasPrefix(operation, "file-write") || strings.Contains(operation, "unlink") || strings.Contains(operation, "rename") {
		access = AccessReadWrite
	}
	return program, operation, path, access, true
}
