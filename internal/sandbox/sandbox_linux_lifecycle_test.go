//go:build linux

package sandbox

import (
	"errors"
	"io"
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

func TestLinuxPreparePreservesInheritedDescriptors(t *testing.T) {
	svc := &linuxService{log: log.NewSlogAdapter(nil), cacheDir: t.TempDir()}
	if status := svc.Status(t.Context()); !status.Available {
		t.Skipf("Landlock unavailable: %s", status.Reason)
	}

	parent, child, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := parent.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("close inherited descriptor reader: %v", closeErr)
		}
		if closeErr := child.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("close inherited descriptor writer: %v", closeErr)
		}
	})

	workspace := t.TempDir()
	pc, err := svc.Prepare(t.Context(), Request{Workspace: workspace}, CommandSpec{
		Path:       "/bin/sh",
		Args:       []string{"-c", "printf inherited-descriptor >&3"},
		Dir:        workspace,
		Env:        os.Environ(),
		ExtraFiles: []*os.File{child},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer pc.Close()
	if len(pc.Cmd.ExtraFiles) != 3 {
		t.Fatalf("ExtraFiles = %d, want inherited + policy + readiness", len(pc.Cmd.ExtraFiles))
	}
	if startErr := pc.Cmd.Start(); startErr != nil {
		t.Fatalf("Start: %v", startErr)
	}
	if readyErr := pc.WaitReady(t.Context()); readyErr != nil {
		t.Fatalf("WaitReady: %v", readyErr)
	}
	if closeErr := child.Close(); closeErr != nil {
		t.Fatalf("close child copy: %v", closeErr)
	}
	if waitErr := pc.Cmd.Wait(); waitErr != nil {
		t.Fatalf("Wait: %v", waitErr)
	}
	got, err := io.ReadAll(parent)
	if err != nil {
		t.Fatalf("read inherited descriptor: %v", err)
	}
	if string(got) != "inherited-descriptor" {
		t.Fatalf("descriptor payload = %q", got)
	}
}
