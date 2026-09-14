package pty

import (
	"bytes"
	"os"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// newRawPTYPair opens a master/slave pair through this package's own
// openMaster, with NO shell attached: these tests are about the master fd's
// own readiness and blocking mode, not about a program's behaviour, and a
// bare pair is what lets a write to the slave be read off the master with no
// process and no timing in between (nocx-6q1uh.3, spec §5.2).
func newRawPTYPair(t *testing.T) (lp *LocalPty, slave *os.File) {
	t.Helper()
	master, slaveName, err := openMaster()
	if err != nil {
		t.Fatalf("openMaster: %v", err)
	}
	s, err := os.OpenFile(slaveName, os.O_RDWR|syscall.O_NOCTTY, 0) //nolint:gosec // slaveName is openMaster's own answer
	if err != nil {
		_ = master.Close()
		t.Fatalf("open the slave %s: %v", slaveName, err)
	}
	t.Cleanup(func() {
		_ = s.Close()
		_ = master.Close()
	})
	return &LocalPty{file: master, done: make(chan struct{})}, s
}

// TestOneDrainConsumesEverythingReadable is the owner's drain step made
// concrete (spec §5.3.1): 4 KiB written to the slave before the call, one
// RawReadUntilAgain delivers all of it and returns without blocking, and a
// second call on the now-idle fd delivers nothing and returns at once — both
// assertions with no sleep, because the write to the slave and the master's
// read of it are one kernel-level path with nothing in between to wait on.
func TestOneDrainConsumesEverythingReadable(t *testing.T) {
	lp, slave := newRawPTYPair(t)

	payload := bytes.Repeat([]byte("x"), 4096)
	if _, err := slave.Write(payload); err != nil {
		t.Fatalf("write %d bytes to the slave: %v", len(payload), err)
	}

	var got []byte
	buf := make([]byte, 8192)
	eof, err := lp.RawReadUntilAgain(buf, func(p []byte) { got = append(got, p...) })
	if err != nil {
		t.Fatalf("RawReadUntilAgain: %v", err)
	}
	if eof {
		t.Fatalf("RawReadUntilAgain reported eof with the slave still open")
	}
	if len(got) != len(payload) {
		t.Fatalf("one drain delivered %d bytes, want all %d written before the call", len(got), len(payload))
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("the delivered bytes do not match what was written to the slave")
	}

	// The second call: nothing was written since, so it must deliver nothing
	// and return immediately rather than blocking for more.
	got = nil
	eof, err = lp.RawReadUntilAgain(buf, func(p []byte) { got = append(got, p...) })
	if err != nil {
		t.Fatalf("second RawReadUntilAgain: %v", err)
	}
	if eof {
		t.Fatalf("second RawReadUntilAgain reported eof with the slave still open")
	}
	if len(got) != 0 {
		t.Fatalf("second RawReadUntilAgain delivered %d bytes with nothing written since, want 0", len(got))
	}
}

// TestTheWrapTimeFdIsNonBlocking is nocx-6q1uh.1's root cause, asserted
// directly rather than through its symptom: the master's raw fd carries
// O_NONBLOCK from the moment openMaster hands it back, and SetReadDeadline
// on the wrapped file answers nil — never os.ErrNoDeadline, which is what a
// blocking-wrapped fd answers because Go never registered it with the
// runtime poller (os/file_unix.go's newFile, both platform files' doc
// comments explain the exact mechanism this guards).
func TestTheWrapTimeFdIsNonBlocking(t *testing.T) {
	lp, slave := newRawPTYPair(t)
	defer func() { _ = slave.Close() }()

	rc, err := lp.file.SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}
	var flags int
	var fcntlErr error
	if ctlErr := rc.Control(func(fd uintptr) {
		flags, fcntlErr = unix.FcntlInt(fd, unix.F_GETFL, 0)
	}); ctlErr != nil {
		t.Fatalf("Control: %v", ctlErr)
	}
	if fcntlErr != nil {
		t.Fatalf("F_GETFL: %v", fcntlErr)
	}
	if flags&unix.O_NONBLOCK == 0 {
		t.Fatalf("the master's F_GETFL is %#o, want O_NONBLOCK set at wrap time", flags)
	}

	if err := lp.file.SetReadDeadline(time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("SetReadDeadline on the wrapped master returned %v, want nil: "+
			"a blocking-wrapped fd answers os.ErrNoDeadline instead of ever reaching this", err)
	}
	if err := lp.file.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("clearing the deadline: %v", err)
	}
}
