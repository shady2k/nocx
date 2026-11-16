// Package sshtest provides switches for tests that need a real SSH link to
// misbehave on cue.
//
// A FaultProxy sits between a client and its server and changes what the wire
// does while the test runs, so a test states a network condition the way it
// states any other input: by name, at the moment it matters.
//
// It forwards bytes rather than packets, so it stages the APPLICATION's view:
// a write that returns success and a reply that never arrives. That is exactly
// what an SSH client sees through a black-holed flow, and it is deliberately
// stronger than a suspended laptop's socket — the kernel's own TCP keepalive
// cannot rescue a client whose peer (this proxy) is answering at the TCP
// layer. A prober that survives this survives the real thing.
//
// The quiet failures are the ones with no other way to stage them, and every
// one of them is a shape a person meets:
//
//   - a laptop that suspended, or a NAT that dropped the flow: the socket
//     stays open, writes succeed, and nothing ever comes back (Blackhole);
//   - a loaded server that answers, just late — which must NOT be treated as
//     a loss, and must be visible to the person while it lasts (Slow);
//   - a listener that is closed while established flows live on, which is what
//     a host being restarted or firewalled looks like from this side (Refuse);
//   - a flow that survives in one direction and not the other, which is what
//     gets a probe answered when nothing else is (LossOneWay);
//   - a host that accepts the connection and answers nothing, so the SSH
//     version exchange never completes (HoldBanner);
//   - a host that was rebooted behind the flow: the socket is dead and both
//     ends learn it by reset rather than by an orderly end of file (Reset).
//
// Every switch is read per chunk, never captured per connection, so it applies
// to flows that are already open. That is the whole point: the test changes
// the network under a live session.
//
// This is test support and is never imported by production code; it lives in a
// normal file in a `…test` package so that another package's tests can import
// it, which a `_test.go` file cannot offer.
package sshtest

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Direction names one half of a proxied connection.
type Direction int32

const (
	// ClientToServer is the request half: the bytes a client sends towards
	// the server.
	ClientToServer Direction = iota
	// ServerToClient is the reply half.
	ServerToClient
)

// linkMode is what the wire is doing right now.
type linkMode int32

const (
	// linkPass forwards both directions unchanged.
	linkPass linkMode = iota
	// linkBlackhole keeps both sockets open and forwards nothing. Reads
	// continue and the bytes are discarded, so the writer's send never
	// blocks and never fails — the connection looks perfectly healthy from
	// the outside and is entirely deaf.
	linkBlackhole
	// linkSlow forwards everything, late. A loaded host, not a lost one.
	linkSlow
	// linkLossOneWay drops one direction and forwards the other unchanged.
	linkLossOneWay
	// linkBandwidth forwards everything, paced to a byte rate.
	linkBandwidth
	// linkRefuse stops accepting: the listening socket is closed, so a new
	// dial is refused, while the flows already relayed keep working.
	linkRefuse
)

// FaultProxy is a TCP relay in front of one upstream address whose behaviour
// is switchable while connections are live.
type FaultProxy struct {
	t        testing.TB
	upstream string
	addr     string
	// done is closed when the proxy itself is over, so a paced or slowed
	// delivery can stop waiting instead of sleeping out its budget while
	// Close waits for it.
	done chan struct{}

	mode    atomic.Int32
	delayNS atomic.Int64
	bps     atomic.Int64
	lossDir atomic.Int32
	held    atomic.Bool

	mu     sync.Mutex
	ln     net.Listener
	relays []*relay
	closed bool
	wg     sync.WaitGroup
}

// NewFaultProxy starts a relay in front of upstream ("host:port") and returns
// it passing everything through unchanged. It is closed by t.Cleanup.
func NewFaultProxy(t testing.TB, upstream string) *FaultProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fault proxy: listen: %v", err)
	}
	p := &FaultProxy{t: t, upstream: upstream, addr: ln.Addr().String(), done: make(chan struct{})}
	p.listen(ln)
	t.Cleanup(p.Close)
	return p
}

// Addr is the loopback address a client should be pointed at instead of the
// server's own.
func (p *FaultProxy) Addr() string { return p.addr }

