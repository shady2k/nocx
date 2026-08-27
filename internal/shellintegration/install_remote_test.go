package shellintegration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"github.com/pkg/sftp"

	"github.com/shady2k/nocx/internal/ssh"
)

// ---------------------------------------------------------------------------
// In-process SSH server with an sftp subsystem, rooted at the real filesystem
// ---------------------------------------------------------------------------

// remoteTestSSHServer is a minimal SSH server that accepts exactly one user
// key and serves the "sftp" subsystem over every session channel, plus the
// two home-discovery commands the carrier runs. Absolute paths pass through
// to the real filesystem (the legacy sftp server only roots relative paths),
// so a test can use t.TempDir() as the remote home and assert on the
// directory afterwards.
type remoteTestSSHServer struct {
	t          *testing.T
	listener   net.Listener
	addr       string
	hostSigner gossh.Signer
	userSigner gossh.Signer
	home       string // the home directory exec'd `echo $HOME` answers with
}

func startRemoteTestSSHServer(t *testing.T) *remoteTestSSHServer {
	t.Helper()

	hostSigner := testSigner(t)
	userSigner := testSigner(t)

	config := &gossh.ServerConfig{
		PublicKeyCallback: func(meta gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			if bytes.Equal(key.Marshal(), userSigner.PublicKey().Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("gossh: unknown public key for %q", meta.User())
		},
	}
	config.AddHostKey(hostSigner)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("test server listen: %v", err)
	}

	srv := &remoteTestSSHServer{
		t:          t,
		listener:   listener,
		addr:       listener.Addr().String(),
		hostSigner: hostSigner,
		userSigner: userSigner,
	}
	go srv.acceptLoop(config)
	return srv
}

func testSigner(t *testing.T) gossh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	return signer
}

func (s *remoteTestSSHServer) acceptLoop(config *gossh.ServerConfig) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			// Listener closed (s.close) — stop.
			return
		}
		go s.serveConn(conn, config)
	}
}

func (s *remoteTestSSHServer) serveConn(conn net.Conn, config *gossh.ServerConfig) {
	sshConn, chans, reqs, err := gossh.NewServerConn(conn, config)
	if err != nil {
		s.t.Logf("test server handshake: %v", err)
		_ = conn.Close()
		return
	}
	go gossh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(gossh.UnknownChannelType, "unknown channel type")
			continue
		}
		ch, reqs, err := newChan.Accept()
		if err != nil {
			s.t.Logf("test server accept channel: %v", err)
			return
		}
		go s.handleSession(ch, reqs)
	}

	_ = sshConn.Close()
}

func (s *remoteTestSSHServer) handleSession(ch gossh.Channel, reqs <-chan *gossh.Request) {
	for req := range reqs {
		switch req.Type {
		case "exec":
			cmd := ""
			if len(req.Payload) >= 4 {
				cmd = string(req.Payload[4:])
			}
			_ = req.Reply(true, nil)
			switch cmd {
			case "echo $HOME", "cd ~ && pwd":
				_, _ = io.WriteString(ch, s.home+"\n")
			default:
				_, _ = io.WriteString(ch, "unknown command\n")
			}
			_, _ = ch.SendRequest("exit-status", false, gossh.Marshal(&struct{ Status uint32 }{0}))
			_ = ch.Close()
			return
		case "subsystem":
			if len(req.Payload) < 4 || string(req.Payload[4:]) != "sftp" {
				_ = req.Reply(false, nil)
				continue
			}
			_ = req.Reply(true, nil)

			srv, err := sftp.NewServer(ch)
			if err != nil {
				s.t.Logf("test sftp server: %v", err)
				return
			}
			if err := srv.Serve(); err != nil && !errors.Is(err, io.EOF) {
				s.t.Logf("test sftp serve: %v", err)
			}
			// Close the channel back: the client's sftp Close() waits on its
			// recv goroutine, which only unblocks once it sees our close.
			_ = ch.Close()
			return
		default:
			_ = req.Reply(false, nil)
		}
	}
}

