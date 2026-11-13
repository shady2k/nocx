package assistant

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheAdHocDispatchEntryIsGone is this task's structural assertion
// (nocx-a62cl.2.4). Both callers run through the authority pipeline, so the
// old parameterised entry and the transform and gate types only it used are
// deleted rather than renamed around. Nothing a person sees changes, so what
// the stage can assert is the package's own source; the behaviour that must
// not change is asserted by every other test in this package.
//
// The forbidden names are assembled from fragments so this file does not
// itself contain the strings the task's grep looks for.
func TestTheAdHocDispatchEntryIsGone(t *testing.T) {
	forbidden := []string{
		"dispatch" + "Gate",
		"dispatch" + "Transform",
		"func (d *dispatchOperation) " + "dispatch(",
	}
	walkErr := filepath.WalkDir("..", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		source, err := os.ReadFile(path) //nolint:gosec // test reading this repository's own sources
		if err != nil {
			return err
		}
		for _, name := range forbidden {
			if strings.Contains(string(source), name) {
				t.Errorf("%s still contains %q: the ad-hoc dispatch entry and the types only it used are deleted, not renamed around", path, name)
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk internal/: %v", walkErr)
	}
}
