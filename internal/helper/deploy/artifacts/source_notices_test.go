//go:build helperacceptance

package artifacts

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/shady2k/nocx/internal/vtpin"
)

// helperArtifactDir is where `make helpers` writes the four artifacts.
const helperArtifactDir = "internal/helper/deploy/artifacts/bin"

// TestHelperArtifactsCarryThePinnedThirdPartyNotices is the check the licences
// of what the helper LINKS are actually SHIPPED: every artifact `make helpers`
// produces must carry the pinned THIRD_PARTY_LICENSES document inside it.
//
// Why it is an artifact check and not a source check: `internal/helper/notices`
// embeds a staged copy, so a test of the package would be testing the stage
// rather than the thing a person downloads. A helper artifact is the byte
// string that reaches a remote host, and the pinned document is the byte string
// the licence requires it to carry — this compares those two, for all four
// targets, and for the host's own target it also runs the binary and compares
// what `--licenses` PRINTS, which is what a reader there actually gets.
//
// THE TWO CHECKS ARE NOT EQUALLY STRONG, deliberately. Containment is the claim
// for a foreign architecture: "this binary carries the pinned notices" is what
// the licence asks for, and a binary that also carries something else still
// satisfies it — so a document with extra bytes around it passes there, by
// design. Byte-for-byte equality against the flag's output is the claim for the
// host's own artifact, where the question "does it print exactly the pin" can be
// asked at all. Extracting the embedded range out of a foreign binary would
// mean reading the compiler's embed table, which is a private format; the flag
// is the supported answer and it is why the flag exists.
//
// THE INSTALL BOUNDARY IS COVERED BY THESE BYTES, not by a second fake of it.
// deploy.Ensure decompresses what its ArtifactSource returns and writes that
// payload verbatim as the installed file — internal/helper/deploy's own tests
// own that path, with synthetic sources and fault injection — so the payload
// checked here is the file a remote host ends up running at
// ~/.nocx/helper/<version>-<goos>-<goarch>-<contenthash>/nocx-helper, and the
// `--licenses` output above is that same file executed. A fake filesystem here
// would restate install.go's semantics rather than test them.
//
// It fails in both directions the acceptance asks for: MISSING (an artifact
// built without the notices, which is what a build that skipped the staging
// produces) and DIFFERENT (a document that is not the pinned bytes — the
// pinned copy is hashed against MANIFEST.json's sha256 before it is used as
// the expectation, so a substituted build/libghostty-vt document cannot make
// this check agree with a substituted artifact).
func TestHelperArtifactsCarryThePinnedThirdPartyNotices(t *testing.T) {
	root := moduleRoot(t)
	want := pinnedNotices(t, root)
	paths := helperArtifactPaths(t)

	for _, name := range sortedNames(paths) {
		plain := decompressHelperArtifact(t, paths[name])
		if !bytes.Contains(plain, want) {
			t.Fatalf("%s does not carry the pinned third-party notices (%d bytes, sha256 %s); the helper links libghostty-vt, so a helper without them is a binary distributed without its licences",
				name, len(want), hashBytes(want))
		}
	}

	// The artifact for THIS machine is executed, which is the only check that
	// sees the document the way a user on a remote host does — and the only one
	// that can ask for exactness rather than presence.
	//
	// It is the LOCAL one, because that is the helper this machine actually
	// runs: the deployed variant in bin/ is the artifact somebody else's host
	// would run, and it is checked above by containment like the other three
	// foreign-architecture ones. helperArtifactPaths has already refused to
	// build the map without the host's own local variant, which is what makes
	// this lookup safe rather than guarded a second time here.
	local := localHelperArtifactDir + "/nocx-helper-" + runtime.GOOS + "-" + runtime.GOARCH + ".gz"
	bin := filepath.Join(t.TempDir(), "nocx-helper")
	if err := os.WriteFile(bin, decompressHelperArtifact(t, paths[local]), 0o700); err != nil {
		t.Fatalf("write %s: %v", bin, err)
	}
	cmd := exec.Command(bin, "--licenses") // #nosec G204 -- the binary is the artifact this test decompressed into its own temp dir
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			t.Fatalf("`%s --licenses` failed with exit %d: %s", local, exit.ExitCode(), exit.Stderr)
		}
		t.Fatalf("run `%s --licenses`: %v", local, err)
	}
	if !bytes.Equal(out, want) {
		t.Fatalf("`%s --licenses` printed %d bytes (sha256 %s), the pinned document is %d bytes (sha256 %s)",
			local, len(out), hashBytes(out), len(want), hashBytes(want))
	}
}

// pinnedNotices returns the pinned THIRD_PARTY_LICENSES document, and refuses
// to be an expectation that is not the pin: the manifest's sha256 is what
// makes this an assertion about the RELEASE rather than about two files that
// happen to agree on this machine. A missing document fails here rather than
// skipping — `make vt-archives` is what puts it there, and the job that runs
// these checks runs it first.
func pinnedNotices(t *testing.T, root string) []byte {
	t.Helper()
	m, err := vtpin.Load(filepath.Join(root, vtpin.ManifestPath))
	if err != nil {
		t.Fatalf("load %s: %v", vtpin.ManifestPath, err)
	}
	path := m.LicensesPath(filepath.Join(root, vtpin.DefaultRoot))
	data, err := os.ReadFile(path) // #nosec G304 -- the pin's own document, named by the manifest
	if err != nil {
		t.Fatalf("read the pinned notices %s: %v (run make vt-archives)", path, err)
	}
	if got := hashBytes(data); got != m.Licenses.SHA256 {
		t.Fatalf("%s is not the pinned document: MANIFEST.json says sha256 %s, the file is %s", path, m.Licenses.SHA256, got)
	}
	return data
}

// decompressHelperArtifact gunzips one artifact, which is the form the bytes
// are in on disk; the decompressed form is the binary that gets installed.
func decompressHelperArtifact(t *testing.T, path string) []byte {
	t.Helper()
	file, err := os.Open(path) // #nosec G304 -- the artifact directory this package builds into
	if err != nil {
		t.Fatalf("open helper artifact %s: %v", path, err)
	}
	defer func() { _ = file.Close() }()
	zr, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("open gzip helper artifact %s: %v", path, err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil {
		_ = zr.Close()
		t.Fatalf("read helper artifact %s: %v", path, err)
	}
	if err := zr.Close(); err != nil {
		t.Fatalf("close helper artifact %s: %v", path, err)
	}
	return plain
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
