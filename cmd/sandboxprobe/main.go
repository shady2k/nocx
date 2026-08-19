// Command sandboxprobe verifies filesystem enforcement through a built nocx
// executable. The parent creates a disposable fixture and invokes the artifact;
// the artifact enters its real platform sandbox backend and launches this
// command again with --child inside the cage.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/shady2k/nocx/internal/sandbox"
)

const (
	envWorkspace              = "NOCX_SB_WORKSPACE"
	envSentinel               = "NOCX_SB_SENTINEL"
	envPreHard                = "NOCX_SB_PREHARD"
	envShell                  = "NOCX_SB_SHELL"
	helperPrefix              = "NOCX_SANDBOX_HELPER_"
	projectedReadOnlyRelative = ".config"
	projectedWritableRelative = ".local/state/tool"
	projectedNestedRWRelative = ".config/tool/state"
)

func main() {
	artifact := flag.String("artifact", "", "path to the built nocx executable")
	child := flag.Bool("child", false, "run the in-cage assertions")
	flag.Parse()

	var err error
	if *child {
		err = runChildProbe()
	} else {
		err = runArtifactProbe(*artifact)
	}
	if err != nil {
		slog.Error("sandbox artifact smoke failed", "error", err)
		os.Exit(1)
	}
	if !*child {
		slog.Info("sandbox artifact smoke passed", "artifact", filepath.Base(*artifact))
	}
}

func runArtifactProbe(artifactPath string) error {
	if artifactPath == "" {
		return errors.New("-artifact is required")
	}
	artifact, err := filepath.Abs(artifactPath)
	if err != nil {
		return fmt.Errorf("resolve artifact: %w", err)
	}
	artifact, err = filepath.EvalSymlinks(artifact)
	if err != nil {
		return fmt.Errorf("resolve artifact symlinks: %w", err)
	}
	info, err := os.Stat(artifact)
	if err != nil {
		return fmt.Errorf("stat artifact: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return errors.New("artifact is not an executable regular file")
	}

	probe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve probe executable: %w", err)
	}
	probe, err = filepath.EvalSymlinks(probe)
	if err != nil {
		return fmt.Errorf("resolve probe symlinks: %w", err)
	}

	base, err := os.MkdirTemp("", "nocx-sandbox-artifact-")
	if err != nil {
		return fmt.Errorf("create fixture: %w", err)
	}
	rawBase := base
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		_ = os.RemoveAll(rawBase)
		return fmt.Errorf("canonicalize fixture: %w", err)
	}
	defer func() { _ = os.RemoveAll(base) }()

	workspace := filepath.Join(base, "workspace")
	cacheDir := filepath.Join(base, "cache")
	hostHome := filepath.Join(base, "host-home")
	readOnlyRoot := filepath.Join(hostHome, projectedReadOnlyRelative)
	writableRoot := filepath.Join(hostHome, filepath.FromSlash(projectedWritableRelative))
	nestedWritable := filepath.Join(hostHome, filepath.FromSlash(projectedNestedRWRelative))
	for _, dir := range []string{workspace, cacheDir, hostHome, readOnlyRoot, writableRoot, nestedWritable} {
		if mkdirErr := os.MkdirAll(dir, 0o700); mkdirErr != nil {
			return fmt.Errorf("create fixture directory: %w", mkdirErr)
		}
	}
	sentinel := filepath.Join(base, "sentinel")
	if writeErr := os.WriteFile(sentinel, []byte("top secret"), 0o600); writeErr != nil {
		return fmt.Errorf("create sentinel: %w", writeErr)
	}
	if writeErr := os.WriteFile(filepath.Join(readOnlyRoot, "keep.txt"), []byte("read-only"), 0o600); writeErr != nil {
		return fmt.Errorf("create read-only projection fixture: %w", writeErr)
	}
	preHard := filepath.Join(workspace, "pre-hard-link")
	if linkErr := os.Link(sentinel, preHard); linkErr != nil {
		return fmt.Errorf("create documented hard-link fixture: %w", linkErr)
	}
	// #nosec G204 -- executable is canonicalized, regular, executable, and supplied explicitly by the release gate.
	cmd := exec.Command(artifact, sandbox.ArtifactSmokeArg, probe) //nolint:gosec
	cmd.Env = withEnv(os.Environ(), map[string]string{
		envWorkspace:                           workspace,
		envSentinel:                            sentinel,
		envPreHard:                             preHard,
		envShell:                               "/bin/sh",
		sandbox.ArtifactSmokeCacheEnv:          cacheDir,
		helperPrefix + "LEAK":                  "must-be-stripped",
		"HOME":                                 hostHome,
		sandbox.ArtifactSmokeReadOnlyEnv:       readOnlyRoot,
		sandbox.ArtifactSmokeWritableEnv:       writableRoot,
		sandbox.ArtifactSmokeNestedWritableEnv: nestedWritable,
	})
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if runErr := cmd.Run(); runErr != nil {
		return fmt.Errorf("artifact exited without a verified cage: %w", runErr)
	}
	// #nosec G304 -- fixture roots are constructed below the validated private base.
	projectedData, err := os.ReadFile(filepath.Join(writableRoot, "projected.txt"))
	if err != nil || string(projectedData) != "updated" {
		return fmt.Errorf("projected writable state did not persist: data=%q error=%v", projectedData, err)
	}
	// #nosec G304 -- fixture roots are constructed below the validated private base.
	nestedData, err := os.ReadFile(filepath.Join(nestedWritable, "nested.txt"))
	if err != nil || string(nestedData) != "nested writable" {
		return fmt.Errorf("nested projected writable state did not persist: data=%q error=%v", nestedData, err)
	}
	// #nosec G304 -- fixture roots are constructed below the validated private base.
	readOnlyData, err := os.ReadFile(filepath.Join(readOnlyRoot, "keep.txt"))
	if err != nil || string(readOnlyData) != "read-only" {
		return fmt.Errorf("projected read-only state changed: data=%q error=%v", readOnlyData, err)
	}
	return nil
}

