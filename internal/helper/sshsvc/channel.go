//go:build nocx_local_ssh

package sshsvc

// The PROXIED-CHANNEL half of the `ssh` service: the pool, the `open`/`close`
// ops, and the bytes in between.
//
// # What this file is for
//
// The owner's invariant is that there is no ssh connection without a helper
// (plan §1-§3), and an invariant about connections is only half a design: a
// connection with no channel on it is a resource nothing uses. So the helper
// opens the channels too — an SFTP subsystem today — and hands the coordinator
// the raw bytes, which is what lets the coordinator's file and install code
// stay exactly where it is while the transport underneath it moves.
//
// # One connection per (destination, identity), channels multiplexed
//
// AD-4's pool, and this service acquires from the coordinator's own ssh
// package rather than keeping one of its own (ssh.RealClient.AcquirePooled):
// one authenticated connection per host+port+user+identity, ref-counted, with
// each channel holding its own reference. So an install and a file listing to
// one host share one transport, and the transport survives the first of them
// ending — which is the whole point of a pool rather than a dial per channel.
//
// The reference is taken per CHANNEL and released when that channel ends,
// whichever end ended it. A channel that is opened and never closed keeps the
// connection alive, and that is deliberate rather than a leak to be collected:
// the coordinator owns the channel's lifetime, and the helper ending a stream
// the caller still holds would be the helper deciding something it was not
// asked about.
//
// # The ordering that makes the open answerable
//
// A caller cannot address a channel before it has been told the channel's id,
// and the id is minted here. So this service must not write a byte of a channel
// until the open's response is on the wire — otherwise the caller's first
// frames arrive for an id it has never heard of and are dropped as orphaned,
// which for an SFTP handshake is a hang.
//
// That is arranged by construction and not by hoping: the channel is registered
// by the handler, but its reader pump starts in ResponseWritten, which the host
// calls AFTER the response frame has been written (host.ResponseObserver). The
// ordering is a happens-before edge on one goroutine, so there is no window to
// lose — see the type's own comment for why "start the pump in the handler"
// would have one.

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/sshdial"
	"github.com/shady2k/nocx/internal/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// channelReadBuf is one read's buffer: the largest payload that still fits in
// ONE TypeChannelData frame (proto.MaxChannelPayloadBytes — the frame bound
// minus the channel header, which is stated there rather than recomputed
// here). A larger read would produce a frame EncodeFrame PANICS on, so the
// bound is the smaller of the two and never the frame bound itself.
const channelReadBuf = proto.MaxChannelPayloadBytes

// errBadChannelParams is an open this helper will not perform: a request that
// does not name a destination, a credential, or a kind it implements.
var errBadChannelParams = errors.New("channel params are incomplete")

// errNoSuchChannel is a close for an id this helper does not hold. It is
// reported as success rather than as a refusal (see closeChannel): the
// ordinary caller is a process shutting down.
var errNoSuchChannel = errors.New("this helper holds no such channel")

// mintChannelID mints one channel's identity.
//
// It lives HERE and not in proto because the minting is the HELPER's act — the
// helper is the end that owns the channel, and the coordinator only echoes what
// it was told. A minter in the wire package would be a function whose only
// caller is a build-tagged service, which is a function the dead-code ratchet
// reports as unreachable on every build that does not pass the tag.
//
// Random rather than sequential: the coordinator hands the id back to the
// helper it got it from, so a counter would do, but a value that is also
// unforgeable costs nothing here and removes a class of cross-connection
// mix-ups from anybody reading a log.
func mintChannelID() (proto.ChannelID, error) {
	var id proto.ChannelID
	if _, err := rand.Read(id[:]); err != nil {
		return proto.ChannelID{}, fmt.Errorf("mint a channel id: %w", err)
	}
	return id, nil
}

