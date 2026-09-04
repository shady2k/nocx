// Command nocx-helper is the helper binary. It runs on EVERY machine,
// including yours (level-1 design D11), and it has one boundary: a private
// Unix socket, mode 0600, in a 0700 directory (§5).
//
//	nocx-helper serve            hold the endpoint for this generation and
//	                             serve every connection that reaches it
//	nocx-helper bridge <gen>     connect to that generation's endpoint and
//	                             copy bytes between it and stdin/stdout
//
// Locally the coordinator connects to the endpoint directly. Remotely
// `bridge` runs over the pty-less ssh exec lane and the coordinator speaks the
// same protocol through it. There is no second mechanism, no local special
// case, and no code path that exists only for one of them: SSH is a transport
// for REACHING the helper, not the terminal protocol — the same relationship
// internal/apisend/ssh_dialer.go already has with HTTP.
//
// AD-1 is therefore untouched. The binary data plane is not re-wrapped in
// JSON-RPC; it is the same plane on a different socket.
//
// The helper serves the git service and the SESSION service — it spawns the
// shell and owns its PTY, which is what makes it the integration rather than a
// script (D3); files and ports are still reserved names.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/shady2k/nocx/internal/git/hostsvc"
	"github.com/shady2k/nocx/internal/git/local"
	"github.com/shady2k/nocx/internal/helper/endpoint"
	"github.com/shady2k/nocx/internal/helper/host"
	helperlocal "github.com/shady2k/nocx/internal/helper/local"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
)

func main() {
	// stdout is the wire — for the bridge it is literally the ssh channel —
	// so every diagnostic goes to stderr (D22).
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	exe, err := os.Executable()
	if err != nil {
		log.Error("executable", "err", err)
		os.Exit(1)
	}
	contentHash, err := hashFile(exe)
	if err != nil {
		log.Error("content hash", "err", err)
		os.Exit(1)
	}
	// The GENERATION is the content hash of this binary, because a helper
	// install is content-addressed: the generation is not a name assigned to
	// the build, it IS the build, so a durable session handle addresses the
	// exact install that minted it and needs no lookup service (D10).
	generation := proto.GenerationID(contentHash)

	home, err := os.UserHomeDir()
	if err != nil {
		log.Error("home", "err", err)
		os.Exit(1)
	}
	dir := endpoint.Dir(home)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	args := os.Args[1:]
	switch {
	case len(args) == 1 && args[0] == endpoint.ServeCommand:
		os.Exit(serve(ctx, log, dir, generation, contentHash, exe))
	case len(args) == 2 && args[0] == endpoint.BridgeCommand:
		os.Exit(bridge(ctx, log, dir, proto.GenerationID(args[1]), generation, exe))
	default:
		fmt.Fprintf(os.Stderr, "usage: nocx-helper %s | nocx-helper %s <generation>\n",
			endpoint.ServeCommand, endpoint.BridgeCommand)
		os.Exit(2)
	}
}

// serve holds the endpoint for this generation and serves every connection
// that reaches it, local or bridged.
//
// Starting twice is not an error to a user and is not treated as one: a helper
// of this generation that is ALREADY serving is answered by exiting 0, having
// changed nothing. That is the race two coordinators reaching for the same
// generation at the same time produce, and the socket is the only authority
// present on both sides of it.
func serve(ctx context.Context, log *slog.Logger, dir string, generation proto.GenerationID, contentHash, exe string) int {
	// Everything that can fail and is not the endpoint happens BEFORE the
	// bind. The instance id used to be minted after it, which left one window
	// in which the socket existed and this process was about to exit without
	// ever serving it — a socket with nothing behind it. Nothing repairs that
	// from this side: the next helper's Listen dials it, is refused, and
	// unlinks it (endpoint.clearStale), while a coordinator only ever reports
	// it as no endpoint. Shrinking the window is cheap, so it is shrunk; what
	// remains is bind → Serve, which cannot be removed because binding is what
	// makes serving possible, and a prober cannot mistake it for a slow
	// daemon: net.Listen creates the socket ALREADY LISTENING, so there is no
	// state in which the file exists and nothing has bound it, and a daemon
	// that is merely slow to reach its accept loop still accepts (the kernel
	// queues it) and is told apart by the handshake budget rather than by the
	// file.
	instanceID, err := randomID()
	if err != nil {
		log.Error("instance id", "err", err)
		return 1
	}

	if alreadyServing(ctx, log, dir, generation) {
		log.Info("a helper of this generation is already serving", "generation", generation)
		return 0
	}

	ln, err := endpoint.Listen(dir, generation)
	if err != nil {
		if errors.Is(err, endpoint.ErrAlreadyServing) {
			// Lost the bind between the probe and here. The winner serves;
			// this process changed nothing and has nothing to report.
			log.Info("another helper took the endpoint first", "generation", generation)
			return 0
		}
		log.Error("endpoint", "err", err)
		return 1
	}
	log.Info("serving", "endpoint", ln.Addr(), "generation", generation, "binary", exe)

	factory := local.NewFactory()
	defer factory.Stop()

	// The session service is PROCESS-scoped: it holds the PTYs, their windows
	// and the write capability, and it is constructed once, out here, beside
	// the accept loop rather than inside it. internal/helper/host is one
	// connection's protocol engine and is constructed per accept. That
	// division is the whole of D1 in code: a connection ending releases that
	// connection's reader and its write capability, and every session, window
	// and process survives it.
	sessions := session.New(session.Options{
		Generation: generation,
		Spawner:    session.NewLocalSpawner(log, session.Shell{}),
		Inspector:  session.NewInspector(),
		Log:        log,
		Limits:     session.DefaultLimits(),
	})
	defer sessions.Close()

	if err := endpoint.Serve(ctx, ln, func(conn net.Conn) {
		h := host.New(conn, conn, contentHash, instanceID, log)
		h.Register(hostsvc.New(factory))
		h.Register(sessions)
		// The connection is bound to the service, not the other way round:
		// the sessions outlive it. These are the two lines that used to sit
		// in main around one stdin/stdout connection and now sit inside the
		// accept loop, which is why Bind exists at all rather than the sink
		// being a constructor argument.
		release := sessions.Bind(h)
		defer release()

		if err := h.Serve(ctx); err != nil {
			// A version mismatch ends this CONNECTION and nothing else. It
			// was the process's exit code while the helper served exactly one
			// connection over stdin/stdout; a daemon holding somebody's
			// running shell may not exit because one caller spoke the wrong
			// version at it.
			log.Info("connection ended", "err", err)
		}
	}); err != nil {
		log.Error("serve", "err", err)
		return 1
	}
	return 0
}

