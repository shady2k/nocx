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
// nocx_local_ssh, it builds the HOST platform alone, and it writes here.
//
// A checkout that has never run it embeds nothing but bin/local/.gitignore —
// which is committed, because a //go:embed pattern matching no file does not
// compile — so LocalArtifact answers ErrLocalArtifactsNotBuilt and the local
// install falls back to the deployable artifact, exactly as it did before this
// variant existed.

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
