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

// TestOneDrainConsumesEverythingReadable proves lossless bounded reads and the
// separate EAGAIN barrier on a real master/slave pair. A data read must never
// claim the barrier, even when it consumed the last byte already readable.
//
// THE PAYLOAD IS DELIBERATELY SMALL, not the 4 KiB spec §5.3.1 first named.
// A synchronous write with nobody draining the master concurrently can only
// stay off-CPU-free the way this test wants when the whole payload fits
// under the pty's OWN output queue high-water mark, and that mark is not
// the same number on every platform this ships to: Linux's line discipline
// buffers a full N_TTY_BUF_SIZE (4096 bytes) before a writer blocks, but
// xnu's tty driver (bsd/sys/tty.h) caps a pty's output queue at
// TTMAXHIWAT = roundup(2048, CBSIZE) and can configure it as low as
// TTMINHIWAT = roundup(100, CBSIZE) depending on the line's nominal speed —
// so a write of exactly 4096 bytes can legitimately block on darwin with no
// reader running to make room, which is what ci-mac measured directly: this
// test hung the full 10-minute panic bound with its only running goroutine
// parked in os.(*File).Write → internal/poll's IO-wait, never in the read
// that follows it (mac4.log, run 35150838522, nocx-4c1rd). 64 bytes is
// comfortably under TTMINHIWAT — the LOWEST that mark can be configured to,
// regardless of the pty's nominal speed — so the write completes on its own
// on every platform. The scratch buffer is deliberately smaller still, so
// unread bytes must survive across several owner-sized work quanta.
func TestOneDrainConsumesEverythingReadable(t *testing.T) {
	lp, slave := newRawPTYPair(t)

	payload := bytes.Repeat([]byte("x"), 64)
	if _, err := slave.Write(payload); err != nil {
		t.Fatalf("write %d bytes to the slave: %v", len(payload), err)
	}

	var got []byte
	buf := make([]byte, 7)
	for len(got) < len(payload) {
		n, barrier, err := lp.RawReadChunk(buf)
		if err != nil {
			t.Fatalf("RawReadChunk: %v", err)
		}
		if barrier || n == 0 {
			t.Fatalf("premature read barrier after %d of %d bytes", len(got), len(payload))
		}
		got = append(got, buf[:n]...)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("bounded reads lost or reordered the slave's output")
	}
	n, barrier, err := lp.RawReadChunk(buf)
	if err != nil || n != 0 || !barrier {
		t.Fatalf("idle read = (%d, barrier=%t, %v), want (0, true, nil)", n, barrier, err)
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