// openChannel is one live proxied channel.
type openChannel struct {
	id   proto.ChannelID
	conn *host.Host
	// pool is the pooled reference this channel holds, released when the
	// channel ends. It is nil for a channel a FORWARD accepted: that
	// connection arrived on a listener, the listener holds the reference that
	// keeps the transport alive, and taking a second one per accepted
	// connection would make the listener's death and a stream's death two
	// states where the caller has one.
	pool *ssh.PooledConn
	// end is the far side of this channel: a subsystem's stream, or a
	// connection dialed for a direct-tcpip open, or an accepted forwarded
	// connection. Whichever it is, the bytes on it move the same way.
	end remoteEnd
	// owner is the listener this channel arrived on, nil for a channel an op
	// opened. A forwarded channel is registered in the same table an op-opened
	// one is — bytes are routed by id and close is close — and the owner is
	// what lets the listener end the streams it produced.
	owner *openForward

	closeOnce sync.Once
}

// remoteEnd is the far side of one proxied channel: where its bytes come from
// and go to, and how it is ended.
//
// It is an interface rather than x/crypto/ssh's Session because the two kinds
// this generation opens have nothing in common below the bytes. A subsystem
// arrives as two pipes on a session (and closing the SESSION is what unblocks a
// write wedged against a server that stopped reading); a direct-tcpip channel
// and a forwarded connection are both a net.Conn. The channel machinery —
// registration, the reader pump, the write path, the close — is identical for
// all three, which is exactly why it is written once against this.
type remoteEnd interface {
	io.Reader
	io.Writer
	Close() error
}

// exitReported is the optional half of a remoteEnd whose far end is a
// PROCESS rather than a stream, and a lane is the only one this generation
// has. It is asked exactly once, from the reader pump after the read loop has
// ended — never from finish's other callers, where the far end is still
// serving and waiting for it would park a close that a person asked for.
type exitReported interface {
	exitStatus() (int, bool)
}

// channelExit asks a channel's far end how it exited, for the closed event.
// A channel that is not a process reports none, and that absence is a fact on
// the wire rather than a zero: an sftp subsystem has no exit status at all.
func channelExit(end remoteEnd) *int32 {
	reported, ok := end.(exitReported)
	if !ok {
		return nil
	}
	code, ok := reported.exitStatus()
	if !ok {
		return nil
	}
	// The range is enforced where the status is READ (laneEnd.exitStatus), so
	// this conversion is total; gosec cannot see across the interface.
	exit := int32(code) // #nosec G115 -- bounded by maxExitStatus at the source
	return &exit
}

// subsystemEnd is a session channel carrying a subsystem.
type subsystemEnd struct {
	sess   *gossh.Session
	stdin  io.WriteCloser
	stdout io.Reader
}

func (e *subsystemEnd) Read(p []byte) (int, error)  { return e.stdout.Read(p) }
func (e *subsystemEnd) Write(p []byte) (int, error) { return e.stdin.Write(p) }
func (e *subsystemEnd) Close() error                { return e.sess.Close() }

// Ops adds the two channel ops to the service's own.
func (s *Service) channelOps() []string { return []string{proto.OpOpen, proto.OpClose} }

