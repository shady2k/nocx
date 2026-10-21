package fishchannel

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclecodec"
	"golang.org/x/sys/unix"
)

// The production Run entrypoint is exercised in a fresh process inheriting the
// exact socket description, as Fish does, without building a second helper.
func TestFishChannelChildProcess(t *testing.T) {
	if os.Getenv("NOCX_FISHCHANNEL_TEST_CHILD") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			code, err := Run(ctx, os.Args[i+1:], os.Stdin, os.Stdout)
			stop()
			if err != nil {
				_, _ = io.WriteString(os.Stderr, err.Error())
			}
			os.Exit(code)
		}
	}
	os.Exit(2)
}

func socketPair(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	a, b := os.NewFile(uintptr(fds[0]), "channel"), os.NewFile(uintptr(fds[1]), "peer")
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	return a, b
}

func frame(t *testing.T, body string) []byte {
	t.Helper()
	var out bytes.Buffer
	if _, err := lifecyclecodec.WriteRawFrame(&out, []byte(body)); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

type child struct {
	cmd *exec.Cmd
	out bytes.Buffer
	err bytes.Buffer
}

func startChild(t *testing.T, socket *os.File, input io.Reader, args ...string) *child {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	t.Cleanup(cancel)
	argv := append([]string{"-test.run=^TestFishChannelChildProcess$", "--"}, args...)
	c := &child{cmd: exec.CommandContext(ctx, os.Args[0], argv...)} // #nosec G204 — executes this test binary with fixture-owned child arguments to exercise the real inherited socket.
	c.cmd.Env = append(os.Environ(), "NOCX_FISHCHANNEL_TEST_CHILD=1")
	c.cmd.ExtraFiles = []*os.File{socket}
	c.cmd.Stdin = input
	c.cmd.Stdout, c.cmd.Stderr = &c.out, &c.err
	if err := c.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return c
}

func (c *child) wait(t *testing.T, want int) {
	t.Helper()
	err := c.cmd.Wait()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	if code != want {
		t.Fatalf("exit = %d, want %d; diagnostic = %q", code, want, c.err.String())
	}
}

func TestReceiveLeavesQueuedFrameAndSharedDescriptionUsable(t *testing.T) {
	for _, nonblock := range []bool{false, true} {
		t.Run(strconv.FormatBool(nonblock), func(t *testing.T) {
			socket, peer := socketPair(t)
			fd := int(socket.Fd())
			if err := unix.SetNonblock(fd, nonblock); err != nil {
				t.Fatal(err)
			}
			before, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
			if err != nil {
				t.Fatal(err)
			}
			first := `{"evt":"first","empty":"","n":900719925474099312345,"unicode":"魚🌊","array":[],"obj":{}}`
			second := `{"evt":"second","n":-0.0000000000000000001e+999}`
			if _, writeErr := peer.Write(append(frame(t, first), frame(t, second)...)); writeErr != nil {
				t.Fatal(writeErr)
			}
			for _, body := range []string{first, second} {
				c := startChild(t, socket, nil, "receive", "--fd", "3", "--wait-ms", "0")
				c.wait(t, 0)
				want, convertErr := jsonToTriples([]byte(body))
				if convertErr != nil || !bytes.Equal(c.out.Bytes(), want) {
					t.Fatalf("reply differs: %q / %v", c.out.Bytes(), convertErr)
				}
			}
			after, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
			if err != nil || before != after {
				t.Fatalf("shared flags changed: %d -> %d (%v)", before, after, err)
			}
			c := startChild(t, socket, strings.NewReader("/evt\x00string\x00still-live\x00"), "send", "--fd", "3")
			c.wait(t, 0)
			body, err := readSocketFrame(t, peer)
			if err != nil || string(body) != `{"evt":"still-live"}` {
				t.Fatalf("shared socket no longer usable: %q / %v", body, err)
			}
		})
	}
}

func TestReceiveFragmentedHeaderAndBody(t *testing.T) {
	socket, peer := socketPair(t)
	wire := frame(t, `{"s":"魚\n\"","n":1.00e-999,"b":false,"nil":null}`)
	c := startChild(t, socket, nil, "receive", "--fd", "3", "--wait-ms", "2000")
	for _, piece := range [][]byte{wire[:1], wire[1:3], wire[3:4], wire[4:7], wire[7:]} {
		if _, err := peer.Write(piece); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.wait(t, 0)
	if !bytes.Contains(c.out.Bytes(), []byte("/n\x00number\x001.00e-999\x00")) || !bytes.Contains(c.out.Bytes(), []byte("/s\x00string\x00魚\n\"\x00")) {
		t.Fatalf("scalar text changed: %q", c.out.Bytes())
	}
}

func TestReceiveBoundedFailureAndNoOutput(t *testing.T) {
	var huge [4]byte
	binary.BigEndian.PutUint32(huge[:], lifecycle.MaxFrameBytes+1)
	cases := []struct {
		name  string
		wire  []byte
		close bool
		wait  string
		code  int
	}{
		{"idle poll", nil, false, "0", 3},
		{"idle wait", nil, false, "30", 3},
		{"EOF", nil, true, "200", 1},
		{"partial header EOF", []byte{0, 0}, true, "200", 1},
		{"partial header stall", []byte{0}, false, "30", 1},
		{"partial body stall", []byte{0, 0, 0, 10, '{'}, false, "30", 1},
		{"poll partial stall", []byte{0}, false, "0", 1},
		{"oversize", huge[:], false, "200", 1},
		{"empty", []byte{0, 0, 0, 0}, false, "200", 1},
		{"invalid JSON", frame(t, `{"secret":"never-print",`), false, "200", 1},
		{"NUL value", frame(t, `{"secret":"never-print\u0000"}`), false, "200", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			socket, peer := socketPair(t)
			if len(tc.wire) > 0 {
				if _, err := peer.Write(tc.wire); err != nil {
					t.Fatal(err)
				}
			}
			if tc.close {
				_ = peer.Close()
			}
			start := time.Now()
			c := startChild(t, socket, nil, "receive", "--fd", "3", "--wait-ms", tc.wait)
			c.wait(t, tc.code)
			if time.Since(start) > 4*time.Second {
				t.Fatal("failure was not bounded")
			}
			if c.out.Len() != 0 || strings.Contains(c.err.String(), "never-print") {
				t.Fatal("failed conversion exposed data")
			}
		})
	}
}

func TestRunCancellationWakesActualReadAndInput(t *testing.T) {
	for _, send := range []bool{false, true} {
		t.Run(strconv.FormatBool(send), func(t *testing.T) {
			socket, _ := socketPair(t)
			input, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = input.Close() }()
			defer func() { _ = writer.Close() }()
			output, outWriter, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = output.Close() }()
			defer func() { _ = outWriter.Close() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			args := []string{"receive", "--fd", strconv.Itoa(int(socket.Fd())), "--wait-ms", "5000"}
			if send {
				args = args[:3]
				args[0] = "send"
			}
			done := make(chan int, 1)
			go func() { code, _ := Run(ctx, args, input, outWriter); done <- code }()
			time.Sleep(20 * time.Millisecond)
			cancel()
			select {
			case code := <-done:
				if code != 130 {
					t.Fatalf("exit = %d, want cancellation", code)
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation did not wake I/O")
			}
		})
	}
}

func TestSendUsesExactFrameAndKeepsCapability(t *testing.T) {
	socket, peer := socketPair(t)
	input := "/cap\x00string\x00private-bearer\x00/evt\x00string\x00hello\x00/n\x00number\x0012345678901234567890.00e+9\x00"
	c := startChild(t, socket, strings.NewReader(input), "send", "--fd", "3")
	c.wait(t, 0)
	body, err := readSocketFrame(t, peer)
	if err != nil || string(body) != `{"cap":"private-bearer","evt":"hello","n":12345678901234567890.00e+9}` {
		t.Fatalf("send changed data: %q / %v", body, err)
	}
	if c.out.Len() != 0 || strings.Contains(c.err.String(), "private-bearer") {
		t.Fatal("send leaked data")
	}
}

func TestUsageDoesNotConsumeInput(t *testing.T) {
	socket, _ := socketPair(t)
	for _, args := range [][]string{
		{},
		{"send", "--fd", "1"},
		{"send", "--fd", "-1"},
		{"receive", "--fd", "3"},
		{"receive", "--fd", "3", "--wait-ms", "-1"},
		{"receive", "--fd", "3", "--wait-ms", "2147483648"},
		{"send", "--fd", "3", "--wait-ms", "0"},
	} {
		c := startChild(t, socket, strings.NewReader("never-print"), args...)
		c.wait(t, 2)
		if c.out.Len() != 0 || strings.Contains(c.err.String(), "never-print") {
			t.Fatal("usage leaked input")
		}
	}
}

func readSocketFrame(t *testing.T, socket *os.File) ([]byte, error) {
	t.Helper()
	op, err := newOperation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer op.close()
	op.deadline = time.Now().Add(time.Second)
	return lifecyclecodec.ReadRawFrame(&socketIO{op: op, fd: int(socket.Fd())})
}
