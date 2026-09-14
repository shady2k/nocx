//go:build nocx_local_ssh

package sshsvc

// The TOOL SOCKET op: a unix socket on the far side whose connections this
// helper pipes into a socket on ITS OWN machine, with the pane record written
// first.
//
// # Why the far side is a socket, and why the record is written here
//
// A pane's agent reaches nocx's tools over a socket its shell was told about
// (NOCX_TOOL_SOCKET). On this machine that socket is the coordinator's own
// tool endpoint: the pane's shell and the endpoint are two processes here, and
// the pane's agent is admitted by the interval its launch's bearer names.
//
// A pane whose shell runs on ANOTHER host has neither of those properties, and
// both halves are why this op exists (nocx-e2bws). The agent there must dial a
// path that host can reach, so the socket is created ON that host by its own
// sshd — a streamlocal forward, requested on the connection this helper holds —
// while the endpoint that admits it stays where it is, on this machine. What
// crosses between the two is the helper's own pipe, and the ENDPOINT CANNOT
// decide anything about the connection without being told WHICH PANE it
// belongs to: that record is written by the party holding the listener, before
// a single far byte, which is this helper and not the coordinator.
//
// It is the same forwarding the spawn-ssh pane already does for a pane on a
// host with no helper of its own (pane.go's tool half), and it is the SAME
// IMPLEMENTATION rather than a second one: `toolSocket` below is that code, and
// the pane holds one. What differs between the two callers is only who asked
// and which connection the far side of the socket was bound on.
//
// # Why a forward plane of its own is not reused
//
// `ssh.forward` binds a TCP listener and hands every accepted connection to the
// coordinator as a proxied CHANNEL. That is the right shape for a port and the
// wrong one here: a channel arrives at the coordinator with no pane on it, and
// the admission that has to happen first happens in this process. The two ops
// answer the same ForwardID and are ended by the same `unforward`, because what
// they leave behind is one thing — a listener — even though what arrives on it
// goes to different places.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/sshdial"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/toolendpoint/panebind"
)

// The named refusals this op raises itself. Each names a REQUEST this helper
// will not honour, and each is raised before anything is dialed — the rule
// every refusal in validatePaneSpec follows, for the same reason: a request it
// cannot honour must cost the far host nothing.
var (
	errBadToolSocketParams = errors.New("tool socket params are incomplete")
	errNoToolSocketTarget  = errors.New("a tool socket was asked for with no tool endpoint behind it")
	errNoToolSocketSession = errors.New("a tool socket was asked for with no session to name")
)

// toolSocketOps adds the tool-socket op to the service's own.
func (s *Service) toolSocketOps() []string { return []string{proto.OpToolSocket} }

// toolSocket is ONE far-side tool socket: the listener the far sshd bound, the
// socket on this machine its connections are piped into, and the pane every one
// of them announces.
//
// It is the pane's tool half, extracted so that both callers — the spawn-ssh
// pane and the `tool-socket` op — are one implementation (AD-8). Everything in
// it is about a LISTENER and the streams it produced, and the pane's own
// lifecycle carrier shares none of it.
type toolSocket struct {
	log *slog.Logger
	ln  net.Listener
	// pool is the pooled reference taken for the listener's connection. The
	// listener and the streams on it are one resource, so the reference is
	// released when the listener ends and never per connection.
	pool    *ssh.PooledConn
	path    string
	target  string
	session string

	mu       sync.Mutex
	forwards map[net.Conn]struct{}
	closed   bool
	closeOne sync.Once
}

// newToolSocket takes ownership of an already-bound listener. The listener is
// not created here: which connection it was bound on is the caller's decision
// (a pane's pooled connection, or the op's), and a constructor that dialed
// would be a second place that knows how to ask.
func newToolSocket(lg *slog.Logger, ln net.Listener, pool *ssh.PooledConn, path, target, session string) *toolSocket {
	return &toolSocket{log: lg, ln: ln, pool: pool, path: path, target: target, session: session}
}

// Path is the far-host path the shell's environment names.
func (t *toolSocket) Path() string { return t.path }

// accept serves the far side's tool socket: every connection an agent process
// makes there is piped into the coordinator's own endpoint socket.
//
// Every accepted connection is REGISTERED before it is served, so its owner can
// end them all when it ends (Close). An accept that loses the race with Close
// gets a connection already marked closed: it is closed here rather than handed
// to a forward nobody will end.
func (t *toolSocket) accept() {
	for {
		conn, err := t.ln.Accept()
		if err != nil {
			return
		}
		if !t.track(conn) {
			_ = conn.Close()
			continue
		}
		go t.forward(conn)
	}
}