// openChannel performs one `ssh.open`.
//
// The handler does everything up to the point of reading bytes: it validates,
// acquires the pooled connection, opens the channel and registers it. The
// reader pump is NOT started here — it is started from ResponseWritten, once
// the caller can address what the pump will write about.
func (s *Service) openChannel(ctx context.Context, p proto.OpenChannelParams) (proto.OpenChannelResult, error) {
	conn, _ := host.ConnectionFrom(ctx).(*host.Host)
	if conn == nil {
		return proto.OpenChannelResult{}, errNoAuthChannel
	}
	if err := validateDestination(p); err != nil {
		return proto.OpenChannelResult{}, err
	}

	pool, end, err := s.dialChannel(ctx, conn, p)
	if err != nil {
		return proto.OpenChannelResult{}, err
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

	s.log.Info("ssh: channel opened",
		"channel", id.String(), "kind", string(p.Kind),
		"host", p.Destination.Host, "port", p.Destination.Port, "user", p.Destination.User)
	return proto.OpenChannelResult{Channel: id}, nil
}

// registerChannel puts one channel in the table bytes, closes and the reader
// pump are routed through. An id the helper already holds is refused rather
// than overwritten — two channels under one id is a stream whose bytes go
// somewhere nobody can predict — and with 128 random bits that is a bug in
// this process rather than a collision anybody will meet.
func (s *Service) registerChannel(ch *openChannel) error {
	s.mu.Lock()
	if s.channels == nil {
		s.channels = make(map[proto.ChannelID]*openChannel)
	}
	if _, exists := s.channels[ch.id]; exists {
		s.mu.Unlock()
		return internalRefusal("channel id collision")
	}
	s.channels[ch.id] = ch
	s.mu.Unlock()
	return nil
}

// takeChannel removes one channel from the table and returns it, or nil when
// this helper does not hold that id.
func (s *Service) takeChannel(id proto.ChannelID) *openChannel {
	s.mu.Lock()
	ch := s.channels[id]
	delete(s.channels, id)
	s.mu.Unlock()
	return ch
}

// dialChannel acquires the pooled connection and opens the requested kind of
// channel on it. On any failure the pooled reference is released before
// returning, so a refused subsystem does not leave a pool entry behind that
// nothing owns.
func (s *Service) dialChannel(ctx context.Context, conn *host.Host, p proto.OpenChannelParams) (*ssh.PooledConn, remoteEnd, error) {
	pool, err := s.acquirePooled(ctx, conn, p.Destination, p.AcceptOnTrust, "")
	if err != nil {
		return nil, nil, err
	}

	switch p.Kind {
	case proto.ChannelSFTP:
		end, err := openSubsystemStream(pool, "sftp")
		if err != nil {
			_ = pool.Close()
			return nil, nil, classifyChannelError(err)
		}
		return pool, end, nil
	case proto.ChannelDirectTCPIP:
		c, err := sshdial.DialDirectTCP(pool.Client(), net.JoinHostPort(p.Target.Host, strconv.Itoa(p.Target.Port)))
		if err != nil {
			_ = pool.Close()
			return nil, nil, classifyChannelError(err)
		}
		return pool, c, nil
	default:
		// validateDestination has already refused this, so reaching it means
		// the two disagree — which is a bug in this process and not a caller's
		// mistake, and saying so is cheaper than a silently refused channel.
		_ = pool.Close()
		return nil, nil, fmt.Errorf("%w: kind %q", errBadChannelParams, p.Kind)
	}
}

// acquirePooled dials or reuses the one pooled connection for a destination,
// with the client configuration this helper authenticates under.
//
// fingerprint is the CALLER's own expectation about the host key, empty when
// it has none: it is enforced in front of the coordinator's verdict, because a
// host that changed between the caller's own check and this dial satisfies the
// verdict's question and not the caller's (pinnedHostKey's own note). A pane's
// shell channel is the caller that pins one, which is why this is a parameter
// rather than a second acquisition function: one connection per destination is
// AD-4's rule and two paths to it would be two answers.
func (s *Service) acquirePooled(ctx context.Context, conn *host.Host, d proto.SSHDestination, acceptOnTrust bool, fingerprint string) (*ssh.PooledConn, error) {
	cfg, err := s.clientConfig(ctx, conn, d.User, d.Identity, acceptOnTrust)
	if err != nil {
		return nil, err
	}
	if fingerprint != "" {
		cfg.HostKeyCallback = pinnedHostKey(cfg.HostKeyCallback, fingerprint)
	}
	pool, err := s.client.AcquirePooled(ctx, ssh.PooledSpec{
		Host:     d.Host,
		Port:     d.Port,
		User:     d.User,
		Identity: identityKey(d.Identity),
		Config:   cfg,
	})
	if err != nil {
		return nil, classifyChannelError(err)
	}
	return pool, nil
}

// openSubsystemStream opens a session channel and starts one subsystem on it.
//
// The order matters and is the reason this is a function rather than four lines
// in the switch: the session is left open until the subsystem request is
// answered, because closing it while the request is in flight would turn a
// refusal into a transport error.
func openSubsystemStream(pool *ssh.PooledConn, subsystem string) (*subsystemEnd, error) {
	sess, err := pool.Client().NewSession()
	if err != nil {
		return nil, err
	}
	if subErr := sess.RequestSubsystem(subsystem); subErr != nil {
		_ = sess.Close()
		return nil, subErr
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
	return &subsystemEnd{sess: sess, stdin: stdin, stdout: stdout}, nil
}

// ResponseWritten starts the channel's reader pump, or a listener's accept
// loop, once the open's or the forward's answer is on the wire
// (host.ResponseObserver). It is the same edge for a LANE: a lane's bytes are
// keyed by the id its open answered with, so a pump started in the handler
// could write about a stream the caller cannot address yet.
//
// Both are deferred starts for the SAME reason, and it is worth saying once
// where both are dispatched: what either writes about is keyed by an id the
// coordinator learns FROM the response, so a pump or a loop started in the
// handler could put a frame on the wire describing something the caller cannot
// address yet — bytes dropped as orphaned, for the sftp handshake that is a
// hang, and for a forwarded connection a stream nobody can read.
func (s *Service) ResponseWritten(_ context.Context, op string, result any) {
	switch op {
	case proto.OpOpen, proto.OpLane:
		res, ok := result.(proto.OpenChannelResult)
		if !ok {
			return
		}
		s.mu.Lock()
		ch := s.channels[res.Channel]
		s.mu.Unlock()
		if ch != nil {
			go ch.pumpToCoordinator(s.log)
		}
	case proto.OpForward:
		s.forwardResponseWritten(result)
	}
}

// pumpToCoordinator moves the remote end's bytes to the coordinator, and
// reports the end of the stream when it comes.
//
// The end is a NOTIFICATION and not a zero-length frame — a write of no bytes
// is a legitimate frame, so end-of-stream has to be said out loud
// (proto.EventChannelClosed). It is sent after the loop, so every byte that did
// arrive has already been written on the same wire: the coordinator's reader
// sees them all before the close, and never a hole it cannot name.
func (c *openChannel) pumpToCoordinator(log *slog.Logger) {
	buf := make([]byte, channelReadBuf)
	var cause string
	for {
		n, err := c.end.Read(buf)
		if n > 0 {
			// No copy: SendChannelData frames the bytes before it returns,
			// under the host's writer mutex.
			if werr := c.conn.SendChannelData(proto.ChannelFrame{Channel: c.id, Payload: buf[:n]}); werr != nil {
				cause = "the helper could not write to the coordinator: " + werr.Error()
				break
			}
		}
		if err != nil {
			// io.EOF leaves the cause EMPTY, and that is parity rather than a
			// gap: x/crypto/ssh reports a channel's end — including one whose
			// transport died under it — as io.EOF, and the coordinator's own
			// pooled lease read that same signal through this same library
			// before the dial moved here. What names a LOST TRANSPORT is a
			// watcher, and both of them are where they belong: the forward's
			// own (forward.go's pool.Wait) and the helper connection's (the
			// coordinator's lease).
			if !errors.Is(err, io.EOF) {
				cause = err.Error()
			}
			break
		}
	}
	c.finish(cause, channelExit(c.end))
}

// ChannelData routes bytes the coordinator wrote to one channel. An id this
// helper does not hold is logged and dropped: the coordinator either closed it
// already or is writing to a stream that ended, and there is nothing on this
// side that could answer it.
func (s *Service) ChannelData(_ context.Context, f proto.ChannelFrame) {
	s.mu.Lock()
	ch := s.channels[f.Channel]
	s.mu.Unlock()
	if ch == nil {
		s.log.Warn("channel data dropped: no such channel", "channel", f.Channel.String(), "bytes", len(f.Payload))
		return
	}
	if _, err := ch.end.Write(f.Payload); err != nil {
		// A write that fails ends THIS channel and nothing else: the pool
		// holds one connection for many channels, and a stream the remote
		// end closed says nothing about the others.
		s.log.Info("ssh: channel write failed", "channel", f.Channel.String(), "error", err)
		ch.finish(err.Error(), nil)
	}
}

// closeChannel ends one channel: the far end is closed first — which is what
// unblocks a read or write wedged against a server that stopped talking — and
// the pooled reference is released after it.
//
// An id this helper does not hold is answered as CLOSED rather than refused.
// The caller is ordinarily a process shutting down, and a second close is not
// a disagreement about state; refusing it would make an idempotent act look
// like a failure, which is how a shutdown path starts logging errors nobody
// can act on. It is still not silent: errNoSuchChannel is logged.
func (s *Service) closeChannel(id proto.ChannelID) (proto.CloseChannelResult, error) {
	if id.IsZero() {
		return proto.CloseChannelResult{}, fmt.Errorf("%w: no channel id", errBadChannelParams)
	}
	ch := s.takeChannel(id)
	if ch == nil {
		s.log.Debug("ssh: close for an unknown channel", "channel", id.String(), "err", errNoSuchChannel)
		return proto.CloseChannelResult{}, nil
	}
	// A close the COORDINATOR asked for carries no exit status, and that is
	// not an omission: asking for the far process's status here would wait for
	// a bridge that is still serving somebody, because nothing has ended it
	// yet — the close itself is what ends it.
	ch.finish("", nil)
	return proto.CloseChannelResult{}, nil
}

// finish ends a channel once: it tells the coordinator the stream is over
// (unless the coordinator is the one that ended it), closes the far end and
// releases the pooled reference.
//
// cause is the helper's sentence for an end nobody asked for, empty for an
// ordinary one.
func (c *openChannel) finish(cause string, exit *int32) {
	c.closeOnce.Do(func() {
		// Announced on BOTH ways of ending, and the coordinator tolerates the
		// redundant one: a notification for an id it has already forgotten is
		// dropped by its own routing (internal/helper/client). Staying silent
		// when the COORDINATOR closed first would be tidier and is not taken,
		// because the tidiness costs a rule — "who closed" would have to be
		// known at the moment this runs, and the only way to know it is to
		// hold a second piece of state that can be wrong. One dropped frame is
		// cheaper than a reader that waits for an end that never comes.
		_ = c.conn.SendNotification(proto.Notification{
			Service: proto.ServiceSSH,
			Event:   proto.EventChannelClosed,
			Params:  proto.ChannelClosedEvent{Channel: c.id, Error: cause, Exit: exit},
		})
		_ = c.end.Close()
		// nil for a forwarded channel: the listener holds the reference (see
		// the field's own doc), and PooledConn.Close is nil-safe anyway.
		_ = c.pool.Close()
		if c.owner != nil {
			c.owner.forget(c.id)
		}
	})
}

// validateDestination refuses an open that cannot be performed before anything
// is dialed. It mirrors validateProbe field for field and adds the kind and
// its target, because the ops take the same destination and a difference
// between them would be a second answer to "is this dialable".
func validateDestination(p proto.OpenChannelParams) error {
	if err := validateDestinationAddress(p.Destination); err != nil {
		return err
	}
	switch p.Kind {
	case proto.ChannelSFTP:
		if p.Target != nil {
			// A target on a subsystem open is a caller that believes it is
			// getting something else, and the failure it would otherwise meet
			// is a working channel to the wrong thing.
			return fmt.Errorf("%w: kind %q takes no target", errBadChannelParams, p.Kind)
		}
	case proto.ChannelDirectTCPIP:
		if p.Target == nil {
			return fmt.Errorf("%w: kind %q needs a target", errBadChannelParams, p.Kind)
		}
		if err := validateTarget(*p.Target); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: kind %q is not one this helper opens", errBadChannelParams, p.Kind)
	}
	return nil
}

// validateDestinationAddress is the half of an open's validation that every op
// taking a destination shares.
func validateDestinationAddress(d proto.SSHDestination) error {
	switch {
	case d.Host == "":
		return fmt.Errorf("%w: no host", errBadChannelParams)
	case d.Port <= 0 || d.Port > 65535:
		return fmt.Errorf("%w: port %d", errBadChannelParams, d.Port)
	case d.User == "":
		return fmt.Errorf("%w: no user", errBadChannelParams)
	}
	return validateIdentity(d.Identity)
}

// validateTarget refuses an address this helper will not connect to, before
// anything is dialed. A port of 0 is refused rather than passed on: the far
// side would answer a connection to port 0 with a refusal of its own, and "the
// server refused" is a worse sentence for a caller's own mistake than this one.
func validateTarget(t proto.ChannelTarget) error {
	switch {
	case t.Host == "":
		return fmt.Errorf("%w: no target host", errBadChannelParams)
	case t.Port <= 0 || t.Port > 65535:
		return fmt.Errorf("%w: target port %d", errBadChannelParams, t.Port)
	}
	return nil
}

// identityKey is the pool key's credential component for a destination.
//
// It follows poolKeyFor's rule rather than inventing one, because two
// derivations of "which principal is this" would eventually disagree about the
// one pair of destinations that matters: a stored credential is identified by
// the COORDINATOR's reference (which is reminted when the material rotates, so
// a rotated credential cannot reuse a transport authenticated with the old
// secret), and an inline key by the fingerprint of the public half that rides
// in the identity — never by a path, which the helper does not have anyway.
func identityKey(id proto.SSHIdentity) string {
	switch id.Auth {
	case proto.SSHAuthKey:
		if key, err := gossh.ParsePublicKey(id.PublicKey); err == nil {
			return gossh.FingerprintSHA256(key)
		}
	}
	return id.Credential.Ref
}

// classifyChannelError types a channel's failure in the coordinator's own
// vocabulary: a far side that will not give the channel is channel_refused, a
// host key nobody has recorded or one that changed are their own two refusals
// with the evidence attached, and anything else is a lost connection.
//
// ONE refused code for the two refusal points — the session and the subsystem
// request — because a caller cannot act differently on them: either way this
// host will not serve that channel. The sentence carries which one it was, so
// the distinction survives for whoever reads the error.
//
// The two host-key arms are the load-bearing part and they come FIRST. A
// handshake that stopped at the host key is not a failed connection: it is the
// coordinator's own accept flow waiting to be asked, and reporting it as a
// transport failure would leave a person staring at "could not open a channel"
// while the sheet they needed was never raised.
func classifyChannelError(err error) error {
	var refusal *proto.Refusal
	if errors.As(err, &refusal) {
		return refusal
	}
	// The two channel-shaped refusals come first, because they are not
	// dial outcomes and no classifier for a handshake knows them: the server
	// would not open the get channel at all, or it would not start the
	// subsystem on the one it did open.
	var openErr *gossh.OpenChannelError
	if errors.As(err, &openErr) {
		return &proto.Refusal{Code: proto.ErrCodeChannelRefused, Message: err.Error()}
	}
	// x/crypto/ssh answers a refused subsystem request with a fixed sentence
	// pinned by go.mod at v0.54.0 — the same string internal/ssh's own
	// openSFTPSubsystem partitions on. It is checked here rather than by
	// importing that package's sentinel because the sentinel means "the
	// subsystem was refused" to a caller that dialed itself, and the caller
	// here is one process away from that.
	if err.Error() == "ssh: subsystem request failed" {
		return &proto.Refusal{Code: proto.ErrCodeChannelRefused, Message: err.Error()}
	}

	// Everything else is classified with the SAME function the probe uses, so
	// an unreachable host, a rejected credential and a locked key are the same
	// facts here as there: one classification, two ops.
	outcome, detail, unclassified := ssh.ClassifyProbeError(err)
	if unclassified != nil {
		// A failure nobody can classify stays this helper's own (`internal`)
		// rather than being folded into a class that would send somebody to
		// look at the host (the probe path's own rule, one op over).
		return fmt.Errorf("open a channel: %w", unclassified)
	}
	code := string(outcome)
	var details json.RawMessage
	var unknown *ssh.ErrUnknownHostKey
	var changed *ssh.ErrHostKeyMismatch
	switch {
	case errors.As(err, &unknown):
		details = hostKeyEvidence(unknown.Addr, unknown.KnownHostsAddr, unknown.KeyAlgo, unknown.Key, unknown.Fingerprint, "")
	case errors.As(err, &changed):
		details = hostKeyEvidence(changed.Addr, changed.KnownHostsAddr, changed.KeyAlgo, changed.Key, changed.Fingerprint, changed.Expected)
	}
	return &proto.Refusal{Code: code, Message: detail, Details: details}
}

// hostKeyEvidence marshals what the coordinator needs to rebuild its own typed
// error. A payload that will not marshal yields nil details rather than an
// error: the refusal is the answer, and losing the evidence degrades it to the
// less helpful half of itself rather than to no answer at all.
func hostKeyEvidence(addr, knownHostsAddr, algorithm string, key []byte, fingerprint, expected string) json.RawMessage {
	raw, err := json.Marshal(proto.HostKeyEvidence{
		Addr:           addr,
		KnownHostsAddr: knownHostsAddr,
		Algorithm:      algorithm,
		Key:            key,
		Fingerprint:    fingerprint,
		Expected:       expected,
	})
	if err != nil {
		return nil
	}
	return raw
}
