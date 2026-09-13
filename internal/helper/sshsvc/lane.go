//go:build nocx_local_ssh

package sshsvc

// The LANE op: one pty-less EXEC LANE on a pooled connection, running the
// installed helper of a named generation in its bridge subcommand.
//
// # Why this is an op and what it replaces
//
// Git over a remote helper reaches that helper through a process on the far
// host: `nocx-helper bridge <generation>`, which connects to the generation's
// endpoint socket and copies the frame protocol between it and its own stdin
// and stdout. Until this op existed the COORDINATOR opened that exec lane
// itself (internal/app's bridgeCommand over ssh.RealClient.HelperConn), and
// under the owner's invariant — no ssh connection without a helper, and the
// coordinator holds no ssh client — that dial has to be the helper's like
// every other one (nocx-50w7p.10, plan §3's `ssh.lane(machine, generation)`).
//
// # The command is built HERE, and that is D3 rather than convenience
//
// A lane runs exactly one command, and the caller names none of it: it names a
// MACHINE (home and platform) and a GENERATION, and this helper turns those
// into the one invocation it is allowed to make —
// deploy.InstalledBinary(...) + endpoint.BridgeInvocation(...), both of which
// are the install layout's own derivation rather than a second one — so a
// caller cannot reach an argument list on somebody else's machine. That is the
// same rule `ssh.open` states with its closed set of channel kinds, applied to
// the one channel whose payload is a program.
//
// # What a lane carries that no other channel does
//
// An exit status. A subsystem and a connection end; a process EXITS, and the
// coordinator's own exec lane read that status to tell "no helper is serving
// that generation" (the bridge's own exit 43) from "the host did not answer
// with our helper" (internal/helper/client's pump, D5). So the closed event
// carries it (proto.ChannelClosedEvent.Exit) and it is asked for exactly once,
// after the read loop ends — never on a close the coordinator asked for, which
// would wait for a bridge that is still serving somebody.
//
// The remote helper's own ABI is unchanged by this file: what rides the lane
// is the frame protocol, and what is on the far end is the same bridge
// subcommand the coordinator used to start.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"path"

	"github.com/shady2k/nocx/internal/helper/deploy"
	"github.com/shady2k/nocx/internal/helper/endpoint"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// errBadLaneParams is a lane this helper will not open: a request that does not
// name a destination, an install or a generation. Refused before anything is
// dialed, for the reason errBadChannelParams is — a dial that cannot end in the
// command it was asked for would report the far side's failure as if it were an
// answer about the request.
var errBadLaneParams = errors.New("lane params are incomplete")

// maxRemoteStderrLine bounds one line of the remote bridge's diagnostics. The
// far side is not ours to trust with this helper's memory, and the lines that
// matter here are short log lines and shell errors.
const maxRemoteStderrLine = 64 * 1024

// laneOps adds the lane op to the service's own.
func (s *Service) laneOps() []string { return []string{proto.OpLane} }

// laneEnd is a session channel carrying one command — the remote helper's
// bridge — and the exit status it ended with.
//
// It is a remoteEnd like a subsystem's, and the difference is the half the
// channel machinery does not use: a subsystem is a stream, while this is a
// process whose ending is itself a fact (readEnd).
type laneEnd struct {
	sess   *gossh.Session
	stdin  io.WriteCloser
	stdout io.Reader
}

func (e *laneEnd) Read(p []byte) (int, error)  { return e.stdout.Read(p) }
func (e *laneEnd) Write(p []byte) (int, error) { return e.stdin.Write(p) }
func (e *laneEnd) Close() error                { return e.sess.Close() }