// track registers one accepted far-side connection, or reports that this
// socket is already closed and the connection must be dropped.
func (t *toolSocket) track(conn net.Conn) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return false
	}
	if t.forwards == nil {
		t.forwards = make(map[net.Conn]struct{})
	}
	t.forwards[conn] = struct{}{}
	return true
}

// untrack forgets a connection whose forward has ended.
func (t *toolSocket) untrack(conn net.Conn) {
	t.mu.Lock()
	delete(t.forwards, conn)
	t.mu.Unlock()
}

// forward pipes one agent connection to the coordinator's tool endpoint.
//
// The endpoint is dialed PER CONNECTION, which is not an optimisation but the
// contract: a tool endpoint connection IS an admission interval (ADR-0058 —
// the endpoint admits exactly once per connection and holds one caller slot for
// its life), so two agent processes must never share one.
//
// The endpoint is THE PANE'S: the one the coordinator that asked for the socket
// named, kept for the socket's whole life (nocx-50w7p.18). A coordinator that
// has gone is therefore a named refusal — and never a re-route: there is no
// second endpoint here to fall back to, which is the defect this replaced (a
// daemon-held target sent a pane opened by one coordinator to whichever
// coordinator had started the daemon).
//
// A dial that fails closes that connection and nothing else: the pane, its
// shell and its lifecycle channel are unaffected by an endpoint that is not
// running, which is the same soft degrade the local path has when there is no
// endpoint at all.
func (t *toolSocket) forward(far net.Conn) {
	defer func() { _ = far.Close() }()
	defer t.untrack(far)
	local, err := net.Dial("unix", t.target)
	if err != nil {
		t.log.Warn("ssh: refusing a far-side tool connection",
			"refusal", errPaneToolEndpointUnreachable.Error(),
			"path", t.target, "error", err)
		return
	}
	defer func() { _ = local.Close() }()
	// BOTH ENDS ARE THE PANE'S. Closing only the far side ends the pump that
	// reads from it, but not the one writing INTO the endpoint: a pump parked
	// on a write would hold its goroutine and this connection open past the
	// pane's end, which is the same defect one pump over. The registration
	// loses to a concurrent Close exactly as the accept did — the pair is
	// closed here and the forward ends before it starts.
	if !t.track(local) {
		return
	}
	defer t.untrack(local)

	// THE PANE RECORD GOES FIRST, before a single far byte, and it is the
	// helper's whole contribution to admission (nocx-50w7p.16): the endpoint
	// decides nothing about a forwarded connection without it, and a helper
	// that cannot say which pane this is must not hand the connection over at
	// all. Refused here rather than written short: a connection the endpoint
	// would refuse on a malformed record is a connection whose far agent gets
	// an MCP error about nocx rather than about its pane.
	record, err := panebind.Encode(t.session)
	if err != nil {
		t.log.Warn("ssh: refusing a far-side tool connection: this pane cannot name its session",
			"path", t.target, "error", err)
		return
	}
	if n, err := local.Write(record); err != nil || n != len(record) {
		// The COUNT is checked as well as the error: a short write with no
		// error would hand the endpoint a truncated record, which is a pane
		// frame that says nothing rather than no frame at all.
		t.log.Warn("ssh: refusing a far-side tool connection: the pane record did not reach the endpoint",
			"path", t.target, "wrote", n, "want", len(record), "error", err)
		return
	}

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(local, far); done <- struct{}{} }()
	go func() { _, _ = io.Copy(far, local); done <- struct{}{} }()
	<-done
}

