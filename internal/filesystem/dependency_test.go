package filesystem_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// forbiddenPackages are the packages a filesystem provider must never reach.
//
// The rule is not stylistic. internal/filesystem serves the Files panel for a
// binding whose provider is local, and the far helper reuses those providers
// inside an artifact written to a host we do not own — so the graph they pull
// in is the graph that artifact links (design C30). The connection stack is
// the coordinator's: internal/session owns the lifecycle and reaches
// internal/ssh and internal/pty; internal/helper/deploy writes and runs the
// helper; internal/hostconn and internal/storage sit on the same side of the
// boundary. golang.org/x/crypto/ssh and github.com/pkg/sftp are the client
// halves underneath.
//
// internal/session is on the list for a narrower reason than the rest, and it
// is the one this test was written for: internal/filesystem needs a session's
// IDENTITY and nothing else, so importing the package that owns the lifecycle
// reached all of the above through one type. sessionid.ID is that identity on
// its own.
var forbiddenPackages = []string{
	"github.com/shady2k/nocx/internal/session",
	"github.com/shady2k/nocx/internal/ssh",
	"github.com/shady2k/nocx/internal/pty",
	"github.com/shady2k/nocx/internal/storage",
	"github.com/shady2k/nocx/internal/helper/deploy",
	"github.com/shady2k/nocx/internal/hostconn",
	"golang.org/x/crypto/ssh",
	"github.com/pkg/sftp",
}

// filesystemPackages are the packages the rule covers: the provider contract
// and the local provider, which is the one the helper reuses.
var filesystemPackages = []string{"./internal/filesystem", "./internal/filesystem/local"}

// TestFilesystemDependencyGraphReachesNoConnectionStack asserts the rule with
// `go list -deps` rather than by reading sources: a blank import, a build
// constraint that does not hold and a dependency of a dependency are one
// question to the compiler and three different ones to a grep.
//
// Four builds are checked, and the GOOS axis is the half this test was missing
// (review of nocx-oalo1.12.1, ALX-1761). The two TAG variants are the deployed
// helper and the local one; the two PLATFORMS are the two this product ships
// on, because a provider is per-platform: mounts_darwin.go and its imports are
// invisible to a Linux run, so a Darwin-only reach into the connection stack
// passed the gate while being the artifact that reaches somebody else's host.
// CGO_ENABLED=0 pins the analysis to what the shipped helper can be built
// with — the release cross-compile has no macOS SDK on Linux — and keeps the
// answer the same on either host, which is what makes the axis meaningful on
// CI rather than a statement about the runner.
func TestFilesystemDependencyGraphReachesNoConnectionStack(t *testing.T) {
	platforms := []struct{ name, goos string }{
		{name: "linux", goos: "linux"},
		{name: "darwin", goos: "darwin"},
	}
	tagsets := []struct {
		name string
		tags []string
	}{
		{name: "untagged"},
		{name: "nocx_local_ssh", tags: []string{"nocx_local_ssh"}},
	}
	for _, platform := range platforms {
		for _, tc := range tagsets {
			t.Run(platform.name+"/"+tc.name, func(t *testing.T) {
				for _, pkg := range filesystemPackages {
					deps := packageDependencies(t, pkg, platform.goos, tc.tags...)
					for _, forbidden := range forbiddenPackages {
						if deps[forbidden] {
							t.Errorf("%s on %s reaches %s: a filesystem provider is reused by the far helper and must link no connection stack", pkg, platform.goos, forbidden)
						}
					}
				}
			})
		}
	}
}

// packageDependencies lists the packages `go list -deps <pkg>` reaches under
// the given GOOS and build tags. GOOS is stated on every call rather than
// inherited, so "which platform did this run measure" is visible in the test
// name that failed; CGO_ENABLED=0 matches the release build and keeps the
// cross-platform answer independent of the host's toolchain.
func packageDependencies(t *testing.T, pkg, goos string, tags ...string) map[string]bool {
	t.Helper()
	args := []string{"list", "-deps"}
	if len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}
	args = append(args, pkg)
	cmd := exec.Command("go", args...) // #nosec G204 — "go" and every argument are literals assembled here
	cmd.Dir = moduleRoot(t)
	cmd.Env = append(os.Environ(), "GOOS="+goos, "CGO_ENABLED=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps %s (GOOS=%s): %v\n%s", pkg, goos, err, output)
	}
	deps := make(map[string]bool)
	for _, dep := range strings.Fields(string(output)) {
		deps[dep] = true
	}
	return deps
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("go", "env", "GOMOD") // #nosec G204 — a fixed literal
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
