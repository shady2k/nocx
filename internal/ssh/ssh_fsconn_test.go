package ssh

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/shady2k/nocx/internal/log"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// ---------------------------------------------------------------------------
// FSConn — the owned SFTP lease for the file manager (spec §3, D3)
// ---------------------------------------------------------------------------

func TestFSConn_ImplementsInterface(t *testing.T) {
	var _ FSConn = (*fsConn)(nil)
}

// ---------------------------------------------------------------------------
// In-process SSH test server with an SFTP subsystem
// ---------------------------------------------------------------------------

// fsServerMode selects how the test server answers the sftp subsystem.
type fsServerMode int

const (
	// fsModeReal serves a real SFTP server over a temp directory.
	fsModeReal fsServerMode = iota
	// fsModeRefuseSubsystem replies false to the sftp subsystem request.
	fsModeRefuseSubsystem
	// fsModeNeverReply answers the version handshake, then swallows every
	// request without ever answering one.
	fsModeNeverReply
	// fsModeNeverInit accepts the subsystem and never answers the version
	// handshake either — FSConn construction itself must time out.
	fsModeNeverInit
	// fsModeNoPosixRename serves a real SFTP server with the
	// posix-rename@openssh.com extension withheld: the request is answered
	// SSH_FX_OP_UNSUPPORTED, as OpenSSH answers an extension it does not
	// implement. Everything else is served normally.
	fsModeNoPosixRename
	// fsModeStallWrites serves a real SFTP server that answers every
	// request except SSH_FXP_WRITE, which it swallows forever — a write
	// wedged against a silent server, unblockable only by closing the
	// subsystem.
	fsModeStallWrites
)

// SFTP packet types this file's fixtures speak directly (draft-ietf-secsh-
// filexfer-02 §3, and posix-rename@openssh.com in OpenSSH's PROTOCOL).
const (
	fsPktWrite            = 6   // SSH_FXP_WRITE
	fsPktStatus           = 101 // SSH_FXP_STATUS
	fsPktExtended         = 200 // SSH_FXP_EXTENDED
	fsStatusOpUnsupported = 8   // SSH_FX_OP_UNSUPPORTED
)

// fsTestServer is the FSConn test double for testSSHServer: the existing
// fixture has no SFTP subsystem, so this one exists beside it, in this file
// only, with just the surface the FSConn tests need.
type fsTestServer struct {
	t          *testing.T
	mode       fsServerMode
	rootDir    string // served as the SFTP root in fsModeReal
	hostSigner gossh.Signer
	userSigner gossh.Signer
	listener   net.Listener
	addr       string

	mu          sync.Mutex
	maxSessions int
	sessions    int
	// requestSeen is signaled once per SFTP request the never-reply server
	// has swallowed, so a test knows a call is genuinely in flight before
	// it acts. Buffered; drops when full.
	requestSeen chan struct{}

	liveMu    sync.Mutex
	liveConns map[*gossh.ServerConn]struct{}
}

func startFSTestServer(t *testing.T, mode fsServerMode) *fsTestServer {
	t.Helper()
	hostKey := generateSigner(t)
	userKey := generateSigner(t)
	config := &gossh.ServerConfig{
		PublicKeyCallback: func(meta gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			if bytes.Equal(key.Marshal(), userKey.PublicKey().Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("gossh: unknown public key for %q", meta.User())
		},
	}
	config.AddHostKey(hostKey)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("test server listen: %v", err)
	}
	srv := &fsTestServer{
		t:           t,
		mode:        mode,
		rootDir:     t.TempDir(),
		hostSigner:  hostKey,
		userSigner:  userKey,
		listener:    listener,
		addr:        listener.Addr().String(),
		requestSeen: make(chan struct{}, 16),
		liveConns:   make(map[*gossh.ServerConn]struct{}),
	}
	t.Cleanup(func() { _ = listener.Close() })
	go srv.acceptLoop(config)
	return srv
}

func (s *fsTestServer) setMaxSessions(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxSessions = n
}

// killConns closes every established server-side connection, simulating
// transport loss for the clients.
func (s *fsTestServer) killConns() {
	s.liveMu.Lock()
	conns := make([]*gossh.ServerConn, 0, len(s.liveConns))
	for c := range s.liveConns {
		conns = append(conns, c)
	}
	s.liveMu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

func (s *fsTestServer) acceptLoop(config *gossh.ServerConfig) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.serveConn(conn, config)
	}
}

func (s *fsTestServer) serveConn(conn net.Conn, config *gossh.ServerConfig) {
	sshConn, chans, reqs, err := gossh.NewServerConn(conn, config)
	if err != nil {
		_ = conn.Close()
		return
	}
	s.liveMu.Lock()
	s.liveConns[sshConn] = struct{}{}
	s.liveMu.Unlock()
	defer func() {
		s.liveMu.Lock()
		delete(s.liveConns, sshConn)
		s.liveMu.Unlock()
	}()

	go gossh.DiscardRequests(reqs)

	for newChan := range chans {
		switch newChan.ChannelType() {
		case "session":
			s.mu.Lock()
			maxSessions := s.maxSessions
			if maxSessions > 0 && s.sessions >= maxSessions {
				s.mu.Unlock()
				_ = newChan.Reject(gossh.ResourceShortage, "too many sessions")
				continue
			}
			s.sessions++
			s.mu.Unlock()
			ch, reqs, err := newChan.Accept()
			if err != nil {
				return
			}
			go s.handleSession(ch, reqs)
		default:
			_ = newChan.Reject(gossh.UnknownChannelType, "unknown channel type")
		}
	}
	_ = sshConn.Close()
}

// handleSession serves one session channel. Each session runs exactly one
// inline loop, so the SFTP server never competes with the echo loop for the
// channel's bytes: a shell request enters the echo loop (the tab's
// interactive session, mirroring testSSHServer), a subsystem request enters
// the SFTP dispatcher, everything else is refused.
func (s *fsTestServer) handleSession(ch gossh.Channel, reqs <-chan *gossh.Request) {
	for req := range reqs {
		switch req.Type {
		case "shell":
			_ = req.Reply(true, nil)
			s.echoLoop(ch)
			return
		case "subsystem":
			var m struct{ Subsystem string }
			if err := gossh.Unmarshal(req.Payload, &m); err != nil || m.Subsystem != "sftp" {
				_ = req.Reply(false, nil)
				continue
			}
			if s.mode == fsModeRefuseSubsystem {
				_ = req.Reply(false, nil)
				return
			}
			_ = req.Reply(true, nil)
			switch s.mode {
			case fsModeNeverReply:
				s.serveNeverReply(ch, false)
			case fsModeNeverInit:
				s.serveNeverReply(ch, true)
			case fsModeNoPosixRename:
				s.serveSFTPFiltered(ch, filterNoPosixRename)
			case fsModeStallWrites:
				s.serveSFTPFiltered(ch, filterStallWrites)
			default:
				s.serveSFTP(ch)
			}
			return
		default:
			_ = req.Reply(false, nil)
		}
	}
	_ = ch.Close()
}