func runChildProbe() error {
	workspace := os.Getenv(envWorkspace)
	sentinel := os.Getenv(envSentinel)
	preHard := os.Getenv(envPreHard)
	shell := os.Getenv(envShell)
	home, homeErr := os.UserHomeDir()
	tmp := os.TempDir()
	readOnlyRoot := os.Getenv(sandbox.ArtifactSmokeReadOnlyEnv)
	writableRoot := os.Getenv(sandbox.ArtifactSmokeWritableEnv)
	nestedWritable := os.Getenv(sandbox.ArtifactSmokeNestedWritableEnv)
	if workspace == "" || sentinel == "" || preHard == "" || shell == "" || readOnlyRoot == "" || writableRoot == "" || nestedWritable == "" || homeErr != nil || home == "" || tmp == "" {
		return errors.New("in-cage fixture environment is incomplete")
	}
	if err := validateChildFixture(workspace, sentinel, preHard, shell, readOnlyRoot, writableRoot, nestedWritable); err != nil {
		return err
	}

	file := filepath.Join(workspace, "a.txt")
	// #nosec G703 -- validated release fixture; successful write is the assertion.
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		return fmt.Errorf("create in workspace: %w", err)
	}
	// #nosec G703 -- validated release fixture; truncate is the assertion.
	if err := os.WriteFile(file, []byte("longer content"), 0o600); err != nil {
		return fmt.Errorf("truncate-rewrite in workspace: %w", err)
	}
	// #nosec G703 -- both paths are inside the validated release fixture.
	if err := os.Rename(file, filepath.Join(workspace, "b.txt")); err != nil {
		return fmt.Errorf("rename in workspace: %w", err)
	}
	if err := os.WriteFile(filepath.Join(home, "probe-home"), []byte("h"), 0o600); err != nil {
		return fmt.Errorf("create in isolated home: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "probe-tmp"), []byte("t"), 0o600); err != nil {
		return fmt.Errorf("create in isolated tmp: %w", err)
	}
	if _, err := os.ReadFile("/etc/hosts"); err != nil {
		return fmt.Errorf("read system root: %w", err)
	}
	projectedReadOnly := filepath.Join(home, filepath.FromSlash(projectedReadOnlyRelative))
	projectedWritable := filepath.Join(home, filepath.FromSlash(projectedWritableRelative))
	projectedNestedRW := filepath.Join(home, filepath.FromSlash(projectedNestedRWRelative))
	if os.Getenv("XDG_CONFIG_HOME") != projectedReadOnly || filepath.Join(os.Getenv("XDG_STATE_HOME"), "tool") != projectedWritable {
		return errors.New("sandbox HOME/XDG environment does not name projection forest")
	}
	projectedROFile := filepath.Join(projectedReadOnly, "keep.txt")
	// #nosec G304 -- projectedROFile is confined to the validated synthetic fixture.
	if data, err := os.ReadFile(projectedROFile); err != nil || string(data) != "read-only" { //nolint:gosec // validated synthetic fixture
		return fmt.Errorf("read through projected read-only root: data=%q error=%v", data, err)
	}
	if err := os.WriteFile(filepath.Join(projectedReadOnly, "projected-new.txt"), []byte("x"), 0o600); err == nil {
		return errors.New("create through projected read-only root succeeded")
	}
	if err := os.WriteFile(projectedROFile, []byte("mutated"), 0o600); err == nil {
		return errors.New("truncate through projected read-only root succeeded")
	}
	if err := os.Remove(projectedROFile); err == nil {
		return errors.New("remove through projected read-only root succeeded")
	}
	projectedRWFile := filepath.Join(projectedWritable, "projected.txt")
	if err := os.WriteFile(projectedRWFile, []byte("created"), 0o600); err != nil {
		return fmt.Errorf("create through projected writable root: %w", err)
	}
	if err := os.WriteFile(projectedRWFile, []byte("updated"), 0o600); err != nil {
		return fmt.Errorf("update through projected writable root: %w", err)
	}
	if err := os.WriteFile(filepath.Join(projectedNestedRW, "nested.txt"), []byte("nested writable"), 0o600); err != nil {
		return fmt.Errorf("write through projected RW child below RO ancestor: %w", err)
	}
	replaceProjection := func(target string) error {
		if err := os.Remove(projectedWritable); err != nil {
			return err
		}
		return os.Symlink(target, projectedWritable)
	}
	if err := replaceProjection(sentinel); err != nil {
		return fmt.Errorf("retarget projection outside grants: %w", err)
	}
	// #nosec G304 -- projectedWritable is confined to the validated synthetic fixture; denial is the assertion.
	if _, err := os.ReadFile(projectedWritable); err == nil { //nolint:gosec // native denial is the assertion
		return errors.New("retargeted projection read outside grants succeeded")
	}
	if err := replaceProjection(readOnlyRoot); err != nil {
		return fmt.Errorf("retarget projection to read-only root: %w", err)
	}
	if err := os.WriteFile(filepath.Join(projectedWritable, "keep.txt"), []byte("mutated"), 0o600); err == nil {
		return errors.New("retargeted projection widened read-only root")
	}
	if err := replaceProjection(writableRoot); err != nil {
		return fmt.Errorf("retarget projection to writable root: %w", err)
	}
	if err := os.WriteFile(filepath.Join(projectedWritable, "retargeted.txt"), []byte("writable"), 0o600); err != nil {
		return fmt.Errorf("retargeted projection lost writable class: %w", err)
	}

	// #nosec G204 G702 -- shell is fixed to /bin/sh and the command text is constant.
	pipeline := exec.Command(shell, "-c", "printf sandbox | cat") //nolint:gosec
	if output, err := pipeline.Output(); err != nil || string(output) != "sandbox" {
		return fmt.Errorf("shell pipeline: output=%q error=%v", output, err)
	}
	// #nosec G204 G702 -- shell is fixed to /bin/sh and the command text is constant.
	redirect := exec.Command(shell, "-c", "printf sandbox >/dev/null") //nolint:gosec
	if output, err := redirect.CombinedOutput(); err != nil {
		return fmt.Errorf("shell redirect: output=%q error=%w", output, err)
	}

	// #nosec G304 G703 -- sentinel is fixed inside the validated fixture; denial is the assertion.
	if _, err := os.ReadFile(sentinel); err == nil { //nolint:gosec
		return errors.New("read of sentinel outside roots succeeded")
	}
	// #nosec G703 -- sentinel is fixed inside the validated fixture; denial is the assertion.
	if err := os.WriteFile(sentinel, []byte("x"), 0o600); err == nil {
		return errors.New("write to sentinel outside roots succeeded")
	}

	symlink := filepath.Join(workspace, "escape")
	if err := os.Symlink(sentinel, symlink); err != nil {
		return fmt.Errorf("create in-workspace symlink: %w", err)
	}
	// #nosec G304 G703 -- symlink is fixed inside the validated fixture; denial is the assertion.
	if _, err := os.ReadFile(symlink); err == nil { //nolint:gosec
		return errors.New("symlink escape read succeeded")
	}
	if err := os.Link(sentinel, filepath.Join(workspace, "hard")); err == nil {
		return errors.New("hard link to outside sentinel succeeded")
	}
	// #nosec G703 -- both paths are fixed inside the validated release fixture.
	if err := os.Rename(sentinel, filepath.Join(workspace, "moved")); err == nil {
		return errors.New("rename of outside sentinel succeeded")
	}

	// #nosec G204 G702 -- fixed shell/program; sentinel is a positional argument from the validated fixture.
	subprocess := exec.Command(shell, "-c", "cat \"$1\"", "sandbox-probe", sentinel) //nolint:gosec
	if output, err := subprocess.CombinedOutput(); err == nil || strings.Contains(string(output), "top secret") {
		return fmt.Errorf("subprocess exposed sentinel: output=%q error=%v", output, err)
	}
	// #nosec G304 G703 -- preHard is fixed inside the validated fixture; reachability is the assertion.
	if _, err := os.ReadFile(preHard); err != nil { //nolint:gosec
		return fmt.Errorf("pre-existing hard link is not reachable: %w", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("loopback listen: %w", err)
	}
	defer func() { _ = listener.Close() }()
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		return fmt.Errorf("loopback connect: %w", err)
	}
	_ = conn.Close()

	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, helperPrefix) {
			return errors.New("helper-only environment reached the sandboxed command")
		}
	}
	return nil
}

