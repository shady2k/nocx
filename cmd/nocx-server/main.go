// Command nocx-server is the nocx coordinator: the Go backend running as a
// process of its own rather than as a part of the Wails window, so a
// session and the work in it outlive the window (design §1).
//
// It is a normal foreground process. It does NOT daemonise itself: setsid,
// the /dev/null stdio and the readiness wait are the LAUNCHER's job, which
// is deliberate — the thing that must decide whether a daemon is already
// running is the thing that would otherwise raise a second one, and it is
// on the other side of this binary (design §4, "Client startup").
//
// What it adds over cmd/devharness, which is otherwise the same two calls,
// is the discovery socket: devharness prints its port and token on stdout
// for a test runner to grep, and a token on stdout is exactly what the
// coordinator's threat model forbids (design §6). Here the token leaves the
// process only through a unix socket whose peer uid has been checked.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/shady2k/nocx/internal/app"
	"github.com/shady2k/nocx/internal/coordinator"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/version"
)

func main() {
	// Every diagnostic goes to stderr: stdout carries nothing, because a
	// detached daemon's stdout is /dev/null and anything written there is
	// a fact nobody will ever read.
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if err := run(logger); err != nil {
		logger.Error("nocx-server stopped", "error", err)
		// A second daemon refusing to start is the lock working, not a
		// crash, and a launcher racing another window has to be able to
		// tell the two apart from an exit status alone.
		if errors.Is(err, coordinator.ErrAlreadyRunning) {
			os.Exit(3)
		}
		os.Exit(1)
	}
}

// run is the composition root: it constructs the real implementations and
// wires them, so nothing below has to choose between a real one and a
// double (AD-8).
func run(logger *slog.Logger) error {
	paths, err := storage.NewAppPaths()
	if err != nil {
		return err
	}

	// No options. The WS server's default is loopback with an OS-chosen
	// port, and a coordinator that could be told to bind elsewhere is a
	// coordinator that can be told to bind off loopback (design §6). The
	// keystore stance is D10's, not a flag's.
	a, err := app.New()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if startErr := a.Start(ctx); startErr != nil {
		return startErr
	}
	defer a.Shutdown(ctx)

	// After Start, so the address and the token exist to be handed out.
	// The token is read by the socket and by nothing else on this path: it
	// is not printed, not written to a file and not put in the
	// environment.
	socket, err := coordinator.NewServer(coordinator.Config{
		Dir:     coordinator.RuntimeDir(paths),
		Build:   coordinator.Build{Version: version.Version, Commit: version.Commit},
		Backend: backend{ws: a.Transport},
		Peers:   coordinator.SystemPeerCredentials{},
		Owner:   coordinator.SystemPathOwner{},
		SelfUID: coordinator.SelfUID(),
		Logger:  logger,
	})
	if err != nil {
		return err
	}
	if err := socket.Start(); err != nil {
		return err
	}
	defer func() {
		if err := socket.Close(); err != nil {
			logger.Error("closing the discovery socket", "error", err)
		}
	}()

	logger.Info("nocx-server ready",
		"version", version.Version,
		"commit", version.Commit,
		"protocol", coordinator.ProtocolVersion,
		"socket", socket.SocketPath(),
	)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	logger.Info("nocx-server shutting down")
	return nil
}

// wsBackend is the part of the running WS server this binary needs, which
// is two facts and no more. *transport.WSServer satisfies it.
type wsBackend interface {
	Addr() string
	Token() string
}

// backend adapts the transport to coordinator.Backend. The adapter is here,
// at the composition root, rather than on either side of it, so the
// coordinator package never sees a transport it could reach further into
// (AD-8).
type backend struct {
	ws wsBackend
}

func (b backend) WSAddress() string { return b.ws.Addr() }
func (b backend) WSToken() string   { return b.ws.Token() }
