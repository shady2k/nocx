package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateChildFixture(t *testing.T) {
	base, err := os.MkdirTemp("", "nocx-sandbox-artifact-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if removeErr := os.RemoveAll(base); removeErr != nil {
			t.Errorf("remove fixture: %v", removeErr)
		}
	})
	workspace := filepath.Join(base, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(base, "sentinel")
	preHard := filepath.Join(workspace, "pre-hard-link")
	readOnlyRoot := filepath.Join(base, "host-home", projectedReadOnlyRelative)
	writableRoot := filepath.Join(base, "host-home", filepath.FromSlash(projectedWritableRelative))
	nestedWritable := filepath.Join(base, "host-home", filepath.FromSlash(projectedNestedRWRelative))
	if err := validateChildFixture(workspace, sentinel, preHard, "/bin/sh", readOnlyRoot, writableRoot, nestedWritable); err != nil {
		t.Fatalf("valid fixture: %v", err)
	}

	for name, values := range map[string][7]string{
		"different shell":        {workspace, sentinel, preHard, "/bin/bash", readOnlyRoot, writableRoot, nestedWritable},
		"relative workspace":     {"workspace", sentinel, preHard, "/bin/sh", readOnlyRoot, writableRoot, nestedWritable},
		"foreign sentinel":       {workspace, filepath.Join(t.TempDir(), "sentinel"), preHard, "/bin/sh", readOnlyRoot, writableRoot, nestedWritable},
		"foreign hard link":      {workspace, sentinel, filepath.Join(t.TempDir(), "pre-hard-link"), "/bin/sh", readOnlyRoot, writableRoot, nestedWritable},
		"foreign read-only root": {workspace, sentinel, preHard, "/bin/sh", filepath.Join(t.TempDir(), ".config"), writableRoot, nestedWritable},
		"foreign writable root":  {workspace, sentinel, preHard, "/bin/sh", readOnlyRoot, filepath.Join(t.TempDir(), "state"), nestedWritable},
		"foreign nested RW root": {workspace, sentinel, preHard, "/bin/sh", readOnlyRoot, writableRoot, filepath.Join(t.TempDir(), "nested")},
		"unrecognized root":      {filepath.Join(t.TempDir(), "workspace"), sentinel, preHard, "/bin/sh", readOnlyRoot, writableRoot, nestedWritable},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateChildFixture(values[0], values[1], values[2], values[3], values[4], values[5], values[6]); err == nil {
				t.Fatal("invalid fixture accepted")
			}
		})
	}
}
