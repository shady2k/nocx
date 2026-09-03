package artifacts

import (
	"io/fs"
	"testing"
)

func TestEmbeddedArtifactFSContainsOnlyArtifactDirectory(t *testing.T) {
	entries, err := fs.ReadDir(artifactsFS, ".")
	if err != nil {
		t.Fatalf("read embedded artifact root: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "bin" || !entries[0].IsDir() {
		t.Fatalf("embedded artifact root contains %v, want only bin/", entryNames(entries))
	}
	if _, err := fs.Stat(artifactsFS, "source_test.go"); err == nil {
		t.Fatal("embedded artifact root contains source_test.go")
	}
}

func entryNames(entries []fs.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
