package lifecyclechannel

// The loopback listener transport: the local sibling of the remote
// adapter's candidate serving (internal/lifecycleremote). It exists for the
// ssh child domain — a parent shell runs `ssh host` itself, and the child's
// lifecycle channel rides a -R reverse forward on that same ssh connection
// (ADR-0022: the ssh command line is the carrier). The -R terminates at
// this transport's loopback listener: the remote sshd forwards the child's
// connection over the user's ssh session to 127.0.0.1:<this port>, and the
// child's envelopes arrive here exactly as a remote adapter's candidates
// do. The kernel stays the sole minter: NewListener binds the transport and
// serves connections but mints nothing — the composition root mints the
// child domain (with its parent) on this transport via kernel.RequestDomain.
//
// The transport is deliberately transport-agnostic about how many domains it
// carries: outbound routing is keyed by the envelope's own domain, and any
// connection that proves the capability is a speaker. The capability — not
// the reachability of the loopback port — is the authenticator (ADR-0024
// decision 2): any local process can open this port, so candidates are
// bounded and each must prove the domain's per-epoch bearer before it can
// deliver an accepted event or receive an outbound envelope.

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclecodec"
	"github.com/shady2k/nocx/internal/log"
)

// writeTimeout bounds one outbound envelope write. The kernel's outbound
// sends are best-effort, so a shell that has stopped reading must never
// wedge the kernel's flush.
const listenerWriteTimeout = 5 * time.Second

// listenerMaxCandidates bounds how many candidate connections the listener
// serves at once. Any local process can open the loopback port; the bound
// keeps a connection flood from exhausting the adapter, and the capability
// — not the port — is what authenticates.
const listenerMaxCandidates = 8

// Listener is one loopback TCP listener transport. It implements
// lifecycle.Port (the outbound half the kernel sends accept and
// refresh_request over) and drives the inbound half through the kernel.
type Listener struct {
	log    log.Logger
	kernel Kernel
	id     lifecycle.TransportID

	mu      sync.Mutex
	closed  bool
	ln      net.Listener
	port    int
	conns   map[net.Conn]struct{}
	claim   map[lifecycle.DomainID]net.Conn
	speaker map[lifecycle.DomainID]net.Conn
	slots   chan struct{}
}

// NewListener creates the loopback listener and binds the transport to the
// kernel. It mints nothing: the caller mints the child domain this
// transport serves (RequestDomain with the parent — the kernel stays the
// sole minter of capabilities).
func NewListener(log log.Logger, k Kernel) (*Listener, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("lifecyclechannel: loopback listener: %w", err)
	}
	// The port is resolved once, here, where an error can still be returned.
	// Asserting it lazily in Port() would leave the only honest failure — a
	// listener that is not TCP and therefore has no port to forward to —
	// with nowhere to go but a silent zero, which composes into a broken -R.
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		_ = ln.Close()
		return nil, fmt.Errorf("lifecyclechannel: loopback listener is not TCP (%T)", ln.Addr())
	}
	tptHex, herr := randHex(8)
	if herr != nil {
		_ = ln.Close()
		return nil, herr
	}
	l := &Listener{
		log:     log,
		kernel:  k,
		id:      lifecycle.TransportID("tpt-" + tptHex),
		ln:      ln,
		port:    addr.Port,
		conns:   make(map[net.Conn]struct{}),
		claim:   make(map[lifecycle.DomainID]net.Conn),
		speaker: make(map[lifecycle.DomainID]net.Conn),
		slots:   make(chan struct{}, listenerMaxCandidates),
	}
	cleanup := func() { _ = ln.Close() }
	if berr := k.BindTransport(l.id, l); berr != nil {
		cleanup()
		return nil, fmt.Errorf("lifecyclechannel: bind listener transport: %w", berr)
	}
	go l.acceptLoop()
	return l, nil
}

// Port returns the loopback port the -R reverse forward terminates at,
// resolved at bind time.
func (l *Listener) Port() int {
	return l.port
}

// TransportID returns the transport id, for the composition root's
// transport-kind registry.
func (l *Listener) TransportID() lifecycle.TransportID {
	return l.id
}

// Send implements lifecycle.Port: it routes one outbound envelope (accept,
// refresh_request, domain_grant — the three kinds the kernel sends) to the
// connection that owns the addressed domain. accept routes to the handshake
// claimant; everything else routes to the authenticated speaker. Failures
// are best-effort.
func (l *Listener) Send(env lifecycle.Envelope) error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return ErrClosed
	}
	target := l.claim[env.Domain]
	if target == nil {
		target = l.speaker[env.Domain]
	}
	l.mu.Unlock()
	if target == nil {
		l.log.Debug("lifecycle listener outbound dropped: no speaker",
			"domain", env.Domain, "kind", env.Event.Kind)
		return nil
	}
	_ = target.SetWriteDeadline(time.Now().Add(listenerWriteTimeout))
	if _, err := lifecyclecodec.Encode(target, env); err != nil {
		l.log.Debug("lifecycle listener outbound send failed", "kind", env.Event.Kind, "error", err)
		return err
	}
	if env.Event.Kind == lifecycle.KindAccept {
		l.mu.Lock()
		if l.claim[env.Domain] == target {
			l.speaker[env.Domain] = target
			delete(l.claim, env.Domain)
		}
		l.mu.Unlock()
		_ = target.SetReadDeadline(time.Time{})
	}
	return nil
}

