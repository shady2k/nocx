package ssh

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// newTrustClient builds a RealClient whose known_hosts lives at the given
// path (which may not exist yet).
func newTrustClient(t *testing.T, khPath string) *RealClient {
	t.Helper()
	client, err := NewReal(
		log.NewSlogAdapter(nil),
		WithKnownHostsFile(khPath),
	)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func probeOnce(t *testing.T, client *RealClient, srv *testSSHServer) error {
	t.Helper()
	return client.Probe(
		context.Background(), srv.addr,
		gossh.PublicKeys(srv.userSigner),
		WithUser("test"),
	)
}

// TestTrustHostKey_UnknownKeyAccept_SecondProbeSucceeds is the product test
// for accept-on-first-use: a host that is not in known_hosts fails the probe,
// the user accepts (TrustHostKey), and the very next probe succeeds.
func TestTrustHostKey_UnknownKeyAccept_SecondProbeSucceeds(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	khPath := filepath.Join(t.TempDir(), "known_hosts")
	client := newTrustClient(t, khPath)

	// First contact: no known_hosts file at all.
	err := probeOnce(t, client, srv)
	if err == nil {
		t.Fatal("expected probe to fail for unknown host key, got nil")
	}
	var unknownKey *ErrUnknownHostKey
	if !errors.As(err, &unknownKey) {
		t.Fatalf("expected ErrUnknownHostKey, got %T: %v", err, err)
	}
	if len(unknownKey.Key) == 0 {
		t.Fatal("expected ErrUnknownHostKey to carry the offered key blob")
	}

	// Accept: the user trusts the offered key.
	fp, err := client.TrustHostKey(unknownKey.Addr, unknownKey.Key)
	if err != nil {
		t.Fatalf("TrustHostKey: %v", err)
	}
	if fp == "" {
		t.Fatal("expected a fingerprint back from TrustHostKey")
	}

	// The second probe must now succeed.
	if err := probeOnce(t, client, srv); err != nil {
		t.Fatalf("probe after accept: expected success, got %v", err)
	}
}

// TestTrustHostKey_ChangedKeyAccept_SecondProbeSucceeds is the changed-key
// path: a stored key and the offered key differ, the user explicitly trusts
// the new key, and the next probe succeeds with the new key.
func TestTrustHostKey_ChangedKeyAccept_SecondProbeSucceeds(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	// known_hosts records a DIFFERENT key for the same host.
	wrongKey := generateSigner(t)
	khPath := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{srv.addr}, wrongKey.PublicKey())
	if err := os.WriteFile(khPath, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	client := newTrustClient(t, khPath)

	err := probeOnce(t, client, srv)
	if err == nil {
		t.Fatal("expected probe to fail on key mismatch, got nil")
	}
	var mismatch *ErrHostKeyMismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected ErrHostKeyMismatch, got %T: %v", err, err)
	}
	if mismatch.Expected == "" {
		t.Fatal("expected mismatch error to carry the stored fingerprint")
	}
	if len(mismatch.Key) == 0 {
		t.Fatal("expected mismatch error to carry the offered key blob")
	}

	if _, err := client.TrustHostKey(mismatch.Addr, mismatch.Key); err != nil {
		t.Fatalf("TrustHostKey: %v", err)
	}

	if err := probeOnce(t, client, srv); err != nil {
		t.Fatalf("probe after accepting changed key: expected success, got %v", err)
	}

	// …and the key it REPLACED must stop being trusted. Appending the new
	// line without removing the old one leaves both valid, so a man in the
	// middle holding the old key still passes — which is the whole case the
	// user was answering when they pressed "Trust the new key".
	if lines := knownHostsLinesFor(t, khPath, srv.addr); len(lines) != 1 {
		t.Fatalf("expected exactly one line for %s after replacing its key, got %d:\n%s",
			srv.addr, len(lines), strings.Join(lines, "\n"))
	}
	stale := knownhosts.Line([]string{srv.addr}, wrongKey.PublicKey())
	body, readErr := os.ReadFile(khPath) //nolint:gosec // a path this test just created under t.TempDir()
	if readErr != nil {
		t.Fatalf("read known_hosts: %v", readErr)
	}
	if strings.Contains(string(body), stale) {
		t.Fatal("the replaced key is still in known_hosts: the old key would still be accepted")
	}
}

