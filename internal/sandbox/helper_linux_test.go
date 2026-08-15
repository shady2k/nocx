//go:build linux

package sandbox

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The enforcement suite proves behavior, not source shape: the test binary
// doubles as the sandbox helper (via TestSandboxChildProcess + env) and as
// the probe that runs INSIDE the cage after the helper exec's it. The helper
// applies real Landlock restrictions, so every probe assertion is executed
// under the enforced policy.

const (
	envHelperChild = "NOCX_SANDBOX_HELPER_CHILD"
	envPolicyFD    = helperEnvPrefix + "POLICY_FD"
	envStatusFD    = helperEnvPrefix + "STATUS_FD"
)

// init makes the sandbox test binary double as the sandbox helper when it is
// re-exec'd with the production argv marker (real Service end-to-end tests,
// where os.Executable() is the test binary). init runs before the test
// framework, so the helper child exits here without running the suite.
func init() {
	if len(os.Args) > 1 && os.Args[1] == helperArg {
		policyFD, statusFD, err := helperFDs(os.Args)
		if err != nil {
			os.Exit(helperExitSetup)
		}
		os.Exit(helperMain(policyFD, statusFD))
	}
}

// TestSandboxChildProcess is the child entry point: with envHelperChild set
// it acts as the sandbox helper via injected fds; with envProbe set it runs
// the assertions inside the cage. (The argv-marker path is handled by init
// above.)
func TestSandboxChildProcess(t *testing.T) {
	switch {
	case os.Getenv(envHelperChild) == "1":
		pfd, e1 := strconv.Atoi(os.Getenv(envPolicyFD))
		sfd, e2 := strconv.Atoi(os.Getenv(envStatusFD))
		if e1 != nil || e2 != nil {
			os.Exit(helperExitSetup)
		}
		os.Exit(helperMain(pfd, sfd))
	case os.Getenv(envProbe) == "1":
		os.Exit(runProbe())
	}
}

// TestLandlockEnforcement is the parent: builds a fixture, launches the
// helper with a real policy, waits for enforcement readiness, and checks the
// probe's verdict inside the cage.
func TestLandlockEnforcement(t *testing.T) {
	abi, err := detectABI()
	if err != nil || abi < minLandlockABI {
		t.Skipf("landlock enforcement requires kernel ABI >= %d (detected %v, err %v)", minLandlockABI, abi, err)
	}

	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	home := filepath.Join(base, "runtime", "home")
	tmp := filepath.Join(base, "runtime", "tmp")
	sentinel := filepath.Join(base, "sentinel-secret.txt")
	for _, d := range []string{workspace, home, tmp} {
		if mkErr := os.MkdirAll(d, 0o750); mkErr != nil {
			t.Fatalf("mkdir %s: %v", d, mkErr)
		}
	}
	if wErr := os.WriteFile(sentinel, []byte("top secret"), 0o600); wErr != nil {
		t.Fatalf("write sentinel: %v", wErr)
	}
	// A hard link to the sentinel that already exists inside the writable
	// root before launch: the documented hierarchy-not-inode limitation.
	preHard := filepath.Join(workspace, "pre-hard-link")
	if lErr := os.Link(sentinel, preHard); lErr != nil {
		t.Fatalf("pre-link sentinel: %v", lErr)
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}

	probeEnv := append(os.Environ(),
		envProbe+"=1",
		"NOCX_SB_SHELL=/bin/sh",
		"NOCX_SB_WORKSPACE="+workspace,
		"NOCX_SB_SENTINEL="+sentinel,
		"NOCX_SB_PREHARD="+preHard,
		"NOCX_SB_HOME="+home,
		"NOCX_SB_TMP="+tmp,
		helperEnvPrefix+"LEAK=must-be-stripped",
	)
	spec := CommandSpec{Path: exe, Args: []string{"-test.run=TestSandboxChildProcess"}, Dir: workspace, Env: probeEnv}

	pol, err := BuildPolicy(workspace, exe, filepath.Join(base, "runtime"), probeEnv)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	payload := helperPayload{Policy: pol, Command: spec}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	policyFD, err := unix.MemfdCreate("nocx-test-policy", unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatalf("memfd: %v", err)
	}
	policyFile := os.NewFile(uintptr(policyFD), "policy")
	defer func() { _ = policyFile.Close() }()
	if _, wErr := policyFile.Write(data); wErr != nil {
		t.Fatalf("write memfd: %v", wErr)
	}
	if _, sErr := policyFile.Seek(0, 0); sErr != nil {
		t.Fatalf("rewind memfd: %v", sErr)
	}

	statusR, statusW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer func() { _ = statusR.Close() }()
	defer func() { _ = statusW.Close() }()

	helperEnv := append(os.Environ(),
		envHelperChild+"=1",
		envPolicyFD+"=3",
		envStatusFD+"=4",
	)
	cmd := exec.Command(exe, "-test.run=TestSandboxChildProcess") //nolint:gosec // test doubles as helper/probe
	cmd.Env = helperEnv
	cmd.Dir = workspace
	cmd.ExtraFiles = []*os.File{policyFile, statusW}
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := readStatus(ctx, statusR, statusW); err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		t.Fatalf("readiness failed: %v\nhelper output:\n%s", err, out.String())
	}

	if err := cmd.Wait(); err != nil {
		t.Fatalf("probe inside the cage failed: %v\noutput:\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "PROBE RESULT: ok") {
		t.Fatalf("probe did not report success:\n%s", out.String())
	}
}
