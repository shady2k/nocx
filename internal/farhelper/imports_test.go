package farhelper_test

// C17 and §5.10's import table, in the shape internal/helper/deploy's
// dependency test uses: `go list` answers what the COMPILER puts in the
// closure, so a transitive reach, a build constraint and a blank import are
// all the same question. The far helper on a connection is part of the
// coordinator, and it must not drag the ssh client or the transport into its
// tests or its package.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const modulePath = "github.com/shady2k/nocx"

// allowedFarHelperImports is §5.10's row for internal/farhelper, spelled as
// package paths. Anything else this package reaches directly is a boundary
// broken, not a convenience.
var allowedFarHelperImports = map[string]bool{
	modulePath + "/internal/hostconn":       true,
	modulePath + "/internal/helper/client":  true,
	modulePath + "/internal/helper/proto":   true,
	modulePath + "/internal/hostmode":       true,
	modulePath + "/internal/helper/consent": true,
	modulePath + "/internal/log":            true,
}

// forbiddenFarHelperDependencies are the edges C17 and C20 refuse through the
// transitive graph as well as directly.
var forbiddenFarHelperDependencies = []string{
	"github.com/shady2k/nocx/internal/ssh",
	"github.com/shady2k/nocx/internal/transport",
	"github.com/shady2k/nocx/internal/panedriver",
	"github.com/shady2k/nocx/internal/app",
	"golang.org/x/crypto/ssh",
	"github.com/pkg/sftp",
}

func TestTheFarHelperImportTableHolds(t *testing.T) {
	for _, tags := range [][]string{nil, {"nocx_local_ssh"}} {
		direct := goList(t, tags, "-f", "{{join .Imports \"\\n\"}}", "./internal/farhelper")
		seen := 0
		for _, pkg := range strings.Fields(direct) {
			if !strings.HasPrefix(pkg, modulePath+"/") {
				continue // the standard library and module-less imports
			}
			if !allowedFarHelperImports[pkg] {
				t.Errorf("internal/farhelper imports %s, which §5.10 does not allow (tags %v)", pkg, tags)
			}
			seen++
		}
		if seen == 0 {
			t.Fatalf("no repository imports seen for internal/farhelper (tags %v): the allowed list would pass on a package that imports nothing", tags)
		}
	}
}

func TestTheFarHelperGraphReachesNoSSHClient(t *testing.T) {
	for _, tags := range [][]string{nil, {"nocx_local_ssh"}} {
		deps := goList(t, tags, "-deps", "./internal/farhelper")
		present := make(map[string]bool)
		for _, pkg := range strings.Fields(deps) {
			present[pkg] = true
		}
		// The package itself must be a line of its own answer: a C17 test that
		// stopped naming farhelper would pass while covering nothing here.
		if !present[modulePath+"/internal/farhelper"] {
			t.Fatalf("the dependency graph does not name internal/farhelper (tags %v)", tags)
		}
		for _, forbidden := range forbiddenFarHelperDependencies {
			if present[forbidden] {
				t.Errorf("internal/farhelper reaches %s (tags %v): the far helper's decision is the coordinator's, and the ssh client, the transport and the composition root are not its dependencies", forbidden, tags)
			}
		}
	}
}

func goList(t *testing.T, tags []string, args ...string) string {
	t.Helper()
	root := moduleRoot(t)
	listArgs := []string{"list"}
	if len(tags) > 0 {
		listArgs = append(listArgs, "-tags", strings.Join(tags, ","))
	}
	listArgs = append(listArgs, args...)
	cmd := exec.Command("go", listArgs...) // #nosec G204 — "go", fixed flags and a fixed package
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(listArgs, " "), err, out)
	}
	return string(out)
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("go", "env", "GOMOD") // #nosec G204 — fixed compiler and argument
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull {
		t.Fatalf("go env GOMOD returned %q", gomod)
	}
	return filepath.Dir(gomod)
}
