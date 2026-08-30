package coordinator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// The launcher: find the coordinator, raise one when there is none, refuse
// one that is not ours.
//
// It is the client-side counterpart of [Server] and it lives in the same
// package for the reason the client does — one owner for one protocol. What
// it adds over the client is the three decisions the protocol itself does
// not make: whether to spawn, how long to wait, and what to do about a
// daemon whose version is not this build's (design §4, D4).

const (
	// DefaultReadyTimeout bounds the wait for a spawned daemon to answer
	// its socket. Generous, because a cold start opens a vault, a settings
	// store and a content database on a machine that may be busy; bounded,
	// because a window that waits forever is the failure mode this replaces
	// (herdr's SERVER_READY_TIMEOUT is the same decision).
	DefaultReadyTimeout = 20 * time.Second

	// DefaultPollInterval is how often the socket is asked during that
	// wait. The daemon publishes its socket by an atomic rename, so a poll
	// never catches a half-built one and the interval only decides how
	// much of a cold start is spent sleeping.
	DefaultPollInterval = 25 * time.Millisecond

	// spawnLockName is the launcher's lock, beside — and deliberately NOT
	// the same as — the daemon's own srv.lock.
	//
	// srv.lock is held by the daemon for its whole life, so a launcher that
	// tried to take it could never take it while anything was running. This
	// one is held only across "look, spawn, wait", which is exactly the
	// window in which two windows opening together would otherwise each
	// decide to raise a daemon.
	spawnLockName = "spawn.lock"
)

var errSpawnLockTimedOut = errors.New("coordinator: spawn lock wait timed out")

// NoticeKind names what a [Notice] is about. A type rather than a free
// string so a UI can switch on it without matching prose.
type NoticeKind string

// NoticeSessionsLost reports that a running coordinator was stopped and
// replaced, and that whatever it was running died with it.
//
// D4 permits the kill in A1 and requires the honesty: "A1 may kill the old
// coordinator and lose its sessions — saying so out loud." A log line is
// not saying it out loud, which is why this travels through an [Announcer]
// and not only through the logger.
const NoticeSessionsLost NoticeKind = "sessionsLost"

// Notice is something the person at the window must be told. It carries
// both a sentence to show and the facts behind it, so a surface can render
// either without re-deriving one from the other.
type Notice struct {
	Kind    NoticeKind
	Message string

	// Running is the coordinator that was replaced; Expected is this
	// build. Both are carried whole because a version alone does not
	// identify a build — two builds of one version differ by commit, which
	// is the case an update produces.
	Running          Build
	RunningProtocol  int
	Expected         Build
	ExpectedProtocol int
}

// Announcer surfaces a notice where a person will see it.
//
// The launcher runs before the renderer has connected to anything, so it
// cannot raise a notification through the transport — and by design it must
// not know how the shell shows things. The composition root supplies the
// surface; this package supplies the fact.
type Announcer interface {
	Announce(Notice)
}

// LauncherConfig is everything the launcher needs, from the composition
// root. Nothing defaults to a real implementation: a nil field is a
// configuration error, so no test can accidentally spawn a process and no
// binary can accidentally reach a double (AD-8).
type LauncherConfig struct {
	// Dir is the runtime directory — see [RuntimeDir]. Must be absolute.
	Dir string
	// Self is this build's identity: what the daemon's is compared against.
	Self ClientIdentity
	// Client asks the discovery socket who is serving it.
	Client Discoverer
	// Spawner raises a daemon when none is.
	Spawner Spawner
	// Stopper ends one that is incompatible.
	Stopper Stopper
	// Announcer surfaces what a person must be told.
	Announcer Announcer
	// ReadyTimeout bounds the wait for a spawned daemon. Zero means
	// DefaultReadyTimeout.
	ReadyTimeout time.Duration
	// PollInterval is how often the socket is asked. Zero means
	// DefaultPollInterval.
	PollInterval time.Duration
	// Logger receives the record. It never receives the token.
	Logger *slog.Logger
}

// Launch is what one start resolved to: the daemon this window will talk
// to, and how it came to be there.
type Launch struct {
	// Hello is the daemon's answer — the address and token the renderer
	// needs, and the versions that were checked before they were accepted.
	Hello Hello
	// Spawned reports that this launcher raised the daemon.
	Spawned bool
	// Replaced reports that an incompatible coordinator was stopped to
	// make room for it, and therefore that sessions were lost.
	Replaced bool
}

// Launcher finds or raises the coordinator for one runtime directory.
type Launcher struct {
	cfg    LauncherConfig
	socket string
	lock   string
}

