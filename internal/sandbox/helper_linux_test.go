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

	"github.com/shady2k/nocx/internal/log"
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

	fixture := newProjectionProbeFixture(t)
	workspace := fixture.workspace

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}

	probeEnv := append(fixture.environment("/bin/sh"), helperEnvPrefix+"LEAK=must-be-stripped")
	spec := CommandSpec{Path: exe, Args: []string{"-test.run=TestSandboxChildProcess"}, Dir: workspace, Env: probeEnv}

	pol, err := BuildPolicy(Request{
		Workspace:   workspace,
		AddReadOnly: []string{fixture.readOnlyRoot},
		AddWritable: []string{fixture.writableRoot, fixture.nestedWritable},
	}, exe, fixture.runtimeRoot, probeEnv)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	if projectionErr := materializeHomeProjections(fixture.runtimeRoot, pol); projectionErr != nil {
		t.Fatalf("materializeHomeProjections: %v", projectionErr)
	}
	spec.Env = sandboxEnv(spec.Env, pol.Home, pol.Tmp)
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

	if startErr := cmd.Start(); startErr != nil {
		t.Fatalf("start helper: %v", startErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if readyErr := readStatus(ctx, statusR, statusW); readyErr != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		t.Fatalf("readiness failed: %v\nhelper output:\n%s", readyErr, out.String())
	}

	if waitErr := cmd.Wait(); waitErr != nil {
		t.Fatalf("probe inside the cage failed: %v\noutput:\n%s", waitErr, out.String())
	}
	if !strings.Contains(out.String(), "PROBE RESULT: ok") {
		t.Fatalf("probe did not report success:\n%s", out.String())
	}
	// #nosec G304 -- paths belong to the trusted synthetic native-smoke fixture.
	projectedData, err := os.ReadFile(filepath.Join(fixture.writableRoot, "projected.txt"))
	if err != nil || string(projectedData) != "updated" {
		t.Fatalf("projected writable state did not persist: data=%q err=%v", projectedData, err)
	}
	// #nosec G304 -- paths belong to the trusted synthetic native-smoke fixture.
	nestedData, err := os.ReadFile(filepath.Join(fixture.nestedWritable, "nested.txt"))
	if err != nil || string(nestedData) != "nested writable" {
		t.Fatalf("nested projected writable state did not persist: data=%q err=%v", nestedData, err)
	}
	// #nosec G304 -- paths belong to the trusted synthetic native-smoke fixture.
	readOnlyData, err := os.ReadFile(filepath.Join(fixture.readOnlyRoot, "keep.txt"))
	if err != nil || string(readOnlyData) != "read-only" {
		t.Fatalf("projected read-only state changed: data=%q err=%v", readOnlyData, err)
	}
}

func TestLinuxAccessMonitorReportsDeniedOpen(t *testing.T) {
	for _, tc := range []struct {
		name     string
		command  string
		access   AccessClass
		existing bool
	}{
		// Resolved on PATH rather than spelled as an FHS path: on a
		// package-store distribution neither /bin/cat nor /usr/bin/touch
		// exists, and the test failed on the shell lookup long before it
		// reached anything about denied access (nocx-263da).
		{name: "read", command: "cat", access: AccessReadOnly, existing: true},
		{name: "write", command: "touch", access: AccessReadWrite},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command, lookErr := exec.LookPath(tc.command)
			if lookErr != nil {
				t.Skipf("%s is not on PATH: %v", tc.command, lookErr)
			}
			base := t.TempDir()
			workspace := filepath.Join(base, "workspace")
			outside := filepath.Join(base, "outside")
			cache := filepath.Join(base, "cache")
			for _, dir := range []string{workspace, outside, cache} {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			denied := filepath.Join(outside, "target.txt")
			if tc.existing {
				if err := os.WriteFile(denied, []byte("not-secret-test-data"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			inbox := NewAccessInbox(nil)
			svc := NewWithAccess(log.NewSlogAdapter(nil), cache, inbox)
			prepared, err := svc.Prepare(t.Context(), Request{
				Workspace: workspace,
				Identity:  SessionIdentity{SessionID: "session", InstanceID: "instance", Epoch: 1},
			}, CommandSpec{
				Path: command,
				Args: []string{denied},
				Dir:  workspace,
				Env:  os.Environ(),
			})
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			defer prepared.Close()
			if err := prepared.Cmd.Start(); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if err := prepared.WaitReady(t.Context()); err != nil {
				t.Fatalf("WaitReady: %v", err)
			}
			_ = prepared.Cmd.Wait()

			deadline := time.Now().Add(time.Second)
			for {
				page := inbox.List(AccessListOptions{Limit: 10})
				if len(page.Events) > 0 {
					event := page.Events[0]
					if event.Path != denied || event.Access != tc.access || event.Source != AccessSourceLinuxSeccomp {
						t.Fatalf("event = %#v", event)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("denied open did not reach access inbox")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func TestObservedPolicyPathDoesNotHideSymlinkEscapeDenial(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "allowed")
	outside := filepath.Join(root, "outside")
	for _, dir := range []string{allowed, outside} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(outside, "target.txt")
	if err := os.WriteFile(target, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(allowed, "escape")
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}
	attempted := filepath.Join(alias, "target.txt")
	policy := &Policy{ReadOnlyRoots: []string{allowed}}
	if !policyAllowsAccess(policy, attempted, AccessReadOnly) {
		t.Fatal("test precondition: lexical attempted path must look allowed")
	}
	if resolved := observedPolicyPath(attempted); policyAllowsAccess(policy, resolved, AccessReadOnly) {
		t.Fatalf("resolved symlink escape %q was treated as allowed", resolved)
	}
}

func TestLinuxAccessMonitorReportsListenerFailure(t *testing.T) {
	failed := make(chan error, 1)
	monitor := newLinuxAccessMonitor(-1, NewAccessInbox(nil).BeginSession(SessionIdentity{
		SessionID: "session", InstanceID: "instance", Epoch: 1,
	}), "/bin/sh", &Policy{}, func(err error) {
		failed <- err
	})
	monitor.Start()
	select {
	case err := <-failed:
		if err == nil {
			t.Fatal("listener failure callback received nil")
		}
	case <-time.After(time.Second):
		t.Fatal("listener failure was not reported")
	}
	monitor.Close()
}
