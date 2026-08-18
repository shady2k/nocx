//go:build darwin

package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

// The production binary dispatches MaybeHelper before app startup. The test
// binary has no application main, so its init path must provide the same
// dispatch when pty.NewLocal re-executes os.Executable as the Seatbelt shim.
func init() {
	_ = MaybeHelper()
}

func TestAddTrustedExecutablesGrantsCanonicalFileAndRuntimeRoots(t *testing.T) {
	workspace, runtimeRoot, _ := fixture(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "agent-real")
	if err := os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil { //nolint:gosec // executable fixture
		t.Fatalf("write executable: %v", err)
	}
	link := filepath.Join(dir, "agent")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	policy, err := BuildPolicy(Request{Workspace: workspace}, "/bin/sh", runtimeRoot, nil)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	if err := addTrustedExecutables(policy, []string{link, link}); err != nil {
		t.Fatalf("addTrustedExecutables: %v", err)
	}
	canonical := canonicalPath(t, target)
	count := 0
	for _, file := range policy.ReadOnlyFiles {
		if file == canonical {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("canonical executable count = %d in %v, want 1", count, policy.ReadOnlyFiles)
	}
	if err := ValidatePolicy(policy); err != nil {
		t.Fatalf("ValidatePolicy: %v", err)
	}
}

// TestDarwinChildProcess is the child entry of the macOS enforcement suite:
// with envProbe set the test binary runs the shared probe assertions INSIDE
// the sandbox-exec cage.
func TestDarwinChildProcess(t *testing.T) {
	if os.Getenv(envProbe) == "1" {
		os.Exit(runProbe())
	}
}

// TestSeatbeltEnforcement is the real enforcement smoke: it launches
// /usr/bin/sandbox-exec -p <rendered profile> <test binary> with the probe
// env and verifies the probe's verdict inside the cage. This is the test the
// sandbox-smoke-macos target and the release gate run on macOS; it cannot
// run on Linux and is skipped loudly, not silently.
func TestSeatbeltEnforcement(t *testing.T) {
	if raceInstrumented() {
		t.Skip("Seatbelt process smoke runs without TSan in the dedicated macOS gate")
	}
	if _, err := os.Stat(sandboxExecPath); err != nil {
		t.Skipf("seatbelt enforcement requires %s (skipped loudly: %v)", sandboxExecPath, err)
	}

	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	home := filepath.Join(base, "runtime", "home")
	tmp := filepath.Join(base, "runtime", "tmp")
	sentinel := filepath.Join(base, "sentinel-secret.txt")
	roRoot := filepath.Join(base, "ro-root")
	rwRoot := filepath.Join(base, "rw-root")
	for _, d := range []string{workspace, home, tmp, roRoot, rwRoot} {
		if mkErr := os.MkdirAll(d, 0o750); mkErr != nil {
			t.Fatalf("mkdir %s: %v", d, mkErr)
		}
	}
	if wErr := os.WriteFile(sentinel, []byte("top secret"), 0o600); wErr != nil {
		t.Fatalf("write sentinel: %v", wErr)
	}
	if wErr := os.WriteFile(filepath.Join(roRoot, "keep.txt"), []byte("read-only"), 0o600); wErr != nil {
		t.Fatalf("write read-only fixture: %v", wErr)
	}
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
		"NOCX_SB_WORKSPACE="+workspace,
		"NOCX_SB_SENTINEL="+sentinel,
		"NOCX_SB_PREHARD="+preHard,
		"NOCX_SB_HOME="+home,
		"NOCX_SB_TMP="+tmp,
		"NOCX_SB_RO_ROOT="+roRoot,
		"NOCX_SB_RW_ROOT="+rwRoot,
	)

	pol, err := BuildPolicy(Request{
		Workspace:   workspace,
		AddReadOnly: []string{roRoot},
		AddWritable: []string{rwRoot},
	}, exe, filepath.Join(base, "runtime"), probeEnv)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	profile, err := renderProfile(pol)
	if err != nil {
		t.Fatalf("renderProfile: %v", err)
	}

	cmd := exec.Command(sandboxExecPath, "-p", profile, exe, "-test.run=TestDarwinChildProcess") //nolint:gosec // test injects the sandbox-exec seam; arguments are asserted below
	cmd.Env = probeEnv
	cmd.Dir = workspace
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		t.Fatalf("probe inside the Seatbelt cage failed: %v\noutput:\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "PROBE RESULT: ok") {
		t.Fatalf("probe did not report success:\n%s", out.String())
	}
}

func TestDarwinSystemRootsIncludeDynamicLoaderCaches(t *testing.T) {
	got := make(map[string]bool)
	for _, root := range systemReadOnlyRoots() {
		got[root] = true
	}
	for _, root := range []string{"/System/Library", "/System/Volumes/Preboot/Cryptexes", "/private/var/db"} {
		if !got[root] {
			t.Errorf("system read-only roots omit dynamic-loader root %q", root)
		}
	}
}

func raceInstrumented() bool {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, setting := range info.Settings {
		if setting.Key == "-race" && setting.Value == "true" {
			return true
		}
	}
	return false
}

