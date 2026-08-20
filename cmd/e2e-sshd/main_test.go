package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
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

// The far side must serve the SFTP subsystem, because that is the only way
// nocx delivers its integration bundle (ADR-0035).
//
// The fixture answered `subsystem` from handleSession's `default` arm — a
// flat refusal — so `sftp.NewClientPipe` got a closed channel instead of a
// version packet and every session to this host came up "Not integrated:
// nocx could not copy its shell integration to this host". Three e2e specs
// failed on it (shell-mode, ports-row-width, nocxify-journey) and none of
// them is about SFTP: they are about a connection coming up integrated,
// which is what a refused subsystem takes away.
//
// A round trip, not a handshake. A server that answers the version packet and
// then cannot write a file would satisfy the client's constructor and fail the
// publisher, so this writes bytes and reads them back.
func TestSubsystem_SFTPIsServedAndCanCarryAFile(t *testing.T) {
	client := dialFixture(t)

	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = sess.Close() }()
	stdin, err := sess.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if subErr := sess.RequestSubsystem("sftp"); subErr != nil {
		t.Fatalf("RequestSubsystem(sftp): %v — the fixture refuses the subsystem nocx publishes over", subErr)
	}

	sc, err := sftp.NewClientPipe(stdout, stdin)
	if err != nil {
		t.Fatalf("NewClientPipe: %v", err)
	}
	defer func() { _ = sc.Close() }()

	// sftpFS.Rename refuses to replace an existing destination on a server
	// that does not advertise this, and there is deliberately no
	// remove-then-rename fallback in the publisher. Without the extension the
	// fixture would accept a first publish and refuse every upgrade after it.
	if _, ok := sc.HasExtension("posix-rename@openssh.com"); !ok {
		t.Error("the fixture's SFTP server does not advertise posix-rename@openssh.com; " +
			"the publisher cannot replace an existing manifest on such a server")
	}

	dir := t.TempDir()
	target := dir + "/bundle"
	fh, err := sc.Create(target)
	if err != nil {
		t.Fatalf("Create over sftp: %v", err)
	}
	const payload = "the bundle travels on the channel, not in the command"
	if _, wErr := fh.Write([]byte(payload)); wErr != nil {
		t.Fatalf("Write over sftp: %v", wErr)
	}
	if cErr := fh.Close(); cErr != nil {
		t.Fatalf("Close over sftp: %v", cErr)
	}

	// Read back through the server, not off the local disk: what is being
	// asserted is that the transfer completed, not that TempDir works.
	rh, err := sc.Open(target)
	if err != nil {
		t.Fatalf("Open over sftp: %v", err)
	}
	defer func() { _ = rh.Close() }()
	got, err := io.ReadAll(rh)
	if err != nil {
		t.Fatalf("ReadAll over sftp: %v", err)
	}
	if string(got) != payload {
		t.Errorf("read back %q, want %q", got, payload)
	}
}

// And the paired refusal: a subsystem the fixture does not implement is still
// refused, rather than the handler accepting anything and serving SFTP for it.
func TestSubsystem_AnUnknownNameIsStillRefused(t *testing.T) {
	client := dialFixture(t)

	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = sess.Close() }()
	if err := sess.RequestSubsystem("netconf"); err == nil {
		t.Error("the fixture accepted a subsystem it does not implement")
	}
}

// dialFixture starts the fixture's real accept path — buildConfig, serveConn,
// handleSession — on an ephemeral port and returns a connected client.
func dialFixture(t *testing.T) *gossh.Client {
	t.Helper()
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
	t.Cleanup(func() { _ = ln.Close() })

	config := buildConfig(userSigner, hostSigner, "", "", func() {})
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go serveConn(conn, config)
		}
	}()

	client, err := gossh.Dial("tcp", ln.Addr().String(), &gossh.ClientConfig{
		User: "e2e",
		Auth: []gossh.AuthMethod{gossh.PublicKeys(userSigner)},
		// The host key is minted by this test; there is no trust decision here.
		HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec // fixture host key, minted in-process
		Timeout:         10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// An `exec` session must run to completion and report its own exit status,
// even though the client sends channel EOF the instant it starts.
//
// It did not. `startCommand`'s input goroutine closed the PTY master when the
// client half-closed its stdin — which gossh does immediately for every
// session with no Stdin, i.e. every `sess.Output(...)` — so the child took
// SIGHUP from its controlling terminal before it could write a byte. EVERY
// exec on this fixture answered "Process exited with status 255" with empty
// output.
//
// The one caller is shellintegration.GetRemoteHome, and while the integration
// bundle travelled in the ssh command line its failure was fail-open and cost
// nothing. Since ADR-0035 the bundle travels over SFTP into that home, so the
// same silent 255 turns into "nocx could not copy its shell integration to
// this host" on the user's screen, and three e2e specs with it.
func TestExec_RunsToCompletionWhenTheClientHalfClosesItsStdin(t *testing.T) {
	client := dialFixture(t)

	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = sess.Close() }()

	// The command GetRemoteHome actually runs, spelled exactly the same way.
	out, err := sess.Output("echo $HOME")
	if err != nil {
		t.Fatalf("Output(echo $HOME): %v — an exec that cannot finish makes every publish over SFTP impossible", err)
	}
	if strings.TrimSpace(string(out)) == "" {
		t.Errorf("Output(echo $HOME) = %q, want the remote home", out)
	}
}

// And the paired failure: a command that exits non-zero reports THAT status,
// not the 255 a signalled child reports. Without this the test above passes
// against a fixture that answers 0 for everything.
func TestExec_ReportsTheCommandsOwnNonZeroStatus(t *testing.T) {
	client := dialFixture(t)

	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = sess.Close() }()

	err = sess.Run("exit 3")
	var exitErr *gossh.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Run(exit 3) error = %v, want an *ssh.ExitError", err)
	}
	if exitErr.ExitStatus() != 3 {
		t.Errorf("exit status = %d, want 3", exitErr.ExitStatus())
	}
}
