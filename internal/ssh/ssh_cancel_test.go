//go:build nocx_local_ssh

package ssh

// Cancellation on the pool's dial path: a dial that is still inside a TCP
// connect or an SSH handshake must return the caller's context error rather
// than outlive it, and the pool must be left with nothing behind.
//
// These tests drive AcquirePooled — the seam the local helper's ssh service
// dials through — rather than a second, coordinator-only dial.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/waittest"
	gossh "golang.org/x/crypto/ssh"
)

// startTestSSHServerWithKey starts a test SSH server using the given signer
// for both host and user authentication (same key for both).
func startTestSSHServerWithKey(t *testing.T, signer gossh.Signer) *testSSHServer {
	t.Helper()

	config := &gossh.ServerConfig{
		PublicKeyCallback: func(meta gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			if bytes.Equal(key.Marshal(), signer.PublicKey().Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("gossh: unknown public key for %q", meta.User())
		},
	}
	config.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("test server listen: %v", err)
	}

	srv := &testSSHServer{
		t:             t,
		hostSigner:    signer,
		userSigner:    signer,
		listener:      listener,
		addr:          listener.Addr().String(),
		shellReady:    make(chan struct{}),
		windowChanged: make(chan struct{}, 8),
		execCommands:  make(chan string, 8),
		liveConns:     make(map[*gossh.ServerConn]struct{}),
	}

	go srv.acceptLoop(config)
	return srv
}

// blockingListener accepts TCP connections but never initiates an SSH
// handshake. The client's gossh.NewClientConn call blocks until ctx
// cancellation closes the socket.
type blockingListener struct {
	l          net.Listener
	done       chan struct{}
	accepted   chan struct{}
	acceptOnce sync.Once
	conns      []net.Conn
	connsMu    sync.Mutex
}

func startBlockingListener(t *testing.T) *blockingListener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	bl := &blockingListener{l: l, done: make(chan struct{}), accepted: make(chan struct{})}
	go bl.acceptLoop()
	return bl
}

func (bl *blockingListener) acceptLoop() {
	for {
		conn, err := bl.l.Accept()
		if err != nil {
			return
		}
		bl.connsMu.Lock()
		bl.conns = append(bl.conns, conn)
		bl.connsMu.Unlock()
		bl.acceptOnce.Do(func() { close(bl.accepted) })
		// Hold the connection open, never do SSH handshake.
		<-bl.done
	}
}

func (bl *blockingListener) addr() string {
	return bl.l.Addr().String()
}

func (bl *blockingListener) close() {
	close(bl.done)
	_ = bl.l.Close()
	bl.connsMu.Lock()
	for _, c := range bl.conns {
		_ = c.Close()
	}
	bl.connsMu.Unlock()
}

// poolSpec builds the one-connection spec these tests dial, with the
// fixture's own host-key answer: trust is not their subject.
func poolSpec(t *testing.T, addr string, auth []gossh.AuthMethod, route ...PooledHop) PooledSpec {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	port, _ := strconv.Atoi(portStr)
	return PooledSpec{
		Host:     host,
		Port:     port,
		User:     "test",
		Identity: "cancel-fixture",
		Config: &gossh.ClientConfig{
			User:            "test",
			Auth:            auth,
			HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec // a fixture server or a listener that never answers
			Timeout:         30 * time.Second,
		},
		Route: route,
	}
}

// TestDialCancel_DirectHandshake verifies that cancelling the context during
// the SSH handshake returns promptly with context.Canceled, not a hung dial.
// Without the watchdog goroutine in dialDirect, this test hangs until the
// timeout expires.
func TestDialCancel_DirectHandshake(t *testing.T) {
	bl := startBlockingListener(t)
	defer bl.close()

	ctx, cancel := context.WithCancel(context.Background())

	client, err := NewReal(log.NewSlogAdapter(nil))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	spec := poolSpec(t, bl.addr(), []gossh.AuthMethod{gossh.Password("irrelevant")})

	errCh := make(chan error, 1)
	go func() {
		_, err := client.AcquirePooled(ctx, spec)
		errCh <- err
	}()

	// Wait for the TCP connection to reach the blocking listener.
	<-bl.accepted
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dial did not return after cancellation within 5s — watchdog missing")
	}
}

// TestDialCancel_JumpHandshake verifies that cancelling the context during
// the target's SSH handshake through a jump host returns promptly with
// context.Canceled. The bastion is a real test SSH server with direct-tcpip
// support; the target is a blocking TCP listener that accepts but never
// completes an SSH handshake. Cancellation closes the forwarded connection,
// unblocking gossh.NewClientConn, and releases the bastion handle.
func TestDialCancel_JumpHandshake(t *testing.T) {
	// Generate keys for the bastion.
	_, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(privKey)
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}

	// Start bastion with direct-tcpip support.
	bastion := startTestSSHServerWithKey(t, signer)
	defer bastion.close()

	// Target: a TCP listener that accepts but never does SSH handshake.
	bl := startBlockingListener(t)
	defer bl.close()

	client, err := NewReal(log.NewSlogAdapter(nil))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithCancel(context.Background())

	bastionHost, bastionPortStr, _ := net.SplitHostPort(bastion.addr)
	bastionPort, _ := strconv.Atoi(bastionPortStr)
	hop := PooledHop{
		Host:     bastionHost,
		Port:     bastionPort,
		User:     "jumpuser",
		Identity: "jump-fixture",
		Config: &gossh.ClientConfig{
			User:            "jumpuser",
			Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
			HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec // fixture server started by this test
			Timeout:         10 * time.Second,
		},
	}
	spec := poolSpec(t, bl.addr(), []gossh.AuthMethod{gossh.Password("irrelevant")}, hop)

	errCh := make(chan error, 1)
	go func() {
		_, err := client.AcquirePooled(ctx, spec)
		errCh <- err
	}()

	// Wait for the target connection to reach the blocking listener.
	<-bl.accepted
	cancel()

	select {
	case er := <-errCh:
		if !errors.Is(er, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", er)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("dial through jump did not return after cancellation within 10s")
	}

	// Bastion pool entry must be released — no leaked handles.
	if got := client.dial.pool.Count(); got != 0 {
		t.Fatalf("pool.Count()=%d after cancelled jump dial, want 0 (bastion handle released)", got)
	}
}