func (s *remoteTestSSHServer) close() {
	_ = s.listener.Close()
}

func dialRemoteTestSSHClient(t *testing.T, srv *remoteTestSSHServer) *gossh.Client {
	t.Helper()

	client, err := gossh.Dial("tcp", srv.addr, &gossh.ClientConfig{
		User: "test",
		Auth: []gossh.AuthMethod{gossh.PublicKeys(srv.userSigner)},
		HostKeyCallback: func(hostname string, remote net.Addr, key gossh.PublicKey) error {
			if !bytes.Equal(key.Marshal(), srv.hostSigner.PublicKey().Marshal()) {
				return fmt.Errorf("host key mismatch for %q", hostname)
			}
			return nil
		},
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial test ssh server: %v", err)
	}
	return client
}

// ---------------------------------------------------------------------------
// The SFTP carrier publishes the bundle
// ---------------------------------------------------------------------------

// TestEnsureInstalledRemote_PublishesBundleOverSFTP proves the happy path
// over a real SSH/SFTP connection: the publisher's committed layout — a
// manifest naming exactly one active generation, the generation's three
// nocx scripts, the launch carrier — lands in the remote home, and a second
// call short-circuits with no duplicate work.
func TestEnsureInstalledRemote_PublishesBundleOverSFTP(t *testing.T) {
	srv := startRemoteTestSSHServer(t)
	defer srv.close()

	remoteHome := t.TempDir()
	srv.home = remoteHome
	ctx := context.Background()
	s := New(testLogger())

	client := dialRemoteTestSSHClient(t, srv)
	defer func() { _ = client.Close() }()

	if err := s.EnsureInstalledRemote(ctx, client, remoteHome); err != nil {
		t.Fatalf("EnsureInstalledRemote: %v", err)
	}

	root := filepath.Join(remoteHome, dirName)

	// A committed manifest names exactly one active generation, and every
	// file it names exists with the recorded hash and mode (the publisher's
	// own Verify is the per-file proof — assert it over the wire).
	vr, err := NewPublisher(testLogger(), sftpFS{SFTPFS: ssh.NewSFTPFS(mustSFTPClient(t, client))}, root).Verify()
	if err != nil {
		t.Fatalf("Verify over SFTP: %v", err)
	}
	if !vr.Installed {
		t.Fatal("Verify over SFTP: bundle not installed")
	}
	if vr.Generation != genDir(version) {
		t.Errorf("active generation = %q, want %q", vr.Generation, genDir(version))
	}

	// The launch carrier is installed once (0700), never rewritten.
	launchData := readFileT(t, filepath.Join(root, launchName))
	if !bytes.Equal(launchData, []byte(launchCarrier())) {
		t.Error("launch carrier content differs from the bundle's")
	}

	// The generation files are the embedded scripts, byte for byte.
	want := map[string]string{
		"nocx.bash":  bashScript,
		"nocx.zsh":   zshScript,
		"nocx.posix": posixScript,
	}
	for name, script := range want {
		got := readFileT(t, filepath.Join(root, integrationDir, genDir(version), name))
		if string(got) != script {
			t.Errorf("%s content differs from the embedded script (%d vs %d bytes)", name, len(got), len(script))
		}
	}

	// A second call short-circuits: the tree is byte-identical.
	before := activationSnapshot(t, root)
	if err := s.EnsureInstalledRemote(ctx, client, remoteHome); err != nil {
		t.Fatalf("second EnsureInstalledRemote: %v", err)
	}
	after := activationSnapshot(t, root)
	if !bytes.Equal(before, after) {
		t.Error("second publish changed the installed tree")
	}
}

// TestSFTPFSRename_ReplacesCommittedManifest pins the carrier operation that
// activates an upgrade. SSH_FXP_RENAME alone refuses manifest.json because
// it already exists; an advertised posix-rename@openssh.com must replace it
// atomically so the publisher can move from one valid generation to another.
func TestSFTPFSRename_ReplacesCommittedManifest(t *testing.T) {
	srv := startRemoteTestSSHServer(t)
	defer srv.close()

	client := dialRemoteTestSSHClient(t, srv)
	defer func() { _ = client.Close() }()
	sftpClient := mustSFTPClient(t, client)
	if data, ok := sftpClient.HasExtension("posix-rename@openssh.com"); !ok || data != "1" {
		t.Fatalf("test SFTP server posix-rename extension = %q, %v; want %q, true", data, ok, "1")
	}

	root := filepath.Join(t.TempDir(), dirName)
	pub := NewPublisher(testLogger(), sftpFS{SFTPFS: ssh.NewSFTPFS(sftpClient)}, root)
	if _, err := pub.Publish(testBundle("1")); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	if _, err := pub.Publish(testBundle("2")); err != nil {
		t.Fatalf("publish v2 over committed manifest: %v", err)
	}
	verified, err := pub.Verify()
	if err != nil {
		t.Fatalf("verify v2: %v", err)
	}
	if !verified.Installed || verified.Version != "2" || verified.Generation != "v2" {
		t.Fatalf("verified activation = %+v, want installed v2", verified)
	}
}

// TestSFTPFSRename_WithoutPosixExtensionKeepsPriorActivation decides the
// unsupported-server contract. A first publish uses standard SFTP rename and
// succeeds because manifest.json is absent. An upgrade is refused instead of
// remove-then-rename: the old manifest remains byte-identical and valid, so
// readers never observe a missing activation pointer.
func TestSFTPFSRename_WithoutPosixExtensionKeepsPriorActivation(t *testing.T) {
	if err := sftp.SetSFTPExtensions("hardlink@openssh.com", "statvfs@openssh.com"); err != nil {
		t.Fatalf("disable posix-rename extension: %v", err)
	}
	t.Cleanup(func() {
		if err := sftp.SetSFTPExtensions(
			"hardlink@openssh.com",
			"posix-rename@openssh.com",
			"statvfs@openssh.com",
		); err != nil {
			t.Errorf("restore SFTP extensions: %v", err)
		}
	})

	srv := startRemoteTestSSHServer(t)
	defer srv.close()
	client := dialRemoteTestSSHClient(t, srv)
	defer func() { _ = client.Close() }()
	sftpClient := mustSFTPClient(t, client)
	if data, ok := sftpClient.HasExtension("posix-rename@openssh.com"); ok {
		t.Fatalf("test SFTP server unexpectedly advertised posix-rename data %q", data)
	}

	root := filepath.Join(t.TempDir(), dirName)
	pub := NewPublisher(testLogger(), sftpFS{SFTPFS: ssh.NewSFTPFS(sftpClient)}, root)
	if _, err := pub.Publish(testBundle("1")); err != nil {
		t.Fatalf("first publish without posix-rename: %v", err)
	}
	manifestBefore := readFileT(t, filepath.Join(root, manifestName))

	if _, err := pub.Publish(testBundle("2")); err == nil {
		t.Fatal("upgrade without atomic replacement support succeeded")
	}
	manifestAfter := readFileT(t, filepath.Join(root, manifestName))
	if !bytes.Equal(manifestAfter, manifestBefore) {
		t.Fatal("failed upgrade changed the committed manifest")
	}
	verified, err := pub.Verify()
	if err != nil {
		t.Fatalf("verify prior activation: %v", err)
	}
	if !verified.Installed || verified.Version != "1" || verified.Generation != "v1" {
		t.Fatalf("verified activation = %+v, want prior installed v1", verified)
	}

	// The refusal must CONVERGE, not merely preserve. cleanupOrphans — the
	// sweep that bounds tmp/ — runs only on the success path, and the nonce
	// is fresh per attempt, so an unsupported server that is reconnected to
	// every day would otherwise gain one dead manifest per connect forever.
	for i := 0; i < 3; i++ {
		if _, perr := pub.Publish(testBundle("2")); perr == nil {
			t.Fatalf("upgrade attempt %d unexpectedly succeeded", i)
		}
	}
	leftovers, rerr := os.ReadDir(filepath.Join(root, tmpName))
	if rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
		t.Fatalf("read tmp dir: %v", rerr)
	}
	for _, e := range leftovers {
		if strings.HasPrefix(e.Name(), "manifest-") {
			t.Fatalf("a refused upgrade left %d tmp entries behind, first %q", len(leftovers), e.Name())
		}
	}
}

