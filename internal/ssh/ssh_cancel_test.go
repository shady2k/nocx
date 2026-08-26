package ssh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/testwait"
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

// TestDialCancel_DirectHandshake verifies that cancelling the context during
// the SSH handshake returns promptly with context.Canceled, not a hung dial.
// Without the watchdog goroutine in dialDirect, this test hangs until the
// 5-second timeout expires.
func TestDialCancel_DirectHandshake(t *testing.T) {
	bl := startBlockingListener(t)
	defer bl.close()

	ctx, cancel := context.WithCancel(context.Background())

	client, err := NewReal(
		log.NewSlogAdapter(nil),
		WithKnownHostsFile("/dev/null"), // bypass host-key check
	)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	// Start Connect in a goroutine — it will block in NewClientConn.
	errCh := make(chan error, 1)
	go func() {
		_, err := client.Connect(
			ctx, bl.addr(),
			WithUser("test"),
			WithAuthMethods([]gossh.AuthMethod{
				gossh.Password("irrelevant"),
			}),
			WithPTYSize(80, 24, 0, 0),
		)
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

// TestDialCancel_DirectNewClientConn tests the dialDirect watchdog directly.
// It dials a blocking listener through dialDirect and cancels the context
// while gossh.NewClientConn is blocked.
func TestDialCancel_DirectNewClientConn(t *testing.T) {
	bl := startBlockingListener(t)
	defer bl.close()

	ctx, cancel := context.WithCancel(context.Background())
	d := &dialer{client: &RealClient{
		log: log.NewSlogAdapter(nil),
	}}

	cfg := &gossh.ClientConfig{
		User:            "test",
		Auth:            []gossh.AuthMethod{gossh.Password("x")},
		Timeout:         30 * time.Second,
		HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec // test-only, target is a blocking listener
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := d.dialDirect(ctx, bl.addr(), cfg, "testhost", "test")
		errCh <- err
	}()

	<-bl.accepted
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dialDirect did not return after cancellation within 5s")
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
	testwait.WaitForTimeout(t, "pool dial registration", 2*time.Second, func() bool {
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
	testwait.WaitForTimeout(t, "pool dial registration", 2*time.Second, func() bool {
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
	testwait.WaitForTimeout(t, "cancelled waiters to return", 2*time.Second, func() bool {
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

// ---------------------------------------------------------------------------
// Channel Close idempotency tests (closeOnce guards are already present from
// 9d411ec; these tests verify the guard works).
// ---------------------------------------------------------------------------

// TestChannelClose_Repeated verifies that calling Close multiple times on the
// same channel is safe and idempotent — no panic, no double-close of done.
func TestChannelClose_Repeated(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	khPath := writeKnownHosts(t, srv, srv.addr)
	client, err := NewReal(
		log.NewSlogAdapter(nil),
		WithKnownHostsFile(khPath),
	)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	ch, err := client.Connect(
		context.Background(), srv.addr,
		WithUser("test"),
		WithAuthMethods([]gossh.AuthMethod{
			gossh.PublicKeys(srv.userSigner),
		}),
		WithPTYSize(80, 24, 0, 0),
	)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	<-srv.shellReady

	// Close repeatedly — must not panic or error.
	for i := range 10 {
		if err := ch.Close(); err != nil {
			t.Fatalf("Close #%d: %v", i+1, err)
		}
	}
}

// TestChannelClose_Concurrent verifies that concurrent calls to Close from
// multiple goroutines are safe (no data race, no double-close).
func TestChannelClose_Concurrent(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	khPath := writeKnownHosts(t, srv, srv.addr)
	client, err := NewReal(
		log.NewSlogAdapter(nil),
		WithKnownHostsFile(khPath),
	)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	ch, err := client.Connect(
		context.Background(), srv.addr,
		WithUser("test"),
		WithAuthMethods([]gossh.AuthMethod{
			gossh.PublicKeys(srv.userSigner),
		}),
		WithPTYSize(80, 24, 0, 0),
	)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	<-srv.shellReady

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = ch.Close()
		}()
	}
	wg.Wait()
	// Pass: no panic, no data race.
}

// ---------------------------------------------------------------------------
// Resize tests
// ---------------------------------------------------------------------------

// TestResize_AfterDisconnect verifies that Resize returns *ErrDisconnected
// after the underlying SSH connection is closed, instead of blocking.
func TestResize_AfterDisconnect(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	khPath := writeKnownHosts(t, srv, srv.addr)
	client, err := NewReal(
		log.NewSlogAdapter(nil),
		WithKnownHostsFile(khPath),
	)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	ch, err := client.Connect(
		context.Background(), srv.addr,
		WithUser("test"),
		WithAuthMethods([]gossh.AuthMethod{
			gossh.PublicKeys(srv.userSigner),
		}),
		WithPTYSize(80, 24, 0, 0),
	)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	<-srv.shellReady

	// Close the server — this triggers session.Wait() → ch.Close() → Done().
	srv.close()

	// Wait for the channel to detect disconnect.
	select {
	case <-ch.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("channel did not detect disconnect within 5s")
	}

	// Resize must return ErrDisconnected, not block.
	done := make(chan error, 1)
	go func() {
		done <- ch.Resize(context.Background(), 100, 40, 0, 0)
	}()

	select {
	case err := <-done:
		var discErr *ErrDisconnected
		if !errors.As(err, &discErr) {
			t.Fatalf("expected *ErrDisconnected after disconnect, got %T: %v", err, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Resize blocked after disconnect — should return promptly")
	}
}

// TestResize_CancelledContext verifies that a cancelled context makes Resize
// return promptly even when the transport is healthy. This tests the
// goroutine watchdog path in Resize.
func TestResize_CancelledContext(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	khPath := writeKnownHosts(t, srv, srv.addr)
	client, err := NewReal(
		log.NewSlogAdapter(nil),
		WithKnownHostsFile(khPath),
	)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	ch, err := client.Connect(
		context.Background(), srv.addr,
		WithUser("test"),
		WithAuthMethods([]gossh.AuthMethod{
			gossh.PublicKeys(srv.userSigner),
		}),
		WithPTYSize(80, 24, 0, 0),
	)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	<-srv.shellReady

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err = ch.Resize(ctx, 100, 40, 0, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Jump-handshake cancellation test
// ---------------------------------------------------------------------------

// TestDialCancel_JumpHandshake verifies that cancelling the context during
// the target's SSH handshake through a jump host returns promptly with
// context.Canceled. The bastion is a real test SSH server with direct-tcpip
// support; the target is a blocking TCP listener that accepts but never
// completes an SSH handshake. Cancellation closes the forwarded connection,
// unblocking gossh.NewClientConn, and releases the jump handle.
func TestDialCancel_JumpHandshake(t *testing.T) {
	// Generate keys for the bastion.
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
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

	// Write the bastion's private key to a temp file for jump auth.
	jumpKeyPath := filepath.Join(t.TempDir(), "jump_key")
	block, err := gossh.MarshalPrivateKey(privKey, "")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	if err = os.WriteFile(jumpKeyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write jump key: %v", err)
	}

	khPath := writeKnownHosts(t, bastion, bastion.addr)
	client, err := NewReal(
		log.NewSlogAdapter(nil),
		WithKnownHostsFile(khPath),
	)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithCancel(context.Background())

	bastionHost, bastionPortStr, _ := net.SplitHostPort(bastion.addr)
	bastionPort, _ := strconv.Atoi(bastionPortStr)

	// Connect through the bastion to the blocking target.
	errCh := make(chan error, 1)
	go func() {
		_, err := client.Connect(
			ctx, bl.addr(),
			WithUser("targetuser"),
			WithAuthMethods([]gossh.AuthMethod{
				gossh.Password("irrelevant"),
			}),
			WithPTYSize(80, 24, 0, 0),
			WithJumpHost(bastionHost, bastionPort, "jumpuser", "publicKey"),
			func(c *ConnectConfig) { c.JumpKeyFile = jumpKeyPath },
		)
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
	if got := client.pool.Count(); got != 0 {
		t.Fatalf("pool.Count()=%d after cancelled jump dial, want 0 (bastion handle released)", got)
	}

	_ = pubKey // used only for key generation
}
