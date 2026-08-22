//go:build darwin

// The Seatbelt availability probe is macOS-only, and tagged so rather than
// baselined as dead on Linux — see profile.go for why that trade is the wrong
// way round (nocx-ru6kq).

package sandbox

import (
	"context"
	"os"
	"os/exec"
	"sync"
)

// sandboxExecPath is the deprecated-but-shipped Seatbelt frontend (design
// spec §9.1). Package-level seams (like the probe below) exist so the
// reason mapping is testable on every platform; they are not service
// globals.
var sandboxExecPath = "/usr/bin/sandbox-exec"

// sandboxExecProbe is the functional availability probe: sandbox-exec is
// executed with a minimal profile and /usr/bin/true as the payload, unlike
// termic's mere Path::exists check (research report §1.6).
var sandboxExecProbe = func(ctx context.Context, execPath string) error {
	cmd := exec.CommandContext(ctx, execPath,
		"-p", "(version 1) (deny default) (allow default)",
		"/usr/bin/true")
	return cmd.Run()
}

// seatbeltProbe implements the runtime availability probe for the macOS
// backend. Only a successful probe is cached for the app lifetime; a failure
// is re-probed on the next call (design spec §9.1).
type seatbeltProbe struct {
	mu       sync.Mutex
	probedOK bool
}

func (p *seatbeltProbe) status(ctx context.Context) Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.probedOK {
		return Status{Available: true, Backend: BackendSeatbelt}
	}
	if _, err := os.Stat(sandboxExecPath); err != nil {
		return Status{Available: false, Backend: BackendSeatbelt, Reason: ReasonSandboxExecUnavailable, Detail: err.Error()}
	}
	if err := sandboxExecProbe(ctx, sandboxExecPath); err != nil {
		return Status{Available: false, Backend: BackendSeatbelt, Reason: ReasonProbeFailed, Detail: err.Error()}
	}
	p.probedOK = true
	return Status{Available: true, Backend: BackendSeatbelt}
}