// TestEnsureInstalledRemote_ModesOverSFTP: modes are set at creation, never
// left to umask — over SFTP the server applies its own umask to mkdir and
// create, so the carrier's chmod is what pins them. Directories 0700, data
// 0600, the launch carrier 0700, the manifest 0600.
func TestEnsureInstalledRemote_ModesOverSFTP(t *testing.T) {
	srv := startRemoteTestSSHServer(t)
	defer srv.close()

	remoteHome := t.TempDir()
	srv.home = remoteHome
	s := New(testLogger())

	client := dialRemoteTestSSHClient(t, srv)
	defer func() { _ = client.Close() }()

	if err := s.EnsureInstalledRemote(context.Background(), client, remoteHome); err != nil {
		t.Fatalf("EnsureInstalledRemote: %v", err)
	}

	root := filepath.Join(remoteHome, dirName)
	for _, dir := range []string{
		root,
		filepath.Join(root, tmpName),
		filepath.Join(root, integrationDir),
		filepath.Join(root, integrationDir, genDir(version)),
	} {
		if got := statModeT(t, dir).Perm(); got != 0o700 {
			t.Errorf("directory mode %s = %04o, want 0700", dir, got)
		}
	}
	for _, name := range []string{"nocx.bash", "nocx.zsh", "nocx.posix"} {
		p := filepath.Join(root, integrationDir, genDir(version), name)
		if got := statModeT(t, p).Perm(); got != 0o600 {
			t.Errorf("data file mode %s = %04o, want 0600", p, got)
		}
	}
	if got := statModeT(t, filepath.Join(root, launchName)).Perm(); got != 0o700 {
		t.Errorf("launch carrier mode = %04o, want 0700", got)
	}
	if got := statModeT(t, filepath.Join(root, manifestName)).Perm(); got != 0o600 {
		t.Errorf("manifest mode = %04o, want 0600", got)
	}
}

