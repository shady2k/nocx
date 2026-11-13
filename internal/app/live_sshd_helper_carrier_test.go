//go:build nocx_local_ssh

package app

// The live-sshd fixture's bundle carrier, in a build WITH the helper's ssh
// service — this machine's helper, for real.
//
// `live_sshd_carrier_test.go` (the untagged half) says why the two exist and
// what each is for. This one makes every live-sshd journey — a real OpenSSH, a
// real bash, a real lifecycle domain — publish through the production route:
// the real ssh service on a real socketpair, the real coordinator client, the
// real carrier, and the fixture's own sshd as the far side. So
// `TestLiveSshd_BashReachesAcceptedDomain` reaches its accepted domain with the
// bundle written by this machine's helper into the session's `$HOME`, which is
// the epic's acceptance criterion (nocx-50w7p.15) rather than a rehearsal of
// it.

import (
	"context"

	"github.com/shady2k/nocx/internal/credential"
)

// liveFixtureKeyRef names the fixture's client key on this wire. It is opaque
// to the helper, which echoes it back when it needs a signature.
const liveFixtureKeyRef = "sec:live-sshd:fixture-key"

// helperLiveCarrier is the production carrier plus the fixture's own identity.
//
// The identity is APPENDED here and not put on the pane's connect options, and
// that is deliberate: a helper needs a credential REFERENCE and a resolver
// (it never holds the key), while this fixture's pane dials with an inline
// signer — and `ssh.ResolveTarget` refuses an inline method by name
// (ErrNoHelperIdentity), because a helper has no file and no agent. So the
// fixture supplies the binding for the same key, and the pane's own dial is
// untouched by it.

// liveFixtureKeyResolver answers the fixture's own private key, PEM-encoded,
// for the one reference this stand asks about.
//
// It is what the reverse `sign` op asks for: the helper must DECLARE which key
// it offers and then ask for one signature per challenge, so the private half
// never leaves this process.
type liveFixtureKeyResolver struct{ pem []byte }

func (r liveFixtureKeyResolver) Resolve(_ context.Context, _ credential.SecretID, _ credential.Stance) (credential.Secret, error) {
	return credential.NewSecret(string(r.pem)), nil
}

// TestLiveSshd_OneSessionPerConnectionStillIntegratesOverTheHelper pins the
// CONSEQUENCE this bead bought, on a real sshd that allows ONE session channel
// per connection.
//
// The publish used to run its home probe and its sftp channel on the PANE's
// connection, so under that bound it was the publish the far side refused and
// the session could never integrate — the state
// TestEpicE2E_MaxSessions1LeavesAWorkingUnintegratedPrompt's saved-connection
// case was written to pin. The publish is this machine's helper's now, on a
// connection of its own, so the pane's one slot belongs to the user's shell and
// nocx's own work cannot spend it: the domain reaches Established, and the
// server sees three logins — the pane, this harness's lifecycle transport
// lease, and the helper's own dial.
//
// This is the tagged half of that pair on purpose. What the far side answers
// there depends on which transport the build wires, so the common observables
// live in the epic test and the route's own verdict is asserted HERE, where the
// route is this machine's helper and nothing is declared at runtime.
