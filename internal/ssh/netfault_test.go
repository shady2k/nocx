package ssh

// A network that can be made to misbehave on cue.
//
// The suite already had ONE way to lose a connection: testSSHServer.killConns
// closes the server side, which the client observes immediately. That is the
// LOUD loss, and it is the one shape the product already handles — the channel
// EOFs, Done fires, the session ends, the tab is marked. Every failure that
// matters in the field is one of the quiet ones, and none of them could be
// staged here at all:
//
//   - a laptop that suspended, or a NAT that dropped the flow: the socket
//     stays open, writes succeed, and nothing ever comes back;
//   - a loaded server that answers, just late — which must NOT be treated as
//     a loss, and must be visible to the person while it lasts;
//   - a server that was slow and recovers, which must clear that statement.
//
// faultProxy sits between the client and the test server and switches between
// those regimes at runtime, so a test states a network condition the way it
// states any other input.
//
// It forwards bytes rather than packets, so it stages the APPLICATION's view:
// a write that returns success and a reply that never arrives. That is exactly
// what the SSH client sees through a black-holed flow, and it is deliberately
// stronger than a suspended laptop's socket — the kernel's own TCP keepalive
// cannot rescue a client whose peer (this proxy) is answering at the TCP layer.
// A prober that survives this survives the real thing.

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// faultMode is what the network is doing right now.
type faultMode int32

const (
	// faultPass forwards both directions unchanged.
	faultPass faultMode = iota
	// faultBlackhole keeps both sockets open and forwards nothing. Reads
	// continue and the bytes are discarded, so the writer's send never
	// blocks and never fails — the connection looks perfectly healthy from
	// the outside and is entirely deaf.
	faultBlackhole
	// faultSlow forwards everything, late. A loaded host, not a lost one.
	faultSlow
)

// faultProxy is a TCP relay whose behaviour is switchable while connections
// are live.
type faultProxy struct {
	t        *testing.T
	ln       net.Listener
	upstream string
	addr     string

	mode    atomic.Int32
	delayNS atomic.Int64

	mu     sync.Mutex
	conns  []net.Conn
	closed bool
	wg     sync.WaitGroup
}

// newFaultProxy starts a relay in front of upstream and returns it in
// faultPass. It is closed by t.Cleanup.
func newFaultProxy(t *testing.T, upstream string) *faultProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fault proxy listen: %v", err)
	}
	p := &faultProxy{t: t, ln: ln, upstream: upstream, addr: ln.Addr().String()}
	p.wg.Add(1)
	go p.accept()
	t.Cleanup(p.close)
	return p
}

// pass, blackhole and slow name the regime rather than set a field, so a test
// reads as the condition it is staging.
func (p *faultProxy) pass()      { p.mode.Store(int32(faultPass)) }
func (p *faultProxy) blackhole() { p.mode.Store(int32(faultBlackhole)) }
func (p *faultProxy) slow(d time.Duration) {
	p.delayNS.Store(int64(d))
	p.mode.Store(int32(faultSlow))
}

// cut closes every relayed connection: the loud loss, staged from the middle
// of the wire rather than from the server, so it can be told apart from a
// server that chose to close.
func (p *faultProxy) cut() {
	p.mu.Lock()
	conns := append([]net.Conn(nil), p.conns...)
	p.conns = nil
	p.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

func (p *faultProxy) close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()
	_ = p.ln.Close()
	p.cut()
	p.wg.Wait()
}

func (p *faultProxy) accept() {
	defer p.wg.Done()
	for {
		down, err := p.ln.Accept()
		if err != nil {
			return
		}
		up, err := net.Dial("tcp", p.upstream)
		if err != nil {
			_ = down.Close()
			return
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			_ = down.Close()
			_ = up.Close()
			return
		}
		p.conns = append(p.conns, down, up)
		p.mu.Unlock()
		p.wg.Add(2)
		go p.relay(down, up)
		go p.relay(up, down)
	}
}

// relay copies src→dst under the current regime. It never returns on a
// blackholed byte: dropping it is the whole point, and closing here would
// turn a silent death into the loud one the test is not staging.
func (p *faultProxy) relay(src, dst net.Conn) {
	defer p.wg.Done()
	defer func() { _ = dst.Close() }()
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			switch faultMode(p.mode.Load()) {
			case faultBlackhole:
				// Read and discard: the sender's write succeeded, and the
				// reply will never come.
			case faultSlow:
				time.Sleep(time.Duration(p.delayNS.Load()))
				if _, werr := dst.Write(buf[:n]); werr != nil {
					return
				}
			default:
				if _, werr := dst.Write(buf[:n]); werr != nil {
					return
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				return
			}
			return
		}
	}
}
