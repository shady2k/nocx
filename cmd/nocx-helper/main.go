// Command nocx-helper is the helper binary. It runs on EVERY machine,
// including yours (level-1 design D11), and it has one boundary: a private
// Unix socket, mode 0600, in a 0700 directory (§5).
//
//	nocx-helper serve            hold the endpoint for this generation and
//	                             serve every connection that reaches it
//	nocx-helper bridge <gen>     connect to that generation's endpoint and
//	                             copy bytes between it and stdin/stdout
//	nocx-helper --licenses       print the third-party notices this binary
//	                             carries (it links libghostty-vt statically)
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
	"github.com/shady2k/nocx/internal/helper/notices"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/mcpstdio"
	"github.com/shady2k/nocx/internal/shellintegration"
)

func main() {
	// stdout is the wire — for the bridge it is literally the ssh channel —
	// so every diagnostic goes to stderr (D22).
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	args := os.Args[1:]
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if len(args) == 3 && args[0] == "mcp" && args[1] == "--socket" && args[2] != "" {
		if err := mcpstdio.Serve(ctx, os.Stdin, os.Stdout, args[2]); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("mcp", "err", err)
			os.Exit(1)
		}
		return
	}
	if len(args) == 1 && args[0] == "--licenses" {
		// A binary that links libghostty-vt statically owes the licences of
		// what is inside it, and this helper is distributed twice over: it
		// ships inside the app for the local install, and it is written to
		// hosts nobody here controls. Carrying the notices INSIDE it is what
		// makes one installed file complete — D7's install is
		// content-addressed on this binary alone, so a sibling file would be
		// bytes the install's own completeness claim does not cover — and
		// printing them is what makes them readable there.
		//
		// Handled before the executable is hashed and before any socket is
		// touched: it answers a question about the FILE, so it must work on a
		// host where nothing else about this binary does.
		doc, err := notices.Document()
		if err != nil {
			fmt.Fprintf(os.Stderr, "nocx-helper: %v\n", err)
			os.Exit(1)
		}
		if _, err := os.Stdout.Write(doc); err != nil {
			fmt.Fprintf(os.Stderr, "nocx-helper: printing the third-party notices: %v\n", err)
			os.Exit(1)
		}
		return
	}

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

	switch {
	case len(args) == 1 && args[0] == endpoint.ServeCommand:
		// The DAEMON gets a file; the bridge keeps stderr alone, which is
		// where D22 puts it and where somebody is actually reading (servelog.go).
		serveLog, logFile := openServeLog(home, string(generation))
		code := serve(ctx, serveLog, dir, generation, contentHash, exe)
		if logFile != nil {
			_ = logFile.Close()
		}
		os.Exit(code)
	case len(args) == 2 && args[0] == endpoint.BridgeCommand:
		os.Exit(bridge(ctx, log, dir, proto.GenerationID(args[1]), generation, exe))
	default:
		fmt.Fprintf(os.Stderr, "usage: nocx-helper %s | nocx-helper %s <generation> | nocx-helper mcp --socket <path> | nocx-helper --licenses\n",
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
	// NOCX_TOOL_SOCKET, read here rather than derived: this process is not
	// the coordinator and has no way to compute a coordinator's tool.sock
	// path itself (internal/toolendpoint owns that name, AD-8) — it is
	// only ever a fact the coordinator that forked this daemon already
	// knew and handed down as this process's own environment (Ensure, in
	// internal/helper/endpoint/bridge.go, is the one place that sets it;
	// on the remote/bridge path nothing sets it, which is the honest
	// answer there — nocx-2tesu). Read once, at composition, and carried
	// for the daemon's whole life: it is a property of which coordinator
	// started this generation, never of one spawn request.
	agentToolSocketPath := os.Getenv(shellintegration.ToolSocketEnvVar)
	// What a pane's shell must exec to reach this generation's MCP adapter:
	// THIS binary. Read here, once, for the same reason the socket above is —
	// it is a property of the daemon and not of one spawn request — and taken
	// from os.Executable() rather than handed down, because a path from the
	// coordinator could name a different generation than the one that forks
	// the shell. Unreadable is not fatal: the wrapper's PATH fallback stands
	// and the pane says its tool surface is unavailable, which is the honest
	// degrade rather than a daemon that refuses to serve (nocx-o36tr).
	agentHelperPath, err := os.Executable()
	if err != nil {
		log.Warn("nocx-helper: cannot name its own executable for a pane's agent", "error", err)
		agentHelperPath = ""
	}
	sessions := session.New(session.Options{
		Generation: generation,
		Spawner:    session.NewLocalSpawner(log, session.Shell{}, agentToolSocketPath, agentHelperPath),
		Inspector:  session.NewInspector(),
		Log:        log,
		Limits:     session.DefaultLimits(),
	})
	defer sessions.Close()

	// THE SSH SERVICE THIS DAEMON SERVES, where the build has one
	// (nocx-50w7p.2). The client and the service are opened here, beside the
	// sessions and out of the accept loop, for the same reason they are: they
	// are properties of the DAEMON, not of one connection — one client serves
	// every host this machine hosts for its whole life — and they are released
	// at shutdown with them.
	//
	// holdSSHClient is a build fact: a helper built with nocx_local_ssh opens
	// a client and a service and says so, and one built without it — every
	// artifact `make helpers` produces, which is what reaches a host nobody
	// here controls — returns a seam that registers nothing, so every ssh op is
	// answered `unknown_service`.
	sshCap, err := holdSSHClient(log)
	if err != nil {
		log.Error("ssh client", "err", err)
		return 1
	}
	defer sshCap.release()

	if err := endpoint.Serve(ctx, ln, func(conn net.Conn) {
		h := host.New(conn, conn, contentHash, instanceID, log)
		// Which services this build answers is decided by
		// registerHelperServices, in one place (services.go) — including
		// whether an ssh service is among them, which is the whole of what
		// nocx_local_ssh changes about a helper.
		registerHelperServices(h, hostsvc.New(factory), sessions, sshCap)
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
	// No extra environment: this generation is being reached over the ssh
	// exec lane, on a machine with no local tool.sock of the caller's to
	// relay (nocx-2tesu) — that concept exists only for the coordinator's
	// own machine, in internal/helper/local's reach().
	conn, err := endpoint.Ensure(ctx, dir, want, generation, exe, nil)
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
