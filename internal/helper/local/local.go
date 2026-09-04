// Package local is this machine as a helper host.
//
// The machine you are sitting at is an ordinary entry in the helper inventory
// (L1 of the local-helper design, D11 of level 1), and it differs from a
// remote host in exactly one thing: the carrier is a private Unix socket
// rather than an ssh exec lane. Everything above the carrier is the same code
// — the same client.Dial, the same hello / sentinel / hello-ok, the same
// session service — because a second mechanism for one machine is a second
// answer to every question the first one already answers.
//
// # The handshake is not skipped here
//
// The obvious local shortcut is to trust the socket: we installed the binary,
// we started it, why ask it what it is. Because the hello-ok is the only thing
// that proves the process answering is the generation we installed (D21), and
// a stale binary under ~/.nocx is likelier on the machine where builds land
// than on a server. The socket's NAME carries only the first 64 bits of the
// generation; the handshake carries the whole content hash, and it is the
// handshake that decides.
//
// # One installer, two transports
//
// The artifact ships embedded in the app, so locally there is nothing to
// upload: Install writes the same content-addressed directory the sftp
// installer writes, from the same bytes, keyed by the same content hash. A
// second local-only install path would be a second answer to "which build is
// serving", which is the question the content hash exists to answer once.
//
// # What this package deliberately does not do
//
// It does not PRUNE. The remote installer prunes every generation but the one
// it just installed, which is safe there because the install is followed by
// the only helper that host runs; locally a previous generation may still be
// serving somebody's shells, and nothing retires a generation yet — the daemon
// lifecycle (D2) is unimplemented and retirement is its own work. Removing an
// install directory out from under a live daemon is not a footprint
// optimisation, it is ending sessions nobody asked to end.
//
// It does not embed the artifact either: the bytes come in through
// deploy.ArtifactSource, so the helper binary can link this package for its
// own endpoint probe without embedding its previous builds.
package local

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"runtime"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/deploy"
	"github.com/shady2k/nocx/internal/helper/endpoint"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// Platform is this machine's build target. It is what the remote path learns
