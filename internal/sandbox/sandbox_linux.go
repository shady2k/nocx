//go:build linux

package sandbox

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"

	"github.com/shady2k/nocx/internal/log"
	"golang.org/x/sys/unix"
)

// linuxService is the Landlock-backed Service (design spec §8).
type linuxService struct {
	log      log.Logger
	cacheDir string
	access   *AccessInbox
}

// New returns the Landlock-backed Service for the current platform.
func New(logger log.Logger, cacheDir string) Service {
	return &linuxService{log: logger, cacheDir: cacheDir}
}

// NewWithAccess additionally enables the denied-access monitor for sandboxed
// sessions. New remains the enforcement-only constructor used by isolated
// package tests and embedders that do not expose the inbox.
func NewWithAccess(logger log.Logger, cacheDir string, access *AccessInbox) Service {
	if access != nil {
		access.SetStatus(AccessMonitorStatus{
			Available: true,
			Platform:  "linux",
			Backend:   string(AccessSourceLinuxSeccomp),
			Detail:    "Best-effort syscall observation; Landlock remains the sole enforcement boundary.",
		})
	}
	return &linuxService{log: logger, cacheDir: cacheDir, access: access}
}

func (s *linuxService) Status(_ context.Context) Status {
	abi, err := detectABI()
	return statusForABI(abi, err)
}

func (s *linuxService) NewRuntimeRoot() (string, error) {
	root, err := NewRuntimeRoot(s.cacheDir)
	if err != nil {
		return "", NewSetupErrorf("runtime root creation failed")
	}
	return root, nil
}

