package proc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// Timer is a scheduled call, as returned by Clock.AfterFunc.
type Timer interface {
	// Stop prevents the scheduled call from running. It reports whether the
	// call was stopped before it fired.
	Stop() bool
}

// Clock is the time source a Supervisor's deadline runs on.
//
// It is a seam and not `time.AfterFunc` for one reason: AGENTS.md forbids a
// test that depends on timing, and a deadline asserted by sleeping past it
// is exactly that. With the clock injected, a test starts the fixture,
// OBSERVES that its processes exist, and then fires the deadline — the
// assertion is on a state change, and it holds on a machine of any speed.
//
// It is deliberately not internal/notify.Clock, which is the same shape.
// That one is the notification pipeline's scheduling seam and lives with the
// router it schedules; making process supervision depend on the notification
// package to borrow an interface would buy a shared declaration at the cost
// of a dependency neither module wants.
type Clock interface {
	// AfterFunc schedules fn to run after d and returns a handle that can
	// stop the call before it runs.
	AfterFunc(d time.Duration, fn func()) Timer
}

// RealClock is the production clock: time.AfterFunc.
type RealClock struct{}

func (RealClock) AfterFunc(d time.Duration, fn func()) Timer { return time.AfterFunc(d, fn) }

// ErrDeadline is returned when the job's deadline stopped the group. It is a
// distinct error because the caller's decision differs: a job the deadline
// cut produced a PARTIAL answer, and a partial answer is never published.
var ErrDeadline = errors.New("proc: deadline")

// ErrOutputBound is returned when the job wrote more than Job.MaxBytes. Same
// reason: what we hold is a prefix, not an answer.
var ErrOutputBound = errors.New("proc: output bound exceeded")

// Job is one supervised invocation.
type Job struct {
	// Argv is the program and its arguments. There is no shell between us
	// and it: argv[0] is executed directly, so nothing here can be injected
	// into by a value in the tail.
	Argv []string

	// Env is the child's environment; nil inherits ours.
	Env []string

	// Dir is the child's working directory; empty inherits ours.
	Dir string

	// Deadline bounds the WHOLE GROUP, not the wait for it. Past it the
	// group is terminated, then killed, then reaped.
	Deadline time.Duration

	// MaxBytes bounds stdout. Past it the run is cut with ErrOutputBound.
	MaxBytes int
}

// Output is what a supervised run produced.
type Output struct {
	// Stdout is what the child wrote, up to MaxBytes.
	Stdout []byte

	// Complete is true only when the child finished on its own, inside the
	// deadline, inside the output bound, with status 0. It is the whole
	// point of the type: a caller publishes a result if and only if this is
	// true, so a deadline that fires can never leave a half-enumeration
	// looking like the answer.
	Complete bool
}

// Supervisor runs a job in a process group it owns.
//
// The defect it replaces: a budget that stops WAITING but not WORKING. A
// timeout that abandons the read leaves the pipeline running — on the far
// side of a completion enumeration that is thousands of directory reads
// continuing after nobody is left to want them — and killing the direct
// child alone orphans the rest of the pipeline, because the child is a
// subshell and the work is its children. Owning the group is what makes the
// deadline a bound on the work rather than on our patience.
type Supervisor struct {
	// Clock is the deadline's time source. Zero value means RealClock.
	Clock Clock

	// Grace is the pause between escalation steps (INT → TERM → KILL). Zero
	// means a small default: by the time this runs the job has already spent
	// its whole budget, so there is little left to spend on politeness.
	Grace time.Duration
}

// defaultGrace is short on purpose. The polite steps exist so a cooperative
// child can flush and exit; a child that wanted longer has already had its
// entire deadline.
const defaultGrace = 50 * time.Millisecond

// Run executes the job and returns what it wrote. The error names WHY the
// run did not complete — ErrDeadline, ErrOutputBound, the context's error,
// or the child's own failure — and Output.Complete is the single question a
// caller must ask before publishing anything.
func (s Supervisor) Run(ctx context.Context, job Job) (Output, error) {
	if len(job.Argv) == 0 {
		return Output{}, errors.New("proc: no argv")
	}
	clock := s.Clock
	if clock == nil {
		clock = RealClock{}
	}
	grace := s.Grace
	if grace <= 0 {
		grace = defaultGrace
	}

	// #nosec G204 — argv is built by the caller from fixed literals and
	// values it owns; there is no shell in the path, so the tail cannot
	// become a command.
	cmd := exec.Command(job.Argv[0], job.Argv[1:]...)
	cmd.Env = job.Env
	cmd.Dir = job.Dir
	InOwnGroup(cmd)

	sink := &boundedSink{max: job.MaxBytes}
	cmd.Stdout = sink
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return Output{}, fmt.Errorf("proc: start %s: %w", job.Argv[0], err)
	}

	// done closes when the child has been REAPED — not when it died. The
	// distinction is the second half of "every member is reaped": a killed
	// child that nobody waits for is still a process.
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	// stopped closes when we decide the group must not run any longer. One
	// goroutine owns the escalation so the three stop reasons cannot each
	// send their own signals into the same group.
	stopped := make(chan struct{})
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { close(stopped) }) }

	go func() {
		select {
		case <-done:
		case <-stopped:
			KillGroup(cmd, done, grace)
		}
	}()

	var deadlineHit, boundHit atomicFlag
	timer := clock.AfterFunc(job.Deadline, func() {
		select {
		case <-done:
			return // finished on its own in the same instant: not a cut
		default:
		}
		deadlineHit.set()
		stop()
	})
	defer timer.Stop()

	go func() {
		select {
		case <-done:
		case <-ctx.Done():
			stop()
		case <-sink.full():
			boundHit.set()
			stop()
		}
	}()

	<-done

	out := Output{Stdout: sink.bytes()}
	switch {
	case deadlineHit.get():
		return out, ErrDeadline
	case boundHit.get():
		return out, ErrOutputBound
	case ctx.Err() != nil:
		return out, ctx.Err()
	}
	if code := cmd.ProcessState.ExitCode(); code != 0 {
		return out, fmt.Errorf("proc: %s exited %d", job.Argv[0], code)
	}
	out.Complete = true
	return out, nil
}

// atomicFlag is a one-shot boolean set from one goroutine and read from
// another after `done` orders them; the mutex is what makes -race agree.
type atomicFlag struct {
	mu sync.Mutex
	v  bool
}

func (f *atomicFlag) set() {
	f.mu.Lock()
	f.v = true
	f.mu.Unlock()
}

func (f *atomicFlag) get() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.v
}

// boundedSink keeps at most max bytes and signals once, on the write that
// first crossed the bound. It never errors: returning an error to exec's
// copier would race the caller's own attribution of why the run stopped, and
// the answer is the same either way — the group is cut.
type boundedSink struct {
	mu   sync.Mutex
	buf  []byte
	max  int
	over chan struct{}
	once sync.Once
}

func (b *boundedSink) full() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.over == nil {
		b.over = make(chan struct{})
	}
	return b.over
}

func (b *boundedSink) Write(p []byte) (int, error) {
	b.mu.Lock()
	room := b.max - len(b.buf)
	if room > 0 {
		if room > len(p) {
			room = len(p)
		}
		b.buf = append(b.buf, p[:room]...)
	}
	over := len(b.buf) >= b.max
	if b.over == nil {
		b.over = make(chan struct{})
	}
	ch := b.over
	b.mu.Unlock()
	if over {
		b.once.Do(func() { close(ch) })
	}
	return len(p), nil
}

func (b *boundedSink) bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf
}
