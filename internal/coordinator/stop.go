package coordinator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"syscall"
	"time"
)

// Stopping an incompatible coordinator: the sharp end of D4.
//
// A1 may kill the old coordinator and lose its sessions — graceful,
// session-preserving handover is A2. What A1 may not do is be quiet about
// it, which is why the launcher announces the loss BEFORE it calls in here
// (launcher.go) rather than after: a stop that then fails must still leave
// the person told what was attempted.

// Stopper ends a running coordinator. An interface because the launcher's
// refusal path has to be testable without putting a process on the machine
// to kill, and because "the daemon could not be stopped" is a real outcome
// a launcher must report rather than paper over (AD-8).
type Stopper interface {
	// Stop ends the coordinator described by the sighting and returns only
	// once it is gone.
	Stop(ctx context.Context, sighting Sighting) error
}

// SignalStopperConfig is the one policy decision this has: how long a
// coordinator gets to shut down cleanly before it is killed.
type SignalStopperConfig struct {
	// Grace bounds the wait after SIGTERM. Zero means DefaultStopGrace.
	Grace time.Duration
	// PollInterval is how often the process is checked. Zero means
	// DefaultStopPollInterval.
	PollInterval time.Duration
	// Logger records what was signalled. Never a token — the launcher has
	// none to give it.
	Logger *slog.Logger
}

const (
	// DefaultStopGrace is what an incompatible coordinator gets to close
	// its sockets, seal its vault and flush its stores. nocx-server's own
	// shutdown is a signal handler and a few Close calls, so seconds is
	// generous; the point of the bound is that a daemon which has wedged
	// must not hold a window's startup open forever.
	DefaultStopGrace = 10 * time.Second
	// DefaultStopPollInterval is how often the process is checked for
	// having gone. Short enough that an ordinary shutdown costs nothing
	// noticeable, long enough not to spin.
	DefaultStopPollInterval = 20 * time.Millisecond
)

// SignalStopper ends the process the kernel named on the discovery socket.
//
// The pid comes from the SIGHTING, never from anything the daemon said
// about itself: a coordinator that reported its own pid could report
// anybody's, and this function's whole job is to send a signal. See
// [Sighting] and peer_linux.go.
type SignalStopper struct {
	cfg SignalStopperConfig
}

// NewSignalStopper returns the real stopper with its defaults applied.
func NewSignalStopper(cfg SignalStopperConfig) *SignalStopper {
	if cfg.Grace <= 0 {
		cfg.Grace = DefaultStopGrace
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultStopPollInterval
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &SignalStopper{cfg: cfg}
}

// Stop asks the coordinator to end, waits for the grace period, and kills
// it if it is still there.
//
// SIGTERM first because nocx-server handles it (cmd/nocx-server: the signal
// is what unwinds the deferred socket close and the app shutdown), and a
// daemon killed outright leaves its socket file behind for the next
// launcher to find and be refused by. SIGKILL after the grace because a
// wedged daemon must not be able to keep the window shut.
func (s *SignalStopper) Stop(ctx context.Context, sighting Sighting) error {
	if sighting.PID <= 0 {
		// Not a failure of the kill — a failure to identify the target.
		// Said as itself, because the two have different answers: this one
		// means the platform could not read the peer pid, and the person
		// has to stop the daemon by hand.
		return errors.New("coordinator: cannot identify the running coordinator to stop it " +
			"(the discovery socket reported no process id)")
	}
	proc, err := os.FindProcess(sighting.PID)
	if err != nil {
		return fmt.Errorf("coordinator: finding coordinator process %d: %w", sighting.PID, err)
	}

	s.cfg.Logger.Warn("coordinator: stopping an incompatible coordinator",
		"pid", sighting.PID,
		"version", sighting.Hello.Build.Version,
		"commit", sighting.Hello.Build.Commit,
		"protocol", sighting.Hello.Protocol,
	)
	if termErr := proc.Signal(syscall.SIGTERM); termErr != nil {
		if isGone(termErr) {
			return nil
		}
		return fmt.Errorf("coordinator: asking coordinator %d to stop: %w", sighting.PID, termErr)
	}
	if gone, waitErr := s.waitGone(ctx, proc, s.cfg.Grace); waitErr != nil {
		return waitErr
	} else if gone {
		return nil
	}

	s.cfg.Logger.Warn("coordinator: the incompatible coordinator did not stop; killing it",
		"pid", sighting.PID, "grace", s.cfg.Grace)
	if killErr := proc.Signal(syscall.SIGKILL); killErr != nil {
		if isGone(killErr) {
			return nil
		}
		return fmt.Errorf("coordinator: killing coordinator %d: %w", sighting.PID, killErr)
	}
	gone, err := s.waitGone(ctx, proc, s.cfg.Grace)
	if err != nil {
		return err
	}
	if !gone {
		return fmt.Errorf("coordinator: coordinator %d is still running after SIGKILL", sighting.PID)
	}
	return nil
}

// waitGone polls until the process no longer exists, the bound elapses or
// the context is cancelled.
//
// Signal 0 is the question "may I signal this process", which is the only
// portable way to ask whether a process that is not our child still exists.
// The daemon usually is NOT our child — a window that finds an incompatible
// coordinator did not start it — so os.Process.Wait is not available.
func (s *SignalStopper) waitGone(ctx context.Context, proc *os.Process, within time.Duration) (bool, error) {
	deadline := time.Now().Add(within)
	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()
	for {
		err := proc.Signal(syscall.Signal(0))
		if isGone(err) {
			return true, nil
		}
		if err != nil && !errors.Is(err, os.ErrProcessDone) {
			// EPERM: something is there and it is not ours to signal.
			// That is not "gone", and pretending otherwise would have the
			// launcher spawn a second daemon beside a live one.
			return false, fmt.Errorf("coordinator: checking coordinator %d: %w", proc.Pid, err)
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, fmt.Errorf("coordinator: waiting for coordinator %d to stop: %w", proc.Pid, ctx.Err())
		case <-ticker.C:
		}
	}
}

// isGone reports whether an error from a signal means the process has
// already ended.
func isGone(err error) bool {
	return errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH)
}