// Pass forwards both directions unchanged, and resumes accepting if Refuse
// closed the listener. It does not end a HoldBanner hold: Release does that,
// because a hold is a gate and not a condition of the wire. It DOES end a
// SilenceEstablished silence: the established flows speak again.
func (p *FaultProxy) Pass() {
	silenced := p.snapshotRelays()
	for _, r := range silenced {
		r.silent.Store(false)
	}
	p.setMode(linkPass)
	for _, r := range silenced {
		r.flush()
	}
}

// SilenceEstablished holds everything on every connection that is open NOW,
// while connections made afterwards pass untouched — the shape of a session
// process starved behind a flow that was never closed: the established
// connection answers nothing and the same host still answers a new one. It is
// what a look-only dial is evidence ABOUT, so it has to be a per-connection
// condition and not the wire-wide Blackhole.
//
// The held bytes are KEPT, not dropped, and Pass delivers them in order: a host
// that was busy and comes back answers the probe it was still holding, which is
// the "and then it answers" half the policy needs. Nothing is asserted about
// when; the senders' writes succeed throughout, exactly as they do through a
// starved peer.
func (p *FaultProxy) SilenceEstablished() {
	for _, r := range p.snapshotRelays() {
		r.silent.Store(true)
	}
}

// Blackhole keeps both sockets open and forwards nothing.
func (p *FaultProxy) Blackhole() { p.setMode(linkBlackhole) }

// Slow forwards everything, late: each chunk of each direction is held by d
// before it is written on, so a round trip is late by about twice d, and a
// steady stream is throttled rather than merely delayed.
func (p *FaultProxy) Slow(d time.Duration) {
	p.delayNS.Store(int64(d))
	p.setMode(linkSlow)
}

// LossOneWay drops every chunk travelling in d and forwards the other
// direction unchanged. The dropped sender's write still succeeds, which is
// what a one-way loss looks like from a socket that is still open.
func (p *FaultProxy) LossOneWay(d Direction) {
	p.lossDir.Store(int32(d))
	p.setMode(linkLossOneWay)
}

// Bandwidth paces delivery to bytesPerSecond in each direction, with no burst
// allowance: a transfer of n bytes takes about n/bytesPerSecond. Order is
// preserved, so a steady stream arrives whole, just slowly.
func (p *FaultProxy) Bandwidth(bytesPerSecond int) {
	p.bps.Store(int64(bytesPerSecond))
	p.setMode(linkBandwidth)
}

// Refuse stops accepting connections: the listening socket is closed, so a
// new dial is refused at the TCP layer with nothing sent, while flows already
// relayed are untouched. Any other mode opens the listener again on the same
// address, so a test can stage a host that was unreachable and came back.
func (p *FaultProxy) Refuse() { p.setMode(linkRefuse) }

// HoldBanner keeps the connection open and delivers nothing in either
// direction until Release: the TCP connection is accepted, and the SSH version
// exchange never completes. Everything that arrives meanwhile is kept, so
// Release delivers exactly what the hold withheld, in the order it arrived.
//
// It is a gate rather than a condition of the wire, so it composes with the
// modes above and survives Pass: only Release ends it.
func (p *FaultProxy) HoldBanner() { p.held.Store(true) }

// Release ends a HoldBanner hold and delivers everything it kept, so it
// returns once the peers have taken those bytes.
func (p *FaultProxy) Release() {
	p.held.Store(false)
	for _, r := range p.snapshotRelays() {
		r.flush()
	}
}

// Reboot stages a host that lost the flow's state without saying so. Bytes
// already held by SilenceEstablished are discarded, nothing more is forwarded,
// and the NEXT write from the client is answered with a reset — what a host
// that was rebooted behind a flow does to a byte it no longer has state for.
// New connections still pass, because the rebooted host accepts them; that is
// what makes the look-only dial evidence and the nudge the thing that provokes
// the reset (design §5.3).
func (p *FaultProxy) Reboot() {
	for _, r := range p.snapshotRelays() {
		r.silent.Store(false)
		r.reboot.Store(true)
		r.mu.Lock()
		r.pending = nil
		r.mu.Unlock()
	}
}

