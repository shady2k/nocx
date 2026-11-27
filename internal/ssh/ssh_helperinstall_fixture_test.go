//go:build nocx_local_ssh

package ssh

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkg/sftp"
	"github.com/shady2k/nocx/internal/log"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type fsTestServer struct {
	rootDir    string
	hostSigner gossh.Signer
	userSigner gossh.Signer
	addr       string
}

// startFSTestServer serves a temporary directory over a real SFTP subsystem.
func startFSTestServer(t *testing.T) *fsTestServer {
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
		rootDir:    t.TempDir(),
		hostSigner: hostKey,
		userSigner: userKey,
		addr:       listener.Addr().String(),
	}
	t.Cleanup(func() { _ = listener.Close() })
	go srv.acceptLoop(listener, config)
	return srv
}

func (s *fsTestServer) acceptLoop(listener net.Listener, config *gossh.ServerConfig) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go s.serveConn(conn, config)
	}
}

func (s *fsTestServer) serveConn(conn net.Conn, config *gossh.ServerConfig) {
	defer func() { _ = conn.Close() }()
	sshConn, chans, reqs, err := gossh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer func() { _ = sshConn.Close() }()
	go gossh.DiscardRequests(reqs)
	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(gossh.UnknownChannelType, "unknown channel type")
			continue
		}
		ch, reqs, err := newChan.Accept()
		if err == nil {
			go s.handleSession(ch, reqs)
		}
	}
}

func (s *fsTestServer) handleSession(ch gossh.Channel, reqs <-chan *gossh.Request) {
	defer func() { _ = ch.Close() }()
	for req := range reqs {
		var m struct{ Subsystem string }
		if req.Type != "subsystem" || gossh.Unmarshal(req.Payload, &m) != nil || m.Subsystem != "sftp" {
			_ = req.Reply(false, nil)
			continue
		}
		_ = req.Reply(true, nil)
		srv, err := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(s.rootDir))
		if err == nil {
			_ = srv.Serve()
		}
		return
	}
}

func fsTestClient(t *testing.T, srv *fsTestServer) *RealClient {
	t.Helper()
	fsWriteKnownHosts(t, srv)
	client, err := NewReal(log.NewSlogAdapter(nil))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func fsWriteKnownHosts(t *testing.T, srv *fsTestServer) {
	t.Helper()
	home := sshTestHome(t)
	line := knownhosts.Line([]string{srv.addr}, srv.hostSigner.PublicKey())
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("prepare %s: %v", dir, err)
	}
	path := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
}
