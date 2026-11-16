package ssh

// The read half of the host record's index: which host-key fingerprints this
// machine already knows for a saved connection's destination. The connection
// editor shows the host's integration method, and the record is keyed by
// fingerprint (ADR-727g63tr decision 1), so the editor's read has to resolve
// the profile the same way a dial does and then ask known_hosts what key that
// destination is stored under.

import (
	"context"
	"os"
	"testing"

	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/shady2k/nocx/internal/log"
)

// knownHostsClient builds a coordinator-side client whose known_hosts is the
// one sshHome gave this test, and whose ~/.ssh/config is a stub (the product
// resolves aliases through `ssh -G`; a test must not shell out).
func knownHostsClient(t *testing.T, resolver *StubConfigResolver) *RealClient {
	t.Helper()
	client, err := NewReal(log.NewSlogAdapter(nil), WithConfigResolver(resolver))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	return client
}

func writeKnownHosts(t *testing.T, path string, key gossh.PublicKey, addr string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(knownhosts.Line([]string{addr}, key)+"\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestKnownHostsFingerprintsForAnswersTheStoredKey: a destination whose key is
// in known_hosts answers that key's fingerprint, which is what the host record
// is looked up by.
func TestKnownHostsFingerprintsForAnswersTheStoredKey(t *testing.T) {
	hosts := sshHome(t)
	key := newTestHostKey(t)
	writeKnownHosts(t, hosts, key, "127.0.0.1:2222")
	client := knownHostsClient(t, NewStubConfigResolver())

	got, err := client.KnownHostsFingerprintsFor(context.Background(), "127.0.0.1", &ConnectConfig{Port: 2222})
	if err != nil {
		t.Fatalf("KnownHostsFingerprintsFor: %v", err)
	}
	if len(got) != 1 || got[0] != gossh.FingerprintSHA256(key) {
		t.Fatalf("fingerprints = %v, want [%s]", got, gossh.FingerprintSHA256(key))
	}
}

// TestKnownHostsFingerprintsForResolvesTheAlias: a saved connection names the
// host a person typed, and the key is stored under the host ssh -G resolved —
// the same answer the dial's own known_hosts lookup uses. A read that looked
// under the alias would report "no known key" for every aliased connection.
func TestKnownHostsFingerprintsForResolvesTheAlias(t *testing.T) {
	hosts := sshHome(t)
	key := newTestHostKey(t)
	writeKnownHosts(t, hosts, key, "10.0.0.42:22")
	resolver := NewStubConfigResolver()
	resolver.AddEntry("myhost", HostConfig{HostName: "10.0.0.42", Port: 22})
	client := knownHostsClient(t, resolver)

	got, err := client.KnownHostsFingerprintsFor(context.Background(), "myhost", &ConnectConfig{})
	if err != nil {
		t.Fatalf("KnownHostsFingerprintsFor: %v", err)
	}
	if len(got) != 1 || got[0] != gossh.FingerprintSHA256(key) {
		t.Fatalf("fingerprints = %v, want the key stored for the resolved host", got)
	}
}

// TestKnownHostsFingerprintsForUsesTheRouteIdentity: a jump-routed destination
// is stored under a route identity, and the read must ask that same identity —
// a direct lookup would answer nothing for every connection reached through a
// bastion.
func TestKnownHostsFingerprintsForUsesTheRouteIdentity(t *testing.T) {
	hosts := sshHome(t)
	key := newTestHostKey(t)
	cfg := &ConnectConfig{JumpHost: "bastion.example", JumpPort: 22}
	writeKnownHosts(t, hosts, key, knownHostsTargetAddr("10.0.0.42:22", cfg))
	resolver := NewStubConfigResolver()
	resolver.AddEntry("target", HostConfig{HostName: "10.0.0.42", Port: 22})
	client := knownHostsClient(t, resolver)

	got, err := client.KnownHostsFingerprintsFor(context.Background(), "target", cfg)
	if err != nil {
		t.Fatalf("KnownHostsFingerprintsFor: %v", err)
	}
	if len(got) != 1 || got[0] != gossh.FingerprintSHA256(key) {
		t.Fatalf("fingerprints = %v, want the route-stored key", got)
	}
}

// TestKnownHostsFingerprintsForAnswersNothingForAnUnknownOrMissingStore: a
// host nobody has trusted has no fingerprint, which is "not answered" — the
// state the editor shows as auto. The missing file is the same answer for the
// same reason: a person who has never accepted a key has nothing recorded.
func TestKnownHostsFingerprintsForAnswersNothingForAnUnknownOrMissingStore(t *testing.T) {
	hosts := sshHome(t)
	key := newTestHostKey(t)
	writeKnownHosts(t, hosts, key, "127.0.0.1:2222")
	client := knownHostsClient(t, NewStubConfigResolver())

	got, err := client.KnownHostsFingerprintsFor(context.Background(), "127.0.0.1", &ConnectConfig{Port: 2200})
	if err != nil || len(got) != 0 {
		t.Fatalf("an unanswered destination answered %v/%v, want no fingerprints", got, err)
	}

	if err := os.Remove(hosts); err != nil {
		t.Fatalf("remove known_hosts: %v", err)
	}
	got, err = client.KnownHostsFingerprintsFor(context.Background(), "127.0.0.1", &ConnectConfig{Port: 2222})
	if err != nil || len(got) != 0 {
		t.Fatalf("a missing known_hosts answered %v/%v, want no fingerprints", got, err)
	}
}