// TestEnsureInstalledRemote_LeavesRcFilesByteIdentical is the N4 assertion:
// no remote rc file is created or modified on any path. A publish into an
// empty home creates none of the five; a publish across pre-existing rc
// files leaves every one byte-identical.
func TestEnsureInstalledRemote_LeavesRcFilesByteIdentical(t *testing.T) {
	srv := startRemoteTestSSHServer(t)
	defer srv.close()

	remoteHome := t.TempDir()
	srv.home = remoteHome
	ctx := context.Background()
	s := New(testLogger())

	client := dialRemoteTestSSHClient(t, srv)
	defer func() { _ = client.Close() }()

	rcNames := []string{".bashrc", ".bash_profile", ".profile", ".zshrc"}
	rcPaths := func() []string {
		paths := make([]string, 0, len(rcNames)+1)
		for _, n := range rcNames {
			paths = append(paths, filepath.Join(remoteHome, n))
		}
		paths = append(paths, filepath.Join(remoteHome, "zdot", ".zshrc"))
		return paths
	}

	// Direction one: no rc files exist, a publish creates none.
	if err := s.EnsureInstalledRemote(ctx, client, remoteHome); err != nil {
		t.Fatalf("EnsureInstalledRemote: %v", err)
	}
	for _, p := range rcPaths() {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("publish created rc file %s — N4 forbids it", p)
		}
	}

	// Direction two: pre-existing rc files with distinctive user content
	// stay byte-identical across a publish. ${ZDOTDIR}/.zshrc is a real
	// directory under the home, exactly as a zsh user would have it.
	rcContent := map[string][]byte{
		filepath.Join(remoteHome, ".bashrc"):        []byte("# user bashrc\nPS1='$ '\nexport FOO=bar\n"),
		filepath.Join(remoteHome, ".bash_profile"):  []byte("# user bash_profile\nexport EDITOR=vim\n"),
		filepath.Join(remoteHome, ".profile"):       []byte("# user profile\numask 022\n"),
		filepath.Join(remoteHome, ".zshrc"):         []byte("# user zshrc\nautoload -Uz compinit\n"),
		filepath.Join(remoteHome, "zdot", ".zshrc"): []byte("# zdotdir zshrc\nsetopt interactivecomments\n"),
	}
	for p, data := range rcContent {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatalf("mkdir for %s: %v", p, err)
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	before := map[string][]byte{}
	for p := range rcContent {
		before[p] = readFileT(t, p)
	}

	if err := s.EnsureInstalledRemote(ctx, client, remoteHome); err != nil {
		t.Fatalf("EnsureInstalledRemote over existing rc files: %v", err)
	}

	for p, want := range before {
		got := readFileT(t, p)
		if !bytes.Equal(got, want) {
			t.Errorf("%s changed across a publish: %d bytes -> %d bytes", p, len(want), len(got))
		}
	}
}