// alreadyServing asks whether a helper of this generation is already holding
// the endpoint, by CONNECTING TO IT AS A COORDINATOR DOES — the same
// endpoint.Dial, the same carrier, the same handshake. A socket that accepts
// is only evidence that something accepts; a completed hello, sentinel and
// hello-ok carrying this content hash is the fact (D4: liveness is a fact,
// never an inference from an error).
//
// It takes the generation and NOT a content hash beside it, because they are
// the same value — the generation IS this binary's content hash — and two
// parameters for one fact is a drift waiting to be introduced.
//
// A "no" here is never a verdict either: it means this process saw nothing
// serving and may try to bind. If it is wrong, Listen finds the live socket
// and refuses.
func alreadyServing(ctx context.Context, log *slog.Logger, dir string, generation proto.GenerationID) bool {
	// The local carrier, which is one thing and not two: the probe a daemon
	// makes of its own generation and the connection a coordinator makes to it
	// are the same dial, the same socket adapter and the same handshake, so
	// there is no second implementation to drift.
	//
	// No binary is offered, and that is the whole difference between this
	// caller and the coordinator's: a process that is about to bind the
	// endpoint must not start a competitor for it.
	c, err := helperlocal.Open(ctx, helperlocal.Config{
		Dir:        dir,
		Generation: generation,
		Log:        log,
	})
	if err != nil {
		log.Info("nothing of this generation answers on the endpoint", "err", err)
		return false
	}
	_ = c.Close()
	return true
}

// bridge connects to the endpoint for the generation the coordinator asked for
// and copies bytes between it and this process's stdin and stdout — which, run
// over the pty-less ssh exec lane, are the ssh channel.
//
// It is stateless and disposable: it holds no session, no window and no lock,
// so killing it drops one attachment (D2) and ends nothing. Nothing is
// forwarded and no port forwarding is configured: the connection it makes is
// to a socket on the machine it is running on.
//
// It needs no authentication of its own, because reaching that socket already
// required becoming the account (D12). A non-ssh carrier must supply one; that
// is the carrier's problem, not this protocol's.
func bridge(ctx context.Context, log *slog.Logger, dir string, want, generation proto.GenerationID, exe string) int {
	conn, err := endpoint.Ensure(ctx, dir, want, generation, exe)
	if err != nil {
		log.Error("bridge", "generation", want, "err", err)
		if errors.Is(err, endpoint.ErrNoEndpoint) {
			return endpoint.ExitNoEndpoint
		}
		return 1
	}
	defer func() { _ = conn.Close() }()

	if err := endpoint.Bridge(ctx, os.Stdin, os.Stdout, conn); err != nil {
		log.Info("bridge ended", "err", err)
	}
	return 0
}

// hashFile hashes the running binary's bytes; the content hash travels in
// the hello-ok so the backend can verify the installed helper is the one it
// deployed (D7), and it is this install's generation (D10).
func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path) // #nosec G304 — the path is the running binary, from os.Executable()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// randomID mints the instance id that distinguishes one helper run from
// another.
func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
