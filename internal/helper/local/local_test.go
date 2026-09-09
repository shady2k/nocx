package local_test

// The local carrier and the local install (L1, L2 of the local-helper design).
//
// What these tests are for: the machine you are sitting at is an ordinary host
// in the helper inventory, and it differs from a remote one in exactly one
// thing — the carrier is a Unix socket rather than an ssh exec lane. So the
// install writes the SAME content-addressed layout the remote installer
// writes, from the same bytes, and the handshake is NOT skipped locally: the
// hello-ok is what proves the binary answering is the generation we installed
// (D21), and a stale binary under ~/.nocx is likelier on the machine where
// builds land than on a server.
//
// Nothing here waits on a duration. The two facts waited on are a socket
// accepting and a child ending, both observable.

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/deploy"
	"github.com/shady2k/nocx/internal/helper/endpoint"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/local"
	"github.com/shady2k/nocx/internal/helper/proto"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// --- artifact sources -------------------------------------------------------

// bytesSource is an ArtifactSource over bytes the test chose: the install
// semantics are transport- and content-independent, so most of these tests
// need no compiler. The two that assert a HANDSHAKE need the real binary and
// say so.
type bytesSource struct {
	payload []byte
	err     error
}

func newBytesSource(payload []byte) bytesSource { return bytesSource{payload: payload} }

func (s bytesSource) Artifact(deploy.Platform) ([]byte, string, error) {
	if s.err != nil {
		return nil, "", s.err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(s.payload); err != nil {
		return nil, "", err
	}
	if err := zw.Close(); err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(s.payload)
	return buf.Bytes(), hex.EncodeToString(sum[:]), nil
}

func hashOf(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// helperSource builds cmd/nocx-helper once and serves it as the artifact — the
// real binary, because a handshake against anything else would be a test
// asserting about itself. The generation is the sha256 of the decompressed
// bytes, which is what the binary computes about its own file when it serves.
var (
	buildOnce   sync.Once
	builtHelper []byte
	buildErr    error
)

func helperSource(t *testing.T) bytesSource {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "nocxbin")
		if err != nil {
			buildErr = err
			return
		}
		defer func() { _ = os.RemoveAll(dir) }()
		bin := filepath.Join(dir, "nocx-helper")
		out, err := exec.Command("go", "build", "-o", bin, "../../../cmd/nocx-helper").CombinedOutput() //nolint:gosec // the arguments are this test's own constants
		if err != nil {
			buildErr = errors.New(string(out))
			return
		}
		builtHelper, buildErr = os.ReadFile(bin) // #nosec G304 — this test's own build output
	})
	if buildErr != nil {
		t.Fatalf("building cmd/nocx-helper: %v", buildErr)
	}
	return newBytesSource(builtHelper)
}

// --- homes, wrappers and the daemons they make killable ---------------------