// TestEnsureInstalledRemote_ReadonlyHomeFailsOpenThenConverges: a read-only
// ~/.nocx publishes nothing (the session still starts — fail-open), the
// previous activation stays byte-identical, and once the obstacle is gone
// the next attempt converges with no manual cleanup.
func TestEnsureInstalledRemote_ReadonlyHomeFailsOpenThenConverges(t *testing.T) {
	srv := startRemoteTestSSHServer(t)
	defer srv.close()

	remoteHome := t.TempDir()
	srv.home = remoteHome
	ctx := context.Background()
	s := New(testLogger())

	client := dialRemoteTestSSHClient(t, srv)
	defer func() { _ = client.Close() }()

	if err := s.EnsureInstalledRemote(ctx, client, remoteHome); err != nil {
		t.Fatalf("EnsureInstalledRemote: %v", err)
	}

	root := filepath.Join(remoteHome, dirName)

	// Take write permission away from ~/.nocx itself (the parent's mode
	// does not matter — every write lands inside the root). The baseline
	// snapshot is taken after this chmod: the mode change is the test's
	// own action, not the publish's, and must not read as a change.
	// #nosec G302 — the read-only mode is the condition under test; the
	// publisher must refuse it rather than widen it.
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatalf("chmod root read-only: %v", err)
	}
	before := activationSnapshot(t, root)

	err := s.EnsureInstalledRemote(ctx, client, remoteHome)
	if err == nil {
		t.Fatal("publish into a read-only home succeeded; want a typed refusal")
	}
	var readonly *ReadonlyError
	if !errors.As(err, &readonly) {
		t.Errorf("error = %v, want a *ReadonlyError (the fail-open condition a carrier must recognise)", err)
	}

	after := activationSnapshot(t, root)
	if !bytes.Equal(before, after) {
		t.Error("failed publish changed the active activation")
	}

	// Restore and retry: converges with no manual cleanup.
	// #nosec G302 — restoring the publisher's own 0700 root mode.
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("chmod root writable: %v", err)
	}
	if err := s.EnsureInstalledRemote(ctx, client, remoteHome); err != nil {
		t.Fatalf("EnsureInstalledRemote after restore: %v", err)
	}
	m := readManifestT(t, root)
	if m.Generation != genDir(version) {
		t.Errorf("active generation after convergence = %q, want %q", m.Generation, genDir(version))
	}
}

