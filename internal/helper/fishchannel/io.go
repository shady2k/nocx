package fishchannel

import (
	"context"
	"errors"
	"io"
	"time"

	"golang.org/x/sys/unix"
)

type operation struct {
	ctx      context.Context
	deadline time.Time
	wakeR    int
	wakeW    int
	stop     chan struct{}
	done     chan struct{}
}

func newOperation(ctx context.Context) (*operation, error) {
	// Only these newly owned pipe descriptions change flags. In particular,
	// a dup of the shared socket would still share its O_NONBLOCK bit.
	r, w, err := newWakePipe()
	if err != nil {
		return nil, err
	}
	op := &operation{ctx: ctx, wakeR: r, wakeW: w, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(op.done)
		select {
		case <-ctx.Done():
			// One byte, one writer, initially empty: never block even when
			// cancellation races close. close waits for this goroutine.
			for {
				_, err := unix.Write(op.wakeW, []byte{1})
				if !errors.Is(err, unix.EINTR) {
					break
				}
			}
		case <-op.stop:
		}
	}()
	return op, nil
}

func (op *operation) close() {
	close(op.stop)
	<-op.done
	_ = unix.Close(op.wakeR)
	_ = unix.Close(op.wakeW)
}

func (op *operation) check() error {
	if op.ctx.Err() != nil {
		return context.Canceled
	}
	if !op.deadline.IsZero() && !time.Now().Before(op.deadline) {
		return errTimeout
	}
	return nil
}

func (op *operation) poll(fd int, events int16, immediate bool) error {
	fds := [2]unix.PollFd{
		{Fd: int32(fd), Events: events},            //nolint:gosec // validated 31-bit descriptor
		{Fd: int32(op.wakeR), Events: unix.POLLIN}, //nolint:gosec // OS descriptor
	}
	for {
		if err := op.check(); err != nil {
			return err
		}
		ms := 0
		if !immediate {
			remaining := time.Until(op.deadline)
			if remaining <= 0 {
				return errTimeout
			}
			// Round up: a sub-millisecond remainder is not an idle poll.
			ms = int((remaining + time.Millisecond - 1) / time.Millisecond)
		}
		fds[0].Revents, fds[1].Revents = 0, 0
		n, err := unix.Poll(fds[:], ms)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if op.ctx.Err() != nil {
			return context.Canceled
		}
		if n == 0 {
			if immediate {
				return errTimeout
			}
			continue
		}
		if fds[0].Revents&unix.POLLNVAL != 0 {
			return errTransport
		}
		// HUP can accompany buffered data; the actual read must drain it
		// before reporting EOF. ERR is likewise resolved by the syscall.
		if fds[0].Revents&(events|unix.POLLHUP|unix.POLLERR) != 0 {
			return nil
		}
	}
}

type socketIO struct {
	op   *operation
	fd   int
	read int
}

func (s *socketIO) Read(p []byte) (int, error) {
	for {
		if err := s.op.check(); err != nil {
			return 0, err
		}
		n, _, err := unix.Recvfrom(s.fd, p, unix.MSG_DONTWAIT)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			if pollErr := s.op.poll(s.fd, unix.POLLIN, false); pollErr != nil {
				return 0, pollErr
			}
			continue
		}
		if n > 0 {
			s.read += n
		}
		if err == nil && n == 0 {
			err = io.EOF
		}
		return n, err
	}
}

// Write requires per-call nonblocking sends or an already nonblocking
// description. Darwin's frame dispatcher enforces the latter (ADR-0097).
func (s *socketIO) Write(p []byte) (int, error) {
	for {
		if err := s.op.check(); err != nil {
			return 0, err
		}
		n, err := unix.SendmsgN(s.fd, p, nil, nil, unix.MSG_DONTWAIT)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			if pollErr := s.op.poll(s.fd, unix.POLLOUT, false); pollErr != nil {
				return 0, pollErr
			}
			continue
		}
		return n, err
	}
}

func (op *operation) readInput(fd int) ([]byte, error) {
	var input []byte
	var buf [4096]byte
	for {
		if err := op.poll(fd, unix.POLLIN, false); err != nil {
			return nil, err
		}
		n, err := unix.Read(fd, buf[:])
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return input, nil
		}
		if n > maxInputBytes-len(input) {
			return nil, errData
		}
		input = append(input, buf[:n]...)
	}
}

func (op *operation) writeOutput(fd int, output []byte) error {
	for len(output) > 0 {
		if err := op.poll(fd, unix.POLLOUT, false); err != nil {
			return err
		}
		// <= POSIX PIPE_BUF. A writable anonymous pipe has space for this
		// atomic write; a large blocking write could outlive the deadline.
		chunk := output
		if len(chunk) > 512 {
			chunk = chunk[:512]
		}
		n, err := unix.Write(fd, chunk)
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		output = output[n:]
	}
	return op.check()
}
