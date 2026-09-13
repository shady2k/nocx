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
	"sync"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
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
	pool *ssh.PooledConn
	// sess is the ssh session channel carrying the subsystem. Closing it is
	// the close-to-cancel mechanism for a write wedged against a server that
	// has stopped reading.
	sess   *gossh.Session
	stdin  io.WriteCloser
	stdout io.Reader

	closeOnce sync.Once
}

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

	pool, sess, stdin, stdout, err := s.dialChannel(ctx, conn, p)
	if err != nil {
		return proto.OpenChannelResult{}, err
	}

	id, err := mintChannelID()
	if err != nil {
		_ = sess.Close()
		_ = pool.Close()
		return proto.OpenChannelResult{}, internalRefusal("mint a channel id: %v", err)
	}
	ch := &openChannel{id: id, conn: conn, pool: pool, sess: sess, stdin: stdin, stdout: stdout}

	s.mu.Lock()
	if s.channels == nil {
		s.channels = make(map[proto.ChannelID]*openChannel)
	}
	if _, exists := s.channels[id]; exists {
		// Astronomically unlikely with 128 random bits, and refused rather
		// than overwritten anyway: two channels under one id is a stream
		// whose bytes go somewhere nobody can predict.
		s.mu.Unlock()
		_ = sess.Close()
		_ = pool.Close()
		return proto.OpenChannelResult{}, internalRefusal("channel id collision")
	}
	s.channels[id] = ch
	s.mu.Unlock()

	s.log.Info("ssh: channel opened",
		"channel", id.String(), "kind", string(p.Kind),
		"host", p.Destination.Host, "port", p.Destination.Port, "user", p.Destination.User)
	return proto.OpenChannelResult{Channel: id}, nil
}

// dialChannel acquires the pooled connection and opens the requested kind of
// channel on it. On any failure the pooled reference is released before
// returning, so a refused subsystem does not leave a pool entry behind that
// nothing owns.
func (s *Service) dialChannel(ctx context.Context, conn *host.Host, p proto.OpenChannelParams) (*ssh.PooledConn, *gossh.Session, io.WriteCloser, io.Reader, error) {
	auth, err := s.authMethod(ctx, conn, p.Destination.Identity)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	timeout := ProbeTimeout
	cfg := &gossh.ClientConfig{
		User: p.Destination.User,
		// Exactly one method, for the reason the probe path gives: a second
		// attempt against one host is indistinguishable from password
		// spraying, and MaxAuthTries is finite.
		Auth:            []gossh.AuthMethod{auth},
		HostKeyCallback: s.hostKeyCallback(ctx, conn, p.AcceptOnTrust),
		Timeout:         timeout,
	}
	pool, err := s.client.AcquirePooled(ctx, ssh.PooledSpec{
		Host:     p.Destination.Host,
		Port:     p.Destination.Port,
		User:     p.Destination.User,
		Identity: identityKey(p.Destination.Identity),
		Config:   cfg,
	})
	if err != nil {
		return nil, nil, nil, nil, classifyChannelError(err)
	}

	sess, err := pool.Client().NewSession()
	if err != nil {
		_ = pool.Close()
		return nil, nil, nil, nil, classifyChannelError(err)
	}
	// WRITE YOUR OWN REFUSAL, and the order matters: the session is left open
	// until the subsystem request is answered, because closing it while the
	// request is in flight would turn a refusal into a transport error.
	switch p.Kind {
	case proto.ChannelSFTP:
		if subErr := sess.RequestSubsystem("sftp"); subErr != nil {
			_ = sess.Close()
			_ = pool.Close()
			return nil, nil, nil, nil, classifyChannelError(subErr)
		}
	default:
		_ = sess.Close()
		_ = pool.Close()
		return nil, nil, nil, nil, fmt.Errorf("%w: kind %q", errBadChannelParams, p.Kind)
	}

	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		_ = pool.Close()
		return nil, nil, nil, nil, classifyChannelError(err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		_ = pool.Close()
		return nil, nil, nil, nil, classifyChannelError(err)
	}
	return pool, sess, stdin, stdout, nil
}

// ResponseWritten starts the channel's reader pump, once the open's answer is
// on the wire (host.ResponseObserver).
func (s *Service) ResponseWritten(_ context.Context, op string, result any) {
	if op != proto.OpOpen {
		return
	}
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
		n, err := c.stdout.Read(buf)
		if n > 0 {
			// No copy: SendChannelData frames the bytes before it returns,
			// under the host's writer mutex.
			if werr := c.conn.SendChannelData(proto.ChannelFrame{Channel: c.id, Payload: buf[:n]}); werr != nil {
				cause = "the helper could not write to the coordinator: " + werr.Error()
				break
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				cause = err.Error()
			}
			break
		}
	}
	c.finish(cause)
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
	if _, err := ch.stdin.Write(f.Payload); err != nil {
		// A write that fails ends THIS channel and nothing else: the pool
		// holds one connection for many channels, and a stream the remote
		// end closed says nothing about the others.
		s.log.Info("ssh: channel write failed", "channel", f.Channel.String(), "error", err)
		ch.finish(err.Error())
	}
}

// closeChannel ends one channel: the ssh session is closed first — which is
// what unblocks a read or write wedged against a server that stopped talking —
// and the pooled reference is released after it.
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
	s.mu.Lock()
	ch := s.channels[id]
	delete(s.channels, id)
	s.mu.Unlock()
	if ch == nil {
		s.log.Debug("ssh: close for an unknown channel", "channel", id.String(), "err", errNoSuchChannel)
		return proto.CloseChannelResult{}, nil
	}
	ch.finish("")
	return proto.CloseChannelResult{}, nil
}

// finish ends a channel once: it tells the coordinator the stream is over
// (unless the coordinator is the one that ended it), closes the ssh session and
// releases the pooled reference.
//
// cause is the helper's sentence for an end nobody asked for, empty for an
// ordinary one.
func (c *openChannel) finish(cause string) {
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
			Params:  proto.ChannelClosedEvent{Channel: c.id, Error: cause},
		})
		_ = c.sess.Close()
		_ = c.pool.Close()
	})
}

// validateDestination refuses an open that cannot be performed before anything
// is dialed. It mirrors validateProbe field for field and adds the kind,
// because the two ops take the same destination and a difference between them
// would be a second answer to "is this dialable".
func validateDestination(p proto.OpenChannelParams) error {
	switch {
	case p.Destination.Host == "":
		return fmt.Errorf("%w: no host", errBadChannelParams)
	case p.Destination.Port <= 0 || p.Destination.Port > 65535:
		return fmt.Errorf("%w: port %d", errBadChannelParams, p.Destination.Port)
	case p.Destination.User == "":
		return fmt.Errorf("%w: no user", errBadChannelParams)
	}
	if err := validateIdentity(p.Destination.Identity); err != nil {
		return err
	}
	switch p.Kind {
	case proto.ChannelSFTP:
	default:
		return fmt.Errorf("%w: kind %q is not one this helper opens", errBadChannelParams, p.Kind)
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