// TestSFTPCarrierFault_InterruptedPublishLeavesActivationUntouched injects
// a fault at a mid-publish boundary through the carrier's own FS seam, over
// a real SFTP connection: the interrupted publish must leave the previous
// activation byte-identical, and the next attempt must converge with no
// manual cleanup.
func TestSFTPCarrierFault_InterruptedPublishLeavesActivationUntouched(t *testing.T) {
	srv := startRemoteTestSSHServer(t)
	defer srv.close()

	remoteHome := t.TempDir()
	srv.home = remoteHome

	client := dialRemoteTestSSHClient(t, srv)
	defer func() { _ = client.Close() }()
	sftpClient := mustSFTPClient(t, client)

	root := filepath.Join(remoteHome, dirName)
	fsys := newFaultFS(sftpFS{SFTPFS: ssh.NewSFTPFS(sftpClient)})
	pub := NewPublisher(testLogger(), fsys, root)

	v1 := testBundle("1")
	if _, err := pub.Publish(v1); err != nil {
		t.Fatalf("baseline publish v1: %v", err)
	}
	before := activationSnapshot(t, root)

	// Fail the second staged file write (nocx.zsh) of the v2 publish: a
	// transfer interrupted mid-staging. Reset the counter first so the
	// fault lands in the v2 publish, not the baseline.
	fsys.resetCounts()
	fsys.setFault("create", 2, errInjected)

	if _, err := pub.Publish(testBundle("2")); err == nil {
		t.Fatal("faulted publish did not fail")
	}

	after := activationSnapshot(t, root)
	if !bytes.Equal(before, after) {
		t.Error("interrupted publish changed the previous activation")
	}

	// Clear the fault and retry: the next attempt converges and the
	// manifest names v2.
	fsys.setFault("create", 0, nil)
	if _, err := pub.Publish(testBundle("2")); err != nil {
		t.Fatalf("retry after interrupted publish: %v", err)
	}
	m := readManifestT(t, root)
	if m.Generation != "v2" {
		t.Errorf("active generation after retry = %q, want v2", m.Generation)
	}
	assertBoundedFootprint(t, root, "v2")
}

// TestEnsureInstalledRemote_SymlinkedRootRefused: a symlinked ~/.nocx is
// never written through (design §4.1) — the publish refuses and the symlink
// target stays untouched.
func TestEnsureInstalledRemote_SymlinkedRootRefused(t *testing.T) {
	srv := startRemoteTestSSHServer(t)
	defer srv.close()

	remoteHome := t.TempDir()
	srv.home = remoteHome
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(remoteHome, dirName)); err != nil {
		t.Fatalf("symlink ~/.nocx: %v", err)
	}

	client := dialRemoteTestSSHClient(t, srv)
	defer func() { _ = client.Close() }()

	err := New(testLogger()).EnsureInstalledRemote(context.Background(), client, remoteHome)
	if err == nil {
		t.Fatal("publish through a symlinked ~/.nocx succeeded; want a refusal")
	}
	var se *SymlinkError
	if !errors.As(err, &se) {
		t.Fatalf("error = %v, want a *SymlinkError", err)
	}

	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatalf("read symlink target: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("wrote through the symlink: target holds %d entries", len(entries))
	}
}