// TestDarwinServicePrepare_ConstructsWrapper asserts the wrapper shape
// (sandbox-exec -p <profile> <nocx-exe> __sandbox-seatbelt-exec <status-fd>
// <shell> <args>), the environment, the working directory, the shim's
// readable executable, the runtime tree lifecycle, and fail-closed behaviour
// — without launching anything (construction-only, runnable in CI).
func TestDarwinServicePrepare_ConstructsWrapper(t *testing.T) {
	origPath, origProbe := sandboxExecPath, sandboxExecProbe
	defer func() {
		sandboxExecPath = origPath
		sandboxExecProbe = origProbe
	}()
	sandboxExecPath = "/usr/bin/true" // exists; probe seam below keeps Status available
	sandboxExecProbe = func(_ context.Context, _ string) error { return nil }

	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	for _, d := range []string{ws, filepath.Join(base, "rt", "home"), filepath.Join(base, "rt", "tmp")} {
		if mkErr := os.MkdirAll(d, 0o750); mkErr != nil {
			t.Fatalf("mkdir %s: %v", d, mkErr)
		}
	}

	svc := New(nil, base)
	pc, err := svc.Prepare(context.Background(), Request{Workspace: ws},
		CommandSpec{Path: "/bin/sh", Args: []string{"-i"}, Dir: ws, Env: []string{"A=B", "NOCX_SANDBOX=filesystem"}})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	exe, exeErr := os.Executable()
	if exeErr != nil {
		t.Fatalf("executable: %v", exeErr)
	}
	cmd := pc.Cmd
	if cmd.Path != "/usr/bin/true" {
		t.Errorf("Cmd.Path = %q, want seam path", cmd.Path)
	}
	// [<sandbox-exec> -p <profile> <exe> __sandbox-seatbelt-exec <fd> /bin/sh -i]
	if len(cmd.Args) < 8 {
		t.Fatalf("Cmd.Args = %v, want sandbox-exec wrapper shape", cmd.Args)
	}
	if cmd.Args[1] != "-p" {
		t.Errorf("Args[1] = %q, want -p", cmd.Args[1])
	}
	if !strings.Contains(cmd.Args[2], "(deny default)") {
		t.Error("rendered profile must start with deny default")
	}
	if cmd.Args[3] != exe {
		t.Errorf("Args[3] = %q, want shim executable %q", cmd.Args[3], exe)
	}
	if cmd.Args[4] != seatbeltHelperArg {
		t.Errorf("Args[4] = %q, want %q", cmd.Args[4], seatbeltHelperArg)
	}
	if cmd.Args[5] != "3" {
		t.Errorf("Args[5] = %q, want status fd 3 (no inherited descriptors)", cmd.Args[5])
	}
	if cmd.Args[6] != "/bin/sh" || cmd.Args[7] != "-i" {
		t.Errorf("Args[6:8] = %v, want [/bin/sh -i]", cmd.Args[6:8])
	}
	if len(cmd.ExtraFiles) != 1 {
		t.Fatalf("ExtraFiles = %d, want exactly the status pipe write end", len(cmd.ExtraFiles))
	}
	if pc.waitReady == nil {
		t.Fatal("macOS Prepare must expose a readiness handshake")
	}
	if cmd.Dir != ws {
		t.Errorf("Cmd.Dir = %q, want %q", cmd.Dir, ws)
	}
	if !strings.Contains(strings.Join(cmd.Env, "\n"), "A=B") || !strings.Contains(strings.Join(cmd.Env, "\n"), "NOCX_SANDBOX=filesystem") {
		t.Errorf("Cmd.Env missing expected vars: %v", cmd.Env)
	}

	// The shim runs under the profile, so its own executable must be readable.
	shimCanon, shimErr := filepath.EvalSymlinks(exe)
	if shimErr != nil {
		t.Fatalf("EvalSymlinks(shim): %v", shimErr)
	}
	foundShim := false
	for _, f := range pc.Policy.ReadOnlyFiles {
		if f == shimCanon {
			foundShim = true
		}
	}
	if !foundShim {
		t.Errorf("ReadOnlyFiles = %v, want shim %q", pc.Policy.ReadOnlyFiles, shimCanon)
	}

	// The per-session runtime tree exists under the cache dir until Close.
	entries, rErr := os.ReadDir(filepath.Join(base, "sandbox-sessions"))
	if rErr != nil || len(entries) != 1 {
		t.Fatalf("sandbox-sessions entries = %v (%v), want exactly one runtime tree", entries, rErr)
	}
	pc.Close()
	pc.Close() // idempotent
	if entries, rErr := os.ReadDir(filepath.Join(base, "sandbox-sessions")); rErr == nil && len(entries) != 0 {
		t.Errorf("runtime tree not removed after Close: %v", entries)
	}
}

