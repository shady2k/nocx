//go:build linux

package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

func TestLinuxPreparedCommandOwnsPolicyFile(t *testing.T) {
	svc := &linuxService{log: log.NewSlogAdapter(nil), cacheDir: t.TempDir()}
	if status := svc.Status(t.Context()); !status.Available {
		t.Skipf("Landlock unavailable: %s", status.Reason)
	}
	workspace := t.TempDir()
	pc, err := svc.Prepare(t.Context(), Request{Workspace: workspace}, CommandSpec{
		Path: "/bin/sh", Args: []string{"-i"}, Dir: workspace, Env: os.Environ(),
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if pc.policyFile == nil {
		t.Fatal("PreparedCommand must retain the policy file as its sole owner")
	}
	runtimeRoot := filepath.Dir(pc.Policy.Home)
	pc.Close()
	if _, err := pc.policyFile.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("policy file after Close: err=%v, want os.ErrClosed", err)
	}
	if _, err := os.Stat(runtimeRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime root remains after Close: err=%v", err)
	}
	pc.Close() // idempotent: a second close must not reach a recycled descriptor.
}