// SocketPathIn is where the discovery socket lives inside a runtime
// directory. Exported because a launcher needs the path before there is a
// [Server] to ask for it — that is the whole situation it exists for — and
// because the name must have exactly one owner (server.go's socketName).
func SocketPathIn(dir string) string { return filepath.Join(dir, socketName) }

// NewLauncher validates the configuration and computes the paths. It
// touches no filesystem: everything that can fail against the disk fails in
// Launch, where a caller is already prepared to show a failure.
func NewLauncher(cfg LauncherConfig) (*Launcher, error) {
	switch {
	case cfg.Dir == "":
		return nil, errors.New("coordinator: no runtime directory")
	case !filepath.IsAbs(cfg.Dir):
		return nil, fmt.Errorf("coordinator: runtime directory %q is not absolute", cfg.Dir)
	case cfg.Client == nil:
		return nil, errors.New("coordinator: no discovery client")
	case cfg.Spawner == nil:
		return nil, errors.New("coordinator: no spawner")
	case cfg.Stopper == nil:
		return nil, errors.New("coordinator: no stopper")
	case cfg.Announcer == nil:
		return nil, errors.New("coordinator: no announcer")
	case cfg.Logger == nil:
		return nil, errors.New("coordinator: no logger")
	case cfg.Self.Protocol == 0:
		// A launcher that did not state its protocol version could not be
		// told it is mismatched, and would then accept any daemon at all.
		return nil, errors.New("coordinator: no protocol version stated for this build")
	}
	if cfg.ReadyTimeout <= 0 {
		cfg.ReadyTimeout = DefaultReadyTimeout
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	return &Launcher{
		cfg:    cfg,
		socket: SocketPathIn(cfg.Dir),
		lock:   filepath.Join(cfg.Dir, spawnLockName),
	}, nil
}

// Launch resolves the coordinator this window will use.
//
// Three outcomes, in the order they are tried: a compatible daemon is
// already running and is used as it is; nothing is running and one is
// raised; something incompatible is running and is replaced, loudly.
func (l *Launcher) Launch(ctx context.Context) (Launch, error) {
	sighting, err := l.cfg.Client.Hello(ctx)
	switch {
	case err == nil:
		if incompat := l.incompatibility(sighting.Hello); incompat != "" {
			return l.replace(ctx, sighting, incompat)
		}
		l.cfg.Logger.Info("coordinator: found a running coordinator",
			"pid", sighting.PID,
			"version", sighting.Hello.Build.Version,
			"commit", sighting.Hello.Build.Commit,
			"protocol", sighting.Hello.Protocol,
			"wsAddress", sighting.Hello.WSAddress,
		)
		return Launch{Hello: sighting.Hello}, nil
	case errors.Is(err, ErrNoCoordinator):
		hello, spawned, raiseErr := l.raise(ctx)
		if raiseErr != nil {
			return Launch{}, raiseErr
		}
		return Launch{Hello: hello, Spawned: spawned}, nil
	default:
		// A socket that answered something we could not use. Spawning a
		// second daemon on top of it would be a guess; reporting it is not.
		return Launch{}, NewLaunchFailure(
			FailureIncompatible,
			fmt.Sprintf("A coordinator at %s answered with an incompatible response.", l.socket),
			"Stop the other or older nocx coordinator, then retry.",
			err,
		)
	}
}

// incompatibility returns why the running daemon cannot be used, or "".
//
// BOTH halves are compared, because they fail differently and D4 needs
// both: the protocol says whether the two can talk at all, and the build
// says whether an update has swapped the bundle underneath a coordinator
// that is still running the old code. An update leaves the protocol
// unchanged more often than not, so a protocol-only check would certify
// exactly the mixed pair D4 exists to prevent.
//
// The commit is part of the build identity, not decoration: two builds
// numbered "dev" are the normal case on a developer's machine and are
// routinely different code.
func (l *Launcher) incompatibility(h Hello) string {
	if h.Protocol != l.cfg.Self.Protocol {
		return fmt.Sprintf("it speaks discovery protocol %d and this build speaks %d",
			h.Protocol, l.cfg.Self.Protocol)
	}
	if h.Build.Version != l.cfg.Self.Version || h.Build.Commit != l.cfg.Self.Commit {
		return fmt.Sprintf("it is nocx %s (commit %s) and this window is nocx %s (commit %s)",
			h.Build.Version, h.Build.Commit, l.cfg.Self.Version, l.cfg.Self.Commit)
	}
	return ""
}

// replace stops an incompatible coordinator and raises the right one.
//
// The announcement comes FIRST, before anything is killed. A stop that then
// fails must still leave the person told what was attempted and why —
// announcing afterwards would make the honesty conditional on the kill
// working, which is precisely backwards.
func (l *Launcher) replace(ctx context.Context, sighting Sighting, why string) (Launch, error) {
	notice := Notice{
		Kind: NoticeSessionsLost,
		Message: fmt.Sprintf(
			"The nocx backend that was already running has been stopped and replaced, because %s. "+
				"Any sessions it was running — commands, SSH connections, agent runs — have ended.",
			why),
		Running:          sighting.Hello.Build,
		RunningProtocol:  sighting.Hello.Protocol,
		Expected:         Build{Version: l.cfg.Self.Version, Commit: l.cfg.Self.Commit},
		ExpectedProtocol: l.cfg.Self.Protocol,
	}
	l.cfg.Announcer.Announce(notice)
	l.cfg.Logger.Warn("coordinator: replacing an incompatible coordinator",
		"reason", why,
		"pid", sighting.PID,
		"runningVersion", sighting.Hello.Build.Version,
		"runningCommit", sighting.Hello.Build.Commit,
		"runningProtocol", sighting.Hello.Protocol,
	)
	if err := l.cfg.Stopper.Stop(ctx, sighting); err != nil {
		return Launch{}, NewLaunchFailure(
			FailureIncompatible,
			fmt.Sprintf("The incompatible coordinator at %s could not be stopped.", l.socket),
			"Stop the other or older nocx coordinator, then retry.",
			err,
		)
	}
	hello, spawned, err := l.raise(ctx)
	if err != nil {
		return Launch{}, err
	}
	// The daemon that came up must be OURS. Without this check a
	// replacement that lost a race — to another window, or to a stale
	// binary on the path — would be accepted silently, which is the mixed
	// pair again with an extra step.
	if incompat := l.incompatibility(hello); incompat != "" {
		return Launch{}, NewLaunchFailure(
			FailureIncompatible,
			fmt.Sprintf("The replacement coordinator at %s is still incompatible with this build.", l.socket),
			"Stop the other or older nocx coordinator, then retry.",
			fmt.Errorf("coordinator replacement is incompatible: %s", incompat),
		)
	}
	return Launch{Hello: hello, Spawned: spawned, Replaced: true}, nil
}

// raise takes the spawn lock, re-checks, spawns and waits for readiness.
//
// The re-check inside the lock is what makes two windows opening together
// raise ONE daemon rather than two: the loser of the race arrives here
// after the winner has already published a socket, sees it, and uses it.
// The lock alone would not do that — it would only serialise two spawns.
func (l *Launcher) raise(ctx context.Context) (Hello, bool, error) {
	// The daemon makes this directory too, and asserts its mode and owner
	// when it does (Server.prepareDir). Here it is created only so the lock
	// has somewhere to live; the ownership check is deliberately not
	// repeated, because a directory that is not ours makes the daemon
	// refuse to start and that refusal reaches this launcher as a readiness
	// failure naming it. One owner for that check.
	if err := os.MkdirAll(l.cfg.Dir, 0o700); err != nil {
		return Hello{}, false, NewLaunchFailure(
			FailureProfileUnusable,
			fmt.Sprintf("The profile runtime directory %s could not be created or used.", l.cfg.Dir),
			fmt.Sprintf("Check that %s can be read and written, then retry.", l.cfg.Dir),
			err,
		)
	}

	lock, err := l.takeSpawnLock(ctx)
	if err != nil {
		if errors.Is(err, errSpawnLockTimedOut) ||
			errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) {
			return Hello{}, false, l.notReadyFailure(
				"The nocx backend could not become ready because another launch is still in progress.",
				err,
			)
		}
		return Hello{}, false, l.profileFailure(
			fmt.Sprintf("The profile runtime directory %s could not be used for launching.", l.cfg.Dir),
			err,
		)
	}
	defer func() {
		if relErr := lock.release(); relErr != nil {
			l.cfg.Logger.Warn("coordinator: releasing the spawn lock", "error", relErr)
		}
	}()

	if sighting, helloErr := l.cfg.Client.Hello(ctx); helloErr == nil {
		if incompat := l.incompatibility(sighting.Hello); incompat != "" {
			// Somebody else raised a daemon while we waited for the lock,
			// and it is not one we can use. Replacing it here would be a
			// second launcher killing a daemon a first one just started —
			// so this stops and says so instead.
			return Hello{}, false, NewLaunchFailure(
				FailureIncompatible,
				fmt.Sprintf("A coordinator at %s was raised by another launcher, but this build cannot use it.", l.socket),
				"Stop the other or older nocx coordinator, then retry.",
				fmt.Errorf("coordinator build is incompatible: %s", incompat),
			)
		}
		l.cfg.Logger.Info("coordinator: another launcher raised the coordinator first", "pid", sighting.PID)
		return sighting.Hello, false, nil
	}

	spawned, err := l.cfg.Spawner.Spawn(ctx)
	if err != nil {
		return Hello{}, false, NewLaunchFailure(
			FailureServerBinaryUnusable,
			"The nocx server binary could not be started.",
			serverBinaryRemedy(),
			err,
		)
	}
	hello, err := l.waitReady(ctx, spawned)
	if err != nil {
		return Hello{}, false, err
	}
	return hello, true, nil
}

