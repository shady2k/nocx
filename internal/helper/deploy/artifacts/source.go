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
	artifactsByPlatform = make(map[deploy.Platform]artifact)
	entries, err := fs.ReadDir(artifactsFS, "bin")
	if err != nil {
		return
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
		data, err := artifactsFS.ReadFile("bin/" + name)
		if err != nil {
			continue
		}
		contentHash, err := hashGzip(data)
		if err != nil {
			continue
		}
		artifactsByPlatform[p] = artifact{compressed: data, contentHash: contentHash}
	}
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
