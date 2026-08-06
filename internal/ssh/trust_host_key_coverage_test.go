package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// These tests are about which known_hosts entries a trust write must stop from
// verifying the rejected key, and which it must leave alone (nocx-9224).
//
// They assert through knownhosts itself rather than by reading the file back
// as text. A test that only checks the bytes proves we wrote what we meant to
// write; building the verification callback over the result and asking it
// which keys pass proves the file MEANS what we meant — which is the whole
// defect, since the trust write and the verifier used to disagree about what
// a line covers.

func newTestHostKey(t *testing.T) gossh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519: %v", err)
	}
	key, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("NewPublicKey: %v", err)
	}
	return key
}

// newTestRSAHostKey is the second algorithm in the cross-algorithm test —
// 2048 bits because the test only needs a key of a different type, not a
// strong one.
func newTestRSAHostKey(t *testing.T) gossh.PublicKey {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa: %v", err)
	}
	key, err := gossh.NewPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("NewPublicKey: %v", err)
	}
	return key
}

// verifies asks the real verification path whether key is accepted for addr
// against the file as it now stands.
func verifies(t *testing.T, path, addr string, key gossh.PublicKey) bool {
	t.Helper()
	cb, err := knownhosts.New(path)
	if err != nil {
		t.Fatalf("knownhosts.New(%s): %v", path, err)
	}
	return cb(addr, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 22}, key) == nil
}

func writeHostsFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
}

func readHostsFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // a path this test just created under t.TempDir()
	if err != nil {
		t.Fatalf("read known_hosts: %v", err)
	}
	return string(b)
}

// A wildcard line covers many hosts. Accepting a changed key for one of them
// must stop that line covering THAT host and must not disturb the others:
// deleting it would silently revoke trust for every sibling the pattern was
// written to cover.
func TestTrustHostKey_WildcardLineIsNarrowedNotDeleted(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	old, accepted := newTestHostKey(t), newTestHostKey(t)
	writeHostsFile(t, kh, knownhosts.Line([]string{"*.example.com"}, old)+"\n")

	client := newTrustClient(t, kh)
	if _, err := client.TrustHostKey("db.example.com:22", accepted.Marshal()); err != nil {
		t.Fatalf("TrustHostKey: %v", err)
	}

	if verifies(t, kh, "db.example.com:22", old) {
		t.Error("the rejected key still verifies for the host it was rejected for")
	}
	if !verifies(t, kh, "db.example.com:22", accepted) {
		t.Error("the accepted key does not verify")
	}
	if !verifies(t, kh, "api.example.com:22", old) {
		t.Error("a sibling host under the same wildcard lost its trust")
	}
}

// The negation has to be written in the form the matcher compares against:
// port is matched exactly, so a bracketed address needs a bracketed negation.
func TestTrustHostKey_WildcardNarrowedAtNonDefaultPort(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	old, accepted := newTestHostKey(t), newTestHostKey(t)
	writeHostsFile(t, kh,
		knownhosts.Line([]string{"[*.example.com]:2222"}, old)+"\n"+
			knownhosts.Line([]string{"*.example.com"}, old)+"\n")

	client := newTrustClient(t, kh)
	if _, err := client.TrustHostKey("db.example.com:2222", accepted.Marshal()); err != nil {
		t.Fatalf("TrustHostKey: %v", err)
	}

	if verifies(t, kh, "db.example.com:2222", old) {
		t.Error("the rejected key still verifies on the port it was rejected on")
	}
	if !verifies(t, kh, "db.example.com:2222", accepted) {
		t.Error("the accepted key does not verify")
	}
	// The port-22 wildcard never covered db.example.com:2222 and is nobody's
	// business here.
	if !verifies(t, kh, "db.example.com:22", old) {
		t.Error("a wildcard at a different port was disturbed")
	}
}