// Prepare builds the common policy, re-execs os.Executable() as the
// __sandbox-landlock-exec helper with the policy in an unlinked mode-0600
// memfd plus a readiness pipe, and returns a PreparedCommand whose WaitReady
// succeeds only after Landlock enforcement is in place. Fail-closed: any
// error (invalid workspace, runtime root, probe, helper) yields a typed error
// and removes the runtime tree.
func (s *linuxService) Prepare(ctx context.Context, req Request, spec CommandSpec) (*PreparedCommand, error) {
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
	if projectionErr := materializeHomeProjections(runtimeRoot, pol); projectionErr != nil {
		return fail(projectionErr)
	}
	// The shell runs with HOME/XDG/TMPDIR pointed into the ephemeral runtime
	// tree and NOCX_SANDBOX=filesystem (design spec §5.3); the policy builder
	// already consumed the base PATH above.
	spec.Env = sandboxEnv(spec.Env, pol.Home, pol.Tmp)

	monitorEnabled := s.access != nil && req.Identity.valid()
	var accessSession *AccessSession
	if monitorEnabled {
		accessSession = s.access.BeginSession(req.Identity)
	}
	monitorChildFD := 0
	if monitorEnabled {
		monitorChildFD = 5 + len(spec.ExtraFiles)
	}
	payload := helperPayload{Policy: pol, Command: spec, AccessMonitorFD: monitorChildFD}
	data, err := json.Marshal(payload)
	if err != nil {
		return fail(NewSetupErrorf("serialize policy: %v", err))
	}
	if len(data) > maxPolicyBytes {
		return fail(NewSetupErrorf("policy payload exceeds %d bytes", maxPolicyBytes))
	}

	policyFD, err := unix.MemfdCreate("nocx-sandbox-policy", unix.MFD_CLOEXEC)
	if err != nil {
		return fail(NewSetupErrorf("policy fd: %v", err))
	}
	policyFile := os.NewFile(uintptr(policyFD), "policy")
	if _, wErr := policyFile.Write(data); wErr != nil {
		_ = policyFile.Close()
		return fail(NewSetupErrorf("write policy: %v", wErr))
	}
	if _, sErr := policyFile.Seek(0, 0); sErr != nil {
		_ = policyFile.Close()
		return fail(NewSetupErrorf("rewind policy: %v", sErr))
	}

	statusR, statusW, err := os.Pipe()
	if err != nil {
		_ = policyFile.Close()
		return fail(NewSetupErrorf("status pipe: %v", err))
	}

	var monitorParent, monitorChild *os.File
	if monitorEnabled {
		pair, socketErr := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
		if socketErr != nil {
			_ = policyFile.Close()
			_ = statusR.Close()
			_ = statusW.Close()
			return fail(NewSetupErrorf("access monitor socket: %v", socketErr))
		}
		monitorParent = os.NewFile(uintptr(pair[0]), "access-monitor-parent")
		monitorChild = os.NewFile(uintptr(pair[1]), "access-monitor-child")
	}

	exe, err := os.Executable()
	if err != nil {
		_ = policyFile.Close()
		_ = statusR.Close()
		_ = statusW.Close()
		if monitorParent != nil {
			_ = monitorParent.Close()
			_ = monitorChild.Close()
		}
		return fail(NewSetupErrorf("executable path: %v", err))
	}

	// Preserve the ordinary command's inherited descriptors at their exact
	// numbers (fd 3, 4, …). The helper protocol follows them, and its fd
	// numbers ride fixed internal argv so the helper can find the policy
	// without shifting lifecycle descriptors.
	policyChildFD := 3 + len(spec.ExtraFiles)
	statusChildFD := policyChildFD + 1
	// #nosec G204 -- re-exec of os.Executable with fixed internal arguments.
	cmd := exec.Command(exe, helperArg, strconv.Itoa(policyChildFD), strconv.Itoa(statusChildFD)) //nolint:gosec
	cmd.Env = spec.Env
	cmd.Dir = spec.Dir
	cmd.ExtraFiles = append(append([]*os.File(nil), spec.ExtraFiles...), policyFile, statusW)
	if monitorChild != nil {
		cmd.ExtraFiles = append(cmd.ExtraFiles, monitorChild)
	}
	// Stdout/Stderr/Stdin stay nil: the PTY path attaches them via
	// pty.StartWithSize; the session must not exist before enforcement.

	var accessMonitor *linuxAccessMonitor
	pc := &PreparedCommand{
		Cmd:        cmd,
		Backend:    BackendLandlock,
		Policy:     pol,
		policyFile: policyFile,
		waitReady: func(ctx context.Context) error {
			if monitorChild != nil {
				_ = monitorChild.Close()
			}
			if err := readStatus(ctx, statusR, statusW); err != nil {
				return err
			}
			if monitorParent == nil {
				return nil
			}
			listenerFD, receiveErr := receiveAccessListener(monitorParent)
			_ = monitorParent.Close()
			if receiveErr != nil || listenerFD < 0 {
				s.access.SetStatus(AccessMonitorStatus{
					Available: false,
					Platform:  "linux",
					Backend:   string(AccessSourceLinuxSeccomp),
					Reason:    "seccomp-user-notify-unavailable",
					Detail:    "Sandbox enforcement is active; denied-access observation is unavailable.",
				})
				return nil
			}
			accessMonitor = newLinuxAccessMonitor(listenerFD, accessSession, spec.Path, pol, func(error) {
				s.access.SetStatus(AccessMonitorStatus{
					Available: false,
					Platform:  "linux",
					Backend:   string(AccessSourceLinuxSeccomp),
					Reason:    "seccomp-listener-failed",
					Detail:    "Denied-access observation stopped unexpectedly; the affected sandbox is terminated fail-closed.",
				})
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
			})
			accessMonitor.Start()
			accessSession.Activate()
			return nil
		},
	}
	pc.cleanup = func() {
		_ = statusR.Close()
		_ = statusW.Close()
		if monitorParent != nil {
			_ = monitorParent.Close()
		}
		if monitorChild != nil {
			_ = monitorChild.Close()
		}
		if cmd.Process != nil && cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		if accessMonitor != nil {
			accessMonitor.Close()
		}
		if accessSession != nil {
			accessSession.Close()
		}
		RemoveRuntimeRoot(runtimeRoot)
	}
	return pc, nil
}