// takeSpawnLock acquires the exclusive spawn lock, waiting for whoever
// holds it.
//
// A retry loop rather than a blocking flock: a blocking one cannot be
// cancelled by a context, and a window whose user has given up must not
// leave a thread wedged in the kernel. The bound is the readiness timeout,
// because what the holder is doing is exactly a readiness wait.
func (l *Launcher) takeSpawnLock(ctx context.Context) (*fileLock, error) {
	deadline := time.Now().Add(l.cfg.ReadyTimeout)
	ticker := time.NewTicker(l.cfg.PollInterval)
	defer ticker.Stop()
	for {
		lock, err := acquireLock(l.lock)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, ErrAlreadyRunning) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w: another launcher has held %s for %s without "+
				"raising a coordinator", errSpawnLockTimedOut, l.lock, l.cfg.ReadyTimeout)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("coordinator: waiting for the spawn lock %s: %w", l.lock, ctx.Err())
		case <-ticker.C:
		}
	}
}

// waitReady polls the socket until the spawned daemon answers a hello.
//
// Readiness is "it answered", not "the file exists": the daemon binds on a
// temporary name and renames it into place precisely so that a socket which
// exists is a socket that is listening, and a launcher that stopped at the
// file would still have to handle the answer failing.
//
// A daemon that EXITS before answering ends the wait immediately with its
// status. Without that, the readiest failure in the whole design — a second
// nocx-server refusing the directory (exit 3) — would be reported as a
// timeout, which says nothing about what happened.
func (l *Launcher) waitReady(ctx context.Context, spawned Spawned) (Hello, error) {
	deadline := time.Now().Add(l.cfg.ReadyTimeout)
	ticker := time.NewTicker(l.cfg.PollInterval)
	defer ticker.Stop()
	var last error
	for {
		select {
		case exitErr, ok := <-spawned.Exit:
			if ok || exitErr != nil {
				return Hello{}, l.notReadyFailure(
					fmt.Sprintf("The nocx backend %s (process %d) exited before its discovery socket at %s became ready.",
						spawned.Command, spawned.PID, l.socket),
					exitErr,
				)
			}
		default:
		}

		sighting, err := l.cfg.Client.Hello(ctx)
		if err == nil {
			l.cfg.Logger.Info("coordinator: the coordinator we started is serving",
				"pid", spawned.PID,
				"socket", l.socket,
				"version", sighting.Hello.Build.Version,
				"protocol", sighting.Hello.Protocol,
				"wsAddress", sighting.Hello.WSAddress,
			)
			return sighting.Hello, nil
		}
		last = err
		if !errors.Is(err, ErrNoCoordinator) {
			// The socket is there and answering something unusable. More
			// waiting cannot fix that.
			return Hello{}, l.notReadyFailure(
				fmt.Sprintf("The nocx backend %s (process %d) answered on %s but did not provide a usable coordinator.",
					spawned.Command, spawned.PID, l.socket),
				err,
			)
		}
		if time.Now().After(deadline) {
			return Hello{}, l.notReadyFailure(
				fmt.Sprintf("The nocx backend %s (process %d) did not become ready on %s within %s.",
					spawned.Command, spawned.PID, l.socket, l.cfg.ReadyTimeout),
				last,
			)
		}
		select {
		case <-ctx.Done():
			return Hello{}, l.notReadyFailure(
				fmt.Sprintf("The nocx backend %s (process %d) was not ready before launch was canceled.",
					spawned.Command, spawned.PID),
				ctx.Err(),
			)
		case <-ticker.C:
		}
	}
}

func (l *Launcher) profileFailure(message string, cause error) *LaunchFailure {
	return NewLaunchFailure(
		FailureProfileUnusable,
		message,
		fmt.Sprintf("Check that %s can be read and written, then retry.", l.cfg.Dir),
		cause,
	)
}

func (l *Launcher) notReadyFailure(message string, cause error) *LaunchFailure {
	return NewLaunchFailure(
		FailureNotReady,
		message,
		"Retry the launch; the daemon did not become ready.",
		cause,
	)
}

func serverBinaryRemedy() string {
	switch runtime.GOOS {
	case "darwin":
		return "Reinstall nocx, then retry."
	case "linux":
		return "Repair the nocx server binary under ~/.local/share/nocx/bin, then retry."
	default:
		return "Reinstall nocx, then retry."
	}
}
