package fishchannel

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclecodec"
	"golang.org/x/sys/unix"
)

func TestFrameWorkerStartFailureLeavesSharedStreamUntouched(t *testing.T) {
	socket, peer := socketPair(t)
	fd := int(socket.Fd())
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	op, err := newOperation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer op.close()
	op.deadline = time.Now().Add(handshakeTimeout)
	err = writeFrameThroughProcess(op, fd, []byte(`{"secret":"must-not-print"}`), filepath.Join(t.TempDir(), "missing-writer"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("start failure = %v, want missing executable", err)
	}
	after, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil || after != flags {
		t.Fatalf("start failure changed shared description: before=%d after=%d err=%v", flags, after, err)
	}
	var buf [1]byte
	if _, _, err := unix.Recvfrom(int(peer.Fd()), buf[:], unix.MSG_DONTWAIT); !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
		t.Fatalf("start failure changed queued data: %v", err)
	}
}

func TestFrameWorkerDeliversMaximumFrameWithoutTrailingData(t *testing.T) {
	socket, peer := socketPair(t)
	op, err := newOperation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer op.close()
	op.deadline = time.Now().Add(handshakeTimeout)
	type received struct {
		body []byte
		err  error
	}
	done := make(chan received, 1)
	go func() {
		body, err := lifecyclecodec.ReadRawFrame(&socketIO{op: op, fd: int(peer.Fd())})
		done <- received{body: body, err: err}
	}()
	body := bytes.Repeat([]byte("x"), lifecycle.MaxFrameBytes)
	writeErr := writeSocketFrame(op, int(socket.Fd()), body)
	result := <-done // The operation's absolute deadline also bounds this read.
	if writeErr != nil || result.err != nil || !bytes.Equal(result.body, body) {
		t.Fatalf("maximum frame: write=%v read=%v equal=%v", writeErr, result.err, bytes.Equal(result.body, body))
	}
	var buf [1]byte
	if _, _, err := unix.Recvfrom(int(peer.Fd()), buf[:], unix.MSG_DONTWAIT); !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
		t.Fatalf("writer left trailing data or closed the shared stream: %v", err)
	}
}
