package app

// The consent resolver (ADR-0034, ADR-0068): what the helper selection may
// do for one machine at one consultation. ConsentRequired is the resolver's
// "no answer yet" outcome; only the connect-time caller may turn it into an
// ask, and every other caller — git.open among them — treats it exactly
// like Refused.

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
// helper-tier answer and whose effective mode is the hardcoded auto default.
var machineWithNoStoredAnswer = Machine{Fingerprint: "SHA256:unanswered"}

var (
	grantedMachine = Machine{Fingerprint: "SHA256:granted"}
	explicitScript = Machine{Fingerprint: "SHA256:script", Mode: profile.DesiredScript}
	explicitRaw    = Machine{Fingerprint: "SHA256:raw", Mode: profile.DesiredRaw}
	explicitHelper = Machine{Fingerprint: "SHA256:helper", Mode: profile.DesiredHelper}
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

// TestResolverLadder pins the whole decision table. Every case names what
// the user is shown, not how the code routes.
func TestResolverLadder(t *testing.T) {
	store := seedGrantedDocument(t, t.TempDir(), grantedMachine.Fingerprint)
	cases := []struct {
		name string
		opts []option
		m    Machine
		want Outcome
	}{
		{
			name: "auto with no stored answer: the ask (the connect-time caller's alone to raise)",
			opts: []option{withHelperArtifactAvailable(true)},
			m:    machineWithNoStoredAnswer,
			want: ConsentRequired,
		},
		{
			name: "auto with a stored grant: helper",
			opts: []option{withHelperArtifactAvailable(true), withStore(store)},
			m:    grantedMachine,
			want: DesiredHelper,
		},
		{
			// "Script is an answer, not a gap", assertable only since
			// ADR-0033 gave silence its own value: while script also carried
			// every unconfigured connection, refusing here would have refused
			// everyone. The refusal is not a dead end — refusedHelperReason
			// names the modes that do offer the helper.
			name: "explicit script: an answer, so neither the ask nor an upgrade",
			opts: []option{withHelperArtifactAvailable(true)},
			m:    explicitScript,
			want: Refused,
		},
		{
			// The same machine at explicit auto: auto IS the unanswered
			// state, so it is askable exactly as silence is. This row and the
			// one above are the whole difference between the two values.
			name: "explicit auto: the same as silence — askable",
			opts: []option{withHelperArtifactAvailable(true)},
			m:    Machine{Fingerprint: "SHA256:auto", Mode: profile.DesiredAuto},
			want: ConsentRequired,
		},
		{
			name: "explicit raw: nothing is written and nothing is asked",
			opts: []option{withHelperArtifactAvailable(true)},
			m:    explicitRaw,
			want: Refused,
		},
		{
			name: "explicit helper: the explicit choice is the consent",
			opts: nil,
			m:    explicitHelper,
			want: DesiredHelper,
		},
		{
			name: "no artifact for the platform: nothing to offer, fail-closed default",
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
// resolve to helper on the strength of a shared empty key — even when a
// foreign document carries an answer under "" (the store drops it).
func TestResolverEmptyFingerprintNeverGrants(t *testing.T) {
	store := seedGrantedDocument(t, t.TempDir(), "")
	r := newResolver(withStore(store), withHelperArtifactAvailable(true))
	if got := r.Resolve(Machine{Fingerprint: ""}); got == DesiredHelper {
		t.Fatal("an empty host-key fingerprint must never resolve to helper")
	}
}

// TestResolverGrantSurvivesStoreReopen is the accept's durable half: the
// grant the panel's RPC persisted is still there for the next git.open,
// even across a store reconstruction.
func TestResolverGrantSurvivesStoreReopen(t *testing.T) {
	dir := t.TempDir()
	seedGrantedDocument(t, dir, grantedMachine.Fingerprint)
	again := consent.NewStore(log.NewSlogAdapter(nil), storage.NewDocumentStore(dir), "consent.json")
	r := newResolver(withStore(again), withHelperArtifactAvailable(true))
	if got := r.Resolve(grantedMachine); got != DesiredHelper {
		t.Errorf("Resolve after store reopen = %q, want helper — the grant must persist", got)
	}
}
