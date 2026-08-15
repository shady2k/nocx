//go:build darwin

package sandbox

import (
	"context"
	"os"
	"os/exec"

	"github.com/shady2k/nocx/internal/log"
)

// darwinService is the Seatbelt-backed Service (design spec §9).
type darwinService struct {
	log      log.Logger
	cacheDir string
	probe    seatbeltProbe
}

// New returns the Seatbelt-backed Service for the current platform.
func New(logger log.Logger, cacheDir string) Service {
	return &darwinService{log: logger, cacheDir: cacheDir}
}

// MaybeHelper is a no-op on non-Linux platforms: the sandbox helper is a
// Linux-only mechanism.
func MaybeHelper() bool { return false }

// Status probes /usr/bin/sandbox-exec; a successful probe is cached for the
// app lifetime (probe.go).
func (s *darwinService) Status(ctx context.Context) Status {
	return s.probe.status(ctx)
}

// Prepare renders the common policy as a deterministic SBPL profile and
// launches /usr/bin/sandbox-exec -p <profile> <shell> -i directly — no
// intermediate shell. Fail-closed: any render/probe error removes the
// runtime tree and yields a typed error. WaitReady is nil: launch success is
// readiness — sandbox-exec either applies the profile and execs the shell,
// or exits nonzero.
func (s *darwinService) Prepare(ctx context.Context, req Request, spec CommandSpec) (*PreparedCommand, error) {
	if spec.Path == "" {
		return nil, NewSetupErrorf("empty command path")
	}
	status := s.Status(ctx)
	if !status.Available {
		return nil, &StatusError{Status: status}
	}

	runtimeRoot, err := NewRuntimeRoot(s.cacheDir)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*PreparedCommand, error) {
		RemoveRuntimeRoot(runtimeRoot)
		return nil, err
	}

	pol, err := BuildPolicy(req.Workspace, spec.Path, runtimeRoot, spec.Env)
	if err != nil {
		return fail(err)
	}
	// The shell runs with HOME/XDG/TMPDIR pointed into the ephemeral runtime
	// tree and NOCX_SANDBOX=filesystem (design spec §5.3); the policy builder
	// already consumed the base PATH above.
	spec.Env = sandboxEnv(spec.Env, pol.Home, pol.Tmp)

	profile, err := renderProfile(pol)
	if err != nil {
		return fail(err)
	}

	args := append([]string{"-p", profile, spec.Path}, spec.Args...)
	cmd := exec.Command(sandboxExecPath, args...) //nolint:gosec // pinned path; shell path is policy-canonicalized
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	cmd.ExtraFiles = append([]*os.File(nil), spec.ExtraFiles...)

	pc := &PreparedCommand{
		Cmd:     cmd,
		Backend: BackendSeatbelt,
		Policy:  pol,
		cleanup: func() {
			if cmd.Process != nil && cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
			RemoveRuntimeRoot(runtimeRoot)
		},
	}
	return pc, nil
}
