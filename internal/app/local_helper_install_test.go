package app

// The composition root's own half of the local helper (L1, L2 of the
// local-helper design): Start installs this machine's generation.
//
// WHY THESE EXIST AT ALL. internal/helper/local proves the installer; these
// prove the SEAM — that Start reaches it, on the home the app actually
// resolves, and that a failure there does not take the backend down with it.
// deadcode can say the path from main() is live and can never say the feature
// is wired, which is the distinction AGENTS.md's rule 2 is about: every other
// test in this package opts the install OUT, so without these two the call
// would be reachable and never once executed.
//
// They opt in, and they move HOME to do it. storagetest.Isolate deliberately
// leaves HOME where it is — the profile directories are not the home — and
// ~/.nocx/helper is resolved from the home, so a test that did not move it
// would be writing into the developer's real one.
//
// IsolateWithHome is safe here for a reason worth stating, because its own
// doc warns against it: a moved HOME leaves macOS `security` with no keychain
// and every system-vault call waiting out its five-second bound. Nothing here
// makes one — newTestApp declares the keystore out of play, so the provider
// answers without touching the Keychain service at all.

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/helper/deploy"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

// fakeArtifacts is an ArtifactSource over bytes this test owns, or a refusal.
// The install semantics are content-independent, so the payload is a few bytes
// rather than a helper.
type fakeArtifacts struct {
	payload []byte
	err     error
}

func (f fakeArtifacts) Artifact(deploy.Platform) ([]byte, string, error) {
	if f.err != nil {
		return nil, "", f.err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(f.payload); err != nil {
		return nil, "", err
	}
	if err := zw.Close(); err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(f.payload)
	return buf.Bytes(), hex.EncodeToString(sum[:]), nil
}

func (f fakeArtifacts) hash() string {
	sum := sha256.Sum256(f.payload)
	return hex.EncodeToString(sum[:])
}

// helperRoot is where the install lands for one home, derived the way the
// installer derives it: version, platform, content hash.
func helperRoot(home string, contentHash string) string {
	return filepath.Join(home, ".nocx", "helper",
		proto.Version+"-"+runtime.GOOS+"-"+runtime.GOARCH+"-"+contentHash)
}

// TestStartInstallsTheLocalGenerationAndTheNextStartReusesIt watches the
// composition root do it, through Start rather than by calling the installer
// itself — a test that called installLocalHelper would prove the function
// works and nothing about whether anything calls it.
//
// The two halves are the two ends of the install's own interval: after the
// first Start the directory is COMPLETE (the marker exists over a binary that
// hashes to the directory's key, which is the whole of what makes an install
// usable), and the second Start over the same home writes nothing, because a
// complete directory is reused.
func TestStartInstallsTheLocalGenerationAndTheNextStartReusesIt(t *testing.T) {
	home := storagetest.IsolateWithHome(t)
	src := fakeArtifacts{payload: []byte("#!/bin/sh\nexit 0\n")}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a, err := newTestApp(t, withLocalHelperArtifacts(src))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if startErr := a.Start(ctx); startErr != nil {
		t.Fatalf("Start: %v", startErr)
	}

	dir := helperRoot(home, src.hash())
	binary := filepath.Join(dir, "nocx-helper")
	installed, err := os.ReadFile(binary) //nolint:gosec // the path is this test's own isolated home
	if err != nil {
		t.Fatalf("Start installed no local generation: %v", err)
	}
	// What makes a directory usable, and therefore what a later dial would be
	// entitled to trust: the marker AND the hash, never the marker alone.
	if _, statErr := os.Lstat(filepath.Join(dir, ".install-complete")); statErr != nil {
		t.Fatalf("the install carries no completeness marker: %v", statErr)
	}
	sum := sha256.Sum256(installed)
	if hex.EncodeToString(sum[:]) != src.hash() {
		t.Fatal("the installed bytes do not hash to the directory's key")
	}
	info, err := os.Lstat(binary)
	if err != nil {
		t.Fatalf("stat the installed helper: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("the installed helper is mode %v, want 0700", info.Mode().Perm())
	}

	// A restart: the first composition root goes away and a second one comes
	// up over the same home, which is quitting and relaunching the app.
	a.Shutdown(ctx)
	a2, err := newTestApp(t, withLocalHelperArtifacts(src))
	if err != nil {
		t.Fatalf("New after restart: %v", err)
	}
	if startErr := a2.Start(ctx); startErr != nil {
		t.Fatalf("Start after restart: %v", startErr)
	}
	defer a2.Shutdown(ctx)

	after, err := os.Lstat(binary)
	if err != nil {
		t.Fatalf("the install did not survive the restart: %v", err)
	}
	if !after.ModTime().Equal(info.ModTime()) {
		t.Fatal("the second start rewrote a complete install")
	}
}

// TestAFailedLocalInstallDoesNotStopTheBackend is ADR-0057 read the right way
// round: there is no Tier A fallback on this machine, and the refusal is
// raised AT THE ACT — when a person tries to open a pane — never at start. A
// backend that refused to come up because a helper artifact was missing would
// take away the terminal, the settings and the vault to protect a feature the
// person has not asked for yet.
//
// So the assertion is that the backend SERVES, and that the failure is said
// once, in the log, where the pane-opening refusal will later come to find it.
func TestAFailedLocalInstallDoesNotStopTheBackend(t *testing.T) {
	home := storagetest.IsolateWithHome(t)
	refused := errors.New("no helper artifact for this platform")
	logPath := filepath.Join(t.TempDir(), "nocx.log")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a, err := newTestApp(t, withLocalHelperArtifacts(fakeArtifacts{err: refused}), WithLogFilePath(logPath))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if startErr := a.Start(ctx); startErr != nil {
		t.Fatalf("a failed helper install stopped the backend: %v", startErr)
	}
	defer a.Shutdown(ctx)

	// Serving, not merely "Start returned nil": the renderer's own socket
	// answers a real method.
	conn := dialAppWS(t, a)
	defer func() { _ = conn.Close() }()
	if resp := callAppWS(t, conn, "history.query", map[string]any{
		"scope": "directory", "cwd": "/srv", "host": "", "limit": 1,
	}, 1); resp.Error != nil {
		t.Fatalf("the backend does not serve after a failed helper install: %+v", resp.Error)
	}

	// Nothing was installed, and nothing half-installed either: the artifact
	// boundary fires before anything is written.
	if _, statErr := os.Lstat(filepath.Join(home, ".nocx", "helper")); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("a refused artifact created the helper root: %v", statErr)
	}

	// Said ONCE. A start that logged the same failure twice would mean two
	// callers, which is the second owner this design exists to keep out.
	logged, err := os.ReadFile(logPath) //nolint:gosec // the path is this test's own temp dir
	if err != nil {
		t.Fatalf("read the backend log: %v", err)
	}
	if n := strings.Count(string(logged), "the local generation is not installed"); n != 1 {
		t.Fatalf("the failed install is recorded %d times in the log, want 1", n)
	}
	if !strings.Contains(string(logged), refused.Error()) {
		t.Fatal("the log does not carry the concrete reason the install failed")
	}
}