// exitStatus asks the far process how it ended. It is called once, after the
// read loop has ended, which is the only moment it cannot block: x/crypto
// reports a session's end as io.EOF on its stdout, and Wait then answers from
// the exit-status request the peer has already sent.
//
// A non-zero status is NOT an error here (x/crypto's ExitError is the ordinary
// spelling of one) and a status that never arrives is not an exit status at
// all: the second case is a transport that died, and the channel reports it as
// no exit rather than as exit 0.
func (e *laneEnd) exitStatus() (int, bool) {
	err := e.sess.Wait()
	if err == nil {
		return 0, true
	}
	var exit *gossh.ExitError
	if errors.As(err, &exit) {
		status := exit.ExitStatus()
		if status < 0 || status > maxExitStatus {
			// A status the wire cannot carry. What the peer sent is whatever
			// its exit-status message held, and a value outside the POSIX range
			// is not a status at all: reporting it would put a number nobody
			// can read into the coordinator's classification, and reporting it
			// as NO status is the honest answer (the same one a transport that
			// went gets).
			return 0, false
		}
		return status, true
	}
	return 0, false
}

// maxExitStatus bounds an exit status at the interface: POSIX statuses are 0-255
// (a signal is reported as 128+n), and the wire field is narrower than the int
// the ssh library hands over.
const maxExitStatus = 255

// openLane performs one `ssh.lane`.
//
// It is openChannel's shape with one op's worth of difference: the far end is a
// session started with ONE command this helper built, and the pooled reference
// is taken exactly as a channel's is — a lane rides the connection the rest of
// this destination's consumers ride (AD-4), and it holds a reference for its
// whole life so the transport cannot close under a live frame protocol.
func (s *Service) openLane(ctx context.Context, p proto.LaneParams) (proto.OpenChannelResult, error) {
	conn, _ := host.ConnectionFrom(ctx).(*host.Host)
	if conn == nil {
		return proto.OpenChannelResult{}, errNoAuthChannel
	}
	if err := validateLane(p); err != nil {
		return proto.OpenChannelResult{}, err
	}

	// The command, from the install layout's ONE derivation: the caller named
	// the install DIRECTORY it wrote and a generation, and what those mean is
	// this repository's install — deploy.InstalledBinary, the same expression
	// Ensure's returned path is built from.
	// The ONE place in this repository where an argv is assembled for a
	// remote host, over typed inputs and for a caller that named a machine and
	// a generation and nothing else (D3). It is written here rather than in
	// internal/helper/endpoint because its only caller IS this op — the
	// coordinator's own exec lane, which used to spell it too, is deleted —
	// and a builder whose only caller is behind a build tag is a function the
	// dead-code ratchet reports as unreachable on every build without it
	// (measured: `deadcode -whylive` on this very symbol).
	//
	// The subcommand's NAME still has one owner: endpoint.BridgeCommand, which
	// cmd/nocx-helper dispatches on, so a rename cannot leave a caller
	// spelling it the old way.
	command := deploy.InstalledBinary(p.Machine.Dir) + " " + endpoint.BridgeCommand + " " + string(p.Generation)

	// A lane never accepts a host key on trust, and it is the same decision
	// `ssh.open`'s callers make: the accept flow belongs to a pane open, where
	// a person is watching, and an unknown key here comes back as
	// host-key-unknown with its evidence so that flow can raise the sheet it
	// always did.
	//
	// The fingerprint is empty — no PINNED key — and that is the same answer
	// `open`, `forward` and the named probes give for this destination
	// (nocx-50w7p.14's pin exists where the key has already been DECIDED: a
	// pane or a shell whose spec carries the key the person accepted). A lane
	// arrives with a machine whose helper was installed under a consent
	// decision, not with a key: its verdict is the coordinator's own, taken
	// through the reverse registry on the handshake, and pinning a second
	// answer here would be a second place for the decision to live.
	pool, err := s.acquirePooled(ctx, conn, p.Destination, false, "")
	if err != nil {
		return proto.OpenChannelResult{}, err
	}
	end, err := s.startLane(pool, p.Destination.Host, command)
	if err != nil {
		_ = pool.Close()
		// The same classification a refused channel gets, for the same reason:
		// a refused exec is channel_refused, an unknown key is its own outcome
		// with the evidence, and anything else is a lost connection.
		return proto.OpenChannelResult{}, classifyChannelError(err)
	}

	id, err := mintChannelID()
	if err != nil {
		_ = end.Close()
		_ = pool.Close()
		return proto.OpenChannelResult{}, internalRefusal("mint a channel id: %v", err)
	}
	ch := &openChannel{id: id, conn: conn, pool: pool, end: end}
	if err := s.registerChannel(ch); err != nil {
		_ = end.Close()
		_ = pool.Close()
		return proto.OpenChannelResult{}, err
	}

	s.log.Info("ssh: lane opened",
		"channel", id.String(), "host", p.Destination.Host, "user", p.Destination.User,
		"generation", string(p.Generation))
	return proto.OpenChannelResult{Channel: id}, nil
}

