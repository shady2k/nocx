package skill_test

import (
	"reflect"
	"testing"

	"github.com/shady2k/nocx/internal/skill"
	"github.com/shady2k/nocx/internal/skill/builtin"
)

func TestFilesystemRootsReturnsOnlyDirectoryRoots(t *testing.T) {
	configDir := t.TempDir()
	authoredDir := configDir + "/skills"
	managedDir := configDir + "/managed-skills"
	roots := []skill.Root{
		{Dir: authoredDir, Provenance: skill.ProvenanceAuthored},
		{FS: builtin.FS, Provenance: skill.ProvenanceBuiltin},
		{Dir: managedDir, Provenance: skill.ProvenanceManaged},
	}

	got := skill.FilesystemRoots(roots)
	want := []string{authoredDir, managedDir}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FilesystemRoots() = %#v, want %#v", got, want)
	}
}
