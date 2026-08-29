package app

// The keystore stance is DECLARED, never discovered (design D10).
//
// Reaching the OS keystore is a per-user OS service call, and until
// nocx-o4hg it was what a test got for saying nothing:
// internal/vault/system.Provider's Probe writes and reads a random entry
// under the "nocx" service, so every backend a test started wrote to the
// login keychain of whoever ran the suite. D10 then found the other half of
// the same fact: that write is also what raises "Keychain not found" under a
// $HOME with no keychain, so a coordinator that probes on a headless host
// raises a modal nobody can dismiss.
//
// These are the checks that make the stance a decision instead of an
// inheritance, and that keep it ONE decision.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/vault"
)

// A test that has not said what it means to do about the OS keystore is
// refused, and the refusal names both ways to say it. This is the whole
// mechanism: it fires wherever the composition root is built from a test
// binary, including from a package that does not know this helper exists.
func TestNew_RefusesATestThatHasNotDeclaredItsKeystoreStance(t *testing.T) {
	storagetest.Isolate(t)

	a, err := New(WithLogFilePath(filepath.Join(t.TempDir(), "nocx.log")))
	if err == nil {
		a.Shutdown(context.Background())
		t.Fatal("New() built the app for a test that never said whether it may " +
			"reach the OS keystore; that construction performs a real keyring write")
	}
	for _, want := range []string{"newTestApp", "WithRealSystemKeystore"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not name %q, so the reader is not told how to say it: %v",
				want, err)
		}
	}
}

// The test constructor keeps the keystore out of reach, and the backend says
// so in its own log — the observable a developer has when a keychain dialog
// does or does not appear.
func TestNewTestApp_KeepsTheOSKeystoreOutOfReach(t *testing.T) {
	storagetest.Isolate(t)
	logPath := filepath.Join(t.TempDir(), "nocx.log")

	a, err := newTestApp(t, WithLogFilePath(logPath))
	if err != nil {
		t.Fatalf("newTestApp: %v", err)
	}
	defer a.Shutdown(context.Background())

	b, err := os.ReadFile(logPath) // #nosec G304 — the test's own temp path.
	if err != nil {
		t.Fatalf("read the backend log: %v", err)
	}
	if !bytes.Contains(b, []byte("out of play")) {
		t.Errorf("the startup did not record an excluded keystore, so it "+
			"reached the real one; log:\n%s", b)
	}
	if !bytes.Contains(b, []byte("ready=false")) {
		t.Errorf("the system provider reported ready without a keystore to be "+
			"ready for; log:\n%s", b)
	}
	if !bytes.Contains(b, []byte(vault.ReasonExcluded)) {
		t.Errorf("the provider reported something other than %q, so the product "+
			"is told a machine fact nobody established; log:\n%s", vault.ReasonExcluded, b)
	}
}

// The opt-in is not a flag: a caller that wants the real store states why,
// and an empty reason is refused. Without this the exception is one
// copy-paste away from being the rule again.
func TestNew_RealKeystoreOptInWithoutAReasonIsRefused(t *testing.T) {
	storagetest.Isolate(t)

	a, err := New(WithRealSystemKeystore("  "),
		WithLogFilePath(filepath.Join(t.TempDir(), "nocx.log")))
	if err == nil {
		a.Shutdown(context.Background())
		t.Fatal("New() accepted a real-keystore opt-in with no reason")
	}
	if !strings.Contains(err.Error(), "reason") {
		t.Errorf("refusal does not say a reason is what is missing: %v", err)
	}
}

