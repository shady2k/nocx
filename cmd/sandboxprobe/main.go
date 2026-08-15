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
	envWorkspace = "NOCX_SB_WORKSPACE"
	envSentinel  = "NOCX_SB_SENTINEL"
	envPreHard   = "NOCX_SB_PREHARD"
	envShell     = "NOCX_SB_SHELL"
	helperPrefix = "NOCX_SANDBOX_HELPER_"
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
	defer func() { _ = os.RemoveAll(base) }()

	workspace := filepath.Join(base, "workspace")
	cacheDir := filepath.Join(base, "cache")
	for _, dir := range []string{workspace, cacheDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create fixture directory: %w", err)
		}
	}
	sentinel := filepath.Join(base, "sentinel")
	if err := os.WriteFile(sentinel, []byte("top secret"), 0o600); err != nil {
		return fmt.Errorf("create sentinel: %w", err)
	}
	preHard := filepath.Join(workspace, "pre-hard-link")
	if err := os.Link(sentinel, preHard); err != nil {
		return fmt.Errorf("create documented hard-link fixture: %w", err)
	}

	// #nosec G204 -- executable is canonicalized, regular, executable, and supplied explicitly by the release gate.
	cmd := exec.Command(artifact, sandbox.ArtifactSmokeArg, probe) //nolint:gosec
	cmd.Env = withEnv(os.Environ(), map[string]string{
		envWorkspace:                  workspace,
		envSentinel:                   sentinel,
		envPreHard:                    preHard,
		envShell:                      "/bin/sh",
		sandbox.ArtifactSmokeCacheEnv: cacheDir,
		helperPrefix + "LEAK":         "must-be-stripped",
	})
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("artifact exited without a verified cage: %w", err)
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
	if workspace == "" || sentinel == "" || preHard == "" || shell == "" || homeErr != nil || home == "" || tmp == "" {
		return errors.New("in-cage fixture environment is incomplete")
	}
	if err := validateChildFixture(workspace, sentinel, preHard, shell); err != nil {
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

func validateChildFixture(workspace, sentinel, preHard, shell string) error {
	if shell != "/bin/sh" || !filepath.IsAbs(workspace) || !filepath.IsAbs(sentinel) || !filepath.IsAbs(preHard) {
		return errors.New("in-cage fixture environment is invalid")
	}
	workspace = filepath.Clean(workspace)
	base := filepath.Dir(workspace)
	if filepath.Base(workspace) != "workspace" ||
		!strings.HasPrefix(filepath.Base(base), "nocx-sandbox-artifact-") ||
		filepath.Clean(sentinel) != filepath.Join(base, "sentinel") ||
		filepath.Clean(preHard) != filepath.Join(workspace, "pre-hard-link") {
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