// Close cancels the far-side listener, ends the forwards it is carrying and
// releases the pooled reference. It is idempotent, and safe to call from a
// session's teardown (the pane's) or from `unforward` (the op's).
func (t *toolSocket) Close() error {
	t.closeOne.Do(func() {
		// THE FORWARDS THIS SOCKET IS CARRYING ARE ENDED WITH IT. Closing the
		// listener stops new connections; what it does not do is end the ones
		// already accepted, and those are connections a far agent holds into a
		// coordinator that has forgotten this pane. The closing event is the
		// pane's — its session ended, its listener was cancelled — and a
		// forward outliving it is a session's authority outliving the session
		// (ADR-0058), one layer below the endpoint where the same rule is
		// already enforced.
		//
		// Taken under the same lock as the registration, so a connection
		// arriving during this Close is either already in the map (and closed
		// here) or sees closed (and is closed by accept).
		t.mu.Lock()
		t.closed = true
		open := make([]net.Conn, 0, len(t.forwards))
		for conn := range t.forwards {
			open = append(open, conn)
		}
		t.forwards = nil
		t.mu.Unlock()
		for _, conn := range open {
			_ = conn.Close()
		}
		if t.ln != nil {
			_ = t.ln.Close()
		}
		if t.pool != nil {
			_ = t.pool.Close()
		}
	})
	return nil
}

// validateToolSocket refuses a request this helper will not honour, before
// anything is dialed.
func validateToolSocket(p proto.ToolSocketParams) error {
	if p.Path == "" {
		return fmt.Errorf("%w: no far-host path", errBadToolSocketParams)
	}
	if p.Target == "" {
		// A socket with nothing behind it reads to a far agent as a broken
		// agent rather than as a missing endpoint, so it is refused by name.
		return fmt.Errorf("%w: %s", errNoToolSocketTarget, p.Path)
	}
	if p.Session == "" {
		return fmt.Errorf("%w: %s", errNoToolSocketSession, p.Path)
	}
	switch {
	case p.Destination.Host == "":
		return fmt.Errorf("%w: no host", errBadToolSocketParams)
	case p.Destination.Port <= 0 || p.Destination.Port > 65535:
		return fmt.Errorf("%w: port %d", errBadToolSocketParams, p.Destination.Port)
	case p.Destination.User == "":
		return fmt.Errorf("%w: no user", errBadToolSocketParams)
	}
	return validateIdentity(p.Destination.Identity)
}

// toolSocket performs one `ssh.tool-socket`: acquire the pooled connection, ask
// the far side for the socket, start serving it, and answer the id `unforward`
// ends it by.
//
// The ACCEPT LOOP STARTS HERE rather than from ResponseWritten, and that is the
// one place this op differs from `forward`: an accepted connection is not
// announced to anybody (it is piped into the coordinator's endpoint with a pane
// record), so there is no id the caller has to have learned before the first
// connection can be served. Starting later would only lose a connection the far
// agent opened in the round trip.
func (s *Service) toolSocket(ctx context.Context, p proto.ToolSocketParams) (proto.ToolSocketResult, error) {
	conn, _ := host.ConnectionFrom(ctx).(*host.Host)
	if conn == nil {
		return proto.ToolSocketResult{}, errNoAuthChannel
	}
	if err := validateToolSocket(p); err != nil {
		return proto.ToolSocketResult{}, err
	}

	pool, err := s.acquirePooled(ctx, conn, p.Destination, p.AcceptOnTrust, p.HostKeyFingerprint)
	if err != nil {
		return proto.ToolSocketResult{}, err
	}
	ln, err := sshdial.ListenRemoteUnix(pool.Client(), p.Path)
	if err != nil {
		// The refusal is the server's, classified the way every other channel
		// failure is. A path whose directory does not exist, a name already
		// bound and a server with streamlocal forwarding disabled are
		// indistinguishable here, and the sentence carries that rather than
		// inventing a diagnosis.
		_ = pool.Close()
		return proto.ToolSocketResult{}, classifyChannelError(err)
	}

	id, err := mintForwardID()
	if err != nil {
		_ = ln.Close()
		_ = pool.Close()
		return proto.ToolSocketResult{}, internalRefusal("mint a forward id: %v", err)
	}

	ts := newToolSocket(s.log, ln, pool, p.Path, p.Target, p.Session)
	s.mu.Lock()
	if s.toolSockets == nil {
		s.toolSockets = make(map[proto.ForwardID]*toolSocket)
	}
	if _, exists := s.toolSockets[id]; exists {
		s.mu.Unlock()
		_ = ts.Close()
		return proto.ToolSocketResult{}, internalRefusal("forward id collision")
	}
	s.toolSockets[id] = ts
	s.mu.Unlock()

	s.log.Info("ssh: pane tool socket opened",
		"host", p.Destination.Host, "port", p.Destination.Port, "path", p.Path)

	go ts.accept()
	return proto.ToolSocketResult{Forward: id, Path: p.Path}, nil
}
