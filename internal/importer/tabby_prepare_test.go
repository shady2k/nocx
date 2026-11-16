package importer

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/profile"
	"gopkg.in/yaml.v3"
)

func prepareFixture(t *testing.T, source string) *TabbyImport {
	t.Helper()
	got, err := PrepareTabbyImport([]byte(source), "")
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestPrepareTabbyNativeEncryptedFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/tabby-native-encrypted.yml")
	if err != nil {
		t.Fatal(err)
	}
	got, err := PrepareTabbyImport(data, "native-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Profiles) != 1 || len(got.Groups) != 1 || len(got.Secrets) != 2 {
		t.Fatalf("unexpected result counts: %d %d %d", len(got.Profiles), len(got.Groups), len(got.Secrets))
	}
	p := got.Profiles[0]
	if p.ID != "ssh:encrypted" || p.Group != "g" || p.Options.Host != "encrypted.example" || *p.Options.Port != 2200 || *p.Options.User != "fixture-user" {
		t.Fatalf("wrong effective profile: %+v", p)
	}
	if got.Groups[0].Defaults != nil {
		t.Fatal("native defaults leaked into nocx group")
	}
	if got.Secrets[1].ProfileID != p.ID || string(got.Secrets[1].Value) != `"inline-fixture"` {
		t.Fatal("inline credential lost")
	}
	for _, phrase := range []string{"", "wrong"} {
		if result, err := PrepareTabbyImport(data, phrase); err != ErrDecryptFailed || result != nil {
			t.Fatalf("wrong phrase: result=%v err=%v", result != nil, err)
		}
	}
}

func TestPrepareTabbyDefaultsAndOwningGroupOnly(t *testing.T) {
	got := prepareFixture(t, `
profileDefaults:
  ssh:
    options: {port: 2200, user: global, keepaliveInterval: 9000, agentForward: true, readyTimeout: 12000}
groups:
  - id: parent
    name: Parent
    defaults:
      ssh:
        options: {port: 3300, user: parent, keepaliveInterval: 8000}
  - id: child
    name: Child
    parentGroupId: parent
    defaults:
      ssh:
        options: {user: child, agentForward: false}
profiles:
  - id: ssh:child
    type: ssh
    group: child
    options: {host: child.example, keepaliveInterval: 0, readyTimeout: 0}
  - id: ssh:parent
    type: ssh
    group: parent
    options: {host: parent.example, user: explicit, agentForward: false}
`)
	if got.Groups[1].ParentGroupID != "parent" || got.Groups[0].Defaults != nil || got.Groups[1].Defaults != nil {
		t.Fatalf("wrong group shape: %+v", got.Groups)
	}
	child := got.Profiles[0].Options
	if *child.Port != 2200 || *child.User != "child" || *child.KeepaliveInterval != 0 || *child.ReadyTimeout != 0 || *child.AgentForward {
		t.Fatalf("source precedence lost: %+v", child)
	}
	parent := got.Profiles[1].Options
	if *parent.Port != 3300 || *parent.User != "explicit" || *parent.KeepaliveInterval != 8000 || *parent.AgentForward {
		t.Fatalf("profile overrides lost: %+v", parent)
	}
}

func TestPrepareTabbyProviderDefaults(t *testing.T) {
	got := prepareFixture(t, `profiles: [{id: "ssh:p", type: ssh, options: {host: example}}]`)
	o := got.Profiles[0].Options
	if o.User == nil || *o.User != "root" || o.Port == nil || *o.Port != 22 || o.KeepaliveInterval == nil || *o.KeepaliveInterval != 5000 || o.AgentForward == nil || *o.AgentForward || o.Forwards == nil || len(*o.Forwards) != 0 {
		t.Fatalf("wrong Tabby provider defaults: %+v", o)
	}
}