// knownHostsLinesFor returns the non-comment lines that name addr, hashed or
// not. It answers the question ssh-keygen -R answers: how many keys does this
// file still trust for this host.
func knownHostsLinesFor(t *testing.T, path, addr string) []string {
	t.Helper()
	body, err := os.ReadFile(path) //nolint:gosec // a path this test just created under t.TempDir()
	if err != nil {
		t.Fatalf("read known_hosts: %v", err)
	}
	var out []string
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if lineNamesHost(trimmed, addr) {
			out = append(out, trimmed)
		}
	}
	return out
}

// TestTrustHostKey_CreatesMissingFile0600 pins the mode of a new file.
func TestTrustHostKey_CreatesMissingFile0600(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	khPath := filepath.Join(t.TempDir(), "known_hosts")
	client := newTrustClient(t, khPath)

	key := srv.hostSigner.PublicKey()
	if _, err := client.TrustHostKey(srv.addr, key.Marshal()); err != nil {
		t.Fatalf("TrustHostKey: %v", err)
	}

	info, err := os.Stat(khPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("expected new known_hosts mode 0600, got %o", perm)
	}
}

// TestTrustHostKey_ExistingFileKeepsModeAndAppends pins the two file rules:
// an existing file keeps its mode, and its bytes are never rewritten or
// reordered — the new line is appended after the existing content.
func TestTrustHostKey_ExistingFileKeepsModeAndAppends(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	existing := []string{
		"# nocx trust test — existing line 1",
		"existing.host ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIC7cYlRke1RKYrV9ZnJqRrrT5JxLTuvz1RzVvJxTc3c",
		"# nocx trust test — existing line 3",
	}
	khPath := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(khPath, []byte(strings.Join(existing, "\n")+"\n"), 0o644); err != nil { //nolint:gosec // the point of the test is that a mode we did not choose survives
		t.Fatalf("write known_hosts: %v", err)
	}
	client := newTrustClient(t, khPath)

	key := srv.hostSigner.PublicKey()
	if _, err := client.TrustHostKey(srv.addr, key.Marshal()); err != nil {
		t.Fatalf("TrustHostKey: %v", err)
	}

	info, err := os.Stat(khPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("expected existing mode 0644 preserved, got %o", perm)
	}

	got, err := os.ReadFile(khPath) //nolint:gosec // a path this test just created under t.TempDir()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")
	if len(lines) != len(existing)+1 {
		t.Fatalf("expected %d lines, got %d", len(existing)+1, len(lines))
	}
	for i, want := range existing {
		if lines[i] != want {
			t.Errorf("line %d changed: got %q, want %q", i+1, lines[i], want)
		}
	}
	// The test server listens on a non-22 port, so the appended line carries
	// the bracketed form — the same convention the next probe matches on.
	if !strings.Contains(lines[len(lines)-1], knownhosts.Normalize(srv.addr)) {
		t.Errorf("expected appended line to name %s, got %q", knownhosts.Normalize(srv.addr), lines[len(lines)-1])
	}
}

// TestTrustHostKey_NonDefaultPortWritesBracketedAddr pins the known_hosts
// address convention: host:2222 is stored as [host]:2222, never host:2222.
func TestTrustHostKey_NonDefaultPortWritesBracketedAddr(t *testing.T) {
	client := newTrustClient(t, filepath.Join(t.TempDir(), "known_hosts"))

	key := generateSigner(t).PublicKey()
	if _, err := client.TrustHostKey("db.example.com:2222", key.Marshal()); err != nil {
		t.Fatalf("TrustHostKey: %v", err)
	}

	got, err := os.ReadFile(client.knownHostsFile)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(got), "[db.example.com]:2222") {
		t.Errorf("expected bracketed [db.example.com]:2222 in line, got %q", string(got))
	}
}

// TestTrustHostKey_InvalidKeyWritesNothing pins that a key that does not
// parse is refused and no file is created.
func TestTrustHostKey_InvalidKeyWritesNothing(t *testing.T) {
	khPath := filepath.Join(t.TempDir(), "known_hosts")
	client := newTrustClient(t, khPath)

	_, err := client.TrustHostKey("host.example.com:22", []byte("not-a-key"))
	if err == nil {
		t.Fatal("expected error for invalid key blob, got nil")
	}
	if _, statErr := os.Stat(khPath); !os.IsNotExist(statErr) {
		t.Errorf("expected no file created for invalid key, stat err = %v", statErr)
	}
}