// ONE DECISION, NOT TWO. Which provider is built and whether the startup
// probe runs come out of the same call, so a build that excluded the
// keystore and a probe that ran anyway — the exact combination that is a
// modal on a headless host — cannot be constructed.
//
// The stance is asserted through decideKeystore rather than by starting a
// backend, because starting one to prove it would be the very keychain write
// this is about. That the probe then succeeds against a working store is
// TestProbeWithFakeKeyring in internal/vault/system.
func TestKeystoreStance(t *testing.T) {
	cases := []struct {
		name      string
		inTest    bool
		opts      []Option
		reachReal bool
		refused   bool
	}{
		{
			name:      "an explicit opt-in with a reason reaches the real store",
			inTest:    true,
			opts:      []Option{WithRealSystemKeystore("asserts the login keychain round trip")},
			reachReal: true,
		},
		{
			name:   "a caller that declares the keystore absent does not reach it",
			inTest: true,
			opts:   []Option{withoutSystemKeystore()},
		},
		{
			name:   "an explicit absent stance still means absent outside a test",
			inTest: false,
			opts:   []Option{withoutSystemKeystore()},
		},
		{
			name:    "a test that says nothing is refused",
			inTest:  true,
			refused: true,
		},
		{
			// Production says nothing and takes the BUILD's stance. This is
			// the case D10 changed: it used to mean "reach the real store",
			// which on a headless coordinator is a modal in front of nobody.
			name:      "production says nothing and takes the build's stance",
			inTest:    false,
			reachReal: buildKeystoreStance == keystoreReal,
		},
	}
	// The zero value is what a caller who passed no keystore option holds,
	// so "said nothing" is only detectable while the two coincide.
	var zero optionSet
	if zero.keystore != keystoreUndeclared {
		t.Fatalf("the zero stance is %v, not keystoreUndeclared: saying nothing "+
			"would then be indistinguishable from declaring something", zero.keystore)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var o optionSet
			for _, opt := range tc.opts {
				opt(&o)
			}
			d, err := decideKeystore(tc.inTest, &o)
			if tc.refused {
				if err == nil {
					t.Fatal("stance accepted, want a refusal")
				}
				return
			}
			if err != nil {
				t.Fatalf("stance refused: %v", err)
			}
			if got := d.stance == keystoreReal; got != tc.reachReal {
				t.Errorf("reaches the real keystore = %v, want %v", got, tc.reachReal)
			}
			// The probe and the provider are the same decision.
			if d.probe != tc.reachReal {
				t.Errorf("probe = %v while the stance says reachReal = %v; a probe "+
					"is a real keystore call and the two may never disagree",
					d.probe, tc.reachReal)
			}
			if d.provider == nil {
				t.Fatal("the decision built no provider")
			}
			// Status is asked only of an out-of-play provider. Asking a real
			// one here would perform the keyring write this whole file exists
			// to keep out of a test run; that the real provider answers a
			// working store is TestProbeWithFakeKeyring in internal/vault/system.
			if !tc.reachReal {
				status := d.provider.Status(context.Background())
				if status.Reason != vault.ReasonExcluded {
					t.Errorf("the provider reports reason %q, want %q for a stance "+
						"that excluded the keystore", status.Reason, vault.ReasonExcluded)
				}
				if status.Ready {
					t.Error("a provider nobody asked reported ready")
				}
			}
			if d.source == "" {
				t.Error("the decision names nobody as its declarer; a keychain " +
					"prompt then leads back to nothing")
			}
		})
	}
}

// THE SINGLE FACT. Changing the build property in its one place changes the
// behaviour, and there is no second place that can disagree: the stance a
// production start resolves to IS buildKeystoreStance, and everything that
// follows — provider, probe — is derived from it in the same call.
func TestKeystoreStance_ProductionIsExactlyTheBuildProperty(t *testing.T) {
	var o optionSet
	d, err := decideKeystore(false, &o)
	if err != nil {
		t.Fatalf("decideKeystore: %v", err)
	}
	if d.stance != buildKeystoreStance {
		t.Fatalf("production resolved to %v, but the build declares %v; the stance "+
			"is then written in two places and they already disagree",
			d.stance, buildKeystoreStance)
	}
	if d.source != buildKeystoreSource {
		t.Errorf("declarer = %q, want the build's own %q", d.source, buildKeystoreSource)
	}
	if want := buildKeystoreStance == keystoreReal; d.probe != want {
		t.Errorf("probe = %v, want %v — derived from the same constant", d.probe, want)
	}
}

