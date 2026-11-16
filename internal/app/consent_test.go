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
	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/storage"
)

// machineWithNoStoredAnswer is a machine whose host key has no stored
// helper-tier answer and whose host record says nothing — auto, the
// hardcoded default (ADR-0033).
var machineWithNoStoredAnswer = Machine{Fingerprint: "SHA256:unanswered"}

var (
	grantedMachine = Machine{Fingerprint: "SHA256:granted"}
	deniedMachine  = Machine{Fingerprint: "SHA256:denied"}
	explicitScript = Machine{Fingerprint: "SHA256:script", Mode: hostmode.Script}
	explicitRaw    = Machine{Fingerprint: "SHA256:raw", Mode: hostmode.Raw}
	explicitHelper = Machine{Fingerprint: "SHA256:helper", Mode: hostmode.Helper}
)

// seedGrantedDocument writes a version-1 consent document carrying a grant
// for fingerprint — the exact shape the accept-write path (nocx-1xxa's
// consent-prompt RPC) persists. This bead deliberately owns no writer for
// it; tests that need a granted store seed the document the way that caller
// will, and the store must read it unchanged.
func seedGrantedDocument(t *testing.T, dir, fingerprint string) *consent.Store {
	t.Helper()
	return seedAnsweredDocument(t, dir, fingerprint, consent.Granted)
}

// seedDeniedDocument is the same document with the machine's other answer.
// Denied is unreachable from any surface today (nocx-j49sp) and the resolver
// already honours it; ADR-0105 puts the explicit `helper` method through that
// same answer, so the row is asserted rather than assumed.
func seedDeniedDocument(t *testing.T, dir, fingerprint string) *consent.Store {
	t.Helper()
	return seedAnsweredDocument(t, dir, fingerprint, consent.Denied)
}

func seedAnsweredDocument(t *testing.T, dir, fingerprint string, answer consent.Answer) *consent.Store {
	t.Helper()
	return seedAnswersDocument(t, dir, map[string]consent.Answer{fingerprint: answer})
}

// seedAnswersDocument is the same document with one answer per machine, for a
// fixture that plays more than one identity. Since ADR-0105 a helper-mode open
// consults this store, and a fixture's PROBE and its SESSION need not report
// the same fingerprint (fakeProbeConn says "SHA256:fake", fakeRemoteSession
// says "SHA256:test-host"), so both have to be answered by a fixture that opens
// the helper either way.
func seedAnswersDocument(t *testing.T, dir string, answers map[string]consent.Answer) *consent.Store {
	t.Helper()
	encoded := make(map[string]string, len(answers))
	for fingerprint, answer := range answers {
		encoded[fingerprint] = string(answer)
	}
	raw, err := json.Marshal(map[string]any{
		"version": 1,
		"answers": encoded,
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
	denied := seedDeniedDocument(t, t.TempDir(), deniedMachine.Fingerprint)
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
			m:    Machine{Fingerprint: "SHA256:auto", Mode: hostmode.Auto},
			want: ConsentRequired,
		},
		{
			name: "explicit raw: nothing is written and nothing is asked",
			opts: []option{withHelperArtifactAvailable(true)},
			m:    explicitRaw,
			want: Refused,
		},
		{
			// ADR-0105, nocx-1kejz.3. Naming the method is not the
			// machine's permission: a connection set to helper whose
			// machine has never answered is asked at connect, exactly as
			// silence is. Before this row, the arm returned helper for
			// this machine and deployed a binary nobody had been asked
			// about.
			name: "explicit helper with no machine answer: the ask, not a deployment",
			opts: []option{withHelperArtifactAvailable(true)},
			m:    explicitHelper,
			want: ConsentRequired,
		},
		{
			name: "explicit helper on a machine that granted: helper, silently",
			opts: []option{withHelperArtifactAvailable(true), withStore(store)},
			m:    Machine{Fingerprint: grantedMachine.Fingerprint, Mode: hostmode.Helper},
			want: DesiredHelper,
		},
		{
			name: "explicit helper on a machine that declined: refused, never upgraded",
			opts: []option{withHelperArtifactAvailable(true), withStore(denied)},
			m:    Machine{Fingerprint: deniedMachine.Fingerprint, Mode: hostmode.Helper},
			want: Refused,
		},
		{
			// Nothing to offer on this platform: no ask, because there is
			// no question behind it (the same fail-closed arm auto uses).
			name: "explicit helper with no artifact for the platform: refused, never asked",
			opts: nil,
			m:    explicitHelper,
			want: Refused,
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
