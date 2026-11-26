package helperfs_test

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// §5.10: helperfs consumes the registry decision; it owns neither an SSH
// connection nor a dependency on the app/control-plane composition root.
func TestHelperFilesystemImportTable(t *testing.T) {
	const internal = "github.com/shady2k/nocx/internal/"
	allowed := map[string]bool{
		"farhelper":     true,
		"filesystem":    true,
		"helper/client": true,
		"helper/proto":  true,
		"transfer":      true,
	}
	forbidden := []string{
		internal + "ssh",
		internal + "transport",
		internal + "app",
		"golang.org/x/crypto/ssh",
		"github.com/pkg/sftp",
	}
	for _, tags := range []string{"", "nocx_local_ssh"} {
		cmd := exec.Command("go", "list", "-json", "-tags", tags, ".") // #nosec G204 -- fixed compiler, package, and tags.
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go list (tags %q): %v\n%s", tags, err, out)
		}
		var pkg struct{ Imports []string }
		if decodeErr := json.Unmarshal(out, &pkg); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		for _, dep := range pkg.Imports {
			if strings.HasPrefix(dep, internal) && !allowed[strings.TrimPrefix(dep, internal)] {
				t.Errorf("direct import %s outside §5.10 (tags %q)", dep, tags)
			}
		}
		cmd = exec.Command("go", "list", "-deps", "-tags", tags, ".") // #nosec G204 -- fixed compiler, package, and tags.
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go list -deps (tags %q): %v\n%s", tags, err, out)
		}
		for _, dep := range strings.Fields(string(out)) {
			for _, denied := range forbidden {
				if dep == denied || strings.HasPrefix(dep, denied+"/") {
					t.Errorf("transitive import %s is forbidden (tags %q)", dep, tags)
				}
			}
		}
	}
}