func TestPrepareTabbyKeysForwardsAndWarnings(t *testing.T) {
	got := prepareFixture(t, `profiles:
  - id: ssh:file
    type: ssh
    options:
      host: example
      privateKeys: [/home/me/.ssh/id_ed25519]
      forwardedPorts:
        - {type: Local, host: 127.0.0.1, port: 8080, targetAddress: "::1", targetPort: 80}
        - {type: Remote, host: 0.0.0.0, port: 9090, targetAddress: localhost, targetPort: 90}
        - {type: Dynamic, host: 127.0.0.1, port: 1080}
      proxyCommand: secret-command-value
      socksProxyHost: secret-proxy-value
      algorithms: {kex: [secret-algorithm-value]}
      x11: true
      keepaliveCountMax: 4
  - id: ssh:vault
    type: ssh
    options: {host: example, privateKeys: ["vault://secret-key-id"]}
  - id: ssh:multiple
    type: ssh
    options: {host: example, privateKeys: [/one, /two]}
`)
	if *got.Profiles[0].Options.KeyPath != "/home/me/.ssh/id_ed25519" {
		t.Fatal("file key not mapped")
	}
	want := []profile.ForwardSpec{{Direction: "local", BindHost: "127.0.0.1", BindPort: 8080, Destination: "[::1]:80"}, {Direction: "remote", BindHost: "0.0.0.0", BindPort: 9090, Destination: "localhost:90"}, {Direction: "dynamic", BindHost: "127.0.0.1", BindPort: 1080}}
	if !reflect.DeepEqual(*got.Profiles[0].Options.Forwards, want) {
		t.Fatalf("forwards: %+v", *got.Profiles[0].Options.Forwards)
	}
	if *got.Profiles[1].Options.KeyPath != "" || *got.Profiles[2].Options.KeyPath != "" {
		t.Fatal("unsupported keys arbitrarily selected")
	}
	fields := map[string]bool{}
	for _, warning := range got.Warnings {
		fields[warning.Field] = true
	}
	for _, field := range []string{"options.privateKeys", "options.proxyCommand", "options.socksProxyHost", "options.algorithms", "options.x11", "options.keepaliveCountMax"} {
		if !fields[field] {
			t.Errorf("missing warning: %s", field)
		}
	}
	wire, _ := json.Marshal(got.Warnings)
	if strings.Contains(string(wire), "secret-") || strings.Contains(string(wire), "/one") {
		t.Fatalf("warning leaked setting values: %s", wire)
	}
}

func TestPrepareTabbyInlineSecretsUseEffectiveEndpointAndProfile(t *testing.T) {
	got := prepareFixture(t, `profileDefaults: {ssh: {options: {port: 2200, user: inherited}}}
profiles:
 - {id: "ssh:a", type: ssh, options: {host: same, password: first}}
 - {id: "ssh:b", type: ssh, options: {host: same, password: second}}
`)
	if len(got.Secrets) != 2 {
		t.Fatal("inline passwords missing")
	}
	for i, secret := range got.Secrets {
		if secret.ProfileID != got.Profiles[i].ID || secret.Type != "ssh:password" {
			t.Fatal("inline password lacks profile scope")
		}
		var key struct {
			Host string
			Port int
			User string
		}
		if err := json.Unmarshal(secret.Key, &key); err != nil {
			t.Fatal(err)
		}
		if key.Host != "same" || key.Port != 2200 || key.User != "inherited" {
			t.Fatalf("key not effective endpoint: %+v", key)
		}
	}
	if string(got.Secrets[0].Value) != `"first"` || string(got.Secrets[1].Value) != `"second"` {
		t.Fatal("same-endpoint passwords conflated")
	}
}

