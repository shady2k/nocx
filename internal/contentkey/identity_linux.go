//go:build linux

package contentkey

import (
	"os"
	"strconv"
	"strings"
)

// readMachineID returns the stable per-machine identifier (nocx-rtg0.14):
// the systemd machine-id. It is NOT a secret (/etc/machine-id is mode 444) —
// the salt is the secret ingredient of the derivation, which is why the salt
// must never sit beside content.db.
func readMachineID() (string, error) {
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		b, err := os.ReadFile(p) // #nosec -- p is a fixed constant path, never caller input
		if err == nil {
			if s := strings.TrimSpace(string(b)); s != "" {
				return s, nil
			}
		}
	}
	return "", errNoMachineID
}

// readUserID returns the stable per-user identifier: the numeric uid.
func readUserID() (string, error) {
	return strconv.Itoa(os.Getuid()), nil
}