// Close tears the transport down: the domain ends (TransportLost), the
// listener closes and every live connection closes.
func (l *Listener) Close() error {
	l.lose()
	return nil
}

// lose is the single loss path, executed once: notify the kernel, mark the
// transport closed, and close the listener so the accept loop unblocks.
func (l *Listener) lose() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	_ = l.ln.Close()
	for c := range l.conns {
		_ = c.Close()
	}
	l.mu.Unlock()
	_ = l.kernel.TransportLost(l.id)
}

// acceptLoop serves candidate connections from the loopback listener until
// the transport closes. Candidates are bounded like the remote adapter's.
func (l *Listener) acceptLoop() {
	for {
		c, err := l.ln.Accept()
		if err != nil {
			return // listener closed (lose) or the transport broke
		}
		select {
		case l.slots <- struct{}{}:
		default:
			l.log.Debug("lifecycle listener candidate over bound; refusing", "max", listenerMaxCandidates)
			_ = c.Close()
			continue
		}
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			<-l.slots
			_ = c.Close()
			return
		}
		l.conns[c] = struct{}{}
		l.mu.Unlock()
		go l.serveCandidate(c)
	}
}

// serveCandidate pumps one candidate connection into the kernel until the
// stream ends or the candidate is rejected. It is the local analog of the
// remote adapter's serveCandidate: read frames, deliver authenticated
// envelopes to the kernel, report gaps for the authenticated speaker, and
// apply the end-of-stream policy.
func (l *Listener) serveCandidate(c net.Conn) {
	defer func() {
		<-l.slots
		l.mu.Lock()
		delete(l.conns, c)
		for d, claimant := range l.claim {
			if claimant == c {
				delete(l.claim, d)
			}
		}
		l.mu.Unlock()
		_ = c.Close()
	}()

	// Handshake bound: a candidate that cannot prove the capability within
	// the window is closed.
	_ = c.SetReadDeadline(time.Now().Add(lifecycle.HelloTimeout))

	dec := lifecyclecodec.NewDecoder(c, lifecyclecodec.Config{}, l.gapSink(c))
	for {
		env, err := dec.ReadFrame()
		if err == nil {
			if env.Event.Kind == lifecycle.KindHello {
				l.mu.Lock()
				l.claim[env.Domain] = c
				l.mu.Unlock()
				ierr := l.kernel.Ingest(l.id, env)
				l.mu.Lock()
				if ierr != nil && l.claim[env.Domain] == c {
					delete(l.claim, env.Domain)
				}
				l.mu.Unlock()
				if ierr != nil {
					l.log.Debug("lifecycle listener hello rejected",
						"domain", env.Domain, "error", ierr)
					return
				}
				continue // the accept comes through Send once acknowledged
			}
			if ierr := l.kernel.Ingest(l.id, env); ierr != nil {
				l.log.Debug("lifecycle listener envelope rejected",
					"domain", env.Domain, "kind", env.Event.Kind, "error", ierr)
				continue
			}
			continue
		}
		switch {
		case errors.Is(err, io.EOF):
			l.endOfStream(c)
			return
		case errors.Is(err, lifecyclecodec.ErrScanBudgetExhausted):
			_, _ = io.Copy(io.Discard, c)
			l.endOfStream(c)
			return
		default:
			if l.isSpeaker(c) {
				l.endOfStream(c)
			}
			return
		}
	}
}

func (l *Listener) isSpeaker(c net.Conn) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, owner := range l.speaker {
		if owner == c {
			return true
		}
	}
	return false
}

func (l *Listener) domainOf(c net.Conn) lifecycle.DomainID {
	l.mu.Lock()
	defer l.mu.Unlock()
	for d, owner := range l.speaker {
		if owner == c {
			return d
		}
	}
	return ""
}

// gapSink reports a gap to the kernel ONLY when this connection is the
// authenticated speaker of a domain — garbage from an unauthenticated
// candidate must never desynchronize a live domain.
func (l *Listener) gapSink(c net.Conn) lifecyclecodec.GapSink {
	return func(bytes, frames int) {
		d := l.domainOf(c)
		if d == "" {
			return
		}
		if err := l.kernel.NotifyGap(l.id, d, bytes, frames); err != nil {
			l.log.Debug("lifecycle listener gap notification rejected",
				"domain", d, "bytes", bytes, "frames", frames, "error", err)
		}
	}
}

// endOfStream applies the end-of-stream policy: a domain whose speaker
// closed cleanly (domain_closed) ends cleanly; a domain still live whose
// speaker vanished lost its voice — the kernel marks it Lost (protocol §12).
// The listener transport itself survives: it serves the next candidate.
func (l *Listener) endOfStream(c net.Conn) {
	d := l.domainOf(c)
	if d == "" {
		return // a candidate or a superseded connection ended
	}
	dom, ok := l.kernel.Domain(d)
	if ok {
		switch dom.State {
		case lifecycle.DomainClosed, lifecycle.DomainLost:
			l.log.Info("lifecycle listener transport ended cleanly", "domain", d)
			return
		}
	}
	l.log.Info("lifecycle listener transport ended with a live domain; marking lost", "domain", d)
	_ = l.kernel.TransportLost(l.id)
}
