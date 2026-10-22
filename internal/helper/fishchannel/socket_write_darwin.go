package fishchannel

import (
	"context"
	"os"
	"os/exec"

	"github.com/shady2k/nocx/internal/lifecyclecodec"
	"golang.org/x/sys/unix"
)

func writeSocketFrame(op *operation, fd int, body []byte) error {
	if err := op.check(); err != nil {
		return err
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return err
	}
	if flags&unix.O_NONBLOCK != 0 {
		_, err = lifecyclecodec.WriteRawFrame(&socketIO{op: op, fd: fd}, body)
		return err
	}
	return writeFrameThroughProcess(op, fd, body, "/bin/cat")
}

// Darwin's MSG_DONTWAIT does not prevent a send-buffer-space wait. Killing an
// owned writer process interrupts that wait without shutdown or an O_NONBLOCK
// change on the description shared with Fish and nested Bash (ADR-0100).
func writeFrameThroughProcess(op *operation, fd int, body []byte, program string) error {
	if err := op.check(); err != nil {
		return err
	}
	copyFD, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return err
	}
	socket := os.NewFile(uintptr(copyFD), "fish-channel-writer")
	defer func() { _ = socket.Close() }()
	ctx, cancel := context.WithDeadline(op.ctx, op.deadline)
	defer cancel()
	// Only the fixed macOS base utility is used in production. Payloads travel
	// through an owned pipe, never argv, environment, files, or diagnostics.
	cmd := exec.CommandContext(ctx, program) // #nosec G204 -- private caller supplies the fixed /bin/cat; tests inject only a fixture executable.
	cmd.Stdout = socket
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	if err = cmd.Start(); err != nil {
		if opErr := op.check(); opErr != nil {
			return opErr
		}
		return err
	}
	// The caller serializes frames. Keep encoding and short-write handling in
	// the canonical codec, and never replay a frame after a worker failure.
	_, writeErr := lifecyclecodec.WriteRawFrame(input, body)
	closeErr := input.Close()
	waitErr := cmd.Wait() // Reap the child and join CommandContext's watcher.
	if err := op.check(); err != nil {
		return err
	}
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return waitErr
}
