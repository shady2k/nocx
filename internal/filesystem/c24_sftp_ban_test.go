//go:build linux || darwin

package filesystem_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"
)

func TestC24FilesReachesNoSFTP(t *testing.T) {
	root := moduleRoot(t)
	for _, goos := range []string{"linux", "darwin"} {
		for _, cgo := range []string{"0", "1"} {
			for _, tags := range []string{"", "nocx_local_ssh", "release", "release,nocx_local_ssh"} {
				name := tags
				if name == "" {
					name = "untagged"
				}
				t.Run(goos+"/cgo="+cgo+"/"+name, func(t *testing.T) {
					checkC24Imports(t, root, goos, cgo, tags)
				})
			}
		}
	}
}

// Check both import views with the same file selection. Without -compiled or
// -export, go list can select cgo sources for either OS without compiling them.
func checkC24Imports(t *testing.T, root, goos, cgo, tags string) {
	t.Helper()
	const repo = "github.com/shady2k/nocx/"
	const sftpClient = "github.com/pkg/sftp"
	const retiredProvider = repo + "internal/filesystem" + "/sftp"
	allowed := map[string]bool{
		repo + "internal/ssh": true,
		repo + "internal/app": true,
		repo + "cmd/e2e-sshd": true,
	}

	cmd := exec.Command("go", "list", "-json=ImportPath,Imports,TestImports,XTestImports", "-tags", tags, "./...") // #nosec G204 -- fixed compiler, packages, and tags.
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS="+goos, "CGO_ENABLED="+cgo)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, output)
	}

	importers := 0
	packages := json.NewDecoder(bytes.NewReader(output))
	for {
		var pkg struct {
			ImportPath                         string
			Imports, TestImports, XTestImports []string
		}
		if err := packages.Decode(&pkg); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		for _, imports := range [][]string{pkg.Imports, pkg.TestImports, pkg.XTestImports} {
			for _, dep := range imports {
				if dep == retiredProvider {
					t.Errorf("%s imports removed provider %s", pkg.ImportPath, dep)
				}
			}
		}
		for _, dep := range pkg.Imports {
			if dep == sftpClient {
				importers++
				if !allowed[pkg.ImportPath] {
					t.Errorf("%s imports %s outside helper installation, shell integration, and the fixture server", pkg.ImportPath, dep)
				}
			}
		}
	}
	if importers == 0 {
		t.Error("go list found no production SFTP importers")
	}
	deps := packageDependencies(t, "./internal/filesystem/...", goos, cgo, tags)
	for _, forbidden := range []string{sftpClient, retiredProvider} {
		if deps[forbidden] {
			t.Errorf("internal/filesystem reaches %s", forbidden)
		}
	}
}
