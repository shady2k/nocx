package deploy

// The embedded helper artifacts (D20): the three build targets ship inside
// the app, gzip-compressed, and are decompressed locally before upload —
// nothing is downloaded at runtime. The embed is package-local
// (//go:embed paths are relative to the source package and may not contain
// ".."), and the artifacts directory carries a COMMITTED .gitignore so a
// fresh checkout compiles: plain patterns skip dotfiles, and "all:" is what
// includes the .gitignore and therefore what makes the pattern match at
// all. Until `make helpers` has run, the embed holds only that file and the
// source answers ErrArtifactsNotBuilt — a visible refusal, never a silent
// degrade into "unsupported platform".
//
// The artifacts are reached through the ArtifactSource seam: Ensure resolves
// the bytes through the source it is given, production installs from
// DefaultSource (the embedded binaries), and tests inject synthetic bytes —
// the D7 install semantics are transport- and size-independent, so they run
// identically on a fresh checkout and in a workspace where `make helpers`
// has run. This is interface-first + DI at the composition root, and it is
// why no test seam exists that only tests use.

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
)

// ErrArtifactsNotBuilt is returned by the embedded source when the helper
// binaries are absent — `make helpers` has not run. It is a build-
// configuration fact, distinct from ErrUnsupportedPlatform: a caller must
// surface it as a visible refusal naming the missing step (AGENTS.md: a
// soft degrade the UI contradicts is forbidden).
var ErrArtifactsNotBuilt = errors.New("deploy: helper artifacts not built (run make helpers)")

// ErrUnsupportedPlatform is returned by the embedded source for a platform
// the build matrix deliberately does not ship a helper for (D20:
// darwin/amd64, and anything a probe cannot map onto a target).
var ErrUnsupportedPlatform = errors.New("deploy: no helper artifact for this platform")

// ArtifactSource supplies the helper artifact for a platform: the
// embedded, still-compressed bytes and the content hash of their
// DECOMPRESSED form — the D7 directory key and the hash the helper reports
// about itself in the hello-ok (D21). Ensure resolves its bytes through
// the source it is given; production installs from DefaultSource.
type ArtifactSource interface {
	Artifact(p Platform) (data []byte, contentHash string, err error)
}

// embeddedSource is the production ArtifactSource: the embedded binaries.
// ErrUnsupportedPlatform answers a platform outside the D20 matrix;
// ErrArtifactsNotBuilt answers a matrix platform whose file is absent, and
// the two refusals must never be confused.
type embeddedSource struct{}

func (embeddedSource) Artifact(p Platform) (data []byte, contentHash string, err error) {
	if a, ok := artifactsByPlatform[p]; ok {
		return a.compressed, a.contentHash, nil
	}
	for _, s := range supportedTargets {
		if s == p {
			return nil, "", ErrArtifactsNotBuilt
		}
	}
	return nil, "", ErrUnsupportedPlatform
}

// DefaultSource is the artifact source production installs from: the
// embedded binaries. It is a variable, not a constant, so the composition
// root can substitute a source — the app's selector tests install
// synthetic bytes, and the ErrArtifactsNotBuilt refusal is exercised
// without moving the embedded files around. Production never reassigns it.
// (The same shape as readFileFn in internal/ssh.)
var DefaultSource ArtifactSource = embeddedSource{}

//go:embed all:artifacts
var artifactsFS embed.FS

// supportedTargets is the D20 build matrix. It is the source of truth for
// distinguishing "we do not build this platform" from "nothing was built":
// a matrix platform with no embedded file means `make helpers` has not run.
var supportedTargets = []Platform{
	{GOOS: "linux", GOARCH: "amd64"},
	{GOOS: "linux", GOARCH: "arm64"},
	{GOOS: "darwin", GOARCH: "arm64"},
}

// artifact is one embedded, still-compressed helper plus the content hash
// of its decompressed bytes — the D7 directory key and the hash the helper
// reports about itself in the hello-ok (D21).
type artifact struct {
	platform    Platform
	compressed  []byte
	contentHash string
}

// artifactsByPlatform indexes the embedded binaries. Empty until `make
// helpers` has run; the embedded source then answers ErrArtifactsNotBuilt
// for every matrix platform.
var artifactsByPlatform map[Platform]artifact

func init() {
	artifactsByPlatform = make(map[Platform]artifact)
	entries, err := fs.ReadDir(artifactsFS, "artifacts")
	if err != nil {
		// The artifacts directory is embedded; a read failure here cannot
		// happen without the embed itself being broken.
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "nocx-helper-") || !strings.HasSuffix(name, ".gz") {
			continue // the .gitignore, anything unexpected — never an artifact
		}
		// nocx-helper-<goos>-<goarch>.gz
		rest := strings.TrimSuffix(strings.TrimPrefix(name, "nocx-helper-"), ".gz")
		parts := strings.Split(rest, "-")
		if len(parts) != 2 {
			continue
		}
		p := Platform{GOOS: parts[0], GOARCH: parts[1]}
		data, err := artifactsFS.ReadFile("artifacts/" + name)
		if err != nil {
			continue
		}
		contentHash, err := hashGzip(data)
		if err != nil {
			// A corrupt embedded artifact is a build defect; skipping it
			// turns into ErrArtifactsNotBuilt, which names the build step.
			continue
		}
		artifactsByPlatform[p] = artifact{platform: p, compressed: data, contentHash: contentHash}
	}
}

// hashGzip decompresses data and returns the SHA-256 of the decompressed
// bytes. The helper's content hash is a property of what RUNS on the remote
// host (D21), so it is computed over the decompressed binary, never over
// the compression container.
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
