package shellintegration

import (
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"
)

// EncodeAgentLaunchPayload serializes an already-validated agent command as a
// shell-safe, versioned data record. Every command, argument and environment
// byte is hex encoded; the separators are therefore unambiguous and never
// interpreted as shell syntax. AgentRecord owns semantic validation before
// this boundary.
func EncodeAgentLaunchPayload(argv, env []string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("agent launch payload has no executable")
	}
	var b strings.Builder
	b.WriteString("v1\n")
	fmt.Fprintf(&b, "%d\n", len(argv))
	for _, arg := range argv {
		if !validLaunchField(arg) {
			return "", fmt.Errorf("agent launch argument is not valid UTF-8 or contains NUL")
		}
		b.WriteString(hex.EncodeToString([]byte(arg)))
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "%d\n", len(env))
	for _, line := range env {
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" || !validLaunchField(key) || !validLaunchField(value) {
			return "", fmt.Errorf("agent launch environment entry is malformed")
		}
		b.WriteString(hex.EncodeToString([]byte(key)))
		b.WriteByte('\t')
		b.WriteString(hex.EncodeToString([]byte(value)))
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func validLaunchField(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}
