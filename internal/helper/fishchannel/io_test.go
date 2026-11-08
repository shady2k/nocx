package fishchannel

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclecodec"
	"golang.org/x/sys/unix"
)

func TestSocketWriteBackpressureDeadlineAndCancellation(t *testing.T) {
	const childMode = "NOCX_FISHCHANNEL_BACKPRESSURE_CASE"
	if mode := os.Getenv(childMode); mode != "" {
		ctx, stop := context.WithCancel(context.Background())
		defer stop()
		op, err := newOperation(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer op.close()
		op.deadline = time.Now().Add(50 * time.Millisecond)
		want := errTimeout
		if mode == "cancel" {
			op.deadline = time.Now().Add(5 * time.Second)
			timer := time.AfterFunc(20*time.Millisecond, stop)
			defer timer.Stop()
			want = context.Canceled
		}
		body := []byte(`"` + strings.Repeat("x", lifecycle.MaxFrameBytes-2) + `"`)
		start := time.Now()
		err = writeSocketFrame(op, 3, body)
		if !errors.Is(err, want) || time.Since(start) > time.Second {
			t.Fatalf("stalled write = %v; want bounded %v", err, want)
		}
		return
	}
	for _, mode := range []string{"deadline", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			socket, peer := socketPair(t)
			fd := int(socket.Fd())
			// The fixture exclusively owns this description until handoff.
			// Darwin's MSG_DONTWAIT does not bound a blocking socket send.
			if err := unix.SetNonblock(fd, true); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, 4096)
			filled := 0
			for {
				n, err := unix.Write(fd, buf)
				if n > 0 {
					filled += n
				}
				if errors.Is(err, unix.EINTR) {
					continue
				}
				if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
					break
				}
				if err != nil || n == 0 {
					t.Fatalf("fill socket: n=%d err=%v", n, err)
				}
			}
			if err := unix.SetNonblock(fd, false); err != nil {
				t.Fatal(err)
			}
			flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
			if err != nil {
				t.Fatal(err)
			}
			// Contain a blocking-write regression in its own process rather
			// than stranding the package until go test's ten-minute panic.
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSocketWriteBackpressureDeadlineAndCancellation$") // #nosec G204 -- this test binary, fixed fixture selection.
			cmd.Env = append(os.Environ(), childMode+"="+mode)
			cmd.ExtraFiles = []*os.File{socket}
			if output, childErr := cmd.CombinedOutput(); childErr != nil {
				t.Fatalf("bounded write child: %v (context: %v)\n%s", childErr, ctx.Err(), output)
			}
			after, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
			if err != nil || after != flags {
				t.Fatalf("shared flags changed: before=%d after=%d err=%v", flags, after, err)
			}
			drained := 0
			for {
				n, _, err := unix.Recvfrom(int(peer.Fd()), buf, unix.MSG_DONTWAIT)
				if n > 0 {
					drained += n
				}
				if errors.Is(err, unix.EINTR) {
					continue
				}
				if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
					break
				}
				if err != nil || n == 0 {
					t.Fatalf("drain surviving stream: n=%d err=%v", n, err)
				}
			}
			if drained != filled {
				t.Fatalf("failed write changed queued bytes: filled=%d drained=%d", filled, drained)
			}
		})
	}
}

// fragmentReadFunc paces the real socket reader at the codec's read boundary.
// Only scheduling is controlled: framing, socket reads and deadline decisions
// still belong to the production consumer.
type fragmentReadFunc func([]byte) (int, error)

func (read fragmentReadFunc) Read(p []byte) (int, error) { return read(p) }

func TestReceiveDeadlineIsNotRenewedByFragments(t *testing.T) {
	for _, tc := range []struct {
		name      string
		lastDelay time.Duration
		cancel    bool
		wantErr   error
	}{
		{name: "within deadline"},
		{name: "whole operation deadline", lastDelay: 80 * time.Millisecond, wantErr: errTimeout},
		{name: "delayed resumption", lastDelay: 227468875 * time.Nanosecond, wantErr: errTimeout},
		{name: "cancellation", cancel: true, wantErr: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				socket, peer := socketPair(t)
				fd := int(socket.Fd())
				flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				op, err := newOperation(ctx)
				if err != nil {
					t.Fatal(err)
				}
				body := `{"s":"fragmented"}`
				wire := frame(t, body)
				next := frame(t, `{"s":"next"}`)
				transport := &socketIO{op: op, fd: fd}
				start := time.Now()
				op.deadline = start.Add(150 * time.Millisecond)
				// Header bytes arrive at 0ms and 80ms. The rest is available
				// at 80ms, 160ms or after delayed consumer resumption. Unlike
				// a wall-clock upper bound, the result cannot confuse a late
				// scheduler with renewal of the original 150ms deadline.
				pieces := [][]byte{wire[:1], wire[1:2], append(wire[2:], next...)}
				delays := []time.Duration{0, 80 * time.Millisecond, tc.lastDelay}
				step := 0
				reader := fragmentReadFunc(func(p []byte) (int, error) {
					if step < len(pieces) {
						time.Sleep(delays[step])
						if _, writeErr := peer.Write(pieces[step]); writeErr != nil {
							t.Fatal(writeErr)
						}
						if tc.cancel && step == 2 {
							cancel()
						}
						step++
					}
					return transport.Read(p)
				})
				var got []byte
				func() {
					defer op.close()
					got, err = lifecyclecodec.ReadRawFrame(reader)
				}()
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("frame = %q, err = %v; want %v (elapsed=%v, read=%d)",
						got, err, tc.wantErr, time.Since(start), transport.read)
				}
				wantRead := len(wire)
				if tc.wantErr != nil {
					wantRead = 2
					if got != nil {
						t.Fatalf("failed receive returned partial frame: %q", got)
					}
				} else if string(got) != body {
					t.Fatalf("frame = %q, want %q", got, body)
				}
				if transport.read != wantRead {
					t.Fatalf("consumed %d bytes, want %d", transport.read, wantRead)
				}
				after, flagErr := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
				if flagErr != nil || after != flags {
					t.Fatalf("shared flags changed: before=%d after=%d err=%v", flags, after, flagErr)
				}
				// Expiry/cancellation must leave the late bytes untouched;
				// success must leave the next frame for the surviving owner.
				wantQueued := append(wire[wantRead:], next...)
				queued := make([]byte, len(wire)+len(next))
				n, _, readErr := unix.Recvfrom(fd, queued, unix.MSG_DONTWAIT)
				if readErr != nil {
					t.Fatalf("read surviving socket: %v", readErr)
				}
				if !bytes.Equal(queued[:n], wantQueued) {
					t.Fatalf("surviving socket = %q; want %q", queued[:n], wantQueued)
				}
				t.Logf("consumer elapsed=%v read=%d err=%v; queued bytes preserved=%d",
					time.Since(start), transport.read, err, n)
			})
		})
	}
}