// HeldChunks answers how many chunks the current silence has queued and not
// delivered. It is the observable a test waits for when it needs "the peer's
// bytes have been consumed and are being withheld" — the parked probe the
// design's nudge is evidence about — rather than a duration.
func (p *FaultProxy) HeldChunks() int {
	total := 0
	for _, r := range p.snapshotRelays() {
		r.mu.Lock()
		total += len(r.pending)
		r.mu.Unlock()
	}
	return total
}

// Reset destroys every relayed connection with a TCP reset, so both peers see
// a dead flow rather than an orderly end of file: the shape of a host that was
// rebooted behind a connection that was never closed.
func (p *FaultProxy) Reset() { p.destroy(true) }

// Cut closes every relayed connection: the loud loss, staged from the middle
// of the wire rather than from the server, so it can be told apart from a
// server that chose to close.
func (p *FaultProxy) Cut() { p.destroy(false) }

// Close stops listening, destroys every relayed connection and waits for the
// proxy's own goroutines. It is idempotent.
func (p *FaultProxy) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	ln := p.ln
	p.ln = nil
	p.mu.Unlock()

	if ln != nil {
		_ = ln.Close()
	}
	close(p.done)
	p.destroy(false)
	p.wg.Wait()
}

// setMode switches the condition and keeps the listening socket in step: a
// refusal means no listener, and every other mode means one.
func (p *FaultProxy) setMode(m linkMode) {
	p.mode.Store(int32(m))

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	wantListening := m != linkRefuse
	ln := p.ln
	if wantListening == (ln != nil) {
		p.mu.Unlock()
		return
	}
	if !wantListening {
		p.ln = nil
	}
	p.mu.Unlock()

	if !wantListening {
		_ = ln.Close()
		return
	}
	ln, err := net.Listen("tcp", p.addr)
	if err != nil {
		p.t.Fatalf("fault proxy: listen again on %s: %v", p.addr, err)
		return
	}
	p.listen(ln)
}

// listen installs ln as the proxy's listener and starts accepting on it.
func (p *FaultProxy) listen(ln net.Listener) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = ln.Close()
		return
	}
	p.ln = ln
	p.wg.Add(1)
	p.mu.Unlock()
	go p.acceptLoop(ln)
}

func (p *FaultProxy) acceptLoop(ln net.Listener) {
	defer p.wg.Done()
	for {
		down, err := ln.Accept()
		if err != nil {
			return
		}
		up, err := net.Dial("tcp", p.upstream)
		if err != nil {
			// One refused upstream connection is not the end of the proxy:
			// the client learns of it from this close, and the next dial is
			// served normally.
			_ = down.Close()
			continue
		}
		toUpstream := &relay{p: p, dir: ClientToServer, src: down, dst: up}
		toClient := &relay{p: p, dir: ServerToClient, src: up, dst: down}

		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			_ = down.Close()
			_ = up.Close()
			return
		}
		p.relays = append(p.relays, toUpstream, toClient)
		p.wg.Add(2)
		p.mu.Unlock()

		go p.pipe(toUpstream)
		go p.pipe(toClient)
	}
}

