package app

// The consent resolver (remote-helper design D8): what the helper selection
// may do for one machine at one git.open. The 2026-08-10 footprint-consent
// design wrote the relay arm of the auto ladder as "a suitable binary
// exists for that platform" — forward structure before any helper existed;
// the day one ships, that arm becomes true everywhere, and every user would
// be asked, on every new machine, about a feature they never reached for.
// D8 adds the second condition: auto resolves to relay only when a surface
// on that connection has asked for the helper.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/storage"
)

// machineWithNoStoredAnswer is a machine whose host key has no stored
// relay-tier answer and whose effective mode is the hardcoded auto default.
var machineWithNoStoredAnswer = Machine{Fingerprint: "SHA256:unanswered"}

var (
	grantedMachine = Machine{Fingerprint: "SHA256:granted"}
	explicitScript = Machine{Fingerprint: "SHA256:script", Mode: profile.DesiredScript}
	explicitRaw    = Machine{Fingerprint: "SHA256:raw", Mode: profile.DesiredRaw}
	explicitRelay  = Machine{Fingerprint: "SHA256:relay", Mode: profile.DesiredRelay}
)

// seedGrantedDocument writes a version-1 consent document carrying a grant
// for fingerprint — the exact shape the accept-write path (nocx-1xxa's
// consent-prompt RPC) persists. This bead deliberately owns no writer for
// it; tests that need a granted store seed the document the way that caller
// will, and the store must read it unchanged.
func seedGrantedDocument(t *testing.T, dir, fingerprint string) *consent.Store {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"version": 1,
		"answers": map[string]string{fingerprint: string(consent.Granted)},
	})
	if err != nil {
		t.Fatalf("seed document: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "consent.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return consent.NewStore(log.NewSlogAdapter(nil), storage.NewDocumentStore(dir), "consent.json")
}

// TestShippingAHelperDoesNotOptEveryMachineIn is the stress test's finding
// as an assertion: auto's relay arm was written as "a suitable binary
// exists for that platform", which becomes true everywhere the day we ship
// one.
func TestShippingAHelperDoesNotOptEveryMachineIn(t *testing.T) {
	r := newResolver(withHelperArtifactAvailable(true), withHelperRequested(false))
	if got := r.Resolve(machineWithNoStoredAnswer); got == DesiredRelay {
		t.Fatal("auto must not reach relay for a connection nothing asked the helper for")
	}
}

// TestResolverLadder pins the whole decision table of D8. Every case names
// what the user is shown, not how the code routes.
func TestResolverLadder(t *testing.T) {
	store := seedGrantedDocument(t, t.TempDir(), grantedMachine.Fingerprint)
	cases := []struct {
		name string
		opts []option
		m    Machine
		want Outcome
	}{
		{
			name: "auto with no stored answer, no surface asked: nothing at all",
			opts: []option{withHelperArtifactAvailable(true), withHelperRequested(false)},
			m:    machineWithNoStoredAnswer,
			want: Refused,
		},
		{
			name: "auto with no stored answer, surface asked: the ask",
			opts: []option{withHelperArtifactAvailable(true), withHelperRequested(true)},
			m:    machineWithNoStoredAnswer,
			want: ConsentRequired,
		},
		{
			name: "auto with a stored grant, surface asked: relay",
			opts: []option{withHelperArtifactAvailable(true), withHelperRequested(true), withStore(store)},
			m:    grantedMachine,
			want: DesiredRelay,
		},
		{
			// D8's "script is an answer, not a gap", assertable only since
			// ADR-0033 gave silence its own value: while script also carried
			// every unconfigured connection, refusing here would have refused
			// everyone. The refusal is not a dead end — refusedHelperReason
			// names the modes that do offer the helper.
			name: "explicit script: an answer, so neither the ask nor an upgrade",
			opts: []option{withHelperArtifactAvailable(true), withHelperRequested(true)},
			m:    explicitScript,
			want: Refused,
		},
		{
			// The same machine at explicit auto: auto IS the unanswered
			// state, so it is askable exactly as silence is. This row and the
			// one above are the whole difference between the two values.
			name: "explicit auto: the same as silence — askable",
			opts: []option{withHelperArtifactAvailable(true), withHelperRequested(true)},
			m:    Machine{Fingerprint: "SHA256:auto", Mode: profile.DesiredAuto},
			want: ConsentRequired,
		},
		{
			name: "explicit raw: nothing is written and nothing is asked",
			opts: []option{withHelperArtifactAvailable(true), withHelperRequested(true)},
			m:    explicitRaw,
			want: Refused,
		},
		{
			name: "explicit relay: the explicit choice is the consent, even without a surface ask",
			opts: []option{withHelperRequested(false)},
			m:    explicitRelay,
			want: DesiredRelay,
		},
		{
			name: "no artifact for the platform: nothing to offer",
			opts: []option{withHelperRequested(true)},
			m:    machineWithNoStoredAnswer,
			want: Refused,
		},
		{
			name: "nothing known about the helper at all: fail closed",
			opts: nil,
			m:    machineWithNoStoredAnswer,
			want: Refused,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newResolver(tc.opts...)
			if got := r.Resolve(tc.m); got != tc.want {
				t.Errorf("Resolve = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestResolverEmptyFingerprintNeverGrants: a machine whose host key was not
// captured ("" — a stub channel, a session that never dialed) must never
// resolve to relay on the strength of a shared empty key — even when a
// foreign document carries an answer under "" (the store drops it).
func TestResolverEmptyFingerprintNeverGrants(t *testing.T) {
	store := seedGrantedDocument(t, t.TempDir(), "")
	r := newResolver(withStore(store), withHelperArtifactAvailable(true), withHelperRequested(true))
	if got := r.Resolve(Machine{Fingerprint: ""}); got == DesiredRelay {
		t.Fatal("an empty host-key fingerprint must never resolve to relay")
	}
}

// TestResolverGrantSurvivesStoreReopen is the accept's durable half: the
// grant the panel's RPC persisted is still there for the next git.open,
// even across a store reconstruction.
func TestResolverGrantSurvivesStoreReopen(t *testing.T) {
	dir := t.TempDir()
	seedGrantedDocument(t, dir, grantedMachine.Fingerprint)
	again := consent.NewStore(log.NewSlogAdapter(nil), storage.NewDocumentStore(dir), "consent.json")
	r := newResolver(withStore(again), withHelperArtifactAvailable(true), withHelperRequested(true))
	if got := r.Resolve(grantedMachine); got != DesiredRelay {
		t.Errorf("Resolve after store reopen = %q, want relay — the grant must persist", got)
	}
}
