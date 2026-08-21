package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"testing"

	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transfer"
)

// fakeLease is an ssh.FSConn double for the adapter's tests. Only the write
// half answers anything; the read half is here because the lease interface
// requires it, and the adapter forwards it untouched.
type fakeLease struct {
	createErr      error
	posixRenameErr error
	renameErr      error
	removeErr      error

	created *fakeLeaseFile
}

type fakeLeaseFile struct {
	written []byte
	closed  bool
}

func (f *fakeLeaseFile) Write(p []byte) (int, error) {
	f.written = append(f.written, p...)
	return len(p), nil
}
func (f *fakeLeaseFile) Close() error { f.closed = true; return nil }

func (l *fakeLease) Create(string) (ssh.FSFile, error) {
	if l.createErr != nil {
		return nil, l.createErr
	}
	l.created = &fakeLeaseFile{}
	return l.created, nil
}
func (l *fakeLease) PosixRename(string, string) error { return l.posixRenameErr }
func (l *fakeLease) Rename(string, string) error      { return l.renameErr }
func (l *fakeLease) Remove(string) error              { return l.removeErr }

func (l *fakeLease) ReadDir(context.Context, string) ([]os.FileInfo, error) {
	return nil, errors.New("not in this test")
}

func (l *fakeLease) Stat(string) (os.FileInfo, error) { return nil, errors.New("not in this test") }

func (l *fakeLease) Lstat(string) (os.FileInfo, error) { return nil, errors.New("not in this test") }
func (l *fakeLease) ReadLink(string) (string, error)   { return "", errors.New("not in this test") }
func (l *fakeLease) RealPath(string) (string, error)   { return "", errors.New("not in this test") }
func (l *fakeLease) ReadFile(context.Context, string, int64) ([]byte, bool, error) {
	return nil, false, errors.New("not in this test")
}
func (l *fakeLease) Done() <-chan struct{} { return nil }
func (l *fakeLease) LostErr() error        { return nil }
func (l *fakeLease) Close() error          { return nil }

// TestUploadLease_TranslatesTheCapabilityAnswer is the seam the sink's
// non-atomic fallback rests on. internal/ssh answers "this server has no
// posix-rename@openssh.com" with its OWN sentinel and internal/transfer keys
// its fallback on a DIFFERENT one — neither package may import the other —
// so if this adapter does not translate, the sink reads a refused capability
// as an ordinary promote failure, deletes the temp and fails, on every
// server that lacks the extension. Both packages' tests stay green while it
// does; only this one can see it.
func TestUploadLease_TranslatesTheCapabilityAnswer(t *testing.T) {
	lease := fsUploadLease{FSConn: &fakeLease{
		posixRenameErr: fmt.Errorf("%w: server said SSH_FX_OP_UNSUPPORTED", ssh.ErrPosixRenameUnsupported),
	}}

	err := lease.PosixRename("/tmp/a.nocx-upload-1", "/tmp/a")

	if !errors.Is(err, transfer.ErrPosixRenameUnsupported) {
		t.Fatalf("PosixRename: %v, want an error the sink's fallback keys on", err)
	}
	if !errors.Is(err, ssh.ErrPosixRenameUnsupported) {
		t.Error("the lease's own answer was dropped; a translation adds a vocabulary, it does not lose one")
	}
}

// TestUploadLease_LeavesAnOrdinaryPromoteFailureAlone is the other half: a
// fallback that cannot tell "the server lacks the extension" from "the
// rename failed" either never runs or runs when it must not (design D6).
func TestUploadLease_LeavesAnOrdinaryPromoteFailureAlone(t *testing.T) {
	boom := errors.New("ssh: sftp lease dead: hard timeout exceeded")
	lease := fsUploadLease{FSConn: &fakeLease{posixRenameErr: boom}}

	err := lease.PosixRename("/tmp/a.nocx-upload-1", "/tmp/a")

	if errors.Is(err, transfer.ErrPosixRenameUnsupported) {
		t.Fatalf("an ordinary failure was translated into the capability answer: %v", err)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("PosixRename: %v, want the lease's error unchanged", err)
	}
}