// TestDarwinServicePrepare_FailClosed covers the typed failure paths.
func TestDarwinServicePrepare_FailClosed(t *testing.T) {
	origPath, origProbe := sandboxExecPath, sandboxExecProbe
	defer func() {
		sandboxExecPath = origPath
		sandboxExecProbe = origProbe
	}()

	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	if mkErr := os.MkdirAll(ws, 0o750); mkErr != nil {
		t.Fatalf("mkdir: %v", mkErr)
	}
	spec := CommandSpec{Path: "/bin/sh", Args: []string{"-i"}, Dir: ws}

	t.Run("backend unavailable is StatusError", func(t *testing.T) {
		sandboxExecPath = "/nonexistent/sandbox-exec"
		svc := New(nil, base)
		var se *StatusError
		_, err := svc.Prepare(context.Background(), Request{Workspace: ws}, spec)
		if !errors.As(err, &se) {
			t.Fatalf("err = %v, want StatusError", err)
		}
	})

	t.Run("invalid workspace is ValidationError and cleans up", func(t *testing.T) {
		sandboxExecPath = "/usr/bin/true"
		sandboxExecProbe = func(_ context.Context, _ string) error { return nil }
		svc := New(nil, base)
		_, err := svc.Prepare(context.Background(), Request{Workspace: "relative/ws"}, spec)
		if !errors.Is(err, ErrInvalidPermissions) {
			t.Fatalf("err = %v, want ErrInvalidPermissions", err)
		}
		if entries, rErr := os.ReadDir(filepath.Join(base, "sandbox-sessions")); rErr == nil && len(entries) != 0 {
			t.Errorf("runtime tree leaked after validation failure: %v", entries)
		}
	})
}

// TestDarwinServicePrepare_ShimUnderWritableRejected verifies that when the
// shim executable lives under a writable root, Prepare fails with a
// SetupError before rendering. The shim is routed through
// addTrustedExecutables which rejects any executable under a user-writable
// root; no policy mutation occurs after the final validation.
func TestDarwinServicePrepare_ShimUnderWritableRejected(t *testing.T) {
	origPath, origProbe := sandboxExecPath, sandboxExecProbe
	defer func() {
		sandboxExecPath = origPath
		sandboxExecProbe = origProbe
	}()
	sandboxExecPath = "/usr/bin/true"
	sandboxExecProbe = func(_ context.Context, _ string) error { return nil }

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	exeDir := filepath.Dir(exe)

	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	if mkErr := os.MkdirAll(ws, 0o750); mkErr != nil {
		t.Fatalf("mkdir: %v", mkErr)
	}

	svc := New(nil, base)
	_, err = svc.Prepare(context.Background(), Request{
		Workspace:   ws,
		AddWritable: []string{exeDir},
	}, CommandSpec{Path: "/bin/sh", Args: []string{"-i"}, Dir: ws})
	if err == nil {
		t.Fatal("expected SetupError when shim is under a writable root")
	}
	var se *SetupError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v (%T), want *SetupError", err, err)
	}
	if !strings.Contains(err.Error(), "trusted executable conflicts with a writable root") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestDarwinServicePrepare_ShimInRenderedProfile verifies that the shim
// executable appears as a literal file-read clause in the rendered Seatbelt
// profile. The shim is added through addTrustedExecutables, which appends it
// to ReadOnlyFiles; renderProfile emits a literal clause for each.
func TestDarwinServicePrepare_ShimInRenderedProfile(t *testing.T) {
	origPath, origProbe := sandboxExecPath, sandboxExecProbe
	defer func() {
		sandboxExecPath = origPath
		sandboxExecProbe = origProbe
	}()
	sandboxExecPath = "/usr/bin/true"
	sandboxExecProbe = func(_ context.Context, _ string) error { return nil }

	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	for _, d := range []string{ws, filepath.Join(base, "rt", "home"), filepath.Join(base, "rt", "tmp")} {
		if mkErr := os.MkdirAll(d, 0o750); mkErr != nil {
			t.Fatalf("mkdir %s: %v", d, mkErr)
		}
	}

	svc := New(nil, base)
	pc, err := svc.Prepare(context.Background(), Request{Workspace: ws},
		CommandSpec{Path: "/bin/sh", Args: []string{"-i"}, Dir: ws})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer pc.Close()
	// The profile must contain a literal file-read* clause for the shim.
	profile := pc.Cmd.Args[2] // rendered SBPL profile

	exe, exeErr := os.Executable()
	if exeErr != nil {
		t.Fatalf("executable: %v", exeErr)
	}
	shimCanon, shimErr := filepath.EvalSymlinks(exe)
	if shimErr != nil {
		t.Fatalf("EvalSymlinks(shim): %v", shimErr)
	}

	wantClause := "(allow file-read* (literal \"" + shimCanon + "\"))"
	if !strings.Contains(profile, wantClause) {
		t.Errorf("rendered profile missing shim literal clause:\nwant: %s\nprofile: %s", wantClause, profile)
	}

	// The shim must also be in the Policy metadata.
	foundShim := false
	for _, f := range pc.Policy.ReadOnlyFiles {
		if f == shimCanon {
			foundShim = true
		}
	}
	if !foundShim {
		t.Errorf("Policy.ReadOnlyFiles = %v, want shim %q", pc.Policy.ReadOnlyFiles, shimCanon)
	}
}
