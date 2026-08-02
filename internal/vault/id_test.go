package vault

import (
	"regexp"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
)

var idShape = regexp.MustCompile(`^sec:v1:(system|file):[0-9a-f]{32}$`)

func TestMintIDShape(t *testing.T) {
	for _, p := range []ProviderID{ProviderSystem, ProviderFile} {
		id, err := mintID(p)
		if err != nil {
			t.Fatalf("mintID(%s): %v", p, err)
		}
		if !idShape.MatchString(string(id)) {
			t.Fatalf("mintID(%s) = %q, want match %s", p, id, idShape)
		}
	}
}

func TestMintIDIsUnique(t *testing.T) {
	seen := make(map[credential.SecretID]bool, 1000)
	for i := 0; i < 1000; i++ {
		id, err := mintID(ProviderFile)
		if err != nil {
			t.Fatalf("mintID: %v", err)
		}
		if seen[id] {
			t.Fatalf("collision at %d: %q", i, id)
		}
		seen[id] = true
	}
}

func TestParseIDRoundTrip(t *testing.T) {
	id, _ := mintID(ProviderSystem)
	got, err := parseID(id)
	if err != nil {
		t.Fatalf("parseID(%q): %v", id, err)
	}
	if got != ProviderSystem {
		t.Fatalf("parseID(%q) = %q, want %q", id, got, ProviderSystem)
	}
}

// An unregistered tag is syntactically valid. The parser must not decide
// availability — spec §6 invariant 3 requires the reference to be preserved
// and reported, never re-routed.
func TestParseIDAcceptsUnknownProvider(t *testing.T) {
	got, err := parseID("sec:v1:hcp:0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("parseID(unknown provider): %v", err)
	}
	if got != ProviderID("hcp") {
		t.Fatalf("got %q, want hcp", got)
	}
}

func TestParseIDRejectsMalformed(t *testing.T) {
	bad := map[string]credential.SecretID{
		"empty":           "",
		"old format":      "sec:0123456789abcdef0123456789abcdef",
		"wrong prefix":    "key:v1:file:0123456789abcdef0123456789abcdef",
		"wrong version":   "sec:v2:file:0123456789abcdef0123456789abcdef",
		"empty provider":  "sec:v1::0123456789abcdef0123456789abcdef",
		"empty material":  "sec:v1:file:",
		"uppercase hex":   "sec:v1:file:0123456789ABCDEF0123456789abcdef",
		"short hex":       "sec:v1:file:0123456789abcdef",
		"long hex":        "sec:v1:file:0123456789abcdef0123456789abcdef00",
		"extra component": "sec:v1:file:0123456789abcdef0123456789abcdef:x",
		"non-hex":         "sec:v1:file:zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
		"uppercase tag":   "sec:v1:File:0123456789abcdef0123456789abcdef",
	}
	for name, id := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := parseID(id); err == nil {
				t.Fatalf("parseID(%q) succeeded, want error", id)
			}
		})
	}
}
