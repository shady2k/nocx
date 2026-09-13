package artifacts

// THE HOST-LOCAL VARIANT OF THE HELPER (nocx-50w7p.1).
//
// One binary runs on every machine and the same bytes are both deployed to a
// host somebody else controls and installed here — except for one difference
// that cannot be spelled in a name: the helper THIS machine runs links an ssh
// client (nocx_local_ssh), and the artifact written to another host must not.
// The artifact name is frozen as nocx-helper-<goos>-<goarch>.gz and the
// platform is the whole of it, so the two variants live in two DIRECTORIES —
// bin/ is deployed, bin/local/ is installed on this machine — and this file
// owns the second one: its embed, its walk and its own "not built" error.
//
// IT IS ABSENT BY DEFAULT, and that is the safety property rather than an
// accident. `make helpers` builds the four deployable targets with no extra
// tags, so a forgotten flag cannot put an ssh client into what reaches a
// remote host; `make helper-local` is the only target that passes
// nocx_local_ssh, it builds the HOST platform by default and every platform
// HELPER_LOCAL_TARGETS names otherwise (the release builds the darwin pair,
// because the macOS bundle is universal and either slice may be the one
// running), and it writes here.
//
// A checkout that has never run it embeds nothing but bin/local/.gitignore —
// which is committed, because a //go:embed pattern matching no file does not
// compile — so LocalArtifact answers ErrLocalArtifactsNotBuilt.
//
// NOTHING FALLS BACK FROM THAT ANSWER (nocx-50w7p.7). A build whose bin/local
// holds nothing for a platform it can run on installs no helper, and since
// ADR-0057 there is no other route to a pane: every terminal in that app
// refuses. That is why the targets which ship or run the app depend on the
// Makefile's require-local-helper rather than on helper-local — building this
// directory and proving the binary embeds it are two acts, and only the second
// one fails when the artifact lands where //go:embed does not read it.

import (
	"embed"
	"errors"

	"github.com/shady2k/nocx/internal/helper/deploy"
)

// ErrLocalArtifactsNotBuilt is returned when this machine's own helper has no
// artifact in the embed. It is deliberately NOT ErrArtifactsNotBuilt: the
// recovery is a different command (`make helper-local`, which passes
// nocx_local_ssh), and a caller told "run make helpers" for a variant that
// target deliberately does not build would be sent to build the wrong thing.
var ErrLocalArtifactsNotBuilt = errors.New("deploy: local helper artifact not built (run make helper-local)")

//go:embed all:bin/local
var localArtifactsFS embed.FS

var localArtifactsByPlatform map[deploy.Platform]artifact

func init() {
	localArtifactsByPlatform = artifactsInDir(localArtifactsFS, "bin/local")
}

// localSource is the ArtifactSource over bin/local: this machine's own helper,
// which is the deployable artifact for one platform plus the ssh client.
type localSource struct{}

func (localSource) Artifact(p deploy.Platform) (data []byte, contentHash string, err error) {
	if a, ok := localArtifactsByPlatform[p]; ok {
		return a.compressed, a.contentHash, nil
	}
	return nil, "", ErrLocalArtifactsNotBuilt
}