// pipe copies one direction of one connection until it ends.
func (p *FaultProxy) pipe(r *relay) {
	defer p.wg.Done()
	defer func() { _ = r.dst.Close() }()
	buf := make([]byte, 32*1024)
	for {
		n, err := r.src.Read(buf)
		if n > 0 {
			if werr := r.deliver(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// snapshotRelays copies the live relay list.
func (p *FaultProxy) snapshotRelays() []*relay {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*relay(nil), p.relays...)
}

// destroy closes every relayed connection and forgets them. With reset it
// marks every socket for an immediate reset BEFORE closing any of them: a pipe
// woken by the first close would otherwise close its peer gracefully on its way
// out, and that peer would see an end of file instead of the reset this is
// staging.
func (p *FaultProxy) destroy(reset bool) {
	p.mu.Lock()
	relays := p.relays
	p.relays = nil
	p.mu.Unlock()

	conns := make([]net.Conn, 0, 2*len(relays))
	for _, r := range relays {
		conns = append(conns, r.src, r.dst)
	}
	if reset {
		for _, c := range conns {
			if tcp, ok := c.(*net.TCPConn); ok {
				_ = tcp.SetLinger(0)
			}
		}
	}
	for _, c := range conns {
		_ = c.Close()
	}
}

// relay is one direction of one proxied connection: bytes read from src and
// written to dst, subject to whatever the proxy is doing at that moment.
type relay struct {
	p   *FaultProxy
	dir Direction
	src net.Conn
	dst net.Conn

	// silent is SilenceEstablished's mark on THIS connection: its bytes are
	// discarded while the flow itself stays open, and a connection made after
	// the call is not marked. Read per chunk, like every other switch here.
	silent atomic.Bool
	// reboot marks a flow whose host has lost its state: the next byte from
	// the client is answered with a reset.
	reboot atomic.Bool

	// mu serialises this direction's deliveries, so the order a hold kept is
	// the order Release replays, and a pipe never writes to dst while a
	// release is writing to it.
	mu      sync.Mutex
	pending [][]byte
}

// deliver forwards one chunk, or keeps it while the hold is raised.
//
// The PACING HAPPENS OUTSIDE THE MUTEX (review finding 15): a slow or
// bandwidth-limited chunk used to sleep while holding r.mu, and Close waits for
// the relays — so a sufficiently low rate could hold test cleanup for the whole
// budget, and no socket close could interrupt it. The lock now covers only the
// decision and the write.
func (r *relay) deliver(chunk []byte) error {
	if r.reboot.Load() {
		// The host lost this flow's state: the byte is answered with a reset.
		r.p.resetFlow(r.src, r.dst)
		return errRelayReset
	}
	r.mu.Lock()
	if r.p.held.Load() || r.silent.Load() {
		r.pending = append(r.pending, append([]byte(nil), chunk...))
		r.mu.Unlock()
		return nil
	}
	if len(r.pending) > 0 {
		// The hold was lifted a moment ago and older bytes are still queued:
		// they go first, in order, or a reader would see the stream reordered.
		r.pending = append(r.pending, append([]byte(nil), chunk...))
		err := r.flushLocked()
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()

	r.pace(len(chunk))

	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pending) > 0 {
		r.pending = append(r.pending, append([]byte(nil), chunk...))
		return r.flushLocked()
	}
	return r.write(chunk)
}

// errRelayReset ends one direction of a flow the host answered with a reset.
var errRelayReset = errors.New("sshtest: the flow was reset")

// pace waits out this chunk's delay, interrupted by the proxy closing.
func (r *relay) pace(n int) {
	var wait time.Duration
	switch linkMode(r.p.mode.Load()) {
	case linkSlow:
		wait = time.Duration(r.p.delayNS.Load())
	case linkBandwidth:
		if bps := r.p.bps.Load(); bps > 0 {
			wait = time.Duration(int64(n) * int64(time.Second) / bps)
		}
	}
	if wait <= 0 {
		return
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-r.p.done:
	}
}

// flush delivers everything a hold kept, in the order it arrived.
func (r *relay) flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	_ = r.flushLocked()
}

// flushLocked is flush for a caller that holds the mutex. Callers hold it so a
// chunk arriving as the hold lifts cannot overtake the older ones.
func (r *relay) flushLocked() error {
	var firstErr error
	for _, chunk := range r.pending {
		if err := r.write(chunk); err != nil {
			firstErr = err
			break
		}
	}
	r.pending = nil
	return firstErr
}

// write applies the current condition to one chunk and writes what survives.
// Callers hold r.mu.
func (r *relay) write(chunk []byte) error {
	switch linkMode(r.p.mode.Load()) {
	case linkBlackhole:
		// Read and discard: the sender's write succeeded, and the reply will
		// never come.
		return nil
	case linkLossOneWay:
		if r.dir == Direction(r.p.lossDir.Load()) {
			return nil
		}
	}
	// Slow and bandwidth waits happened in pace, outside this lock.
	_, err := r.dst.Write(chunk)
	return err
}

// resetFlow answers a byte on a flow the host no longer holds with a TCP reset,
// which is what a rebooted host does: both sockets go with linger 0 so the
// peer learns by reset rather than by an orderly end of file.
func (p *FaultProxy) resetFlow(conns ...net.Conn) {
	for _, c := range conns {
		if tcp, ok := c.(*net.TCPConn); ok {
			_ = tcp.SetLinger(0)
		}
	}
	for _, c := range conns {
		_ = c.Close()
	}
}
