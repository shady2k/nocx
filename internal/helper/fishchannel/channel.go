// Package fishchannel adapts private lifecycle frames to Fish's NUL-separated
// data. It owns neither authentication nor lifecycle state; the kernel does.
package fishchannel

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/shady2k/nocx/internal/lifecyclecodec"
	"golang.org/x/sys/unix"
)

const (
	handshakeTimeout = 5 * time.Second
	pollStallTimeout = time.Second
)

var (
	errUsage     = errors.New("usage: fish-channel send --fd N | fish-channel receive --fd N --wait-ms N")
	errData      = errors.New("fish-channel: invalid or excessive data")
	errTransport = errors.New("fish-channel: channel I/O failed")
	errTimeout   = errors.New("fish-channel: operation timed out")
	errCanceled  = errors.New("fish-channel: operation canceled")
)

type options struct {
	send bool
	fd   int
	wait time.Duration
}

func parseArgs(args []string) (options, error) {
	var o options
	if len(args) != 3 && len(args) != 5 {
		return o, errUsage
	}
	o.send = args[0] == "send"
	if !o.send && args[0] != "receive" || args[1] != "--fd" {
		return o, errUsage
	}
	fd, err := strconv.ParseUint(args[2], 10, 31)
	if err != nil || fd < 3 {
		return o, errUsage
	}
	o.fd = int(fd)
	if o.send {
		if len(args) != 3 {
			return o, errUsage
		}
		return o, nil
	}
	if len(args) != 5 || args[3] != "--wait-ms" {
		return o, errUsage
	}
	// Limit to the poll syscall's signed millisecond range, without duration
	// overflow. This remains an absolute deadline across every fragment.
	ms, err := strconv.ParseUint(args[4], 10, 31)
	if err != nil {
		return o, errUsage
	}
	o.wait = time.Duration(ms) * time.Millisecond
	return o, nil
}

// Run executes send or receive (args excludes "fish-channel"). stdin/stdout
// are anonymous pipes in normal use. Neither they nor the inherited socket are
// closed, shut down, wrapped by net.FileConn, or changed to O_NONBLOCK. All
// returned diagnostics are fixed strings, never input or underlying I/O errors.
// Callers must discard stdout unless exitCode is zero.
func Run(ctx context.Context, args []string, stdin, stdout *os.File) (exitCode int, err error) {
	o, err := parseArgs(args)
	if err != nil {
		return 2, err
	}
	if ctx.Err() != nil {
		return 130, errCanceled
	}
	// A connected stream socket is the only supported channel. This also
	// rejects pipes without consuming any bytes from them.
	kind, e := unix.GetsockoptInt(o.fd, unix.SOL_SOCKET, unix.SO_TYPE)
	if e != nil || kind != unix.SOCK_STREAM {
		return 1, errTransport
	}
	op, e := newOperation(ctx)
	if e != nil {
		return 1, errTransport
	}
	defer op.close()
	if o.send {
		op.deadline = time.Now().Add(handshakeTimeout)
		if stdin == nil {
			return 1, errTransport
		}
		var input []byte
		input, e = op.readInput(int(stdin.Fd()))
		if e == nil {
			var body []byte
			body, e = triplesToJSON(input)
			if e == nil {
				e = writeSocketFrame(op, o.fd, body)
			}
		}
	} else {
		if stdout == nil {
			return 1, errTransport
		}
		if o.wait == 0 {
			e = op.poll(o.fd, unix.POLLIN, true)
			if errors.Is(e, errTimeout) {
				return 3, nil
			}
			op.deadline = time.Now().Add(pollStallTimeout)
		} else {
			op.deadline = time.Now().Add(o.wait)
		}
		transport := &socketIO{op: op, fd: o.fd}
		if e == nil {
			var body, output []byte
			body, e = lifecyclecodec.ReadRawFrame(transport)
			if e == nil {
				output, e = jsonToTriples(body)
			}
			if e == nil {
				e = op.writeOutput(int(stdout.Fd()), output)
			}
		}
		if errors.Is(e, errTimeout) && transport.read == 0 {
			return 3, nil
		}
	}
	if ctx.Err() != nil || errors.Is(e, context.Canceled) {
		return 130, errCanceled
	}
	if e == nil {
		return 0, nil
	}
	if errors.Is(e, errData) || errors.Is(e, lifecyclecodec.ErrFrameTooLarge) || errors.Is(e, lifecyclecodec.ErrInvalidFrame) || errors.Is(e, io.ErrUnexpectedEOF) {
		return 1, errData
	}
	if errors.Is(e, errTimeout) {
		return 1, errTimeout
	}
	return 1, errTransport
}
