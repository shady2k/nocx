package portsprobe_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// forbiddenPackages are the packages the port-sample vocabulary and its
// parsers must never reach.
//
// The rule is a LINK, not a style (design §5.10, C30). internal/portsprobe is
// what the far helper cross-compiles onto a host we do not own, and whatever
// that artifact links it carries: the connection stack (internal/ssh and the
// client half underneath it, golang.org/x/crypto/ssh), the helper's own
// deployment machinery (internal/helper/...), the coordinator's connection
// registry (internal/hostconn, internal/farhelper) and the probe vocabulary
// the helper wire speaks (internal/remoteprobe) are all the coordinator's
// side of the boundary. A parser that named one of them would put the
// coordinator's stack on somebody else's machine, one import at a time.
var forbiddenPackages = []string{
	"github.com/shady2k/nocx/internal/ssh",
	"golang.org/x/crypto/ssh",
	"github.com/pkg/sftp",
	"github.com/shady2k/nocx/internal/helper",
	"github.com/shady2k/nocx/internal/hostconn",
	"github.com/shady2k/nocx/internal/farhelper",
	"github.com/shady2k/nocx/internal/remoteprobe",
}

// prefixForbidden are the rules stated as a family rather than a package: any
// subpackage of the helper's tree, and any package of the helper's tree that
// appears later. A list that only names today's subpackages is a rule that a
// new nocx-oalo1.12.x directory silently leaves.
var prefixForbidden = []string{
	"github.com/shady2k/nocx/internal/helper/",
}

// TestPortsProbeDependencyGraphReachesNoConnectionStack asserts the rule with
// `go list -deps` rather than by reading sources: a blank import, a build
// constraint that does not hold and a dependency of a dependency are one
// question to the compiler and three different ones to a grep.
//
// Four builds are checked, and the GOOS axis is the half that matters most
// here: the far helper's artifact is cross-compiled for the host it is
// deployed to, so a darwin-only importer of the connection stack would pass a
// Linux run while being the artifact that ships. CGO_ENABLED=0 is the release
// build's own setting, which also makes the answer the same on either host.
func TestPortsProbeDependencyGraphReachesNoConnectionStack(t *testing.T) {
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
				deps := packageDependencies(t, "./internal/portsprobe", platform.goos, tc.tags...)
				for _, forbidden := range forbiddenPackages {
					if deps[forbidden] {
						t.Errorf("./internal/portsprobe on %s reaches %s: the far helper links this package, so it must link no connection stack", platform.goos, forbidden)
					}
				}
				for dep := range deps {
					for _, prefix := range prefixForbidden {
						if strings.HasPrefix(dep, prefix) {
							t.Errorf("./internal/portsprobe on %s reaches %s: the far helper links this package, so it must link no part of %s", platform.goos, dep, prefix)
						}
					}
				}
			})
		}
	}
}

// TestNativePortsDependencyGraphReachesNoDiscovery asserts the other half of
// the same move: nativeports reads this machine's kernel and returns
// portsprobe's types, so the local path and the far helper read one
// vocabulary. It imported internal/discovery — the cadence, the detector and
// the exec seam — only to name the types, and that made "the local machine's
// listener table" depend on a package that owns a scheduler and an SSH
// connector. Nothing in the module needs a transport to read /proc, and the
// test is a `go list` rather than a reading of imports for the reason above.
func TestNativePortsDependencyGraphReachesNoDiscovery(t *testing.T) {
	platforms := []struct{ name, goos string }{
		{name: "linux", goos: "linux"},
		{name: "darwin", goos: "darwin"},
	}
	for _, platform := range platforms {
		t.Run(platform.name, func(t *testing.T) {
			deps := packageDependencies(t, "./internal/nativeports", platform.goos)
			if deps["github.com/shady2k/nocx/internal/discovery"] {
				t.Errorf("./internal/nativeports on %s reaches internal/discovery: a machine's kernel read needs no cadence, detector or exec seam", platform.goos)
			}
		})
	}
}

// packageDependencies lists the packages `go list -deps <pkg>` reaches under
// the given GOOS and build tags. GOOS is stated on every call rather than
// inherited, so "which platform did this run measure" is visible in the test
// name that failed.
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
