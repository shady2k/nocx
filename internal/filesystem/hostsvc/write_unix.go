//go:build unix

package hostsvc

import (
	"errors"
	"os"
	"syscall"
)

// processUmask is the mask this process was started with, sampled ONCE at
// package initialisation. Reading a umask means setting it for the duration of
// the read, which is process-wide state: doing that lazily, while a helper is
// already serving several connections, would briefly widen the mode of a file
// another goroutine is creating. Package initialisation happens before any
// goroutine exists, which is the only moment the read is free.
var processUmask = readUmask()

func readUmask() os.FileMode {
	mask := syscall.Umask(0)
	syscall.Umask(mask)
	// #nosec G115 — a umask is nine permission bits; the conversion cannot
	// lose anything a mode has.
	return os.FileMode(mask)
}

// newFileMode is the mode a NEW file is created with: the ordinary 0666 less
// the umask, exactly what an ordinary editor's save would produce. An existing
// target keeps its own mode instead (applyMode), because a write must not
// silently change what a person can do with a file they already had.
func newFileMode() os.FileMode { return 0o666 &^ processUmask }

// chownToTarget gives the temporary the existing target's owner and group, so
// the file that lands is owned as the file it replaces was. The attempt is
// best effort: a helper runs as the account, and giving a file away is
// privileged — EPERM is the ordinary answer when the target belongs to
// somebody else, and it is not a reason to refuse a write the account could
// otherwise make.
func chownToTarget(temp string, info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	// #nosec G115 — uid/gid are the kernel's own 32-bit account identities.
	err := os.Chown(temp, int(st.Uid), int(st.Gid))
	if err == nil || errors.Is(err, syscall.EPERM) {
		return nil
	}
	return err
}

// syncDir flushes a directory entry to disk, which is what makes a rename
// survive a crash. It is best effort by design: see install's comment — the
// rename has already happened, so a refusal here costs durability, not the
// answer.
func syncDir(path string) error {
	// #nosec G304 — the directory belongs to the session this service opened.
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}
