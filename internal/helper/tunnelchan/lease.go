package tunnelchan

// The LEASE: one group of streams to one destination, and the two ways it ends.
//
// # Who ends what
//
// Every stream this lease opened is closed by Close, and every listener too.
// The pooled connection is NOT: it belongs to the helper's pool (AD-4) and
// outlives this lease, which is what makes a second lease to the same
// destination free. Nothing here is reference-counted, because nothing here is
// a reference to anything the helper owns beyond the streams themselves — an
// ssh.open and an ssh.forward each take their own pooled reference on the
// helper's side and each releases it when the stream ends, whichever end ended
// it. That accounting is the helper's, and it is already written.
//
// # What the loss signal means now
//
// Done closes when nothing this lease holds can work any more: the HELPER
// connection died (the daemon is gone or its lane ended — the case a whole
// process is affected by), or a listener ended for a reason this lease did not
// ask for (the far side's connection died under it, which is the only way a
// remote forward's owner can be told). It deliberately does NOT close when an
// individual stream dies: a stream's end is a fact about that stream, reported
// on its own reads, and a lease that declared "connection lost" every time a
// target refused a connection would stop a healthy forward at the first
// mistake of a stranger on the far side.
//
// The exception that is worth naming: a lease whose only stream was a LISTENER
// does read a lost ssh connection as a loss, because the listener is what the
// caller asked for and it is gone. That is the honest difference between "the
// transport under my listener died" and "one of my connections failed".

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/ssh"
)

// lease is one acquired tunnel lease: the helper connection it rides, the
// destination it is for, and the streams it has open.
type lease struct {
	client *helperclient.Client
	// host is the RESOLVED destination's host for the log, and target is
	// everything the helper needs to dial: it is resolved once, at
	// acquisition, so every channel of this lease goes to the same place with
	// the same identity — which is the property the pool key had.
	host   string
	target ssh.DialTarget
	log    *slog.Logger

	mu       sync.Mutex
	streams  map[*helperclient.ChannelStream]struct{}
	forwards map[*remoteListener]struct{}
	closed   bool

	done     chan struct{}
	lostOnce sync.Once
	lostErr  error
}

// newLease wires a lease and starts the one watcher it needs: the helper
// connection's end.
func newLease(client *helperclient.Client, host string, target ssh.DialTarget, log *slog.Logger) *lease {
	l := &lease{
		client:   client,
		host:     host,
		target:   target,
		log:      log,
		streams:  make(map[*helperclient.ChannelStream]struct{}),
		forwards: make(map[*remoteListener]struct{}),
		done:     make(chan struct{}),
	}
	go func() {
		<-client.Done()
		l.markLost(fmt.Errorf("%w: %v", ssh.ErrTunnelConnLost, client.LostErr()))
	}()
	return l
}

// destination is the resolved triple every op of this lease carries.
func (l *lease) destination() proto.SSHDestination {
	return proto.SSHDestination{
		Host: l.target.Host,
		Port: l.target.Port,
		User: l.target.User,
		Identity: proto.SSHIdentity{
			Credential: proto.SSHCredential{
				Ref:           string(l.target.Credential),
				PassphraseRef: string(l.target.Passphrase),
			},
			Auth:      proto.SSHAuthKind(l.target.Auth),
			PublicKey: l.target.PublicKey,
		},
	}
}

// usability answers whether a new stream may be opened, in the order the
// interface's own errors require: a lease this caller RELEASED reports the
// closed error even when the connection also died (closing the last reference
// does both), because the lease's own state is the deterministic answer.
func (l *lease) usability() error {
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed {
		return ssh.ErrTunnelConnClosed
	}
	select {
	case <-l.done:
		return fmt.Errorf("%w: %v", ssh.ErrTunnelConnLost, l.LostErr())
	default:
	}
	return nil
}

// Dial opens a direct-tcpip channel to addr over this lease's destination: the
// far side connects to addr on ITS network, which is where a local forward's
// and a SOCKS proxy's targets live.
func (l *lease) Dial(addr string) (net.Conn, error) {
	if err := l.usability(); err != nil {
		return nil, err
	}
	target, err := parseTarget(addr)
	if err != nil {
		return nil, err
	}
	stream, err := l.client.OpenChannel(context.Background(), proto.OpenChannelParams{
		Destination: l.destination(),
		Kind:        proto.ChannelDirectTCPIP,
		Target:      &target,
		// FALSE, and the reason is the same one the install lease gives: no
		// caller on this path can answer the accept flow (that flow belongs to
		// a pane open, where a person is watching), so an unknown host key comes
		// back as the helper's refusal naming the key rather than as a hang.
		AcceptOnTrust: false,
	})
	if err != nil {
		return nil, l.wrap("open a channel", addr, err)
	}
	l.trackStream(stream)
	return &channelConn{
		stream: stream,
		local:  tcpAddrOf(l.target.Host, l.target.Port),
		remote: tcpAddrOf(target.Host, target.Port),
		// The lease stops tracking once the caller is done with the
		// connection: an untracked stream is one Close no longer has to
		// touch, and the map is a list of what this lease still holds rather
		// than a history of what it once opened.
		onClose: func() { l.untrackStream(stream) },
	}, nil
}

