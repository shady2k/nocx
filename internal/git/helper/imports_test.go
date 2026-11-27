package helper_test

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// C17: a consumer reads the far-helper decision, never opens an SSH lane.
func TestGitHelperImportTable(t *testing.T) {
	const module = "github.com/shady2k/nocx/internal/"
	allowed := map[string]bool{"farhelper": true, "git": true, "git/hostsvc": true, "helper/client": true}
	for _, tags := range []string{"", "nocx_local_ssh"} {
		cmd := exec.Command("go", "list", "-json", "-tags", tags, ".") // #nosec G204 — fixed compiler and package; tags are fixed test cases
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go list: %v\n%s", err, out)
		}
		var pkg struct{ Imports []string }
		if err := json.Unmarshal(out, &pkg); err != nil {
			t.Fatal(err)
		}
		for _, dep := range pkg.Imports {
			if strings.HasPrefix(dep, module) && !allowed[strings.TrimPrefix(dep, module)] {
				t.Errorf("Git consumer imports %s outside its C17 table (tags %q)", dep, tags)
			}
		}
	}
}