// A line naming several hosts exactly is narrowed too. Deleting it would take
// web.example.com's trust with it.
func TestTrustHostKey_MultiHostLineIsNarrowedNotDeleted(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	old, accepted := newTestHostKey(t), newTestHostKey(t)
	writeHostsFile(t, kh,
		knownhosts.Line([]string{"db.example.com", "web.example.com"}, old)+"\n")

	client := newTrustClient(t, kh)
	if _, err := client.TrustHostKey("db.example.com:22", accepted.Marshal()); err != nil {
		t.Fatalf("TrustHostKey: %v", err)
	}

	if verifies(t, kh, "db.example.com:22", old) {
		t.Error("the rejected key still verifies")
	}
	if !verifies(t, kh, "web.example.com:22", old) {
		t.Error("a host named on the same line lost its trust")
	}
}

// A line that names only this host is removed rather than negated: leaving
// `db.example.com,!db.example.com` behind would be dead text in the user's
// file.
func TestTrustHostKey_SingleHostLineIsRemoved(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	old, accepted := newTestHostKey(t), newTestHostKey(t)
	writeHostsFile(t, kh, knownhosts.Line([]string{"db.example.com"}, old)+"\n")

	client := newTrustClient(t, kh)
	if _, err := client.TrustHostKey("db.example.com:22", accepted.Marshal()); err != nil {
		t.Fatalf("TrustHostKey: %v", err)
	}

	if got := readHostsFile(t, kh); strings.Contains(got, "!") {
		t.Errorf("a line naming only this host should be removed, not negated:\n%s", got)
	}
	if verifies(t, kh, "db.example.com:22", old) {
		t.Error("the rejected key still verifies")
	}
}

// A hashed entry names exactly one host, so it is removed like any other
// single-host line — and a hashed entry for a different host is not touched.
func TestTrustHostKey_HashedEntryForTheHostIsRemoved(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	old, other, accepted := newTestHostKey(t), newTestHostKey(t), newTestHostKey(t)
	writeHostsFile(t, kh,
		knownhosts.Line([]string{knownhosts.HashHostname("db.example.com")}, old)+"\n"+
			knownhosts.Line([]string{knownhosts.HashHostname("api.example.com")}, other)+"\n")

	client := newTrustClient(t, kh)
	if _, err := client.TrustHostKey("db.example.com:22", accepted.Marshal()); err != nil {
		t.Fatalf("TrustHostKey: %v", err)
	}

	if verifies(t, kh, "db.example.com:22", old) {
		t.Error("the rejected key still verifies behind its hashed entry")
	}
	if !verifies(t, kh, "api.example.com:22", other) {
		t.Error("another host's hashed entry was disturbed")
	}
}

// A rejected host identity must stop being usable, not merely stop being
// usable under the one algorithm the server happened to present. Otherwise
// whoever holds the old key of another algorithm still passes — the same
// attack the mismatch warning exists to stop.
func TestTrustHostKey_RevokesEveryAlgorithmForTheHost(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	oldEd, accepted := newTestHostKey(t), newTestHostKey(t)
	oldRSA := newTestRSAHostKey(t)
	writeHostsFile(t, kh,
		knownhosts.Line([]string{"db.example.com"}, oldEd)+"\n"+
			knownhosts.Line([]string{"db.example.com"}, oldRSA)+"\n")

	client := newTrustClient(t, kh)
	if _, err := client.TrustHostKey("db.example.com:22", accepted.Marshal()); err != nil {
		t.Fatalf("TrustHostKey: %v", err)
	}

	if verifies(t, kh, "db.example.com:22", oldRSA) {
		t.Error("the host's old key of another algorithm still verifies")
	}
	if !verifies(t, kh, "db.example.com:22", accepted) {
		t.Error("the accepted key does not verify")
	}
}

// @revoked is a global statement about a key, not a statement about a host:
// knownhosts indexes it by key and ignores the host field when checking. A
// trust write that deletes one re-enables a key somebody deliberately banned,
// everywhere.
func TestTrustHostKey_RevokedLineSurvivesByteForByte(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	banned, old, accepted := newTestHostKey(t), newTestHostKey(t), newTestHostKey(t)
	revoked := "@revoked " + knownhosts.Line([]string{"db.example.com"}, banned)
	writeHostsFile(t, kh,
		revoked+"\n"+knownhosts.Line([]string{"db.example.com"}, old)+"\n")

	client := newTrustClient(t, kh)
	if _, err := client.TrustHostKey("db.example.com:22", accepted.Marshal()); err != nil {
		t.Fatalf("TrustHostKey: %v", err)
	}

	if !strings.Contains(readHostsFile(t, kh), revoked) {
		t.Fatalf("the @revoked line was rewritten or removed:\n%s", readHostsFile(t, kh))
	}
	if verifies(t, kh, "db.example.com:22", banned) {
		t.Error("a deliberately revoked key became valid again")
	}
}

