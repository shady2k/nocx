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

// LocalArtifactSource is the OPTIONAL companion of ArtifactSource for the
// variant this machine runs ITSELF (nocx-50w7p.1). One binary serves every
// machine, and the same bytes are both deployed to somebody else's host and
// installed locally — except for one variant: the local helper links an ssh
// client, and the artifact that reaches a host nobody here controls must not.
// The two are different BYTES for the same platform, so they cannot be told
// apart by the platform, and the caller that installs locally must not have to
// know which artifacts its build happens to carry.
//
// Hence a companion rather than a second ArtifactSource method, and hence an
// interface a source MAY implement: the question "which helper does this
// machine run" is asked in exactly one place (internal/helper/local, which
// installs THIS variant and, when the build carries none of them, reports the
// source's own "not built" error instead of the deployable bytes — ADR-0057
// leaves no Tier A behind the local helper to fall back to), while the remote
// install reads Artifact and cannot reach this one at all.
//
// As with Artifact, the bytes are still compressed and contentHash is the
// hash of their decompressed form.
type LocalArtifactSource interface {
	LocalArtifact(p Platform) (data []byte, contentHash string, err error)
}
