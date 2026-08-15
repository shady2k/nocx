package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
)

// The fixture's user key has to be loadable by the OpenSSH CLIENT, which is
// what the nocxify journey actually runs — not merely by Go.
//
// It was not. writeUserKey wrote PKCS#8 ("PRIVATE KEY"), and OpenSSH reads
// ed25519 private keys only in the OpenSSH format. `ssh -i` answered
//
//	Load key "…/id_e2e": invalid format
//
// and, having no key to offer, never sent a publickey userauth request. Go's
// own ssh.ParsePrivateKey accepts BOTH encodings, so nothing on this side of
// the wire could notice — which is why this asserts the encoding rather than
// round-tripping through the library that is indifferent to it (nocx-z9s9.12).
func TestWriteUserKey_IsInTheFormatOpenSSHCanLoad(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	path, err := writeUserKey(priv)
	if err != nil {
		t.Fatalf("writeUserKey: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })

	// The path is this test's own MkdirTemp output, not input.
	raw, err := os.ReadFile(path) //nolint:gosec // path is minted by writeUserKey above
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatal("the key file is not PEM at all")
	}
	if block.Type != "OPENSSH PRIVATE KEY" {
		t.Errorf("PEM block type = %q, want %q — OpenSSH cannot load an ed25519 key in any other encoding",
			block.Type, "OPENSSH PRIVATE KEY")
	}

	// And the paired assertion: it is still a key, not merely a correct label.
	if _, err := gossh.ParsePrivateKey(raw); err != nil {
		t.Errorf("ParsePrivateKey: %v", err)
	}
}

// CONN= must mean "a client reached me and tried to authenticate", for any
// client — not "a client offered a public key".
//
// It was wired only into PublicKeyCallback, so a client that cannot offer a key
// (because the key would not load, because it was told not to, because it has
// none) authenticated by password perfectly well while the fixture stayed
// silent. The journey waits 30 seconds for a line that will never come and then
// reports "saw 0/1 CONN= lines" — a timeout that names the signal and not the
// cause. Password-only is the case that has to work, so it is the case tested
// (nocx-z9s9.12).
func TestConnSignal_FiresForAClientThatOffersNoPublicKey(t *testing.T) {
	userSigner, _, _, err := signer()
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	hostSigner, _, _, err := signer()
	if err != nil {
		t.Fatalf("signer: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	fired := make(chan struct{}, 1)
	var once sync.Once
	config := buildConfig(userSigner, hostSigner, "banner", "the-password", func() {
		once.Do(func() { fired <- struct{}{} })
	})

	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		// Only the handshake matters here; the session that follows is
		// handleSession's subject, not this test's.
		_, _, _, _ = gossh.NewServerConn(conn, config)
	}()

	// A client with NO public key at all: password is the only method it can
	// offer, which is exactly the journey's hand-typed ssh once its -i key
	// fails to load.
	client, err := gossh.Dial("tcp", ln.Addr().String(), &gossh.ClientConfig{
		User: "e2e",
		Auth: []gossh.AuthMethod{gossh.Password("the-password")},
		// The host key is minted by this test a dozen lines up; there is no
		// trust decision here to get wrong.
		HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec // fixture host key, minted in-process
		Timeout:         10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = client.Close() }()

	select {
	case <-fired:
	case <-time.After(10 * time.Second):
		t.Fatal("no CONN= signal for a client that authenticated by password")
	}
}

// A real sshd tells the session which login shell it is, and the nocx launcher
// carrier reads exactly that.
//
// The fixture did not. It handed the session `os.Environ()` plus TERM, so
// $SHELL was whatever had leaked in from whoever started e2e-sshd — and inside
// the e2e container nothing sets it at all. `~/.nocx/launch` selects its script
// with
//
//	case "${SHELL:-/bin/sh}" in */bash) … */zsh) … *) … posix
//
// so an absent $SHELL sent every installed connection down the POSIX fallback
// and the passport came back `tier=minimal` where the first, argv-borne
// connection had produced `tier=enhanced`. The journey saw a second ssh block
// that never entered an environment (nocx-z9s9.13).
//
// The fixture already decides the login shell — it execs bash. This asserts it
// publishes that decision rather than leaving the far side to guess.
func TestSessionEnv_NamesTheShellTheFixtureActuallyRuns(t *testing.T) {
	const bashPath = "/some/where/bin/bash"

	env := sessionEnv(bashPath)

	var shell string
	var seen int
	for _, kv := range env {
		if strings.HasPrefix(kv, "SHELL=") {
			shell = strings.TrimPrefix(kv, "SHELL=")
			seen++
		}
	}
	if seen == 0 {
		t.Fatal("the session environment carries no SHELL; a real sshd always sets one")
	}
	// Exactly one: appending a second SHELL= would leave the winner up to the
	// consumer, and `case "$SHELL"` reads whichever the shell resolved.
	if seen != 1 {
		t.Errorf("SHELL appears %d times, want exactly 1", seen)
	}
	if shell != bashPath {
		t.Errorf("SHELL = %q, want %q — the shell the fixture execs", shell, bashPath)
	}

	// And it still carries a terminal, which the integration scripts need.
	if !slices.Contains(env, "TERM=xterm-256color") {
		t.Error("the session environment lost TERM")
	}
}

// startFixture starts the in-process server the way run() does and returns
// everything a client needs to reach it: the address, the host key, and the
// user key's signer (the server accepts exactly that key). The listener is
// closed by the test's cleanup.
func startFixture(t *testing.T) (addr string, hostKey gossh.PublicKey, userSigner gossh.Signer) {
	t.Helper()
	hostSigner, _, _, err := signer()
	if err != nil {
		t.Fatalf("host signer: %v", err)
	}
	userSigner, _, _, err = signer()
	if err != nil {
		t.Fatalf("user signer: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			config := buildConfig(userSigner, hostSigner, "", "", func() {})
			go serveConn(conn, config)
		}
	}()
	return ln.Addr().String(), hostSigner.PublicKey(), userSigner
}

// dial opens a client connection to the fixture, trusting exactly the host
// key startFixture returned.
func dial(t *testing.T, addr string, hostKey gossh.PublicKey, userSigner gossh.Signer) *gossh.Client {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	config := &gossh.ClientConfig{
		User: "e2e",
		Auth: []gossh.AuthMethod{gossh.PublicKeys(userSigner)},
		HostKeyCallback: func(_ string, _ net.Addr, key gossh.PublicKey) error {
			if string(key.Marshal()) != string(hostKey.Marshal()) {
				return fmt.Errorf("host key mismatch")
			}
			return nil
		},
	}
	sshConn, chans, reqs, err := gossh.NewClientConn(conn, addr, config)
	if err != nil {
		_ = conn.Close()
		t.Fatalf("ssh connect: %v", err)
	}
	client := gossh.NewClient(sshConn, chans, reqs)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// TestExecChannelIsPtyLess proves the fixture is faithful to sshd for exec
// channels: no pty means no line discipline, so a binary frame survives.
// A pty would turn the 0x0A into 0x0D 0x0A and echo the input back.
func TestExecChannelIsPtyLess(t *testing.T) {
	addr, hostKey, signer := startFixture(t)
	client := dial(t, addr, hostKey, signer)
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	defer func() { _ = sess.Close() }()

	stdin, err := sess.StdinPipe()
	if err != nil {
		t.Fatalf("stdin: %v", err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout: %v", err)
	}
	if err = sess.Start("cat"); err != nil {
		t.Fatalf("start: %v", err)
	}

	want := []byte{0x00, 0x0A, 0x0D, 0x0A, 0xFF, 'x'}
	if _, err = stdin.Write(want); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = stdin.Close()

	got, err := io.ReadAll(stdout)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("bytes mangled: want %v, got %v", want, got)
	}
}

// TestSFTPSubsystemServesTheRealFilesystem proves the fixture is faithful
// to sshd for the sftp subsystem: the helper install path (plan Task 9,
// internal/ssh.HelperInstallConn) writes the versioned binary through an
// SFTP lease, and a fixture that refuses the subsystem would make every
// remote helper install answer deployFailed instead of serving the panel.
// pkg/sftp's server serves the real filesystem with absolute paths, which
// is exactly what the installer needs — its paths are absolute
// (~/.nocx/helper/<version>-<goos>-<goarch>-<hash>/), and a server rooted
// at a virtual directory would fail the install in a different way. The
// create-and-read round trip through an absolute path is the assertion,
// not merely that the subsystem request was accepted.
func TestSFTPSubsystemServesTheRealFilesystem(t *testing.T) {
	addr, hostKey, signer := startFixture(t)
	client := dial(t, addr, hostKey, signer)

	conn, err := sftp.NewClient(client)
	if err != nil {
		t.Fatalf("sftp connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	target := filepath.Join(t.TempDir(), "installed-helper")
	want := []byte{0x00, 0x0A, 0x0D, 0xFF, 'f', 'r', 'a', 'm', 'e'}
	f, err := conn.Create(target)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err = f.Write(want); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err = f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	rf, err := conn.Open(target)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	got, err := io.ReadAll(rf)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	_ = rf.Close()
	if !bytes.Equal(got, want) {
		t.Fatalf("bytes mangled: want %v, got %v", want, got)
	}
}

// TestSFTPRootIsTheAccountHomeNotTheProcessCwd pins the sftp server's
// relative-path root: a real sshd's sftp-server starts in the user's home,
// and the helper install lease discovers that home via SFTP RealPath(".")
// (internal/ssh.HelperInstallConn.Home). The fixture chdirs into the seeded
// repository at -repo, so a server rooted at the process cwd would answer
// the repository path as the home and install the helper INTO the
// repository — the acceptance test caught exactly that. The process cwd is
// moved away from the home before the assertion, which is the condition
// that used to fail.
func TestSFTPRootIsTheAccountHomeNotTheProcessCwd(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("no HOME to assert the sftp root against")
	}
	t.Chdir(t.TempDir()) // the server must NOT root relative paths here

	addr, hostKey, signer := startFixture(t)
	client := dial(t, addr, hostKey, signer)

	conn, err := sftp.NewClient(client)
	if err != nil {
		t.Fatalf("sftp connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	got, err := conn.Getwd() // client Getwd is the server's RealPath(".")
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if got != home {
		t.Fatalf("sftp root = %q, want the account home %q", got, home)
	}
}