// helperHome is the account under test as far as the endpoint is concerned:
// the run directory is derived from the home and from nothing else, which is
// what keeps these tests off the developer's real endpoint and off each
// other's.
func helperHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("", "nocxhome")
	if err != nil {
		t.Fatalf("temp home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	return home
}

// killableStarter wraps the installed helper in a script that records its pid
// and pins HOME. A helper is deliberately detached from whoever started it
// (D1), so a test that could not stop one again would leave a daemon running
// on the machine — and one whose HOME was the developer's would serve their
// real endpoint.
func killableStarter(t *testing.T, home, binary string) string {
	t.Helper()
	wrapper := filepath.Join(t.TempDir(), "start-helper")
	pidFile := filepath.Join(home, "helper.pid")
	script := "#!/bin/sh\necho $$ > " + pidFile + "\nexport HOME=" + home + "\nexec " + binary + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil { //nolint:gosec // it is meant to be executable
		t.Fatalf("write wrapper: %v", err)
	}
	t.Cleanup(func() {
		raw, err := os.ReadFile(pidFile) // #nosec G304 — this test's wrapper wrote it
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil || pid <= 0 {
			return
		}
		p, err := os.FindProcess(pid)
		if err != nil {
			return
		}
		_ = p.Signal(os.Interrupt)
	})
	return wrapper
}

// respondAs puts a helper protocol engine on the endpoint for gen, answering
// the hello-ok with contentHash. It is the fixture the wrong-hash assertion
// needs: a MISMATCHED BINARY at the install path hashes to a different
// generation and is refused before any handshake begins, so it proves nothing
// about the handshake. This does: the socket is the right one, the protocol is
// the real one, and only the hash is wrong.
func respondAs(t *testing.T, dir string, gen proto.GenerationID, contentHash string) {
	t.Helper()
	ln, err := endpoint.Listen(dir, gen)
	if err != nil {
		t.Fatalf("listen on the endpoint: %v", err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = endpoint.Serve(ctx, ln, func(conn net.Conn) {
			h := host.New(conn, conn, contentHash, "instance-under-test", discardLog())
			_ = h.Serve(ctx)
		})
	}()
	t.Cleanup(func() {
		stop()
		<-done
	})
}

// respondWithGarbage accepts on the endpoint, says something that is not our
// protocol at all, and STAYS CONNECTED.
//
// Staying is what makes the fixture prove one thing at a time. A responder
// that hangs up immediately produces three different sentences depending on
// which goroutine wins — the pump's ErrNotOurHelper, the carrier's ErrLost, or
// a bare EPIPE on the hello write — because over a socket "the peer answered
// wrong" and "the peer went away" are the same event, observed by two watchers
// and a writer. That ambiguity is real and is reported as a finding; it is not
// what this test is for. A peer that answers and stays is answered by exactly
// one thing: the handshake budget.
func respondWithGarbage(t *testing.T, dir string, gen proto.GenerationID) {
	t.Helper()
	ln, err := endpoint.Listen(dir, gen)
	if err != nil {
		t.Fatalf("listen on the endpoint: %v", err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = endpoint.Serve(ctx, ln, func(conn net.Conn) {
			_, _ = conn.Write([]byte("SSH-2.0-OpenSSH_9.6\r\n"))
			<-ctx.Done()
		})
	}()
	t.Cleanup(func() {
		stop()
		<-done
	})
}

func socketAccepts(t *testing.T, dir string, gen proto.GenerationID) bool {
	t.Helper()
	conn, err := endpoint.Dial(context.Background(), dir, gen)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// --- the install ------------------------------------------------------------

// TestInstallWritesTheContentAddressedLayoutTheRemoteInstallerWrites is L2:
// one installer, two transports. The layout is the D7 one, byte for byte the
// same shape sftp writes on a remote host, because it is the same installer
// with the filesystem as its carrier.
func TestInstallWritesTheContentAddressedLayoutTheRemoteInstallerWrites(t *testing.T) {
	home := helperHome(t)
	payload := []byte("#!/bin/sh\nexit 0\n")

	got, err := local.Install(context.Background(), newBytesSource(payload), home)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	p := local.Platform()
	wantDir := filepath.Join(home, ".nocx", "helper",
		proto.Version+"-"+p.GOOS+"-"+p.GOARCH+"-"+hashOf(payload))
	if got.Binary != filepath.Join(wantDir, "nocx-helper") {
		t.Fatalf("installed at %s, want %s", got.Binary, filepath.Join(wantDir, "nocx-helper"))
	}
	if string(got.Generation) != hashOf(payload) {
		t.Fatalf("generation = %s, want the content hash %s", got.Generation, hashOf(payload))
	}

	// The bytes are the artifact's, decompressed (D20), and the modes are the
	// installer's rather than the umask's.
	data, err := os.ReadFile(got.Binary) // #nosec G304 — this test installed it
	if err != nil {
		t.Fatalf("read the installed helper: %v", err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatal("the installed bytes are not the artifact's")
	}
	info, err := os.Lstat(got.Binary)
	if err != nil {
		t.Fatalf("stat the installed helper: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("the installed helper is mode %v, want 0700", info.Mode().Perm())
	}
	dirInfo, err := os.Lstat(wantDir)
	if err != nil {
		t.Fatalf("stat the install directory: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("the install directory is mode %v, want 0700", dirInfo.Mode().Perm())
	}
	if _, err := os.Lstat(filepath.Join(wantDir, ".install-complete")); err != nil {
		t.Fatalf("the completed install carries no marker: %v", err)
	}
}

// TestASecondInstallOfTheSameGenerationWritesNothing — the acceptance
// criterion's no-op. A complete directory is REUSED: nothing is decompressed,
// nothing is rewritten, and the file that was there is the file that stays.
func TestASecondInstallOfTheSameGenerationWritesNothing(t *testing.T) {
	home := helperHome(t)
	src := newBytesSource([]byte("#!/bin/sh\nexit 0\n"))

	first, err := local.Install(context.Background(), src, home)
	if err != nil {
		t.Fatalf("first Install: %v", err)
	}
	before, err := os.Lstat(first.Binary)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	second, err := local.Install(context.Background(), src, home)
	if err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if second != first {
		t.Fatalf("the second install answered %+v, want %+v", second, first)
	}
	after, err := os.Lstat(first.Binary)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("the second install rewrote a complete directory")
	}
	// And it left no temporary behind: a directory holds the binary and the
	// marker, and nothing else this package wrote.
	entries, err := os.ReadDir(filepath.Dir(first.Binary))
	if err != nil {
		t.Fatalf("read the install directory: %v", err)
	}
	if len(entries) != 2 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("the install directory holds %v, want the binary and the marker", names)
	}
}

// TestAnInstallThatCannotWriteLeavesNothingThatLooksComplete is the local
// carrier's own failure boundary: the filesystem refusing the write. What must
// be true afterwards is the same thing that must be true after an interrupted
// upload — no complete-looking directory — and the paired half is that on a
// machine that CAN write, the very next attempt succeeds.
func TestAnInstallThatCannotWriteLeavesNothingThatLooksComplete(t *testing.T) {
	home := helperHome(t)
	payload := []byte("#!/bin/sh\nexit 0\n")
	root := filepath.Join(home, ".nocx", "helper")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("prepare the helper root: %v", err)
	}
	// The read-only directory is this fault injection and nothing else: the
	// install root is the developer's own, and the modes go back before the
	// test ends.
	if err := os.Chmod(root, 0o500); err != nil { //nolint:gosec // a directory, and the fault under test
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) }) //nolint:gosec // restoring the directory's own mode

	if _, err := local.Install(context.Background(), newBytesSource(payload), home); err == nil {
		t.Fatal("an install into a directory it may not write reported success")
	}
	p := local.Platform()
	dir := filepath.Join(root, proto.Version+"-"+p.GOOS+"-"+p.GOARCH+"-"+hashOf(payload))
	if _, err := os.Lstat(filepath.Join(dir, ".install-complete")); err == nil {
		t.Fatal("a failed install left a directory that looks complete")
	}

	// Healed: the same call on a machine that can write installs.
	if err := os.Chmod(root, 0o700); err != nil { //nolint:gosec // a directory, restored to what deploy writes
		t.Fatalf("chmod: %v", err)
	}
	got, err := local.Install(context.Background(), newBytesSource(payload), home)
	if err != nil {
		t.Fatalf("Install after the permission was restored: %v", err)
	}
	if _, err := os.Lstat(got.Binary); err != nil {
		t.Fatalf("the recovered install wrote no binary: %v", err)
	}
}

// TestInstallRefusesAPlatformWithNoArtifact — the artifact-selection boundary,
// the first of the dozen. It leaves the filesystem untouched, because nothing
// has been written when it fires.
func TestInstallRefusesAPlatformWithNoArtifact(t *testing.T) {
	home := helperHome(t)
	src := bytesSource{err: deploy.ErrUnsupportedPlatform}

	_, err := local.Install(context.Background(), src, home)
	if !errors.Is(err, deploy.ErrUnsupportedPlatform) {
		t.Fatalf("Install = %v, want ErrUnsupportedPlatform", err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".nocx", "helper")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused platform created %s", filepath.Join(home, ".nocx", "helper"))
	}
}

// TestInstallStopsOnACancelledContext — the context boundary. A cancelled
// install stops rather than ploughing on, and it writes nothing.
func TestInstallStopsOnACancelledContext(t *testing.T) {
	home := helperHome(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := local.Install(ctx, newBytesSource([]byte("payload")), home)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Install = %v, want context.Canceled", err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".nocx", "helper")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a cancelled install wrote into the helper root")
	}
}

// --- the carrier ------------------------------------------------------------

// TestTheLocalHandshakeRunsAgainstADaemonOpenStarted is L1 end to end on this
// machine: the artifact is installed, the daemon is started from what was
// installed, and the SHIPPED client completes the same hello / sentinel /
// hello-ok it completes over ssh — over a socket instead of an exec lane. The
// instance id proves the hello-ok was read and verified; the inventory proves
// the session service is reachable through the carrier and not merely that
// something answered.
func TestTheLocalHandshakeRunsAgainstADaemonOpenStarted(t *testing.T) {
	home := helperHome(t)
	installed, err := local.Install(context.Background(), helperSource(t), home)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	cfg := local.Config{
		Dir:        endpoint.Dir(home),
		Generation: installed.Generation,
		Binary:     killableStarter(t, home, installed.Binary),
		Log:        discardLog(),
	}
	c, err := local.Open(t.Context(), cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = c.Close() }()

	if c.InstanceID() == "" {
		t.Fatal("the client carries no instance id: the hello-ok was never verified")
	}
	var inv proto.SessionsResult
	if callErr := c.Call(t.Context(), proto.ServiceSession, proto.OpSessions, proto.SessionsParams{}, &inv); callErr != nil {
		t.Fatalf("the session service is not reachable through the local carrier: %v", callErr)
	}
	if len(inv.Sessions) != 0 {
		t.Fatalf("a fresh daemon reports %d sessions", len(inv.Sessions))
	}

	// D12 is same-UID trust: a second coordinator reaches the same daemon, and
	// it starts nothing — the endpoint is already served.
	second, err := local.Open(t.Context(), cfg)
	if err != nil {
		t.Fatalf("the second carrier could not reach the daemon: %v", err)
	}
	defer func() { _ = second.Close() }()
	if callErr := second.Call(t.Context(), proto.ServiceSession, proto.OpSessions, proto.SessionsParams{}, &inv); callErr != nil {
		t.Fatalf("the second carrier cannot ask: %v", callErr)
	}
}

// TestASocketAnsweringWithAWrongContentHashIsRefusedAndTheEndpointIsLeftAlone
// is assertion 5 of the design, with the fixture it names: the socket is the
// expected generation's, the protocol is ours, and only the content hash is
// wrong. That is the only way to prove the handshake RAN — a mismatched binary
// at the install path hashes to a different generation and never gets this
// far.
//
// And the second half is the rule that keeps a probe from destroying somebody
// else's sessions: nobody unlinks a live endpoint because its protocol answer
// was unexpected. What answered stays UNKNOWN.
func TestASocketAnsweringWithAWrongContentHashIsRefusedAndTheEndpointIsLeftAlone(t *testing.T) {
	home := helperHome(t)
	dir := endpoint.Dir(home)
	gen := proto.GenerationID(hashOf([]byte("the generation we installed")))
	other := hashOf([]byte("some other build entirely"))
	respondAs(t, dir, gen, other)

	_, err := local.Open(t.Context(), local.Config{Dir: dir, Generation: gen, Log: discardLog()})
	if !errors.Is(err, client.ErrHashMismatch) {
		t.Fatalf("Open = %v, want ErrHashMismatch", err)
	}
	if !strings.Contains(err.Error(), other) || !strings.Contains(err.Error(), string(gen)) {
		t.Fatalf("the refusal does not name the mismatch: %v", err)
	}
	if !socketAccepts(t, dir, gen) {
		t.Fatal("a refused handshake unlinked a live endpoint")
	}
}

// TestASocketThatIsNotOurHelperStaysUnknown — the same rule, one boundary
// further out: something is listening on our path and it is not us at all. It
// is refused, and it is LEFT EXACTLY WHERE IT WAS.
//
// The budget is short because a short one cannot produce a wrong answer here:
// the peer never sends a sentinel, so no machine is slow enough to turn a
// refusal into a pass, and no machine is fast enough to turn a pass into a
// refusal. It is not a wait on a duration standing in for an observation —
// the timeout IS the observation, and it is the mechanism under test.
func TestASocketThatIsNotOurHelperStaysUnknown(t *testing.T) {
	home := helperHome(t)
	dir := endpoint.Dir(home)
	gen := proto.GenerationID(hashOf([]byte("a generation nobody serves")))
	respondWithGarbage(t, dir, gen)

	_, err := local.Open(t.Context(), local.Config{
		Dir: dir, Generation: gen, SentinelTTL: 250 * time.Millisecond, Log: discardLog(),
	})
	if !errors.Is(err, client.ErrSentinelTimeout) {
		t.Fatalf("Open = %v, want ErrSentinelTimeout", err)
	}
	if !socketAccepts(t, dir, gen) {
		t.Fatal("an unrecognised answer cost somebody their endpoint")
	}
}

// TestOpenAnswersNoEndpointWhenNothingIsServing — an ANSWER about this
// generation's socket, never a verdict about a session (D5). A carrier that
// may not start one says so and creates nothing.
func TestOpenAnswersNoEndpointWhenNothingIsServing(t *testing.T) {
	home := helperHome(t)
	dir := endpoint.Dir(home)
	gen := proto.GenerationID(hashOf([]byte("nothing serves this")))

	_, err := local.Open(t.Context(), local.Config{Dir: dir, Generation: gen, Log: discardLog()})
	if !errors.Is(err, endpoint.ErrNoEndpoint) {
		t.Fatalf("Open = %v, want ErrNoEndpoint", err)
	}
	path, perr := endpoint.Path(dir, gen)
	if perr != nil {
		t.Fatalf("Path: %v", perr)
	}
	if _, serr := os.Lstat(path); serr == nil {
		t.Fatalf("a dial that found nothing created %s", path)
	}
}

// TestAStartThatFailsLeavesTheInstallReusableAndClaimsNoDaemon is the first of
// the three holes this bead owes an answer to: the install succeeded and the
// process did not start.
//
// The install stays reusable because its completeness is a claim about BYTES
// and never about liveness — no pid file, no lock, nothing in the layout that
// names a process. And nothing claims a daemon exists, because the only thing
// that ever claims one is the socket, and the socket is created by the daemon
// itself when it binds. A helper that never got that far leaves an empty run
// directory, which is not a claim: both sides create it, including a
// coordinator that only ever dials.
func TestAStartThatFailsLeavesTheInstallReusableAndClaimsNoDaemon(t *testing.T) {
	home := helperHome(t)
	src := newBytesSource([]byte("#!/bin/sh\nexit 3\n"))
	installed, err := local.Install(context.Background(), src, home)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	dir := endpoint.Dir(home)

	_, err = local.Open(t.Context(), local.Config{
		Dir: dir, Generation: installed.Generation, Binary: installed.Binary, Log: discardLog(),
	})
	if err == nil {
		t.Fatal("Open returned a client for a helper that never served")
	}

	// The install is reusable: the next attempt reads what is there rather
	// than reinstalling it.
	before, err := os.Lstat(installed.Binary)
	if err != nil {
		t.Fatalf("stat the installed helper: %v", err)
	}
	again, err := local.Install(context.Background(), src, home)
	if err != nil {
		t.Fatalf("the install did not survive a failed start: %v", err)
	}
	if again != installed {
		t.Fatalf("the second install answered %+v, want %+v", again, installed)
	}
	after, err := os.Lstat(installed.Binary)
	if err != nil {
		t.Fatalf("stat the installed helper: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("a failed start made the next install rewrite a complete directory")
	}

	// And nothing claims a daemon: no socket, and no lock of any other name.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the run directory: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("a helper that never served left %v in the run directory", names)
	}
}