// startLane opens a session on the pooled connection and starts ONE command on
// it, with no pty-req: a pty applies line discipline and would corrupt the
// frame protocol the bridge carries (D19).
func (s *Service) startLane(pool *ssh.PooledConn, host string, command string) (*laneEnd, error) {
	sess, err := pool.Client().NewSession()
	if err != nil {
		return nil, err
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		return nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		return nil, err
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		_ = sess.Close()
		return nil, err
	}
	if err := sess.Start(command); err != nil {
		_ = sess.Close()
		return nil, err
	}
	// The remote bridge's stderr is DIAGNOSTICS — the far daemon's own log
	// lines — and nothing here reads it, so it is drained and said rather than
	// left to fill: a pipe nobody reads is a bridge that blocks on its own log
	// line once the buffer fills, which would present as a hang in the frame
	// protocol.
	//
	// It is not carried to the coordinator, and that is the same choice the
	// socket carrier makes: HelperConn.Stderr is "diagnostics only", a socket
	// has no second stream, and a lane that is one of the two has no reason to
	// invent one.
	go s.drainBridgeStderr(host, stderr)
	return &laneEnd{sess: sess, stdin: stdin, stdout: stdout}, nil
}

// drainBridgeStderr reads the remote bridge's diagnostics so its process can
// never block writing them, and says each line at debug level — where it is
// useful for exactly the diagnosis this file's own failure would need.
func (s *Service) drainBridgeStderr(host string, r io.Reader) {
	lines := bufio.NewScanner(r)
	lines.Buffer(make([]byte, 0, 64*1024), maxRemoteStderrLine)
	for lines.Scan() {
		s.log.Debug("ssh: the remote bridge said", "host", host, "line", lines.Text())
	}
}

// validateLane refuses a lane that cannot be opened, before anything is dialed.
//
// Two of these checks are D3's rather than hygiene's. The generation and the
// directory are SPLICED INTO A COMMAND LINE by this helper, so a value that is
// more than the fact it claims to be would be an argv this op exists to refuse:
// the generation must be the hex content hash the installer writes, and the
// directory must be an absolute path. What remains — a destination that is not
// dialable — is the caller's own mistake and reads as one, which is why the
// checks are here rather than left to the far shell's error message.
func validateLane(p proto.LaneParams) error {
	if err := validateDestinationAddress(p.Destination); err != nil {
		return err
	}
	switch {
	case p.Generation == "":
		return fmt.Errorf("%w: no generation", errBadLaneParams)
	case !isContentHash(string(p.Generation)):
		return fmt.Errorf("%w: generation %q is not a content hash", errBadLaneParams, string(p.Generation))
	case p.Machine.Dir == "":
		return fmt.Errorf("%w: no install directory", errBadLaneParams)
	case !path.IsAbs(p.Machine.Dir):
		return fmt.Errorf("%w: install directory %q is not absolute", errBadLaneParams, p.Machine.Dir)
	}
	return nil
}

// isContentHash reports whether s is the hex sha256 the deploy key is
// (internal/helper/deploy: contentHash is hex.EncodeToString of a sha256).
// It refuses anything else rather than uppercasing or trimming it: a value that
// is not the hash the installer wrote names no install, and guessing at one
// would be this helper inventing a generation.
func isContentHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}