// by running `uname -s -m` over the exec lane — locally the process asking is
// the process running, so the answer is already in the binary and a probe
// would be a second, worse source for it.
func Platform() deploy.Platform {
	return deploy.Platform{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
}

// Installed is one complete install: where the binary is, and which generation
// it IS. The two travel together because they are one fact — the generation is
// the content hash of that file — and separating them is how a caller ends up
// dialling one generation's socket while starting another's binary.
type Installed struct {
	Binary     string
	Generation proto.GenerationID
}

// Install writes the artifact for this platform into ~/.nocx/helper, in the
// D7 content-addressed layout, and answers with the binary and its generation.
// A complete directory for the same generation is reused and nothing is
// written.
//
// home is a parameter rather than something this package reads from the
// environment, and that is the same discipline endpoint.Dir applies for the
// same reason: the coordinator, the daemon and the bridge must agree on one
// path without sharing an environment.
//
// What each of the install's failure boundaries leaves behind, and which trees
// the next attempt may reuse, is stated where the sequence lives — see the
// interval on deploy.Ensure. It holds unchanged with the filesystem as the
// transport: the local errors differ (ENOSPC, EACCES, a read-only mount) and
// the two states they can leave do not.
func Install(ctx context.Context, src deploy.ArtifactSource, home string) (Installed, error) {
	binary, contentHash, err := deploy.Ensure(ctx, FS{}, src, home, Platform())
	if err != nil {
		return Installed{}, err
	}
	return Installed{Binary: binary, Generation: proto.GenerationID(contentHash)}, nil
}

// Config is everything Open needs to reach one generation on this machine.
type Config struct {
	// Dir is the endpoint directory — endpoint.Dir(home).
	Dir string
	// Generation is the generation to reach. It names the socket and it is
	// the content hash the handshake must come back with.
	Generation proto.GenerationID
	// Binary is the installed helper Open may start when nothing is serving.
	// EMPTY MEANS THIS CALLER MAY NOT START ONE, and that is a real caller
	// rather than a degenerate case: the daemon's own pre-bind probe asks
	// whether a helper of its generation is already serving, and a process
	// about to bind the endpoint must not spawn a competitor for it.
	Binary string
	// SentinelTTL bounds the handshake; zero is client.DefaultSentinelTTL.
	SentinelTTL time.Duration
	Log         *slog.Logger
}

// Open reaches the helper for one generation on this machine: it dials the
// endpoint (starting the installed binary when nothing is serving and the
// caller supplied one), then performs the full handshake over that socket.
//
// The failures are told apart by sentinel, because the product renders them
// as different sentences: endpoint.ErrNoEndpoint is "nothing is serving and
// this caller may not start one"; an error from the start says the process
// did not come up; client.ErrHashMismatch, ErrNotOurHelper, ErrSentinelTimeout
// and ErrLost are all "something answered and it is not the generation we
// installed".
//
// ONE CASE HAS NO STABLE SENTINEL YET, and it is written down rather than
// smoothed over, because L4's refusal is owed a reason from a CLOSED SET and
// this one is not in any set. A peer that answers on the socket and then hangs
// up mid-handshake produces three different errors depending on which
// goroutine wins: ErrNotOurHelper (the pump saw what was said), ErrLost (the
// carrier calls a peer close transport loss — SocketConn, and its own test
// pins that), or a bare EPIPE from the hello write. Over an ssh exec lane
// those are three distinct events; over a socket they are one event with
// three observers, and client.Dial selects over them, which Go resolves at
// random. Nothing here papers over it: a caller cannot switch on that answer
// today, and the fix belongs where the carrier decides what a peer close IS,
// not in a wrapper that renames it.
//
// NOTHING IS REPAIRED ON A REFUSAL. A socket that accepts and then answers
// something unexpected stays exactly where it is: it may be somebody's live
// daemon speaking a protocol this build does not understand, and unlinking it
// would end their sessions to tidy up our own confusion. Only a helper about
// to BIND removes a socket, and only after a dial to it was refused
// (endpoint.Listen). A daemon this call started and then failed to handshake
// with is left running for the same reason — it may already hold a PTY, and
// the next attempt will ask it again rather than guess.
func Open(ctx context.Context, cfg Config) (*client.Client, error) {
	conn, err := reach(ctx, cfg)
	if err != nil {
		return nil, err
	}
	// The carrier owns the connection from here: on every failure below it is
	// the carrier that closes, so there is exactly one owner of the socket
	// from this line until the returned client is closed.
	carrier := client.NewSocketConn(conn)
	c, err := client.Dial(ctx, client.Config{
		Exec: carrier,
		// No command, deliberately: the exec lane LAUNCHES the helper and the
		// command names which binary, while this socket is already being
		// served and the generation was decided by which socket was dialled.
		// The carrier refuses a command outright rather than ignoring one.
		ExpectHash:  string(cfg.Generation),
		SentinelTTL: cfg.SentinelTTL,
		Log:         cfg.Log,
	})
	if err != nil {
		_ = carrier.Close()
		return nil, err
	}
	return c, nil
}

// reach gets a connection to the endpoint, starting a helper when the caller
// supplied a binary to start.
func reach(ctx context.Context, cfg Config) (net.Conn, error) {
	if cfg.Binary == "" {
		return endpoint.Dial(ctx, cfg.Dir, cfg.Generation)
	}
	// Both generations are cfg.Generation, and they are the same value for a
	// reason rather than by omission: locally the binary we may start IS the
	// generation we are reaching for, because Install put it there under its
	// own content hash. The two arguments differ only for the bridge, which
	// runs one generation's binary and may be asked for another's.
	conn, err := endpoint.Ensure(ctx, cfg.Dir, cfg.Generation, cfg.Generation, cfg.Binary)
	if err != nil {
		return nil, fmt.Errorf("local helper %s: %w", short(cfg.Generation), err)
	}
	return conn, nil
}

// short names a generation in a message; the whole content hash says nothing
// more to a reader than its head does.
func short(gen proto.GenerationID) string {
	const head = 16
	if len(gen) <= head {
		return string(gen)
	}
	return string(gen[:head])
}