// ---------------------------------------------------------------------------
// Pool waiter cancellation tests
// ---------------------------------------------------------------------------

// TestPoolAcquire_WaiterCancellation verifies that a goroutine waiting on an
// in-flight dial returns ctx.Err() when its context cancels, without getting
// a connection and without touching pool state.
func TestPoolAcquire_WaiterCancellation(t *testing.T) {
	pool := NewConnPool(log.NewSlogAdapter(nil))
	key := poolKey{host: "h", user: "u"}

	blockCh := make(chan struct{})
	dialStarted := make(chan struct{})

	pool.dial = func(key poolKey) (sshClientConn, error) {
		close(dialStarted)
		<-blockCh // block until released
		return &fakeClient{}, nil
	}

	// Start first dialer (blocks in dial).
	firstErrCh := make(chan error, 1)
	go func() {
		_, err := pool.Acquire(context.Background(), key)
		firstErrCh <- err
	}()

	// Wait for the dial to actually start.
	<-dialStarted
	waittest.WaitForTimeout(t, "pool dial registration", 2*time.Second, func() bool {
		pool.mu.Lock()
		defer pool.mu.Unlock()
		_, ok := pool.dialing[key]
		return ok
	})

	// Second caller — waiter — with cancellable context.
	ctx2, cancel2 := context.WithCancel(context.Background())
	secondErrCh := make(chan error, 1)
	go func() {
		_, err := pool.Acquire(ctx2, key)
		secondErrCh <- err
	}()

	// Cancellation is itself the observable event; the in-flight dial is
	// still blocked, so the waiter cannot race a completed dial.
	cancel2()

	// Waiter must return promptly with context.Canceled.
	select {
	case err := <-secondErrCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter: expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not return after context cancellation")
	}

	// Release the first dialer — its connection should be usable.
	close(blockCh)
	if err := <-firstErrCh; err != nil {
		t.Fatalf("first dialer: %v", err)
	}

	// Pool must have exactly one entry (the first dialer's result).
	if got := pool.Count(); got != 1 {
		t.Fatalf("pool.Count()=%d after waiter cancelled, want 1", got)
	}
}

// cancelling while the first dialer is still in-flight under -race.
func TestPoolAcquire_WaiterCancellationConcurrent(t *testing.T) {
	pool := NewConnPool(log.NewSlogAdapter(nil))
	key := poolKey{host: "h", user: "u", identity: "id"}

	blockCh := make(chan struct{})
	dialStarted := make(chan struct{})

	pool.dial = func(key poolKey) (sshClientConn, error) {
		close(dialStarted)
		<-blockCh
		return &fakeClient{}, nil
	}

	// Start the first dialer.
	firstErrCh := make(chan error, 1)
	go func() {
		_, err := pool.Acquire(context.Background(), key)
		firstErrCh <- err
	}()

	<-dialStarted
	waittest.WaitForTimeout(t, "pool dial registration", 2*time.Second, func() bool {
		pool.mu.Lock()
		defer pool.mu.Unlock()
		_, ok := pool.dialing[key]
		return ok
	})

	// Spawn waiters. Half get a cancellable context and cancel immediately;
	// the other half keep their context alive and will get a connection when
	// the first dialer completes.
	const waiters = 8
	var wg sync.WaitGroup
	var cancelledMu sync.Mutex
	var cancelledCount int
	for i := range waiters {
		ctx := context.Background()
		if i%2 == 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithCancel(context.Background())
			cancel() // cancel immediately
		}
		wg.Add(1)
		go func(ctx context.Context) {
			defer wg.Done()
			_, err := pool.Acquire(ctx, key)
			if errors.Is(err, context.Canceled) {
				cancelledMu.Lock()
				cancelledCount++
				cancelledMu.Unlock()
			}
		}(ctx)
	}

	// Wait for every already-cancelled waiter to report its cancellation
	// before releasing the in-flight dial.
	waittest.WaitForTimeout(t, "cancelled waiters to return", 2*time.Second, func() bool {
		cancelledMu.Lock()
		defer cancelledMu.Unlock()
		return cancelledCount == waiters/2
	})

	// Release the first dialer — remaining waiters should now succeed.
	close(blockCh)
	if err := <-firstErrCh; err != nil {
		t.Fatalf("first dialer: %v", err)
	}
	wg.Wait()

	if got := cancelledCount; got != waiters/2 {
		t.Errorf("cancelled waiters: got %d, want %d", got, waiters/2)
	}

	if got := pool.Count(); got != 1 {
		t.Fatalf("pool.Count()=%d after concurrent cancellations, want 1", got)
	}
}