// @cert-authority authorises host certificates. It is a different trust
// mechanism from a raw host key and is not what the user rejected.
func TestTrustHostKey_CertAuthorityLineSurvivesByteForByte(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	ca, old, accepted := newTestHostKey(t), newTestHostKey(t), newTestHostKey(t)
	authority := "@cert-authority " + knownhosts.Line([]string{"*.example.com"}, ca)
	writeHostsFile(t, kh,
		authority+"\n"+knownhosts.Line([]string{"db.example.com"}, old)+"\n")

	client := newTrustClient(t, kh)
	if _, err := client.TrustHostKey("db.example.com:22", accepted.Marshal()); err != nil {
		t.Fatalf("TrustHostKey: %v", err)
	}

	if !strings.Contains(readHostsFile(t, kh), authority) {
		t.Errorf("the @cert-authority line was rewritten or removed:\n%s", readHostsFile(t, kh))
	}
}

// Comments, blank lines and unrelated hosts are the user's file and survive a
// rewrite untouched.
func TestTrustHostKey_UnrelatedContentSurvives(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	old, other, accepted := newTestHostKey(t), newTestHostKey(t), newTestHostKey(t)
	content := "# my hosts\n\n" +
		knownhosts.Line([]string{"db.example.com"}, old) + "\n" +
		knownhosts.Line([]string{"unrelated.internal"}, other) + "\n"
	writeHostsFile(t, kh, content)

	client := newTrustClient(t, kh)
	if _, err := client.TrustHostKey("db.example.com:22", accepted.Marshal()); err != nil {
		t.Fatalf("TrustHostKey: %v", err)
	}

	got := readHostsFile(t, kh)
	if !strings.Contains(got, "# my hosts") {
		t.Errorf("the user's comment was dropped:\n%s", got)
	}
	if !verifies(t, kh, "unrelated.internal:22", other) {
		t.Error("an unrelated host lost its trust")
	}
}

// A file the verifier cannot parse is one whose contents we cannot reason
// about. Appending to it would claim a trust we cannot establish, so the
// write refuses and says which line to fix — and changes nothing.
func TestTrustHostKey_UnparseableFileRefusesAndChangesNothing(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	accepted := newTestHostKey(t)
	content := "db.example.com ssh-ed25519 not-base64!!\n"
	writeHostsFile(t, kh, content)

	client := newTrustClient(t, kh)
	_, err := client.TrustHostKey("db.example.com:22", accepted.Marshal())
	if err == nil {
		t.Fatal("expected a refusal for a known_hosts file that does not parse")
	}
	if got := readHostsFile(t, kh); got != content {
		t.Errorf("the file was modified despite the refusal:\nwant %q\ngot  %q", content, got)
	}
}

// known_hosts separates fields with a space OR a tab. Narrowing has to cut the
// host field on either, or a tab-separated line ends up with its key swallowed
// into the pattern list and covers nothing at all.
func TestTrustHostKey_NarrowsATabSeparatedLine(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	old, accepted := newTestHostKey(t), newTestHostKey(t)
	tabbed := strings.Replace(knownhosts.Line([]string{"*.example.com"}, old), " ", "\t", 1)
	writeHostsFile(t, kh, tabbed+"\n")

	client := newTrustClient(t, kh)
	if _, err := client.TrustHostKey("db.example.com:22", accepted.Marshal()); err != nil {
		t.Fatalf("TrustHostKey: %v", err)
	}

	if verifies(t, kh, "db.example.com:22", old) {
		t.Error("the rejected key still verifies")
	}
	if !verifies(t, kh, "api.example.com:22", old) {
		t.Error("a sibling host under the same tab-separated wildcard lost its trust")
	}
}
