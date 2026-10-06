//go:build linux

package sandbox

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRunnerRejectsDescriptorIdentityMismatch(t *testing.T) {
	root := t.TempDir()
	rootFD, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(rootFD) }()
	workspaceFD, err := unix.FcntlInt(uintptr(rootFD), unix.F_DUPFD_CLOEXEC, rootFD+1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(workspaceFD) }()
	var st unix.Stat_t
	if err := unix.Fstat(rootFD, &st); err != nil {
		t.Fatal(err)
	}
	p := runnerTestPlan()
	p.Policy.WorkspaceRoot = root
	p.Policy.Roots[0].Path = root
	p.Policy.Roots[0].Identity = FileIdentity{Device: st.Dev, Inode: st.Ino}
	p.RootFDs = []int{rootFD}
	p.WorkspaceFD = workspaceFD
	if err := validatePinnedObjects(p); err != nil {
		t.Fatalf("matching pinned directory rejected: %v", err)
	}
	p.Policy.Roots[0].Identity.Inode++
	if err := validatePinnedObjects(p); err == nil {
		t.Fatal("accepted root descriptor with a different object identity")
	}
}

func TestRunnerRejectsNonDirectoryWorkspaceDescriptor(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "not-a-directory")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	p := runnerTestPlan()
	p.Policy.Roots = []Root{}
	p.RootFDs = nil
	p.WorkspaceFD = int(file.Fd())
	if err := validatePinnedObjects(p); err == nil {
		t.Fatal("accepted a regular file as working directory")
	}
}
