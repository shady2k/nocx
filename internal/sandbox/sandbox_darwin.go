//go:build darwin

package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"github.com/shady2k/nocx/internal/log"
	"golang.org/x/sys/unix"
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

// MaybeHelper runs the macOS post-profile shim when this process was
// re-executed by sandbox-exec with the helper marker as argv[1], and returns
// true (the process has exited). It must be called before app startup
// (design spec §7.2).
func MaybeHelper() bool {
	statusFD, shell, shellArgs, ok, errCode := parseSeatbeltShimArgv(os.Args)
	if !ok {
		return false
	}
	if errCode != 0 {
		fmt.Fprintln(os.Stderr, "sandbox seatbelt shim: malformed invocation")
		os.Exit(errCode)
	}
	os.Exit(seatbeltShimMain(statusFD, shell, shellArgs, unix.Exec))
	return true
}

// Status probes /usr/bin/sandbox-exec; a successful probe is cached for the
// app lifetime (probe.go).
func (s *darwinService) Status(ctx context.Context) Status {
	return s.probe.status(ctx)
}

func (s *darwinService) NewRuntimeRoot() (string, error) {
	root, err := NewRuntimeRoot(s.cacheDir)
	if err != nil {
		return "", NewSetupErrorf("runtime root creation failed")
	}
	return root, nil
}

// Prepare renders the common policy as a deterministic SBPL profile and
// launches /usr/bin/sandbox-exec -p <profile> <nocx-exe>
// __sandbox-seatbelt-exec <status-fd> <shell> <shell-args>. sandbox-exec
// applies Seatbelt before execing the nocx shim; the shim acknowledges
// readiness only after the profile applied, then unix.Execs the real shell.
// Fail-closed: any error removes the runtime tree and yields a typed error.
func (s *darwinService) Prepare(ctx context.Context, req Request, spec CommandSpec) (*PreparedCommand, error) {
	if spec.Path == "" {
		return nil, NewSetupErrorf("empty command path")
	}
	status := s.Status(ctx)
	if !status.Available {
		return nil, &StatusError{Status: status}
	}

	runtimeRoot := req.RuntimeRoot
	if runtimeRoot == "" {
		var err error
		runtimeRoot, err = s.NewRuntimeRoot()
		if err != nil {
			return nil, err
		}
	}
	fail := func(err error) (*PreparedCommand, error) {
		RemoveRuntimeRoot(runtimeRoot)
		return nil, err
	}

	pol, err := BuildPolicy(req, spec.Path, runtimeRoot, spec.Env)
	if err != nil {
		return fail(err)
	}

	// The shim runs under the Seatbelt profile, so its own executable must be
	// readable. Route it through addTrustedExecutables so writable conflicts,
	// dependency roots, root-count, and serialized-size bounds are checked
	// before render. No policy mutation after the final validation.
	exe, err := os.Executable()
	if err != nil {
		return fail(NewSetupErrorf("executable path: %v", err))
	}
	trusted := make([]string, 0, len(spec.TrustedExecutables)+1)
	trusted = append(trusted, exe)
	trusted = append(trusted, spec.TrustedExecutables...)
	if trustedErr := addTrustedExecutables(pol, trusted); trustedErr != nil {
		return fail(trustedErr)
	}
	// The shell runs with HOME/XDG/TMPDIR pointed into the ephemeral runtime
	// tree and NOCX_SANDBOX=filesystem (design spec §5.3); the policy builder
	// already consumed the base PATH above.
	spec.Env = sandboxEnv(spec.Env, pol.Home, pol.Tmp)

	profile, err := renderProfile(pol)
	if err != nil {
		return fail(err)
	}

	statusR, statusW, err := os.Pipe()
	if err != nil {
		return fail(NewSetupErrorf("status pipe: %v", err))
	}

	// Preserve the ordinary command's inherited descriptors at their exact
	// numbers; the shim's status descriptor follows them, and its number rides
	// fixed internal argv so the shim finds it without shifting lifecycle
	// descriptors.
	statusChildFD := 3 + len(spec.ExtraFiles)
	shimArgs := append([]string{seatbeltHelperArg, strconv.Itoa(statusChildFD), spec.Path}, spec.Args...)
	args := append([]string{"-p", profile, exe}, shimArgs...)
	cmd := exec.Command(sandboxExecPath, args...) //nolint:gosec // pinned path; shim and shell are policy-canonicalized
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	cmd.ExtraFiles = append(append([]*os.File(nil), spec.ExtraFiles...), statusW)

	pc := &PreparedCommand{
		Cmd:     cmd,
		Backend: BackendSeatbelt,
		Policy:  pol,
		waitReady: func(ctx context.Context) error {
			return readStatus(ctx, statusR, statusW)
		},
	}
	pc.cleanup = func() {
		_ = statusR.Close()
		_ = statusW.Close()
		if cmd.Process != nil && cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		RemoveRuntimeRoot(runtimeRoot)
	}
	return pc, nil
}