// TestUploadLease_PreservesTheNotExistContract pins the second contract
// transfer.RemoteFS documents and the compiler cannot enforce: a missing
// path must satisfy errors.Is(err, fs.ErrNotExist), because the fallback's
// "there was nothing to back up" branch keys on exactly that and any other
// error there aborts the transfer. pkg/sftp normalises SSH_FX_NO_SUCH_FILE
// to os.ErrNotExist (client.go:2237) and the lease's classify passes an
// unclassified error through, so the contract holds upstream — what this
// asserts is that the adapter does not break it on the way past.
func TestUploadLease_PreservesTheNotExistContract(t *testing.T) {
	lease := fsUploadLease{FSConn: &fakeLease{
		renameErr: fmt.Errorf("sftp: rename /home/u/a.txt: %w", fs.ErrNotExist),
	}}

	err := lease.Rename("/home/u/a.txt", "/home/u/a.txt.nocx-bak-1")

	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Rename of a missing path: %v, want an error satisfying fs.ErrNotExist", err)
	}
}

// TestUploadLease_DoesNotInventANotExist is that contract's paired negative:
// an ordinary rename failure must not read as "nothing was there", which
// would make the fallback promote over a destination it never backed up.
func TestUploadLease_DoesNotInventANotExist(t *testing.T) {
	lease := fsUploadLease{FSConn: &fakeLease{renameErr: errors.New("permission denied")}}

	err := lease.Rename("/home/u/a.txt", "/home/u/a.txt.nocx-bak-1")

	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("an ordinary failure read as not-exist: %v", err)
	}
}

// TestUploadLease_ForwardsTheWriteHandle is the ordinary path: Create hands
// back the lease's own handle, and the bytes the sink writes reach it.
func TestUploadLease_ForwardsTheWriteHandle(t *testing.T) {
	inner := &fakeLease{}
	lease := fsUploadLease{FSConn: inner}

	f, err := lease.Create("/home/u/a.txt.nocx-upload-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := f.Write([]byte("hi")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if string(inner.created.written) != "hi" || !inner.created.closed {
		t.Errorf("the lease's handle saw %q closed=%v, want \"hi\" closed=true", inner.created.written, inner.created.closed)
	}
}

// TestUploadLease_ReportsACreateRefusal is Create's failure path: O_EXCL
// refusing an existing temp is how a lost race is detected, so the refusal
// must arrive at the sink rather than being smoothed over.
func TestUploadLease_ReportsACreateRefusal(t *testing.T) {
	refused := errors.New("sftp: file already exists")
	lease := fsUploadLease{FSConn: &fakeLease{createErr: refused}}

	f, err := lease.Create("/home/u/a.txt.nocx-upload-1")
	if !errors.Is(err, refused) {
		t.Fatalf("Create: %v, want the lease's refusal", err)
	}
	if f != nil {
		t.Error("a refused Create returned a handle")
	}
}

// TestUploadLease_ForwardsRemove covers the last of the four calls: the
// temp's cleanup and the backup's, whose failures the sink reports as
// stranded paths.
func TestUploadLease_ForwardsRemove(t *testing.T) {
	boom := errors.New("permission denied")
	if err := (fsUploadLease{FSConn: &fakeLease{removeErr: boom}}).Remove("/home/u/x"); !errors.Is(err, boom) {
		t.Fatalf("Remove: %v, want the lease's error", err)
	}
	if err := (fsUploadLease{FSConn: &fakeLease{}}).Remove("/home/u/x"); err != nil {
		t.Fatalf("Remove on an ordinary lease: %v", err)
	}
}

// TestUploadLease_IsTheWriteSurfaceTheSinkNeeds pins the adapter's whole
// reason to exist: an ssh.FSConn does not satisfy transfer.RemoteFS (Create's
// result type differs by name), and wrapped in this adapter it does.
func TestUploadLease_IsTheWriteSurfaceTheSinkNeeds(t *testing.T) {
	var _ transfer.RemoteFS = fsUploadLease{FSConn: &fakeLease{}}
}