// echoLoop mirrors testSSHServer's interactive session: whatever the tab
// writes comes back prefixed with "echo:".
func (s *fsTestServer) echoLoop(ch gossh.Channel) {
	buf := make([]byte, 4096)
	for {
		n, err := ch.Read(buf)
		if n > 0 {
			reply := append([]byte("echo:"), buf[:n]...)
			_, _ = ch.Write(reply)
		}
		if err != nil {
			return
		}
	}
}

func (s *fsTestServer) serveSFTP(ch gossh.Channel) {
	defer func() { _ = ch.Close() }()
	srv, err := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(s.rootDir))
	if err != nil {
		return
	}
	_ = srv.Serve()
}

// serveNeverReply is the server half of the close-to-cancel proof: it
// answers the client's version handshake (or not, when neverInit) and then
// swallows every request without ever answering. A call against this server
// can only be unblocked by closing the subsystem — or by the lane's hard
// timeout, which does exactly that.
func (s *fsTestServer) serveNeverReply(ch gossh.Channel, neverInit bool) {
	defer func() { _ = ch.Close() }()
	// The first packet is SSH_FXP_INIT (type 1). Answer it unless the mode
	// wants the handshake to hang too.
	typ, _, err := readSFTPPacket(ch)
	if err != nil {
		return
	}
	if !neverInit && typ == 1 {
		// SSH_FXP_VERSION (type 2), protocol version 3: 4-byte length 5,
		// 1-byte type, 4-byte version.
		if _, err := ch.Write([]byte{0, 0, 0, 5, 2, 0, 0, 0, 3}); err != nil {
			return
		}
	}
	for {
		if _, _, err := readSFTPPacket(ch); err != nil {
			return
		}
		select {
		case s.requestSeen <- struct{}{}:
		default:
		}
	}
}

// readSFTPPacket reads one length-prefixed SFTP packet, returning its type
// byte and payload (type stripped).
func readSFTPPacket(r io.Reader) (byte, []byte, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(lenBuf[:])
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, nil, err
	}
	return buf[0], buf[1:], nil
}

// fsVerdict is what the filtering proxy does with one client packet.
type fsVerdict int

const (
	// fsForward passes the packet through to the real SFTP server.
	fsForward fsVerdict = iota
	// fsUnsupported answers SSH_FX_OP_UNSUPPORTED without forwarding —
	// what a server that does not implement an extension replies.
	fsUnsupported
	// fsSwallow never answers at all — a request wedged against a silent
	// server, unblockable only by closing the subsystem.
	fsSwallow
)

// filterNoPosixRename withholds posix-rename@openssh.com and nothing else.
// pkg/sftp's Client.PosixRename sends the extended request unconditionally
// rather than consulting the VERSION advertisement (client.go:912), so what
// distinguishes a server without the extension is the status it returns,
// which is exactly what this reproduces.
func filterNoPosixRename(typ byte, payload []byte) fsVerdict {
	if typ == fsPktExtended && fsExtendedName(payload) == "posix-rename@openssh.com" {
		return fsUnsupported
	}
	return fsForward
}

// filterStallWrites answers everything but SSH_FXP_WRITE, which never comes
// back — the open succeeds, the first write hangs.
func filterStallWrites(typ byte, _ []byte) fsVerdict {
	if typ == fsPktWrite {
		return fsSwallow
	}
	return fsForward
}

// fsExtendedName reads the extended-request name out of an SSH_FXP_EXTENDED
// payload (uint32 request-id, string extended-request).
func fsExtendedName(payload []byte) string {
	if len(payload) < 8 {
		return ""
	}
	n := binary.BigEndian.Uint32(payload[4:8])
	if uint64(len(payload)) < 8+uint64(n) {
		return ""
	}
	return string(payload[8 : 8+n])
}

// fsPacketID reads the request id every packet this fixture answers carries
// first in its payload.
func fsPacketID(payload []byte) uint32 {
	if len(payload) < 4 {
		return 0
	}
	return binary.BigEndian.Uint32(payload[:4])
}

// fsFramePacket re-frames a packet the proxy read back into wire form.
func fsFramePacket(typ byte, payload []byte) []byte {
	out := make([]byte, 4, 5+len(payload))
	// #nosec G115 — fixture packets are a few hundred bytes; the SFTP
	// length prefix is uint32 by protocol.
	binary.BigEndian.PutUint32(out, uint32(1+len(payload)))
	out = append(out, typ)
	return append(out, payload...)
}

// fsStatusPacket builds an SSH_FXP_STATUS reply: id, code, message, lang.
func fsStatusPacket(id uint32, code uint32, msg string) []byte {
	payload := make([]byte, 0, 16+len(msg))
	payload = binary.BigEndian.AppendUint32(payload, id)
	payload = binary.BigEndian.AppendUint32(payload, code)
	// #nosec G115 — msg is a fixture constant.
	payload = binary.BigEndian.AppendUint32(payload, uint32(len(msg)))
	payload = append(payload, msg...)
	payload = binary.BigEndian.AppendUint32(payload, 0) // empty language tag
	return fsFramePacket(fsPktStatus, payload)
}