func TestPrepareTabbySecretsOnlyVaultAndRootAuthority(t *testing.T) {
	for _, plaintext := range []string{`{"secrets":[]}`, `{"config":{"profiles":[{"id":"ssh:ignored","type":"ssh","options":{"host":"ignored"}}]},"secrets":[]}`} {
		vault := encryptTestPlaintext(t, plaintext)
		data, err := yaml.Marshal(map[string]any{"vault": vault, "profiles": []any{map[string]any{"id": "ssh:outer", "type": "ssh", "options": map[string]any{"host": "outer"}}}})
		if err != nil {
			t.Fatal(err)
		}
		got, err := PrepareTabbyImport(data, "pw")
		if err != nil || len(got.Profiles) != 1 || got.Profiles[0].ID != "ssh:outer" {
			t.Fatalf("outer configuration replaced: %v", err)
		}
	}
}

func TestPrepareTabbyRejectsInvalidEncryptedConfig(t *testing.T) {
	for _, config := range []string{"", `null`, `[]`, `"secret-value"`, `{"encrypted":true}`, `{"profiles":"secret-value"}`} {
		plaintext := `{"secrets":[]}`
		if config != "" {
			plaintext = `{"config":` + config + `,"secrets":[]}`
		}
		data, err := yaml.Marshal(map[string]any{"encrypted": true, "vault": encryptTestPlaintext(t, plaintext)})
		if err != nil {
			t.Fatal(err)
		}
		got, err := PrepareTabbyImport(data, "pw")
		if err == nil || got != nil || strings.Contains(err.Error(), "secret-value") {
			t.Fatalf("unsafe inner config accepted or disclosed: %v", err)
		}
	}
}

func TestPrepareTabbyRejectsInvalidSource(t *testing.T) {
	for _, source := range []string{
		`profiles: [{type: ssh, options: {host: example}}]`,
		`profiles: [{id: "ssh:p", type: ssh}]`,
		`profiles: [{id: "ssh:p", type: ssh, options: {host: example, port: 0}}]`,
		`profiles: [{id: "ssh:p", type: ssh, options: {host: example, keepaliveInterval: -1}}]`,
		`profiles: [{id: "ssh:p", type: ssh, options: {host: example, auth: invented}}]`,
		`profiles: [{id: "ssh:p", type: ssh, options: {host: example, forwardedPorts: [{type: Local, port: 80, targetPort: 0}]}}]`,
		`profiles: [{id: "ssh:p", type: ssh, options: {host: a}}, {id: "ssh:p", type: ssh, options: {host: b}}]`,
		`groups: [{id: g}, {id: g}]`,
		`groups: [{name: missing}]`,
		"encrypted: true", "[]", "null", "", "profiles: []\n---\nprofiles: []",
	} {
		if got, err := PrepareTabbyImport([]byte(source), ""); err == nil || got != nil {
			t.Errorf("invalid source accepted: %s", source)
		}
	}
}

func TestPrepareTabbyEffectiveOptionsIgnoreNocxDefaults(t *testing.T) {
	got := prepareFixture(t, `
groups:
 - {id: parent, name: Parent, defaults: {ssh: {options: {port: 3300, user: ancestor}}}}
 - {id: child, parentGroupId: parent, name: Child}
profiles:
 - id: ssh:p
   type: ssh
   group: child
   options: {host: example, keepaliveInterval: 0, readyTimeout: 0, agentForward: false, privateKeys: [], forwardedPorts: []}
`)
	effective, err := profile.ResolveEffectiveProfile(got.Profiles[0], got.Groups, profile.SparseSSHOptions{
		Port: profile.Ptr(4400), User: profile.Ptr("nocx-user"), Auth: profile.Ptr(profile.AuthAgent),
		KeepaliveInterval: profile.Ptr(12000), ReadyTimeout: profile.Ptr(13000), AgentForward: profile.Ptr(true),
		JumpHost: profile.Ptr("nocx-jump"), KeyPath: profile.Ptr("/nocx-key"),
	})
	if err != nil {
		t.Fatal(err)
	}
	o := effective.ResolvedOptions
	// Host and forwards are profile-owned, not part of the sparse cascade
	// returned in ResolvedOptions. Assert them on the resolved profile.
	if effective.Profile.Options.Host != "example" || o.Port != 22 || o.User != "root" || o.Auth != profile.AuthAuto ||
		o.KeepaliveInterval != 0 || o.ReadyTimeout != 0 || o.AgentForward || o.JumpHost != "" || o.KeyPath != "" ||
		effective.Profile.Options.Forwards == nil || len(*effective.Profile.Options.Forwards) != 0 {
		t.Fatalf("imported source semantics changed during nocx resolution: %+v", o)
	}
}

