package deploy_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelperDependencyGraphHasNoEmbeddedArtifacts(t *testing.T) {
	for _, dir := range helperDependencyDirs(t) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read dependency directory %q: %v", dir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			file := filepath.Join(dir, entry.Name())
			data, err := os.ReadFile(file) // #nosec G304 -- file is joined from go list output and os.ReadDir entries
			if err != nil {
				t.Fatalf("read dependency source %q: %v", file, err)
			}
			if embedsHelperArtifacts(file, string(data)) {
				t.Fatalf("helper dependency graph reaches embedded artifacts: %s", file)
			}
		}
	}
}

// forbiddenSSHPackages are the packages the DEPLOYED helper must never reach.
// golang.org/x/crypto/ssh and its agent/knownhosts extensions are the client
// stack, github.com/pkg/sftp is the client half of the file transport, and
// internal/ssh is this repository's client over both. Every one of them
// belongs to the coordinator and to the helper that runs on a machine we
// control; none of them may be inside the artifact written to somebody else's
// host.
var forbiddenSSHPackages = []string{
	"golang.org/x/crypto/ssh",
	"golang.org/x/crypto/ssh/agent",
	"golang.org/x/crypto/ssh/knownhosts",
	"github.com/pkg/sftp",
	"github.com/shady2k/nocx/internal/ssh",
}

// TestDeployedHelperDependencyGraphHasNoSSHClient is half (a) of the pair: the
// UNTAGGED build — which is what `make helpers` produces and what is deployed
// — reaches no ssh client at all. It is the assertion the whole build-tag
// design exists to make mechanical: the tag nocx_local_ssh is absent by
// default, and a forgotten flag must fail here rather than ship an ssh client
// to a host we do not own.
//
// It reads `go list -deps` rather than the sources: a blank import, a build
// constraint that does not hold, and a dependency of a dependency are all the
// same question to the compiler, and only one of them to a grep.
func TestDeployedHelperDependencyGraphHasNoSSHClient(t *testing.T) {
	deps := helperDependencies(t)
	for _, pkg := range forbiddenSSHPackages {
		if deps[pkg] {
			t.Fatalf("the deployed helper's dependency graph reaches %s: the artifact written to a host we do not own must link no ssh client (build tag nocx_local_ssh must stay absent)", pkg)
		}
	}
}

// TestLocalHelperDependencyGraphReachesSSHClient is half (b): the SAME command
// built with the tag does reach the client stack — x/crypto/ssh directly and
// internal/ssh on top of it. Without this, the tag could stop linking anything
// and half (a) would still pass, which is the state that matters most: a local
// helper built without the client refuses every ssh pane while announcing
// nothing wrong.
//
// Both halves are `go list -deps`, so neither is a source grep that could pass
// while the build does something else.
func TestLocalHelperDependencyGraphReachesSSHClient(t *testing.T) {
	deps := helperDependencies(t, "nocx_local_ssh")
	for _, pkg := range []string{"golang.org/x/crypto/ssh", "github.com/shady2k/nocx/internal/ssh"} {
		if !deps[pkg] {
			t.Fatalf("a helper built with nocx_local_ssh does not reach %s: the local artifact would link no ssh client and refuse every ssh pane", pkg)
		}
	}
}

// helperDependencies lists the packages `go list -deps ./cmd/nocx-helper`
// reaches under the given build tags.
//
// The command is the real one, run in the module root, not a parse of the
// module graph: it is the same question the compiler answers.
func helperDependencies(t *testing.T, tags ...string) map[string]bool {
	t.Helper()
	root := moduleRoot(t)
	args := []string{"list", "-deps"}
	if len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}
	args = append(args, "./cmd/nocx-helper")
	cmd := exec.Command("go", args...) // #nosec G204 — "go" and every argument are fixed literals assembled here
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list helper dependencies: %v\n%s", err, output)
	}

	deps := make(map[string]bool)
	for _, pkg := range strings.Fields(string(output)) {
		deps[pkg] = true
	}
	return deps
}

func helperDependencyDirs(t *testing.T) []string {
	t.Helper()
	root := moduleRoot(t)
	cmd := exec.Command("go", "list", "-deps", "-f", "{{.Dir}}", "./cmd/nocx-helper") // #nosec G204 — "go" and all arguments are fixed test literals
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list helper dependencies: %v\n%s", err, output)
	}

	modulePrefix := root + string(filepath.Separator)
	var dirs []string
	for _, dir := range strings.Fields(string(output)) {
		if strings.HasPrefix(dir, modulePrefix) {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

func embedsHelperArtifacts(file, source string) bool {
	if !hasEmbedDirective(source) {
		return false
	}
	slashPath := filepath.ToSlash(file)
	return strings.Contains(slashPath, "/internal/helper/deploy/artifacts/") ||
		strings.Contains(source, "all:artifacts") ||
		strings.Contains(source, "nocx-helper-")
}

func hasEmbedDirective(source string) bool {
	for _, line := range strings.Split(source, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//go:embed ") {
			return true
		}
	}
	return false
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("go", "env", "GOMOD") // #nosec G204 — "go" and all arguments are fixed test literals
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	gomod := strings.TrimSpace(string(output))
	if gomod == "" || gomod == "/dev/null" {
		t.Fatalf("go env GOMOD returned %q", gomod)
	}
	return filepath.Dir(gomod)
}