// serveSFTPFiltered puts a packet filter between the client and a real SFTP
// server, so a fixture can withhold one operation the way a real server
// does — on the wire — while everything else works. Server replies are
// copied back verbatim; injected replies take the same write lock, so the
// two never interleave mid-packet.
func (s *fsTestServer) serveSFTPFiltered(ch gossh.Channel, filter func(typ byte, payload []byte) fsVerdict) {
	defer func() { _ = ch.Close() }()
	clientSide, serverSide := net.Pipe()
	srv, err := sftp.NewServer(serverSide, sftp.WithServerWorkingDirectory(s.rootDir))
	if err != nil {
		return
	}
	defer func() { _ = clientSide.Close() }()
	go func() {
		defer func() { _ = serverSide.Close() }()
		_ = srv.Serve()
	}()

	var wmu sync.Mutex
	writeCh := func(b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		_, err := ch.Write(b)
		return err
	}
	go func() {
		buf := make([]byte, 64<<10)
		for {
			n, err := clientSide.Read(buf)
			if n > 0 {
				if werr := writeCh(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		typ, payload, err := readSFTPPacket(ch)
		if err != nil {
			return
		}
		switch filter(typ, payload) {
		case fsUnsupported:
			if werr := writeCh(fsStatusPacket(fsPacketID(payload), fsStatusOpUnsupported, "operation unsupported")); werr != nil {
				return
			}
		case fsSwallow:
			select {
			case s.requestSeen <- struct{}{}:
			default:
			}
		default:
			if _, werr := clientSide.Write(fsFramePacket(typ, payload)); werr != nil {
				return
			}
		}
	}
}

// fsTestClient builds a RealClient pointed at the test server, cleaned up
// with the test (clone of tunnelTestClient, which is typed to testSSHServer).
func fsTestClient(t *testing.T, srv *fsTestServer) *RealClient {
	t.Helper()
	khPath := fsWriteKnownHosts(t, srv, srv.addr)
	client, err := NewReal(log.NewSlogAdapter(nil), WithKnownHostsFile(khPath))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func fsConnectOpts(srv *fsTestServer) []ConnectOption {
	return []ConnectOption{
		WithUser("test"),
		WithAuthMethods([]gossh.AuthMethod{gossh.PublicKeys(srv.userSigner)}),
	}
}

func fsWriteKnownHosts(t *testing.T, srv *fsTestServer, addr string) string {
	t.Helper()
	line := knownhosts.Line([]string{addr}, srv.hostSigner.PublicKey())
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	return path
}

// waitPoolEmpty polls the pool count down to zero, so a regression that
// leaves a lease's reference behind fails the test instead of hanging it.
func waitPoolEmpty(t *testing.T, client *RealClient) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for client.pool.Count() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("pool count = %d, want 0", client.pool.Count())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// Ordinary success — every operation works against a real SFTP server
// ---------------------------------------------------------------------------

func TestFSConn_ReadDir_ReturnsEntries(t *testing.T) {
	srv := startFSTestServer(t, fsModeReal)
	if err := os.WriteFile(filepath.Join(srv.rootDir, "alpha.txt"), []byte("a"), 0o600); err != nil {
		t.Fatalf("write alpha: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srv.rootDir, "beta.txt"), []byte("b"), 0o600); err != nil {
		t.Fatalf("write beta: %v", err)
	}
	if err := os.Mkdir(filepath.Join(srv.rootDir, "sub"), 0o750); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	client := fsTestClient(t, srv)
	fc, err := client.FSConn(context.Background(), srv.addr, fsConnectOpts(srv)...)
	if err != nil {
		t.Fatalf("FSConn: %v", err)
	}

	entries, err := fc.ReadDir(context.Background(), ".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	names := make(map[string]bool, len(entries))
	for _, e := range entries {
		names[e.Name()] = true
	}
	for _, want := range []string{"alpha.txt", "beta.txt", "sub"} {
		if !names[want] {
			t.Errorf("ReadDir missing %q (got %v)", want, entries)
		}
	}

	if err := fc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitPoolEmpty(t, client)
}

func TestFSConn_Stat_Lstat_RealPath(t *testing.T) {
	srv := startFSTestServer(t, fsModeReal)
	if err := os.WriteFile(filepath.Join(srv.rootDir, "data.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write data: %v", err)
	}
	if err := os.Symlink("data.txt", filepath.Join(srv.rootDir, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	client := fsTestClient(t, srv)
	fc, err := client.FSConn(context.Background(), srv.addr, fsConnectOpts(srv)...)
	if err != nil {
		t.Fatalf("FSConn: %v", err)
	}
	defer func() { _ = fc.Close() }()

	info, err := fc.Stat("data.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Name() != "data.txt" || info.Size() != 5 {
		t.Errorf("Stat = %v (size %d), want data.txt size 5", info.Name(), info.Size())
	}

	// Stat follows the symlink; Lstat does not.
	info, err = fc.Stat("link")
	if err != nil {
		t.Fatalf("Stat(link): %v", err)
	}
	if info.Size() != 5 {
		t.Errorf("Stat(link) size = %d, want 5 (followed)", info.Size())
	}
	lst, err := fc.Lstat("link")
	if err != nil {
		t.Fatalf("Lstat(link): %v", err)
	}
	if lst.Mode()&os.ModeSymlink == 0 {
		t.Errorf("Lstat(link) mode = %v, want symlink", lst.Mode())
	}

	tgt, err := fc.ReadLink("link")
	if err != nil {
		t.Fatalf("ReadLink: %v", err)
	}
	if tgt != "data.txt" {
		t.Errorf("ReadLink = %q, want the stored link text %q", tgt, "data.txt")
	}

	// A broken symlink still returns its target text: ReadLink reads the
	// link, not its resolution, which is what distinguishes "target
	// missing" from "cannot read the link".
	if err = os.Symlink(filepath.Join(srv.rootDir, "gone"), filepath.Join(srv.rootDir, "broken")); err != nil {
		t.Fatalf("broken symlink: %v", err)
	}
	tgt, err = fc.ReadLink("broken")
	if err != nil {
		t.Fatalf("ReadLink(broken): %v", err)
	}
	if tgt != filepath.Join(srv.rootDir, "gone") {
		t.Errorf("ReadLink(broken) = %q, want the stored target %q", tgt, filepath.Join(srv.rootDir, "gone"))
	}

	rp, err := fc.RealPath(".")
	if err != nil {
		t.Fatalf("RealPath: %v", err)
	}
	if rp == "" {
		t.Error("RealPath returned an empty path")
	}
}

func TestFSConn_ReadFile_ContentAndTruncation(t *testing.T) {
	srv := startFSTestServer(t, fsModeReal)
	if err := os.WriteFile(filepath.Join(srv.rootDir, "data.txt"), []byte("hello world"), 0o600); err != nil {
		t.Fatalf("write data: %v", err)
	}
	client := fsTestClient(t, srv)
	fc, err := client.FSConn(context.Background(), srv.addr, fsConnectOpts(srv)...)
	if err != nil {
		t.Fatalf("FSConn: %v", err)
	}
	defer func() { _ = fc.Close() }()

	data, truncated, err := fc.ReadFile(context.Background(), "data.txt", 100)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "hello world" || truncated {
		t.Errorf("ReadFile = %q truncated=%v, want %q false", data, truncated, "hello world")
	}

	data, truncated, err = fc.ReadFile(context.Background(), "data.txt", 5)
	if err != nil {
		t.Fatalf("ReadFile bounded: %v", err)
	}
	if string(data) != "hello" || !truncated {
		t.Errorf("ReadFile(5) = %q truncated=%v, want %q true", data, truncated, "hello")
	}

	// maxBytes <= 0 means the lease default cap; the file is well under it.
	data, truncated, err = fc.ReadFile(context.Background(), "data.txt", 0)
	if err != nil || string(data) != "hello world" || truncated {
		t.Errorf("ReadFile(0) = %q truncated=%v err=%v, want full content", data, truncated, err)
	}

	if _, _, err := fc.ReadFile(context.Background(), "missing.txt", 10); err == nil {
		t.Error("ReadFile(missing) = nil error, want the remote status error")
	}
}

// TestFSConn_ReadFile_EmptyAndBoundaries pins the two outcomes ReadFull's
// error vocabulary hides: an empty file is a successful zero-byte read (the
// io.EOF fix), a file ending exactly at the bound is not truncated, and a
// file one byte past the bound is.
func TestFSConn_ReadFile_EmptyAndBoundaries(t *testing.T) {
	srv := startFSTestServer(t, fsModeReal)
	client := fsTestClient(t, srv)
	fc, err := client.FSConn(context.Background(), srv.addr, fsConnectOpts(srv)...)
	if err != nil {
		t.Fatalf("FSConn: %v", err)
	}
	defer func() { _ = fc.Close() }()

	// Empty file: a successful zero-byte read, never io.EOF.
	if err = os.WriteFile(filepath.Join(srv.rootDir, "empty.txt"), nil, 0o600); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	data, truncated, err := fc.ReadFile(context.Background(), "empty.txt", 100)
	if err != nil {
		t.Fatalf("ReadFile(empty): %v", err)
	}
	if len(data) != 0 || truncated {
		t.Errorf("ReadFile(empty) = %d bytes truncated=%v, want 0 bytes false", len(data), truncated)
	}

	// Exactly at the bound: the extra byte is not readable, so not
	// truncated.
	exact := bytes.Repeat([]byte("x"), 100)
	if err = os.WriteFile(filepath.Join(srv.rootDir, "exact.txt"), exact, 0o600); err != nil {
		t.Fatalf("write exact: %v", err)
	}
	data, truncated, err = fc.ReadFile(context.Background(), "exact.txt", 100)
	if err != nil {
		t.Fatalf("ReadFile(exact): %v", err)
	}
	if len(data) != 100 || truncated {
		t.Errorf("ReadFile(exact) = %d bytes truncated=%v, want 100 bytes false", len(data), truncated)
	}

	// One past the bound: the extra byte IS readable, so truncated.
	if err = os.WriteFile(filepath.Join(srv.rootDir, "over.txt"), append(exact, 'y'), 0o600); err != nil {
		t.Fatalf("write over: %v", err)
	}
	data, truncated, err = fc.ReadFile(context.Background(), "over.txt", 100)
	if err != nil {
		t.Fatalf("ReadFile(over): %v", err)
	}
	if len(data) != 100 || !truncated {
		t.Errorf("ReadFile(over) = %d bytes truncated=%v, want 100 bytes true", len(data), truncated)
	}
}

// ---------------------------------------------------------------------------
// Construction failures — three different facts, three different errors
// ---------------------------------------------------------------------------

// TestFSConn_Handshake_SessionRefused_MaxSessions proves the MaxSessions-1
// case over a real channel open: the interactive shell holds the only
// session channel, FSConn's NewSession is rejected with ResourceShortage,
// and the shell stays fully usable.
func TestFSConn_Handshake_SessionRefused_MaxSessions(t *testing.T) {
	srv := startFSTestServer(t, fsModeReal)
	srv.setMaxSessions(1)
	client := fsTestClient(t, srv)
	opts := fsConnectOpts(srv)

	tab, err := client.Connect(context.Background(), srv.addr, opts...)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = tab.Close() }()

	fc, err := client.FSConn(context.Background(), srv.addr, opts...)
	if !errors.Is(err, ErrFSSessionRefused) {
		t.Fatalf("FSConn error = %v, want ErrFSSessionRefused", err)
	}
	if fc != nil {
		t.Fatal("FSConn returned a lease alongside the refusal error")
	}

	// The interactive session survived the refusal.
	if _, err := tab.Write([]byte("hi")); err != nil {
		t.Fatalf("tab write after refusal: %v", err)
	}
	if got := readWithTimeout(t, tab); got != "echo:hi" {
		t.Errorf("tab echo = %q, want %q", got, "echo:hi")
	}
}

func TestFSConn_Handshake_SubsystemRefused(t *testing.T) {
	srv := startFSTestServer(t, fsModeRefuseSubsystem)
	client := fsTestClient(t, srv)

	fc, err := client.FSConn(context.Background(), srv.addr, fsConnectOpts(srv)...)
	if !errors.Is(err, ErrFSSubsystemRefused) {
		t.Fatalf("FSConn error = %v, want ErrFSSubsystemRefused", err)
	}
	if fc != nil {
		t.Fatal("FSConn returned a lease alongside the refusal error")
	}
	// The refused lease must not linger in the pool.
	waitPoolEmpty(t, client)
}

// TestFSConn_Connect_Refused proves the dial-level failure: no connection
// exists, so FSConn reports the dial error and no lease.
func TestFSConn_Connect_Refused(t *testing.T) {
	srv := startFSTestServer(t, fsModeReal)
	client := fsTestClient(t, srv)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	closedAddr := ln.Addr().String()
	_ = ln.Close()

	if _, err := client.FSConn(context.Background(), closedAddr, fsConnectOpts(srv)...); err == nil {
		t.Fatal("FSConn to a refused port = nil error, want the dial error")
	}
	waitPoolEmpty(t, client)
}

// TestFSConn_Handshake_NeverInit_TimesOut proves construction cannot hang:
// a server that accepts the subsystem and never answers the version
// handshake is closed down by the hard timeout — closing the session is what
// unblocks the handshake — and FSConn reports ErrFSTimedOut, releasing the
// pooled reference.
func TestFSConn_Handshake_NeverInit_TimesOut(t *testing.T) {
	srv := startFSTestServer(t, fsModeNeverInit)
	client := fsTestClient(t, srv)

	acq, err := client.acquirePooled(context.Background(), srv.addr, fsConnectOpts(srv))
	if err != nil {
		t.Fatalf("acquirePooled: %v", err)
	}
	fc, err := newFSConnLane(acq.client, func() { client.pool.Release(acq.handle) }, context.Background(), 300*time.Millisecond)
	if !errors.Is(err, ErrFSTimedOut) {
		t.Fatalf("FSConn error = %v, want ErrFSTimedOut", err)
	}
	if fc != nil {
		t.Fatal("FSConn returned a lease alongside the timeout")
	}
	waitPoolEmpty(t, client)
}

// ---------------------------------------------------------------------------
// Lease semantics — the three properties DiscoveryConn's failures bought
// ---------------------------------------------------------------------------

// TestFSConn_Close_DoesNotCloseDone proves property 3: an intentional Close
// must not read as connection loss, and a real transport loss must. Done
// closes only on the latter.
func TestFSConn_Close_DoesNotCloseDone(t *testing.T) {
	srv := startFSTestServer(t, fsModeReal)
	client := fsTestClient(t, srv)
	opts := fsConnectOpts(srv)

	tab, err := client.Connect(context.Background(), srv.addr, opts...)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = tab.Close() }()
	fc, err := client.FSConn(context.Background(), srv.addr, opts...)
	if err != nil {
		t.Fatalf("FSConn: %v", err)
	}

	if err := fc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-fc.Done():
		t.Fatal("Done closed on Close — an intentional stop read as connection loss")
	default:
	}
	// The connection is still shared with the tab and fully usable.
	if _, err := tab.Write([]byte("alive")); err != nil {
		t.Fatalf("tab write after lease close: %v", err)
	}
	if got := readWithTimeout(t, tab); got != "echo:alive" {
		t.Errorf("tab echo = %q, want %q", got, "echo:alive")
	}

	srv.killConns()
	select {
	case <-fc.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done did not close after connection loss")
	}
	if fc.LostErr() == nil {
		t.Fatal("LostErr = nil after connection loss, want the transport error")
	}
}

// TestFSConn_Close_ReleasesReference proves the interval invariant with both
// ends named: from FSConn returning until Close returns, the pooled
// reference is held; after Close returns it is released and the shared
// connection survives for the tab.
func TestFSConn_Close_ReleasesReference(t *testing.T) {
	srv := startFSTestServer(t, fsModeReal)
	client := fsTestClient(t, srv)
	opts := fsConnectOpts(srv)

	tab, err := client.Connect(context.Background(), srv.addr, opts...)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = tab.Close() }()
	fc, err := client.FSConn(context.Background(), srv.addr, opts...)
	if err != nil {
		t.Fatalf("FSConn: %v", err)
	}

	// Held: tab + lease on one shared connection.
	if got := client.pool.Count(); got != 1 {
		t.Fatalf("pool count = %d, want 1", got)
	}
	if err := fc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Released, but the connection stays up for the tab.
	if got := client.pool.Count(); got != 1 {
		t.Errorf("pool count after lease close = %d, want 1 (tab still holds it)", got)
	}
	if _, err := tab.Write([]byte("still-alive")); err != nil {
		t.Fatalf("tab write after lease close: %v", err)
	}
	if got := readWithTimeout(t, tab); got != "echo:still-alive" {
		t.Errorf("tab echo = %q, want %q", got, "echo:still-alive")
	}
}

// TestFSConn_Loss_MidCall proves a transport dying while a call is in flight
// unblocks the call (the channel read fails, pkg/sftp broadcasts the loss to
// every in-flight request), reports ErrFSLost, closes Done and reclaims the
// pool.
func TestFSConn_Loss_MidCall(t *testing.T) {
	srv := startFSTestServer(t, fsModeNeverReply)
	client := fsTestClient(t, srv)
	fc, err := client.FSConn(context.Background(), srv.addr, fsConnectOpts(srv)...)
	if err != nil {
		t.Fatalf("FSConn: %v", err)
	}

	outCh := make(chan error, 1)
	go func() {
		_, err := fc.Stat("/wedged")
		outCh <- err
	}()
	select {
	case <-srv.requestSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("server never received the STAT request")
	}

	srv.killConns()
	select {
	case err := <-outCh:
		if !errors.Is(err, ErrFSLost) {
			t.Fatalf("Stat error = %v, want ErrFSLost", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stat did not return after transport loss")
	}
	select {
	case <-fc.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done did not close after connection loss")
	}
	if fc.LostErr() == nil {
		t.Fatal("LostErr = nil after connection loss, want the transport error")
	}
	waitPoolEmpty(t, client)
}

// ---------------------------------------------------------------------------
// Cancellation: listing by context, everything else by closing
// ---------------------------------------------------------------------------

// TestFSConn_ReadDir_Cancel_DoesNotPoison proves the lane's reason for
// existing: ReadDirContext is natively cancellable, so cancelling a listing
// returns ctx.Err() WITHOUT closing the client out from under a concurrent
// call. The concurrent Stat stays in flight, and only Close unblocks it.
func TestFSConn_ReadDir_Cancel_DoesNotPoison(t *testing.T) {
	srv := startFSTestServer(t, fsModeNeverReply)
	client := fsTestClient(t, srv)
	fc, err := client.FSConn(context.Background(), srv.addr, fsConnectOpts(srv)...)
	if err != nil {
		t.Fatalf("FSConn: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	rdCh := make(chan error, 1)
	go func() {
		_, err := fc.ReadDir(ctx, "/wedged")
		rdCh <- err
	}()
	select {
	case <-srv.requestSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("server never received the OPENDIR request")
	}

	statCh := make(chan error, 1)
	go func() {
		_, err := fc.Stat("/wedged")
		statCh <- err
	}()
	select {
	case <-srv.requestSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("server never received the STAT request")
	}

	cancel()
	select {
	case err := <-rdCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ReadDir error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ReadDir did not return after cancel — ReadDirContext is not context-cancellable")
	}

	// The lease is NOT poisoned: the concurrent Stat is still in flight.
	select {
	case err := <-statCh:
		t.Fatalf("Stat returned %v while the lease should still be alive", err)
	case <-time.After(200 * time.Millisecond):
	}

	// Closing the lease is what unblocks the non-context call.
	if err := fc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-statCh:
		if !errors.Is(err, ErrFSClosed) {
			t.Fatalf("Stat error = %v, want ErrFSClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stat did not return after Close")
	}
}

// TestFSConn_HardTimeout_PoisonsLease proves the lane's backstop: a call a
// server will never answer is killed by the hard timeout, which closes the
// subsystem (unblocking the call), releases the pooled reference and reports
// the lease dead — a visible terminal state, not a silent retry loop.
func TestFSConn_HardTimeout_PoisonsLease(t *testing.T) {
	srv := startFSTestServer(t, fsModeNeverReply)
	client := fsTestClient(t, srv)

	acq, err := client.acquirePooled(context.Background(), srv.addr, fsConnectOpts(srv))
	if err != nil {
		t.Fatalf("acquirePooled: %v", err)
	}
	fc, err := newFSConnLane(acq.client, func() { client.pool.Release(acq.handle) }, context.Background(), 300*time.Millisecond)
	if err != nil {
		t.Fatalf("newFSConnLane: %v", err)
	}

	outCh := make(chan error, 1)
	go func() {
		_, err := fc.Stat("/wedged")
		outCh <- err
	}()
	select {
	case <-srv.requestSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("server never received the STAT request")
	}

	start := time.Now()
	select {
	case err := <-outCh:
		if !errors.Is(err, ErrFSDead) {
			t.Fatalf("Stat error = %v, want ErrFSDead", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stat did not return after the hard timeout — closing did not unblock it")
	}
	t.Logf("wedged Stat returned ErrFSDead after %s", time.Since(start))

	// Dead is terminal and observable: every call, context-aware or not,
	// reports ErrFSDead.
	if _, err := fc.Stat("/x"); !errors.Is(err, ErrFSDead) {
		t.Fatalf("Stat after poison = %v, want ErrFSDead", err)
	}
	if _, err := fc.ReadDir(context.Background(), "/x"); !errors.Is(err, ErrFSDead) {
		t.Fatalf("ReadDir after poison = %v, want ErrFSDead", err)
	}

	// The poisoned lease released its pooled reference, so the connection
	// is reclaimed and the transport shuts down — Done closes for real.
	waitPoolEmpty(t, client)
	select {
	case <-fc.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done did not close after the poisoned lease released the connection")
	}
}

// TestFSConn_Close_UnblocksNonContextCalls is the acceptance condition the
// design records as a promise to prove, not assert: against a server that
// accepts requests and never replies, closing the subsystem unblocks every
// non-context call we make. Each call is genuinely in flight — its request
// packet has reached the server — when Close fires, so the close, not a
// state check, is what releases it.
func TestFSConn_Close_UnblocksNonContextCalls(t *testing.T) {
	srv := startFSTestServer(t, fsModeNeverReply)
	client := fsTestClient(t, srv)

	// The lease's lane caps concurrent in-flight non-context calls at
	// fsLaneCap (4), so all five non-context calls cannot be wedged at
	// once: the fifth would wait for a slot that never frees and its
	// request would never reach the server. The proof therefore runs in
	// two rounds of four, each round filling the lane, ReadLink joining
	// round B — every call genuinely in flight before Close fires.
	var fc FSConn
	var err error

	runRound := func(round string, calls []func(chan<- error)) {
		n := len(calls)
		outCh := make(chan error, n)
		for _, c := range calls {
			go c(outCh)
		}
		// All four requests must be in flight server-side before Close: a
		// call that had not started would fail its state check, which
		// proves nothing about close-to-cancel.
		for i := range n {
			select {
			case <-srv.requestSeen:
			case <-time.After(5 * time.Second):
				t.Fatalf("round %s: server saw only %d/%d requests", round, i, n)
			}
		}
		start := time.Now()
		if err = fc.Close(); err != nil {
			t.Fatalf("round %s: Close: %v", round, err)
		}
		for i := range n {
			select {
			case err = <-outCh:
				if !errors.Is(err, ErrFSClosed) {
					t.Errorf("round %s: call %d error = %v, want ErrFSClosed", round, i, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("round %s: call %d did not return after Close — closing the subsystem did not unblock it", round, i)
			}
		}
		t.Logf("round %s: all %d non-context calls returned within %s of Close", round, n, time.Since(start))
		waitPoolEmpty(t, client)
	}

	fc, err = client.FSConn(context.Background(), srv.addr, fsConnectOpts(srv)...)
	if err != nil {
		t.Fatalf("FSConn: %v", err)
	}
	baseline := runtime.NumGoroutine()
	runRound("A", []func(chan<- error){
		func(outCh chan<- error) {
			_, callErr := fc.Stat("/wedged")
			outCh <- callErr
		},
		func(outCh chan<- error) {
			_, callErr := fc.Lstat("/wedged")
			outCh <- callErr
		},
		func(outCh chan<- error) {
			_, callErr := fc.RealPath("/wedged")
			outCh <- callErr
		},
		func(outCh chan<- error) {
			_, _, callErr := fc.ReadFile(context.Background(), "/wedged", 16)
			outCh <- callErr
		},
	})

	fc, err = client.FSConn(context.Background(), srv.addr, fsConnectOpts(srv)...)
	if err != nil {
		t.Fatalf("FSConn round B: %v", err)
	}
	runRound("B", []func(chan<- error){
		func(outCh chan<- error) {
			_, callErr := fc.ReadLink("/wedged")
			outCh <- callErr
		},
		func(outCh chan<- error) {
			_, callErr := fc.Stat("/wedged")
			outCh <- callErr
		},
		func(outCh chan<- error) {
			_, callErr := fc.Lstat("/wedged")
			outCh <- callErr
		},
		func(outCh chan<- error) {
			_, _, callErr := fc.ReadFile(context.Background(), "/wedged", 16)
			outCh <- callErr
		},
	})

	// No goroutine from either lease outlives Close: the leases were the
	// only references, so closing them reclaimed the connections and the
	// loss watchers exited with them. The allowance of baseline+1 matches
	// the discovery cancel test; the deadline loop tolerates the watchers'
	// asynchronous exit.
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > baseline+1 {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines = %d, want <= %d (lease goroutine outlived Close)", runtime.NumGoroutine(), baseline+1)
		}
		time.Sleep(10 * time.Millisecond)
	}
	waitPoolEmpty(t, client)
}

// ---------------------------------------------------------------------------
// The write half — Create/Write/Close, rename and remove (design §5.1, D2, D5)
// ---------------------------------------------------------------------------

// newTestFSConnOn stands up a fixture server in mode and takes a lease on it
// whose watchdog fires after hardTimeout. The lease is closed with the test.
func newTestFSConnOn(t *testing.T, mode fsServerMode, hardTimeout time.Duration) (*fsConn, *fsTestServer, *RealClient) {
	t.Helper()
	srv := startFSTestServer(t, mode)
	client := fsTestClient(t, srv)
	acq, err := client.acquirePooled(context.Background(), srv.addr, fsConnectOpts(srv))
	if err != nil {
		t.Fatalf("acquirePooled: %v", err)
	}
	fc, err := newFSConnLane(acq.client, func() { client.pool.Release(acq.handle) }, context.Background(), hardTimeout)
	if err != nil {
		t.Fatalf("newFSConnLane: %v", err)
	}
	t.Cleanup(func() { _ = fc.Close() })
	return fc, srv, client
}

// newTestFSConn is the ordinary fixture: a real SFTP server, the production
// hard timeout, and the served directory so a test can check the bytes that
// actually landed on disk.
func newTestFSConn(t *testing.T) (FSConn, string) {
	t.Helper()
	fc, srv, _ := newTestFSConnOn(t, fsModeReal, fsHardTimeout)
	return fc, srv.rootDir
}

// newTestFSConnWithTimeout is the same fixture with a watchdog short enough
// that a test can prove a property about it without waiting on a duration.
func newTestFSConnWithTimeout(t *testing.T, hardTimeout time.Duration) (FSConn, string) {
	t.Helper()
	fc, srv, _ := newTestFSConnOn(t, fsModeReal, hardTimeout)
	return fc, srv.rootDir
}

// newTestFSConnNoPosixRename serves everything except the
// posix-rename@openssh.com extension, which is answered unsupported.
func newTestFSConnNoPosixRename(t *testing.T) (FSConn, string) {
	t.Helper()
	fc, srv, _ := newTestFSConnOn(t, fsModeNoPosixRename, fsHardTimeout)
	return fc, srv.rootDir
}

// TestFSConn_CreateIsExclusiveAndDoesNotTruncate pins D5: the create is the
// arbiter of a collision, so it must refuse an existing path rather than
// empty it. sftp.Client.Create is O_RDWR|O_CREATE|O_TRUNC (client.go:304) and
// would have destroyed the file this test writes first.
func TestFSConn_CreateIsExclusiveAndDoesNotTruncate(t *testing.T) {
	c, dir := newTestFSConn(t)
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := c.Create(path)
	if err == nil {
		_ = f.Close()
		t.Fatal("Create on an existing path must fail; sftp.Client.Create would have truncated it")
	}
	if f != nil {
		t.Fatal("Create returned a handle alongside the error")
	}

	got, err := os.ReadFile(path) // #nosec G304 — test-owned path under the fixture's served temp directory.
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Fatalf("Create truncated an existing file: content is now %q", got)
	}
}

// TestFSConn_CreateWriteClose_LandsTheBytes is the paired success: on an
// ordinary server the write half writes, and the file on disk is exactly
// what was written.
func TestFSConn_CreateWriteClose_LandsTheBytes(t *testing.T) {
	c, dir := newTestFSConn(t)
	path := filepath.Join(dir, "new.txt")

	f, err := c.Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	want := []byte("hello upload")
	n, err := f.Write(want)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(want) {
		t.Fatalf("Write n = %d, want %d", n, len(want))
	}
	if closeErr := f.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}

	got, err := os.ReadFile(path) // #nosec G304 — test-owned path under the fixture's served temp directory.
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("file contains %q, want %q", got, want)
	}
}

// TestFSConn_ManyShortWritesOutliveTheHardTimeout is D2 stated as a test:
// the watchdog times one lane CALL, never the transfer, so a transfer made
// of short chunks runs arbitrarily longer than fsHardTimeout without
// poisoning the lease. It deliberately does not sleep — it makes many real
// calls whose sum exceeds a deliberately short watchdog while each one is
// short.
func TestFSConn_ManyShortWritesOutliveTheHardTimeout(t *testing.T) {
	c, dir := newTestFSConnWithTimeout(t, 200*time.Millisecond)
	path := filepath.Join(dir, "big.bin")
	f, err := c.Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	chunk := bytes.Repeat([]byte("x"), 4096)
	const chunks = 40
	start := time.Now()
	for i := 0; i < chunks; i++ {
		if _, writeErr := f.Write(chunk); writeErr != nil {
			t.Fatalf("write %d failed — the watchdog is timing the transfer, not the call: %v", i, writeErr)
		}
	}
	elapsed := time.Since(start)
	if closeErr := f.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	t.Logf("%d chunks in %s against a %s watchdog", chunks, elapsed, 200*time.Millisecond)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() != int64(chunks*len(chunk)) {
		t.Fatalf("file size = %d, want %d", info.Size(), chunks*len(chunk))
	}
	// The lease is still usable: nothing was poisoned along the way.
	if _, err := c.Stat(path); err != nil {
		t.Fatalf("lease unusable after the transfer: %v", err)
	}
}

// TestFSConn_WriteHalfRespectsTheLease proves the write half runs INSIDE the
// lane rather than beside it: after the lease is released, every call on it —
// including the ones a handle makes — reports the lease's own state instead
// of reaching the wire. A *sftp.File handed out raw would happily keep
// writing here.
func TestFSConn_WriteHalfRespectsTheLease(t *testing.T) {
	c, dir := newTestFSConn(t)
	path := filepath.Join(dir, "closed.txt")
	f, err := c.Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("lease Close: %v", err)
	}

	if _, err := f.Write([]byte("x")); !errors.Is(err, ErrFSClosed) {
		t.Fatalf("Write after lease Close = %v, want ErrFSClosed", err)
	}
	if err := f.Close(); !errors.Is(err, ErrFSClosed) {
		t.Fatalf("handle Close after lease Close = %v, want ErrFSClosed", err)
	}
	if _, err := c.Create(filepath.Join(dir, "other.txt")); !errors.Is(err, ErrFSClosed) {
		t.Fatalf("Create after lease Close = %v, want ErrFSClosed", err)
	}
	if err := c.PosixRename(path, filepath.Join(dir, "b")); !errors.Is(err, ErrFSClosed) {
		t.Fatalf("PosixRename after lease Close = %v, want ErrFSClosed", err)
	}
	if err := c.Rename(path, filepath.Join(dir, "b")); !errors.Is(err, ErrFSClosed) {
		t.Fatalf("Rename after lease Close = %v, want ErrFSClosed", err)
	}
	if err := c.Remove(path); !errors.Is(err, ErrFSClosed) {
		t.Fatalf("Remove after lease Close = %v, want ErrFSClosed", err)
	}
	// Nothing reached the server after the lease was released.
	if _, err := os.Stat(filepath.Join(dir, "other.txt")); !os.IsNotExist(err) {
		t.Fatalf("a call escaped the released lease and created a file: %v", err)
	}
}

// TestFSConn_WedgedWriteIsUnblockedByPoison closes the interval the lane
// opens: a write against a server that never answers is stuck from the
// moment its packet leaves until the watchdog poisons the lease, which
// closes the subsystem and is the only thing that unblocks it. The handle is
// invalidated by the same event, so nothing survives the poisoning.
func TestFSConn_WedgedWriteIsUnblockedByPoison(t *testing.T) {
	fc, srv, client := newTestFSConnOn(t, fsModeStallWrites, 300*time.Millisecond)
	f, err := fc.Create(filepath.Join(srv.rootDir, "stalled.bin"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	outCh := make(chan error, 1)
	go func() {
		_, werr := f.Write([]byte("payload"))
		outCh <- werr
	}()
	select {
	case <-srv.requestSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("server never received the WRITE request")
	}

	select {
	case werr := <-outCh:
		if !errors.Is(werr, ErrFSDead) {
			t.Fatalf("wedged Write = %v, want ErrFSDead", werr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wedged Write did not return after the hard timeout — poisoning did not unblock it")
	}

	// The handle is dead with the lease, not merely this one call.
	if _, werr := f.Write([]byte("more")); !errors.Is(werr, ErrFSDead) {
		t.Fatalf("Write after poison = %v, want ErrFSDead", werr)
	}
	if werr := f.Close(); !errors.Is(werr, ErrFSDead) {
		t.Fatalf("Close after poison = %v, want ErrFSDead", werr)
	}
	waitPoolEmpty(t, client)
}

// TestFSConn_PosixRenameUnsupportedIsDistinguishable pins the one error the
// sink's fallback keys on (D6). A server without the extension must be
// distinguishable from a dead lease, a lost connection, a released lease and
// an ordinary failure — otherwise the fallback either never runs or runs
// when it must not.
func TestFSConn_PosixRenameUnsupportedIsDistinguishable(t *testing.T) {
	c, dir := newTestFSConnNoPosixRename(t)
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := c.PosixRename(filepath.Join(dir, "a"), filepath.Join(dir, "b"))
	if !errors.Is(err, ErrPosixRenameUnsupported) {
		t.Fatalf("the fallback keys on exactly this error; got %v", err)
	}
	for _, other := range []error{ErrFSDead, ErrFSLost, ErrFSClosed, ErrFSTimedOut} {
		if errors.Is(err, other) {
			t.Fatalf("unsupported-extension error also matches %v — the fallback cannot tell them apart", other)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "b")); !os.IsNotExist(err) {
		t.Fatalf("the refused rename moved the file anyway: %v", err)
	}

	// The lease survives a refused extension: it is a server capability
	// answer, not a transport failure.
	if _, err := c.Stat(filepath.Join(dir, "a")); err != nil {
		t.Fatalf("lease unusable after an unsupported extension: %v", err)
	}
}

// TestFSConn_PosixRename_ReplacesOnAnOrdinaryServer is the paired success:
// on a server that has the extension, the rename happens and replaces an
// existing destination atomically (D6).
func TestFSConn_PosixRename_ReplacesOnAnOrdinaryServer(t *testing.T) {
	c, dir := newTestFSConn(t)
	src := filepath.Join(dir, "temp.nocx-upload-1")
	dst := filepath.Join(dir, "dest.txt")
	if err := os.WriteFile(src, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := c.PosixRename(src, dst); err != nil {
		t.Fatalf("PosixRename: %v", err)
	}
	got, err := os.ReadFile(dst) // #nosec G304 — test-owned path under the fixture's served temp directory.
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != "new" {
		t.Fatalf("dest contains %q, want %q", got, "new")
	}
	if _, statErr := os.Stat(src); !os.IsNotExist(statErr) {
		t.Fatalf("source still present after the rename: %v", statErr)
	}

	// The other half of the same partition: on a server that HAS the
	// extension, an ordinary failure must not claim the extension is
	// missing, or the fallback replaces a file the server was willing to
	// replace atomically.
	err = c.PosixRename(filepath.Join(dir, "missing"), filepath.Join(dir, "c"))
	if err == nil {
		t.Fatal("PosixRename of a missing source succeeded")
	}
	if errors.Is(err, ErrPosixRenameUnsupported) {
		t.Fatalf("a missing source was reported as an unsupported extension: %v", err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("PosixRename of a missing source = %v, want os.ErrNotExist", err)
	}
}

// TestFSConn_Rename_MovesAFile is the plain v3 rename's success half: the
// fallback's two moves (dest → backup, temp → dest) are this call.
func TestFSConn_Rename_MovesAFile(t *testing.T) {
	c, dir := newTestFSConn(t)
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := c.Rename(src, dst); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	got, err := os.ReadFile(dst) // #nosec G304 — test-owned path under the fixture's served temp directory.
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("dest contains %q, want %q", got, "payload")
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source still present after the rename: %v", err)
	}
}

// TestFSConn_Rename_MissingSourceFails is its failure half: the call the
// fallback makes can fail, and it must say so rather than report success.
func TestFSConn_Rename_MissingSourceFails(t *testing.T) {
	c, dir := newTestFSConn(t)
	err := c.Rename(filepath.Join(dir, "missing.txt"), filepath.Join(dir, "dst.txt"))
	if err == nil {
		t.Fatal("Rename of a missing source succeeded")
	}
	if errors.Is(err, ErrPosixRenameUnsupported) {
		t.Fatalf("an ordinary rename failure claimed the extension is unsupported: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "dst.txt")); !os.IsNotExist(err) {
		t.Fatalf("a failed rename created the destination: %v", err)
	}
}

// TestFSConn_Remove_DeletesAFile is the success half of the fallback's last
// step, unlinking the backup once the replacement is in place.
func TestFSConn_Remove_DeletesAFile(t *testing.T) {
	c, dir := newTestFSConn(t)
	path := filepath.Join(dir, "bak.txt")
	if err := os.WriteFile(path, []byte("backup"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := c.Remove(path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file still present after Remove: %v", err)
	}
}

// TestFSConn_Remove_MissingPathFails is its failure half: a cleanup that
// silently succeeds on a path it never removed hides a leaked temp file.
func TestFSConn_Remove_MissingPathFails(t *testing.T) {
	c, dir := newTestFSConn(t)
	err := c.Remove(filepath.Join(dir, "never-existed"))
	if err == nil {
		t.Fatal("Remove of a missing path succeeded")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Remove of a missing path = %v, want os.ErrNotExist", err)
	}
}

// ── the read-stream half (FSConn.Open) ───────────────────────────────────

// TestFSConn_OpenReadsAFileBackOverTheLease is the paired success for the
// read direction, against a real SFTP server: the handle opens, reports the
// size the file actually has, and reads back byte for byte.
func TestFSConn_OpenReadsAFileBackOverTheLease(t *testing.T) {
	c, dir := newTestFSConn(t)
	path := filepath.Join(dir, "f.txt")
	want := []byte("hello download")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}

	r, size, err := c.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = r.Close() }()
	if size != int64(len(want)) {
		t.Fatalf("size = %d, want %d", size, len(want))
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("read %q, want %q", got, want)
	}
}

// TestFSConn_OpenRefusesADirectory pins the kind check, which is the one
// thing about this method that is not obvious from its signature. Opening a
// directory over SFTP SUCCEEDS on an OpenSSH server and fails only at the
// first read, so without the check a download of a folder would become a
// framed 200 that dies mid-body — and the person would be told the transfer
// broke rather than that they picked a folder.
func TestFSConn_OpenRefusesADirectory(t *testing.T) {
	c, dir := newTestFSConn(t)
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}

	r, _, err := c.Open(sub)
	if err == nil {
		_ = r.Close()
		t.Fatal("Open of a directory succeeded; the kind check is what keeps it out of a framed response")
	}
	if !errors.Is(err, ErrNotRegularFile) {
		t.Fatalf("Open of a directory: %v, want ErrNotRegularFile", err)
	}
	if r != nil {
		t.Fatal("Open returned a handle alongside the error")
	}
}

// TestFSConn_OpenOfAMissingPathReportsNotExist is the contract
// transfer.RemoteReadFS documents and the compiler cannot check: the
// transport turns fs.ErrNotExist into a request-shaped refusal a person can
// act on, and anything unclassified into a server fault.
func TestFSConn_OpenOfAMissingPathReportsNotExist(t *testing.T) {
	c, dir := newTestFSConn(t)

	if _, _, err := c.Open(filepath.Join(dir, "nope")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Open of a missing path: %v, want an error satisfying fs.ErrNotExist", err)
	}
}

// TestFSConn_ManyShortReadsOutliveTheHardTimeout is D2 in the read
// direction, and it is the assertion that makes a large download possible
// at all: the watchdog times one lane CALL and never the transfer, so a
// read made of short chunks runs arbitrarily longer than the hard timeout
// without poisoning the lease. Like its write counterpart it sleeps for
// nothing — it makes many real calls whose sum exceeds a deliberately short
// watchdog while each one is short.
func TestFSConn_ManyShortReadsOutliveTheHardTimeout(t *testing.T) {
	c, dir := newTestFSConnWithTimeout(t, 200*time.Millisecond)
	path := filepath.Join(dir, "big.bin")
	const chunks = 40
	body := bytes.Repeat([]byte("y"), 4096*chunks)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	r, size, err := c.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = r.Close() }()
	if size != int64(len(body)) {
		t.Fatalf("size = %d, want %d", size, len(body))
	}
	buf := make([]byte, 4096)
	var total int
	start := time.Now()
	for total < len(body) {
		n, readErr := r.Read(buf)
		total += n
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read at %d failed — the watchdog is timing the transfer, not the call: %v", total, readErr)
		}
	}
	elapsed := time.Since(start)
	if total != len(body) {
		t.Fatalf("read %d of %d bytes", total, len(body))
	}
	t.Logf("%d chunks in %s against a %s watchdog", chunks, elapsed, 200*time.Millisecond)
	// The lease is still usable: nothing was poisoned along the way. That
	// is the assertion, and it is a state rather than a duration — a
	// watchdog that timed the whole read would have poisoned the lease
	// before this line, and the loop above would already have failed.
	if _, err := c.Stat(path); err != nil {
		t.Fatalf("lease unusable after the transfer: %v", err)
	}
}

// TestFSConn_ReadHalfRespectsTheLease proves the read half runs INSIDE the
// lane rather than beside it: after the lease is released, every call the
// handle makes reports the lease's own state instead of reaching the wire.
// A *sftp.File handed out raw would happily keep reading here — which is
// the whole reason fsReadFile keeps the lease instead of the file.
func TestFSConn_ReadHalfRespectsTheLease(t *testing.T) {
	c, dir := newTestFSConn(t)
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, _, err := c.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("lease Close: %v", err)
	}

	if _, err := r.Read(make([]byte, 4)); !errors.Is(err, ErrFSClosed) {
		t.Fatalf("Read after lease Close = %v, want ErrFSClosed", err)
	}
	if err := r.Close(); !errors.Is(err, ErrFSClosed) {
		t.Fatalf("handle Close after lease Close = %v, want ErrFSClosed", err)
	}
}

// TestFSConn_OpenAfterCloseReportsAClosedLease closes the read handle's
// interval at the far end: once the lease is closed nothing opened through
// it can succeed, and the failure is the lease's own typed answer rather
// than an I/O error nobody can classify.
func TestFSConn_OpenAfterCloseReportsAClosedLease(t *testing.T) {
	c, dir := newTestFSConn(t)
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, _, err := c.Open(path); !errors.Is(err, ErrFSClosed) {
		t.Fatalf("Open on a closed lease: %v, want ErrFSClosed", err)
	}
}
