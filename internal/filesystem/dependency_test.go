package filesystem_test

import (
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
// Both builds are checked. The untagged one is what `make helpers` produces
// and what is deployed to somebody else's host; the nocx_local_ssh one is the
// local helper, where the client stack exists — and still must not be reached
// from a filesystem provider, because the provider is the half the far helper
// links too.
func TestFilesystemDependencyGraphReachesNoConnectionStack(t *testing.T) {
	for _, tc := range []struct {
		name string
		tags []string
	}{
		{name: "untagged"},
		{name: "nocx_local_ssh", tags: []string{"nocx_local_ssh"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, pkg := range filesystemPackages {
				deps := packageDependencies(t, pkg, tc.tags...)
				for _, forbidden := range forbiddenPackages {
					if deps[forbidden] {
						t.Errorf("%s reaches %s: a filesystem provider is reused by the far helper and must link no connection stack", pkg, forbidden)
					}
				}
			}
		})
	}
}

// packageDependencies lists the packages `go list -deps <pkg>` reaches under
// the given build tags.
func packageDependencies(t *testing.T, pkg string, tags ...string) map[string]bool {
	t.Helper()
	args := []string{"list", "-deps"}
	if len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}
	args = append(args, pkg)
	cmd := exec.Command("go", args...) // #nosec G204 — "go" and every argument are literals assembled here
	cmd.Dir = moduleRoot(t)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps %s: %v\n%s", pkg, err, output)
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