func TestNativeSecretsCannotSpoofInlineProfileScope(t *testing.T) {
	vault := encryptTestPlaintext(t, `{"secrets":[{"type":"ssh:password","key":{"host":"example","port":22,"user":"root"},"value":"fixture","ProfileID":"ssh:p"}]}`)
	if _, err := DecryptTabbyVault(vault, "pw"); err != ErrDecryptFailed {
		t.Fatalf("native secret scope accepted: %v", err)
	}
}

func TestPrepareTabbyAllowsDestinationParent(t *testing.T) {
	got := prepareFixture(t, `groups: [{id: child, parentGroupId: existing, name: Child}]
profiles: [{id: "ssh:p", type: ssh, group: child, options: {host: example}}]`)
	if got.Groups[0].ParentGroupID != "existing" {
		t.Fatal("destination-owned group parent lost")
	}
}

func TestPrepareTabbyInheritedSupportedOptionsAndClearing(t *testing.T) {
	got := prepareFixture(t, `
profileDefaults:
 ssh:
  options:
   auth: publicKey
   privateKeys: [/global-key]
   jumpHost: ssh:jump
   forwardedPorts: [{type: Dynamic, host: 127.0.0.1, port: 1080}]
groups:
 - id: g
   defaults:
    ssh:
     options: {privateKeys: [/group-key], agentForward: true}
profiles:
 - {id: "ssh:inherited", type: ssh, group: g, options: {host: inherited}}
 - {id: "ssh:cleared", type: ssh, group: g, options: {host: cleared, auth: null, jumpHost: null, privateKeys: [], forwardedPorts: [], agentForward: false}}
`)
	a, b := got.Profiles[0].Options, got.Profiles[1].Options
	if *a.Auth != profile.AuthPublicKey || *a.KeyPath != "/group-key" || *a.JumpHost != "ssh:jump" || !*a.AgentForward || len(*a.Forwards) != 1 {
		t.Fatalf("supported source defaults lost: %+v", a)
	}
	if *b.Auth != profile.AuthAuto || *b.KeyPath != "" || *b.JumpHost != "" || *b.AgentForward || len(*b.Forwards) != 0 {
		t.Fatalf("explicit clearing lost: %+v", b)
	}
}

func TestPrepareTabbyWarningsIncludeFutureForwardSettings(t *testing.T) {
	got := prepareFixture(t, `profiles: [{id: "ssh:p", type: ssh, options: {host: example, forwardedPorts: [{type: Dynamic, port: 1080, socketPath: sensitive-socket-value}]}}]`)
	if len(got.Warnings) != 1 || got.Warnings[0].Field != "options.forwardedPorts.socketPath" {
		t.Fatalf("unmapped forwarding option not reported: %+v", got.Warnings)
	}
}

func TestPrepareTabbyWarningProfileLabels(t *testing.T) {
	got := prepareFixture(t, `profiles:
 - {id: "ssh:named", type: ssh, name: Production, options: {host: example, x11: true}}
 - {id: "ssh:unnamed", type: ssh, options: {host: example, x11: true}}
`)
	if len(got.Warnings) != 2 || got.Warnings[0].Profile != "Production" || got.Warnings[1].Profile != "ssh:unnamed" {
		t.Fatalf("warnings must identify profiles by name with an ID fallback: %+v", got.Warnings)
	}
}
