package artifacts

// The embedded helper artifacts (D20): the four build targets ship inside
// the app, gzip-compressed, and are decompressed locally before upload —
// nothing is downloaded at runtime. This package owns the embed so the helper
// binary itself can depend on deploy's interfaces without embedding its own
// previous builds.

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/shady2k/nocx/internal/helper/deploy"
)

// ErrArtifactsNotBuilt is returned when a matrix platform has no artifact in
// the embed because `make helpers` has not run. It is distinct from
// deploy.ErrUnsupportedPlatform so callers can name the required recovery.
var ErrArtifactsNotBuilt = errors.New("deploy: helper artifacts not built (run make helpers)")

// embeddedSource is the production ArtifactSource: the embedded binaries.
// deploy remains unaware of this package, keeping the artifact bytes out of
// binaries such as nocx-helper that only use deploy's filesystem types.
type embeddedSource struct{}

// THIS IS THE COMPILE-TIME HALF of "production cannot reach the injection seam
// in internal/helper/local's preferredLocal": the production source carries the
// local companion by construction rather than by hope, so the branch that
// installs a source's own artifact can only be taken by a caller that supplied
// bytes of its own — a test. The runtime half is
// TestTheProductionArtifactSourceCarriesTheLocalVariant in that package, which
// holds the value the composition root actually passes (DefaultSource).
var _ deploy.LocalArtifactSource = embeddedSource{}

func (embeddedSource) Artifact(p deploy.Platform) (data []byte, contentHash string, err error) {
	if a, ok := artifactsByPlatform[p]; ok {
		return a.compressed, a.contentHash, nil
	}
	for _, s := range supportedTargets {
		if s == p {
			return nil, "", ErrArtifactsNotBuilt
		}
	}
	return nil, "", deploy.ErrUnsupportedPlatform
}

// LocalArtifact answers this machine's OWN helper — the variant built with
// nocx_local_ssh, which is the same platform with an ssh client linked in. The
// walk over its directory lives in source_local.go; this method exists so the
// production source the composition root already passes around is the one that
// carries both variants (deploy.LocalArtifactSource), rather than a second
// source somebody has to remember to pass to the local install.
//
// Its error is not a fallback's trigger: a directory holding nothing for the
// platform is what the local install reports, because a build without this
// variant has no helper to install at all (nocx-50w7p.7).
func (embeddedSource) LocalArtifact(p deploy.Platform) (data []byte, contentHash string, err error) {
	return localSource{}.Artifact(p)
}

// DefaultSource is the artifact source production installs from. It is a
// variable so the composition-root tests can inject synthetic bytes while
// production uses the embedded binaries.
var DefaultSource deploy.ArtifactSource = embeddedSource{}

//go:embed all:bin
var artifactsFS embed.FS

// supportedTargets is the D20 build matrix. It distinguishes a matrix
// platform whose build output is absent from a platform we do not ship.
var supportedTargets = []deploy.Platform{
	{GOOS: "linux", GOARCH: "amd64"},
	{GOOS: "linux", GOARCH: "arm64"},
	{GOOS: "darwin", GOARCH: "amd64"},
	{GOOS: "darwin", GOARCH: "arm64"},
}

type artifact struct {
	compressed  []byte
	contentHash string
}

var artifactsByPlatform map[deploy.Platform]artifact

func init() {
	artifactsByPlatform = artifactsInDir(artifactsFS, "bin")
}

// artifactsInDir reads one embedded artifact directory. THE NAMING CONVENTION
// LIVES HERE ONCE: an artifact is named nocx-helper-<goos>-<goarch>.gz, the
// platform is the whole of the name, and a file that does not parse as one is
// skipped rather than guessed at. Two directories are read this way — bin/,
// which is what gets deployed, and bin/local/, which is what this machine runs
// itself — and they are separate DIRECTORIES rather than longer names because
// the name is already spoken for by the platform: two variants of one platform
// cannot be told apart by a name that spells only the platform.
//
// A directory that is absent embeds nothing but its committed .gitignore, so
// an empty result is the ordinary state of a checkout that has not built that
// variant — which is why the callers distinguish "not built" by their own
// error rather than by this function failing.
func artifactsInDir(fsys fs.FS, dir string) map[deploy.Platform]artifact {
	found := make(map[deploy.Platform]artifact)
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return found
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "nocx-helper-") || !strings.HasSuffix(name, ".gz") {
			continue
		}
		rest := strings.TrimSuffix(strings.TrimPrefix(name, "nocx-helper-"), ".gz")
		parts := strings.Split(rest, "-")
		if len(parts) != 2 {
			continue
		}
		p := deploy.Platform{GOOS: parts[0], GOARCH: parts[1]}
		data, err := fs.ReadFile(fsys, dir+"/"+name)
		if err != nil {
			continue
		}
		contentHash, err := hashGzip(data)
		if err != nil {
			continue
		}
		found[p] = artifact{compressed: data, contentHash: contentHash}
	}
	return found
}

func hashGzip(data []byte) (string, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("deploy: embedded artifact is not gzip: %w", err)
	}
	defer func() { _ = zr.Close() }()
	plain, err := io.ReadAll(zr)
	if err != nil {
		return "", fmt.Errorf("deploy: embedded artifact corrupt: %w", err)
	}
	sum := sha256.Sum256(plain)
	return hex.EncodeToString(sum[:]), nil
}
