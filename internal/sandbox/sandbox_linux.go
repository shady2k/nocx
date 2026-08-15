//go:build linux

package sandbox

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/shady2k/nocx/internal/log"
	"golang.org/x/sys/unix"
)

// linuxService is the Landlock-backed Service (design spec §8).
type linuxService struct {
	log      log.Logger
	cacheDir string
}

// New returns the Landlock-backed Service for the current platform.
func New(logger log.Logger, cacheDir string) Service {
	return &linuxService{log: logger, cacheDir: cacheDir}
}

func (s *linuxService) Status(_ context.Context) Status {
	abi, err := detectABI()
	return statusForABI(abi, err)
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

	payload := helperPayload{Policy: pol, Command: spec}
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

	exe, err := os.Executable()
	if err != nil {
		_ = policyFile.Close()
		_ = statusR.Close()
		_ = statusW.Close()
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
	// Stdout/Stderr/Stdin stay nil: the PTY path attaches them via
	// pty.StartWithSize; the session must not exist before enforcement.

	pc := &PreparedCommand{
		Cmd:        cmd,
		Backend:    BackendLandlock,
		Policy:     pol,
		policyFile: policyFile,
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

// readStatus reads the readiness byte with a bounded deadline. A zero byte
// means enforcement succeeded; anything else is a typed setup failure with
// the helper's reason. The parent's own copy of the write end is closed
// first — WaitReady is only called after the helper is started, so the child
// already holds its duplicate, and a child that exits without reporting now
// EOFs the read instead of blocking until the deadline. On timeout the
// goroutine stays blocked until cleanup closes the read end.
func readStatus(ctx context.Context, r, w *os.File) error {
	if ctx == nil {
		ctx = context.Background()
	}
	_ = w.Close()
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 1)
		n, err := r.Read(buf)
		if err != nil {
			done <- NewSetupErrorf("readiness: %v", err)
			return
		}
		if n != 1 {
			done <- NewSetupErrorf("readiness: short read")
			return
		}
		if buf[0] != 0 {
			rest, _ := io.ReadAll(r)
			detail := strings.TrimSpace(string(rest))
			if detail == "" {
				detail = "unknown helper failure"
			}
			done <- NewSetupErrorf("helper setup failed: %s", detail)
			return
		}
		done <- nil
	}()

	select {
	case <-ctx.Done():
		return NewSetupErrorf("readiness timeout: %v", ctx.Err())
	case err := <-done:
		return err
	}
}
