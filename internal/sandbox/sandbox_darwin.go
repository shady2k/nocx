//go:build darwin

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/shady2k/nocx/internal/log"
	"golang.org/x/sys/unix"
)

// darwinService is the Seatbelt-backed Service (design spec §9).
type darwinService struct {
	log      log.Logger
	cacheDir string
	probe    seatbeltProbe
	access   *AccessInbox
}

// New returns the Seatbelt-backed Service for the current platform.
func New(logger log.Logger, cacheDir string) Service {
	return &darwinService{log: logger, cacheDir: cacheDir}
}

func NewWithAccess(logger log.Logger, cacheDir string, access *AccessInbox) Service {
	if access != nil {
		access.SetStatus(AccessMonitorStatus{
			Available: true,
			Platform:  "darwin",
			Backend:   string(AccessSourceDarwinSeatbelt),
			Detail:    "Best-effort token-correlated Seatbelt unified-log observation.",
		})
	}
	return &darwinService{log: logger, cacheDir: cacheDir, access: access}
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

// addTrustedExecutables extends an already-built policy with backend-resolved
// fixed launch executables and their runtime roots. Nothing from the renderer
// reaches this seam. The executable itself and every discovered dependency
// root must remain outside user-writable policy roots; otherwise the caged
// process could replace code that the policy is about to trust.
func addTrustedExecutables(p *Policy, executables []string) error {
	if len(executables) == 0 {
		return nil
	}
	roots := append([]string(nil), p.ReadOnlyRoots...)
	seenRoots := make(map[string]bool, len(roots))
	for _, root := range roots {
		seenRoots[root] = true
	}
	seenFiles := make(map[string]bool)
	files := append([]string(nil), p.ReadOnlyFiles...)
	var elfCount, resolveAttempts int
	var elfBytes uint64
	for _, executable := range executables {
		canonical, err := filepath.EvalSymlinks(executable)
		if err != nil {
			return NewSetupErrorf("trusted executable cannot be resolved")
		}
		if policyPathWritable(p, canonical) {
			return NewSetupErrorf("trusted executable conflicts with a writable root")
		}
		before := len(roots)
		if err := addExecutableRoots(canonical, &roots, seenFiles, seenRoots, &elfCount, &elfBytes, &resolveAttempts); err != nil {
			if errors.Is(err, errRuntimeELFInterp) || errors.Is(err, errRuntimeELFNeeded) ||
				errors.Is(err, errRuntimeELFSearchDir) || errors.Is(err, errRuntimeELFDynString) ||
				errors.Is(err, errRuntimeELFAggregate) || errors.Is(err, errRuntimeELFMetadataBudget) ||
				errors.Is(err, errRuntimeELFWorkBudget) {
				return NewSetupErrorf("trusted executable runtime metadata exceeds bound")
			}
			return NewSetupErrorf("trusted executable runtime: %v", err)
		}
		for _, root := range roots[before:] {
			if policyPathWritable(p, root) {
				return NewSetupErrorf("trusted executable runtime conflicts with a writable root")
			}
		}
		files = append(files, canonical)
	}
	p.ReadOnlyRoots = roots
	p.ReadOnlyFiles = files
	if err := p.normalize(); err != nil {
		return NewSetupErrorf("trusted executable policy: %v", err)
	}
	return nil
}

func policyPathWritable(p *Policy, path string) bool {
	for _, root := range append(append([]string(nil), p.WritableRoots...), p.WritableDirs...) {
		if pathWithin(root, path) {
			return true
		}
	}
	for _, file := range p.WritableFiles {
		if pathWithin(file, path) {
			return true
		}
	}
	return false
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
	if trustedErr := addTrustedExecutables(pol, []string{exe}); trustedErr != nil {
		return fail(trustedErr)
	}
	spec.Env = sandboxEnv(spec.Env, pol.Home, pol.Tmp)

	var accessToken string
	if s.access != nil && req.Identity.valid() {
		if id, tokenErr := newAccessEventID(); tokenErr == nil {
			accessToken = "nocx-sandbox-" + id
		}
	}
	profile, err := renderProfile(pol, accessToken)
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

	var accessMonitor *darwinAccessMonitor
	var accessSession *AccessSession
	if accessToken != "" {
		accessSession = s.access.BeginSession(req.Identity)
	}
	if accessToken != "" {
		accessMonitor, err = startDarwinAccessMonitor(accessSession, spec.Path, accessToken, func(error) {
			s.access.SetStatus(AccessMonitorStatus{
				Available: false,
				Platform:  "darwin",
				Backend:   string(AccessSourceDarwinSeatbelt),
				Reason:    "unified-log-exited",
				Detail:    "Sandbox enforcement remains active; denied-access observation stopped unexpectedly.",
			})
		})
		if err != nil {
			s.access.SetStatus(AccessMonitorStatus{
				Available: false,
				Platform:  "darwin",
				Backend:   string(AccessSourceDarwinSeatbelt),
				Reason:    "unified-log-unavailable",
				Detail:    "Sandbox enforcement is active; denied-access observation is unavailable.",
			})
			accessMonitor = nil
		}
	}
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
			if readyErr := readStatus(ctx, statusR, statusW); readyErr != nil {
				return readyErr
			}
			if accessSession != nil && accessMonitor != nil {
				accessSession.Activate()
			}
			return nil
		},
	}
	pc.cleanup = func() {
		_ = statusR.Close()
		if accessMonitor != nil {
			accessMonitor.Close()
		}
		if accessSession != nil {
			accessSession.Close()
		}
		_ = statusW.Close()
		if cmd.Process != nil && cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		RemoveRuntimeRoot(runtimeRoot)
	}
	return pc, nil
}