// Listen asks the far side for a listener: the remote forward (-R) and the
// remote lifecycle channel (ADR-0024) both ride one.
//
// The refusal is the SERVER's — AllowTcpForwarding off, or a bind outside
// PermitListen — and reaches the caller with the helper's sentence, wrapped
// with the act. The listener's Addr reports what the server BOUND: a requested
// port 0 comes back allocated, and the host is the server's answer rather than
// a verified bind (the forward's own caveat discloses that, as it always has).
func (l *lease) Listen(addr string) (net.Listener, error) {
	if err := l.usability(); err != nil {
		return nil, err
	}
	bind, err := parseTarget(addr)
	if err != nil {
		return nil, err
	}
	f, err := l.client.OpenForward(context.Background(), proto.ForwardParams{
		Destination:   l.destination(),
		Bind:          bind,
		AcceptOnTrust: false,
	})
	if err != nil {
		return nil, l.wrap("listen", addr, err)
	}
	rl := &remoteListener{lease: l, f: f, local: tcpAddrOf(f.Bind().Host, f.Bind().Port)}
	l.trackForward(rl)
	// The listener's own end, watched for the one case the lease has to
	// report: an end this lease did not ask for. Close's own unforward is not
	// a loss, and the helper says which it is by carrying a cause only when
	// nobody here asked.
	go rl.watchLoss()
	return rl, nil
}

// Done closes when nothing this lease holds can work any more. See the file
// header for what that includes and what it deliberately excludes.
func (l *lease) Done() <-chan struct{} { return l.done }

// LostErr reports why this lease was lost. Meaningful once Done has closed.
func (l *lease) LostErr() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lostErr
}

// Close releases this lease: every stream it opened is closed, every listener
// it asked for is cancelled, and later Dials refuse. The helper's pooled
// connection stays open for whatever asks next (AD-4), and Done does NOT close
// — an intentional stop must not read as connection loss.
func (l *lease) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	streams := make([]*helperclient.ChannelStream, 0, len(l.streams))
	for s := range l.streams {
		streams = append(streams, s)
	}
	forwards := make([]*remoteListener, 0, len(l.forwards))
	for f := range l.forwards {
		forwards = append(forwards, f)
	}
	l.streams = nil
	l.forwards = nil
	l.mu.Unlock()

	for _, s := range streams {
		_ = s.Close()
	}
	for _, f := range forwards {
		_ = f.Close()
	}
	return nil
}

// markLost records the loss once and closes Done. The message is kept in the
// error Call would have failed a request with, so a caller that watched Done
// reads the same cause.
func (l *lease) markLost(err error) {
	l.lostOnce.Do(func() {
		l.mu.Lock()
		if l.lostErr == nil {
			l.lostErr = err
		}
		l.mu.Unlock()
		close(l.done)
	})
}

// wrap names the act that failed and the destination, and leaves errors.Is
// reaching the helper's refusal and whatever it carries.
func (l *lease) wrap(act, addr string, err error) error {
	return fmt.Errorf("ssh: %s to %s through %s (helper): %w", act, addr, l.host, err)
}

func (l *lease) trackStream(s *helperclient.ChannelStream) {
	l.mu.Lock()
	if l.streams == nil {
		// Close already ran: the connection is one this lease no longer owns,
		// so it is closed here rather than left to a caller that would hold a
		// stream nobody will release.
		l.mu.Unlock()
		_ = s.Close()
		return
	}
	l.streams[s] = struct{}{}
	l.mu.Unlock()
}

func (l *lease) untrackStream(s *helperclient.ChannelStream) {
	l.mu.Lock()
	delete(l.streams, s)
	l.mu.Unlock()
}

func (l *lease) trackForward(f *remoteListener) {
	l.mu.Lock()
	if l.forwards == nil {
		l.mu.Unlock()
		_ = f.Close()
		return
	}
	l.forwards[f] = struct{}{}
	l.mu.Unlock()
}

func (l *lease) untrackForward(f *remoteListener) {
	l.mu.Lock()
	delete(l.forwards, f)
	l.mu.Unlock()
}

// reportListenerGone is the one loss a listener may report: it ended for a
// reason this lease did not ask for, so the caller's Accept would otherwise
// wait for ever and the lease has nothing left that works.
//
// A listener a caller CLOSED is dropped from the map by Close, and its own
// end carries no cause, so neither path can turn an intentional stop into a
// reported loss.
func (l *lease) reportListenerGone(err error) {
	select {
	case <-l.done:
		return
	default:
	}
	l.log.Warn("ssh: a forwarded listener ended under the lease",
		"host", l.host, "error", err)
	l.markLost(fmt.Errorf("%w: %v", ssh.ErrTunnelConnLost, err))
}

// errNoDeadline is what this transport answers a deadline request with.
//
// It is not a gap this package introduced, and saying so exactly is the point:
// the connection every one of these tenants drove before the dial moved to the
// helper came from x/crypto/ssh's Client.Dial, whose chanConn answers
//
//	errors.New("ssh: tcpChan: deadline not supported")
//
// to both SetReadDeadline and SetWriteDeadline (tcpip.go:531-545, v0.54.0).
// So a deadline set on a tunnel connection has never done anything in this
// codebase — the callers that set them (`_ = target.SetWriteDeadline(...)` in
// the lifecycle adapter) ignore the error, and net/http ignores it too — and
// this transport answers the same way rather than pretending to a bound it
// cannot enforce across a socket it does not own.
var errNoDeadline = errors.New("tunnelchan: deadline not supported by an ssh channel carried by a helper")
