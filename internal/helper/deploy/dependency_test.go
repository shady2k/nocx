package deploy_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelperDependencyGraphHasNoEmbeddedArtifacts(t *testing.T) {
	root := moduleRoot(t)
	cmd := exec.Command("go", "list", "-deps", "-f", "{{.Dir}}", "./cmd/nocx-helper") // #nosec G204 — "go" and all arguments are fixed test literals
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list helper dependencies: %v\n%s", err, output)
	}

	modulePrefix := root + string(filepath.Separator)
	for _, dir := range strings.Fields(string(output)) {
		if !strings.HasPrefix(dir, modulePrefix) {
			continue
		}
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
