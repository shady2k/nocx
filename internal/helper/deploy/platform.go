package deploy

// The platform probe (D20): one bounded command asks the remote host what
// it is, and its answer maps onto Go's GOOS/GOARCH vocabulary. The bound is
// the ExecOnce implementation's — a command that cannot be bounded is not
// an ExecOnce, and an output that cannot be parsed names no platform.

import (
	"context"
	"fmt"
	"strings"
)

// Platform is a GOOS/GOARCH pair naming a helper build target.
type Platform struct {
	GOOS   string
	GOARCH string
}

// ExecOnce runs one bounded command on the remote host and returns its
// captured stdout. The bound is the implementation's: a command that
// exceeds it is refused, never returned truncated as if complete.
type ExecOnce interface {
	Exec(ctx context.Context, cmd string) ([]byte, error)
}

// Probe asks the remote host what it is, with one bounded command:
// `uname -s -m` answers the kernel and machine in one line. The kernel and
// machine names are translated onto Go's vocabulary; anything that does not
// translate is carried through verbatim so Artifact can answer
// ErrUnsupportedPlatform with the host's own words — never a guessed
// platform.
func Probe(ctx context.Context, exec ExecOnce) (Platform, error) {
	out, err := exec.Exec(ctx, "uname -s -m")
	if err != nil {
		return Platform{}, err
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		return Platform{}, fmt.Errorf("deploy: probe: unexpected uname output %q", strings.TrimSpace(string(out)))
	}
	return Platform{GOOS: mapKernel(fields[0]), GOARCH: mapMachine(fields[1])}, nil
}

// mapKernel translates a uname kernel name onto Go's GOOS. Unknown kernels
// pass through: they are real facts about the host, and Artifact refuses
// them.
func mapKernel(s string) string {
	switch s {
	case "Linux":
		return "linux"
	case "Darwin":
		return "darwin"
	}
	return s
}

// mapMachine translates a uname machine name onto Go's GOARCH. Unknown
// machines pass through for the same reason.
func mapMachine(s string) string {
	switch s {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	}
	return s
}
