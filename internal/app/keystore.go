package app

// THE KEYSTORE STANCE IS DECLARED, NEVER DISCOVERED (design D10).
//
// The OS keystore is the per-user OS service the vault's system provider
// talks to, and the one piece of a machine's state that $HOME isolation
// cannot move. Until now the backend found out whether it had one by
// PROBING: internal/vault/system.Provider.Probe writes a fresh random entry
// on every backend start, reads it back and deletes it.
//
// That probe cannot be the mechanism, because the probe is the failure.
// Measured on macOS during the design session: $HOME does move the login
// keychain (`security` resolves it under $HOME/Library/Keychains); a READ
// under a $HOME with no keychain fails silently; and a WRITE raises
// "Keychain not found" — a modal. On a headless host, a VPS, a container or
// any redirected $HOME, a coordinator that probes puts a dialog in front of
// nobody and waits. "Probe, then fall back" has no fall-back path: it
// deadlocks in front of a person who is not there.
//
// So the stance is stated before anything is built, by exactly one of two
// declarers, and the probe runs only once the stance says the store is in
// play:
//
//   - AN EXPLICIT OPTION. WithRealSystemKeystore(reason) or
//     WithoutSystemKeystore(), from a composition root that knows — a
//     launcher starting a backend inside a login session, or a test.
//   - THE BUILD. Everything else takes buildKeystoreStance, which is a
//     compile-time constant selected by build tag (keystore_build*.go).
//     It is a build property and not an environment variable on purpose:
//     for a process that lives for days, a keystore switch any process of
//     the user can set is the wrong shape (design §6). The dev/test
//     override that IS an environment variable lives in cmd/devharness,
//     where it cannot reach a shipped build, and the coordinator launcher
//     strips it from a spawned daemon's environment (coordinator/spawn.go).
//
// ONE POLICY IN ONE PLACE. decideKeystore below returns the stance, the
// provider built from it and whether to probe, together, as one value. They
// cannot disagree, because there is nowhere for a second opinion to live: no
// caller of this function may build a system provider of its own.

import (
	"fmt"
	"strings"

	"github.com/shady2k/nocx/internal/vault/system"
)

// keystoreStance is what has been decided about the OS keystore.
//
// The zero value is "undeclared", and undeclared is not a stance — it is the
// absence of one. Off `go test` it resolves to the build's stance; under
// `go test` it is refused, because a test that says nothing would otherwise
// write to the login keychain of whoever is running the suite.
type keystoreStance int

const (
	// keystoreUndeclared is what a caller who passed no keystore option
	// holds. It must stay the zero value: "said nothing" is only detectable
	// while the two coincide.
	keystoreUndeclared keystoreStance = iota
	// keystoreAbsent builds the provider over a keyring that is never
	// called and skips the startup probe: there is nothing to call, and
	// asking is the failure.
	keystoreAbsent
	// keystoreReal reaches the real per-user store, with a stated reason.
	keystoreReal
)

func (s keystoreStance) String() string {
	switch s {
	case keystoreAbsent:
		return "absent"
	case keystoreReal:
		return "real"
	default:
		return "undeclared"
	}
}

// keystoreDecision is everything the stance decides, decided once.
//
// It carries the provider itself rather than a boolean the caller acts on,
// so "which provider" and "may we probe" cannot drift apart: a build that
// excluded the keystore and a probe that ran anyway would be exactly the
// modal D10 exists to prevent, and that combination is unrepresentable here.
type keystoreDecision struct {
	// stance is what was decided.
	stance keystoreStance
	// source names who decided it, for the startup log: a person reading a
	// keychain prompt has to be able to find what asked for it.
	source string
	// reason is why the real store is in play. Required with keystoreReal.
	reason string
	// provider is the vault's system provider, built from the stance.
	provider *system.Provider
	// probe is whether the startup probe may run. True only with
	// keystoreReal — a probe is a real keystore call.
	probe bool
}

// decideKeystore resolves the stance and builds everything that follows from
// it, or refuses.
//
// The refusal for an undeclared test lives here — inside the composition
// root, at run time, keyed on testing.Testing() — rather than in a linter or
// a ratchet, for the reason storage.NewAppPaths puts the same kind of
// refusal in the same place: it cannot be routed around. It fires for every
// construction from every test binary, in packages that have never heard of
// internal/app's test helper, under a renamed constructor, and in code a
// grep-based ratchet would not recognise. It costs one comparison on the
// production path.
func decideKeystore(inTest bool, o *optionSet) (keystoreDecision, error) {
	stance, source, reason := o.keystore, "option", o.keystoreReason
	switch stance {
	case keystoreReal:
		if strings.TrimSpace(reason) == "" {
			return keystoreDecision{}, fmt.Errorf(
				"app: WithRealSystemKeystore needs a reason — it writes to the " +
					"login keychain of whoever runs the suite, and the reason is " +
					"what tells the next reader why this build may")
		}
	case keystoreAbsent:
	default:
		if inTest {
			return keystoreDecision{}, fmt.Errorf(
				"app: this test has not said whether it may reach the OS keystore, " +
					"and building the app can reach it: the startup probe is a real " +
					"keyring write, which on macOS is a keychain dialog per backend " +
					"start (nocx-o4hg). Build it with newTestApp(t), which keeps the " +
					"keystore out of reach, or state the exception with " +
					"app.WithRealSystemKeystore(reason). $HOME isolation cannot cover " +
					"this: go-keyring talks to a per-user OS service, not to a directory")
		}
		stance, source, reason = buildKeystoreStance, buildKeystoreSource, buildKeystoreReason
	}

	d := keystoreDecision{stance: stance, source: source, reason: reason}
	if stance == keystoreReal {
		d.provider = system.New()
		d.probe = true
		return d, nil
	}
	d.provider = system.NotInPlay()
	return d, nil
}