// TestGetRemoteHome proves the home-discovery command over a real SSH
// connection.
func TestGetRemoteHome(t *testing.T) {
	srv := startRemoteTestSSHServer(t)
	defer srv.close()
	srv.home = "/home/testuser"

	client := dialRemoteTestSSHClient(t, srv)
	defer func() { _ = client.Close() }()

	home, err := New(testLogger()).GetRemoteHome(client)
	if err != nil {
		t.Fatalf("GetRemoteHome: %v", err)
	}
	if home != "/home/testuser" {
		t.Errorf("GetRemoteHome = %q, want /home/testuser", home)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// mustSFTPClient opens an sftp client over an SSH client, failing the test
// on error.
func mustSFTPClient(t *testing.T, client *gossh.Client) *sftp.Client {
	t.Helper()
	c, err := sftp.NewClient(client)
	if err != nil {
		t.Fatalf("sftp client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// activationSnapshot returns a deterministic digest of the activation under
// root: every entry's relative path, mode and content, EXCLUDING the
// publish-transient tmp/ and lock/ directories (a failed publish may leave
// staging behind by design — the invariant is that the manifest and the
// generation it names stay byte-identical, and the next attempt converges).
// Used to assert that a failed or interrupted publish leaves the previous
// activation byte-identical.
func activationSnapshot(t *testing.T, root string) []byte {
	t.Helper()
	var b bytes.Buffer
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if d.IsDir() && (rel == tmpName || rel == lockName) {
			return fs.SkipDir
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		fmt.Fprintf(&b, "%s %04o %d", rel, info.Mode().Perm(), info.Size())
		if !d.IsDir() {
			// #nosec G304 — test-only path under t.TempDir().
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			b.Write(data)
		}
		b.WriteByte(0)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return b.Bytes()
}

// TestUninstallRemote_RemovesManifestOwnedFilesOverSFTP: after a publish,
// UninstallRemote removes exactly the manifest-owned, unmodified files and
// reports them; a file the user changed is reported as a conflict and stays;
// ~/.nocx itself and the launch carrier are never removed recursively.
func TestUninstallRemote_RemovesManifestOwnedFilesOverSFTP(t *testing.T) {
	srv := startRemoteTestSSHServer(t)
	defer srv.close()

	remoteHome := t.TempDir()
	srv.home = remoteHome
	ctx := context.Background()
	s := New(testLogger())

	client := dialRemoteTestSSHClient(t, srv)
	defer func() { _ = client.Close() }()

	if err := s.EnsureInstalledRemote(ctx, client, remoteHome); err != nil {
		t.Fatalf("EnsureInstalledRemote: %v", err)
	}
	root := filepath.Join(remoteHome, dirName)

	// The user modified one generation file after the publish.
	gen := filepath.Join(root, integrationDir, genDir(version))
	if err := os.WriteFile(filepath.Join(gen, "nocx.bash"), []byte("user edit"), 0o600); err != nil {
		t.Fatalf("user edit: %v", err)
	}

	removed, conflicts, err := s.UninstallRemote(ctx, client, remoteHome)
	if err != nil {
		t.Fatalf("UninstallRemote: %v", err)
	}

	if !slices.Contains(removed, "manifest.json") {
		t.Errorf("removed = %v, want manifest.json among them", removed)
	}
	if !slices.Contains(removed, filepath.ToSlash(filepath.Join(integrationDir, genDir(version), "nocx.zsh"))) {
		t.Errorf("removed = %v, want the unmodified generation files among them", removed)
	}
	if !slices.Contains(conflicts, filepath.ToSlash(filepath.Join(integrationDir, genDir(version), "nocx.bash"))) {
		t.Errorf("conflicts = %v, want the user-modified nocx.bash reported", conflicts)
	}

	// The modified file stays; the root and the launch carrier stay; the
	// manifest is gone, so nothing is active anymore.
	if _, statErr := os.Stat(filepath.Join(gen, "nocx.bash")); statErr != nil {
		t.Errorf("user-modified file was removed: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, launchName)); statErr != nil {
		t.Errorf("launch carrier removed: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, manifestName)); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("manifest still present after uninstall: %v", statErr)
	}
	if _, statErr := os.Stat(root); statErr != nil {
		t.Errorf("~/.nocx removed recursively: %v", statErr)
	}

	// A second uninstall finds nothing to remove — idempotent, no error.
	removed2, conflicts2, err := s.UninstallRemote(ctx, client, remoteHome)
	if err != nil {
		t.Fatalf("second UninstallRemote: %v", err)
	}
	if len(removed2) != 0 || len(conflicts2) != 0 {
		t.Errorf("second uninstall = removed %v conflicts %v, want none", removed2, conflicts2)
	}
}