func validateChildFixture(workspace, sentinel, preHard, shell, readOnlyRoot, writableRoot, nestedWritable string) error {
	if shell != "/bin/sh" || !filepath.IsAbs(workspace) || !filepath.IsAbs(sentinel) || !filepath.IsAbs(preHard) || !filepath.IsAbs(readOnlyRoot) || !filepath.IsAbs(writableRoot) || !filepath.IsAbs(nestedWritable) {
		return errors.New("in-cage fixture environment is invalid")
	}
	workspace = filepath.Clean(workspace)
	base := filepath.Dir(workspace)
	if filepath.Base(workspace) != "workspace" ||
		!strings.HasPrefix(filepath.Base(base), "nocx-sandbox-artifact-") ||
		filepath.Clean(sentinel) != filepath.Join(base, "sentinel") ||
		filepath.Clean(preHard) != filepath.Join(workspace, "pre-hard-link") ||
		filepath.Clean(readOnlyRoot) != filepath.Join(base, "host-home", projectedReadOnlyRelative) ||
		filepath.Clean(writableRoot) != filepath.Join(base, "host-home", filepath.FromSlash(projectedWritableRelative)) ||
		filepath.Clean(nestedWritable) != filepath.Join(base, "host-home", filepath.FromSlash(projectedNestedRWRelative)) {
		return errors.New("in-cage fixture paths are outside the release fixture")
	}
	canonBase, err := filepath.EvalSymlinks(base)
	if err != nil || canonBase != base {
		return errors.New("in-cage fixture root is not canonical")
	}
	info, err := os.Stat(base)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("in-cage fixture root is not private")
	}
	return nil
}

func withEnv(base []string, values map[string]string) []string {
	out := make([]string, 0, len(base)+len(values))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if ok {
			if _, replace := values[key]; replace {
				continue
			}
		}
		out = append(out, entry)
	}
	for key, value := range values {
		out = append(out, key+"="+value)
	}
	return out
}