// A HOST WITH NO LOGIN KEYCHAIN: the coordinator starts, chooses the file
// provider, and asks the keystore nothing at all.
//
// "Zero modal dialogs" is asserted as zero keyring calls, because the modal
// IS the write: go-keyring execs /usr/bin/security, and a Set under a $HOME
// with no keychain is what raises "Keychain not found". A provider that
// makes no call cannot raise one. The $HOME here contains no keychain, which
// on this Linux host is what an empty directory already is — the macOS
// dialog itself cannot be exercised here, and this is the check that stands
// in for it.
func TestKeystoreStance_AHostWithNoKeychainIsNeverAsked(t *testing.T) {
	storagetest.Isolate(t)
	t.Setenv("HOME", t.TempDir()) // no Library/Keychains under it

	var o optionSet
	d, err := decideKeystore(false, &o)
	if err != nil {
		t.Fatalf("decideKeystore: %v", err)
	}
	if d.stance == keystoreReal {
		t.Skip("this build declares a login session; the headless case is the " +
			"other build of keystore_build_*.go")
	}

	counted := &countingKeyring{}
	prov := newCountedNotInPlayProvider(counted)
	ctx := context.Background()
	if got := prov.Status(ctx).Reason; got != vault.ReasonExcluded {
		t.Errorf("status reason = %q, want %q", got, vault.ReasonExcluded)
	}
	if got := prov.Probe(ctx).Reason; got != vault.ReasonExcluded {
		t.Errorf("probe reason = %q, want %q", got, vault.ReasonExcluded)
	}
	if n := counted.calls(); n != 0 {
		t.Errorf("%d keyring calls reached the OS under a $HOME with no keychain; "+
			"each one of those is the modal D10 is about", n)
	}
}

// And the whole backend starts on such a host, with the file provider as the
// vault's store of record and the exclusion visible in the product — the
// "it starts, and says so" half of the same claim. Read over the real socket,
// because vault.status is what the Vault page reads: a choice that is only in
// a log is the silent degrade AGENTS.md names.
func TestKeystoreStance_TheBackendStartsOnAHostWithNoKeychain(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // startedApp isolates the profile; this removes the keychain
	a := startedApp(t)

	conn := dialAppWS(t, a)
	defer func() { _ = conn.Close() }()

	if resp := callAppWS(t, conn, "vault.setup",
		map[string]any{"passphrase": "a passphrase, since there is no OS key"}, 1); resp.Error != nil {
		t.Fatalf("vault.setup on a host with no keychain: %s", resp.Error.Message)
	}

	resp := callAppWS(t, conn, "vault.status", nil, 2)
	if resp.Error != nil {
		t.Fatalf("vault.status: %s", resp.Error.Message)
	}
	var status struct {
		DefaultProvider string `json:"defaultProvider"`
		Providers       []struct {
			ID     string `json:"id"`
			Ready  bool   `json:"ready"`
			Reason string `json:"reason"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(resp.Result, &status); err != nil {
		t.Fatalf("decode vault.status: %v", err)
	}
	if status.DefaultProvider != string(vault.ProviderFile) {
		t.Errorf("default provider = %q, want %q: with no keystore in play the file "+
			"store is the only one that can hold anything",
			status.DefaultProvider, vault.ProviderFile)
	}
	var sawSystem bool
	for _, p := range status.Providers {
		if p.ID != string(vault.ProviderSystem) {
			continue
		}
		sawSystem = true
		if p.Ready {
			t.Error("the system provider reported ready on a host that was never asked")
		}
		if p.Reason != string(vault.ReasonExcluded) {
			t.Errorf("the product is told %q rather than %q — a claim about the "+
				"machine that nothing established", p.Reason, vault.ReasonExcluded)
		}
	}
	if !sawSystem {
		t.Error("the system provider is missing from the status; the choice is " +
			"then invisible in the product")
	}
}
