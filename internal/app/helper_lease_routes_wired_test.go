package app

// The composition-root half of a defect the merge of nocx-50w7p.9 found.
//
// `installLeaseRoutes.DiscoveryConn` serves the platform probe from `r.probes`,
// and a nil `*helperProbes` refuses it with "the helper's probes are not wired at
// this composition root" — so on a root that built the routes WITHOUT that field
// the remote helper install's platform probe is refused before it ever reaches
// this machine's helper. Nothing said so: every unit test built its own
// `installLeaseRoutes` literal with the field set by hand, and the shipped root
// was the one place nobody looked. The probe's failure is then SWALLOWED by the
// selection (`if err != nil && !available { return …, false, nil }` in
// OpenHosted), which is why the shipped app would have declined the helper with
// no evidence anywhere.
//
// So this test drives the SHIPPED routes — the value `helperGitFactory` was
// given, read back off the registry the app holds — and asserts the probe
// reaches this machine's helper and is refused BY THE HELPER rather than by the
// composition root. The two sentences are the discriminator, and a test cannot
// be written more tightly than that: `transport.RefuseLocalHelper` deliberately
// drops the concrete cause (the sentence is the whole answer), so the boundary's
// refusal is only reachable as text.

import (
	"context"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

func TestTheShippedLeaseRoutesServeThePlatformProbe(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("newTestApp: %v", err)
	}
	if a.helperRegistry == nil {
		t.Fatal("the composition root built no helper registry, so there are no shipped lease routes to check")
	}
	// The value the git factory was given (and the file panel's factory too —
	// one literal, read twice). Not a stand-in: this is the object the product
	// would serve an install's platform probe with.
	leases := a.helperRegistry.install
	if leases == nil {
		t.Fatal("the shipped lease routes are nil: the git factory was handed nothing to probe with")
	}

	// A probe through the shipped routes. The destination does not have to exist:
	// what this asserts is which party refuses it.
	// The credential is a REFERENCE the coordinator holds, which is what makes
	// the destination one a helper can be handed at all: resolution runs first
	// (the helper reads no config and holds no secret at rest), so a destination
	// it cannot express would refuse before the boundary this test is about.
	_, err = leases.DiscoveryConn(context.Background(), "host.example",
		ssh.WithUser("deploy"),
		ssh.WithCredentials(nil, credential.SecretID("secret-7")),
	)
	if err == nil {
		t.Fatal("the platform probe was served with no error at all — a lease the test cannot explain")
	}

	// The failure must be THIS MACHINE'S HELPER refusing: the boundary was
	// reached, and it said what it always says in a test — no generation is
	// installed here. (That is the failing stand-in the assertion needs: the
	// boundary is real, and only a real boundary can produce its own sentence.)
	if !strings.Contains(err.Error(), errNoLocalGeneration.Error()) {
		t.Errorf("the platform probe did not reach this machine's helper.\n\n"+
			"got: %v\n\nwant the helper boundary's own refusal (it contains %q).\n"+
			"A composition root that built its lease routes without `probes` refuses here instead, "+
			"with `the helper's probes are not wired at this composition root` — and the selection "+
			"swallows that refusal, so the product would decline the helper with no evidence anywhere.",
			err, errNoLocalGeneration.Error())
	}
	if strings.Contains(err.Error(), "not wired at this composition root") {
		t.Errorf("the shipped lease routes have no probe source: %v", err)
	}
}