func TestOperationClosesWakeDescriptorsAndJoinsGoroutine(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		op, err := newOperation(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if canceled {
			cancel()
		}
		op.close()
		cancel()
		select {
		case <-op.done:
		default:
			t.Fatal("wake goroutine survived operation")
		}
		for _, fd := range []int{op.wakeR, op.wakeW} {
			if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
				t.Fatalf("wake descriptor leaked: %v", err)
			}
		}
	}
}

func TestSendOversizeInputHasNoSocketOutput(t *testing.T) {
	socket, peer := socketPair(t)
	c := startChild(t, socket, strings.NewReader(strings.Repeat("never-print", maxInputBytes/11+1)), "send", "--fd", "3")
	c.wait(t, 1)
	if c.out.Len() != 0 || strings.Contains(c.err.String(), "never-print") {
		t.Fatal("oversize input leaked")
	}
	var buf [1]byte
	if _, _, err := unix.Recvfrom(int(peer.Fd()), buf[:], unix.MSG_DONTWAIT); !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
		t.Fatal("invalid input wrote a frame")
	}
}

func TestRunCancellationWakesBlockedOutput(t *testing.T) {
	socket, peer := socketPair(t)
	output, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = output.Close() }()
	defer func() { _ = writer.Close() }()
	fd := int(writer.Fd())
	if err := unix.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	for {
		_, err := unix.Write(fd, buf)
		if errors.Is(err, unix.EAGAIN) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.Write(frame(t, `{"s":"never-print"}`)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	code, _ := Run(ctx, []string{"receive", "--fd", strconv.Itoa(int(socket.Fd())), "--wait-ms", "5000"}, nil, writer)
	if code != 130 || time.Since(start) > time.Second {
		t.Fatal("blocked output was not cancellable")
	}
}

func TestMalformedSendDoesNotConsumeChannel(t *testing.T) {
	socket, peer := socketPair(t)
	if _, err := peer.Write(frame(t, `{"queued":"untouched"}`)); err != nil {
		t.Fatal(err)
	}
	c := startChild(t, socket, strings.NewReader("/a\x00array\x002\x00/a/1\x00null\x00\x00"), "send", "--fd", "3")
	c.wait(t, 1)
	body, err := readSocketFrame(t, socket)
	if err != nil || string(body) != `{"queued":"untouched"}` {
		t.Fatal("failed send consumed a reply")
	}
}

func TestRunRejectsNonSocketBeforeReadingInput(t *testing.T) {
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	defer func() { _ = writer.Close() }()
	if _, err := io.WriteString(writer, "secret"); err != nil {
		t.Fatal(err)
	}
	code, _ := Run(context.Background(), []string{"send", "--fd", strconv.Itoa(int(input.Fd()))}, input, nil)
	if code != 1 {
		t.Fatal("non-socket accepted")
	}
	var buf [6]byte
	if _, err := io.ReadFull(input, buf[:]); err != nil || string(buf[:]) != "secret" {
		t.Fatal("bad channel consumed stdin")
	}
}

func TestSendClosedPeerKeepsDiagnosticsPrivateAndDescriptionAlive(t *testing.T) {
	socket, peer := socketPair(t)
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	fd := int(socket.Fd())
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	c := startChild(t, socket, strings.NewReader("/secret\x00string\x00must-not-print\x00"), "send", "--fd", "3")
	c.wait(t, 1)
	if c.out.Len() != 0 || c.err.String() != errTransport.Error() {
		t.Fatalf("unexpected transport diagnostic: stdout=%q stderr=%q", c.out.String(), c.err.String())
	}
	after, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil || after != flags {
		t.Fatalf("failed send changed shared description: before=%d after=%d err=%v", flags, after, err)
	}
}
