package artifacts

// THE BUILD-TIME GATE FOR THIS MACHINE'S OWN HELPER (nocx-50w7p.7).
//
// The local variant is not an optimisation of the deployable one: it is what
// every pane on this machine is served by (ADR-0057, and internal/helper/local
// stopped falling back to the deployable bytes), so a build whose bin/local
// holds nothing for a platform it can run on is a build that installs no helper
// and refuses every terminal. That is a fact about the BINARY, and this test is
// where it is asserted — after the Go compiler has read the embed, which is the
// only moment it can be observed at all.
//
// It is GATED the way internal/helper/deploy's artifact tests are, and for the
// same reason: `go test` runs on a fresh checkout where the artifacts are
// gitignored, and a fresh checkout is not a broken build. What makes it a gate
// is NOCX_REQUIRE_LOCAL_ARTIFACTS, which every target that produces a runnable
// binary sets (the Makefile's require-local-helper, the release workflow's two
// build jobs) — those builds were supposed to run `make helper-local`, and a
// skip there is how a shipped app came to embed nothing for a platform it runs
// on. The one difference from NOCX_REQUIRE_HELPER_ARTIFACTS is that this
// variable CARRIES the platforms: a universal macOS bundle runs on two of them,
// and "the app can install its own helper" has to be asked of both slices.

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/helper/deploy"
)

func TestEmbeddedLocalArtifactsCoverTheBuildTargets(t *testing.T) {
	want := strings.Fields(os.Getenv("NOCX_REQUIRE_LOCAL_ARTIFACTS"))
	if len(want) == 0 {
		t.Skip("NOCX_REQUIRE_LOCAL_ARTIFACTS is empty — run `make helper-local` (or `make require-local-helper`) first; the targets that ship or run the app set this and fail here instead")
	}
	// The production source is declared as the plain ArtifactSource (a caller
	// may replace it with one that answers that alone), so the companion has to
	// be asked for. A source without it is a build that cannot answer for its
	// own platform at all, which is the same defect one step earlier.
	local, carries := DefaultSource.(deploy.LocalArtifactSource)
	if !carries {
		t.Fatalf("the production artifact source is %T and carries no host-local variant: this build installs no helper for any platform", DefaultSource)
	}
	for _, target := range want {
		goos, goarch, parsed := strings.Cut(target, "/")
		if !parsed || goos == "" || goarch == "" {
			t.Fatalf("NOCX_REQUIRE_LOCAL_ARTIFACTS names %q, which is not <goos>/<goarch>", target)
		}
		p := deploy.Platform{GOOS: goos, GOARCH: goarch}
		_, contentHash, err := local.LocalArtifact(p)
		if errors.Is(err, ErrLocalArtifactsNotBuilt) {
			t.Fatalf("no local helper artifact is embedded for %s while NOCX_REQUIRE_LOCAL_ARTIFACTS names it: this build was supposed to run `make helper-local` for that platform and did not, so it installs no helper and every pane in it refuses", target)
		}
		if err != nil {
			t.Fatalf("LocalArtifact(%s): %v", target, err)
		}

		// "Present" is a stronger claim than it looks, and it is deliberately
		// not re-checked here. The map this answers from was built by a walk
		// that parses the name, reads the file and gunzips it (hashGzip),
		// skipping anything it cannot decompress — so bytes reaching this line
		// are a real gzip stream and contentHash is the hash of their
		// DECOMPRESSED form, which is the same thing deploy.Ensure verifies
		// once more on the host. Re-asserting it here would call the package's
		// own function a second time and prove nothing the walk did not.

		// AND IT IS THE LOCAL VARIANT, not the deployable bytes under a second
		// name. The two are different builds for the same platform — the local
		// one links an ssh client, the deployed one must not — so the
		// generation, which is the hash of the decompressed artifact, is what
		// tells them apart. It is a comparison of hashes rather than of bytes
		// because the bytes in the embed are still compressed, and gzip stamps
		// the source file's mtime into its header: two builds of one binary
		// never produce equal .gz bytes, so the compressed comparison would
		// pass on a copy of the deployable artifact.
		//
		// What the TAG means is asked at the source level, not here:
		// internal/helper/deploy's TestLocalHelperDependencyGraphReachesSSHClient
		// is the check that a helper built with nocx_local_ssh links an ssh
		// client and one built without it does not.
		_, deployedHash, err := DefaultSource.Artifact(p)
		if err != nil {
			t.Fatalf("Artifact(%s): %v — the deployed variant must exist for every platform the local one does", target, err)
		}
		if deployedHash == contentHash {
			t.Fatalf("the local artifact embedded for %s is the deployable one (%s): `make helper-local` writes the same binary with an ssh client linked in, so equal generations mean this machine's helper cannot dial ssh and nothing would say so", target, contentHash)
		}
	}
}
