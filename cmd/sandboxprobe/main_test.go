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
	if err := validateChildFixture(workspace, sentinel, preHard, "/bin/sh"); err != nil {
		t.Fatalf("valid fixture: %v", err)
	}

	for name, values := range map[string][4]string{
		"different shell":    {workspace, sentinel, preHard, "/bin/bash"},
		"relative workspace": {"workspace", sentinel, preHard, "/bin/sh"},
		"foreign sentinel":   {workspace, filepath.Join(t.TempDir(), "sentinel"), preHard, "/bin/sh"},
		"foreign hard link":  {workspace, sentinel, filepath.Join(t.TempDir(), "pre-hard-link"), "/bin/sh"},
		"unrecognized root":  {filepath.Join(t.TempDir(), "workspace"), sentinel, preHard, "/bin/sh"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateChildFixture(values[0], values[1], values[2], values[3]); err == nil {
				t.Fatal("invalid fixture accepted")
			}
		})
	}
}
