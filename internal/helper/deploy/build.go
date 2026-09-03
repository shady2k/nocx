package deploy

// Artifact deployment depends only on this package's stable source seam and
// platform errors. The embedded implementation lives in the sibling
// internal/helper/deploy/artifacts package so consumers that only need the
// deploy types never inherit the helper binaries.

import "errors"

// ErrUnsupportedPlatform is returned by the embedded source for a platform
// the build matrix deliberately does not ship a helper for (anything a
// probe cannot map onto one of the four targets: windows, 32-bit, the
// BSDs). darwin/amd64 was such a platform until 2026-08-30; it is shipped
// now, because the app itself is universal and an Intel Mac is a host we
// support.
var ErrUnsupportedPlatform = errors.New("deploy: no helper artifact for this platform")

// ArtifactSource supplies the helper artifact for a platform: the
// embedded, still-compressed bytes and the content hash of their
// DECOMPRESSED form — the D7 directory key and the hash the helper reports
// about itself in the hello-ok (D21). The composition root selects the
// implementation; deploy itself does not import the embedded source.
type ArtifactSource interface {
	Artifact(p Platform) (data []byte, contentHash string, err error)
}
