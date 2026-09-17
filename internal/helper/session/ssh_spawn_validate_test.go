package session

// validateSSHSpawn's own coverage: the interactive rung's identity carries no
// credential reference BY DESIGN (proto.SSHIdentity's own comment — "the
// interactive rung names neither"), so a spawn asking for it must not be
// refused as ErrBadSSHParams the way a password or key identity with a blank
// reference would be. Before this fix, validateSSHSpawn read
// Identity.CredentialOf().Ref for every auth kind alike, which is exactly the
// refusal connection-password.spec.ts:202 measured against a live helper:
// "the ssh spawn request is incomplete: no credential reference" on a
// profile that has nothing stored yet and is expected to raise the password
// prompt instead.

import (
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
)

func validSSHSpawnParams() proto.SSHSpawnParams {
	return proto.SSHSpawnParams{
		Destination: proto.SSHDestination{
			Host: "example.com",
			Port: 22,
			User: "test",
		},
	}
}

// TestValidateSSHSpawn_InteractiveIdentityCarriesNoCredential is the red
// case: an interactive-rung identity is the ordinary shape for "the profile
// named nothing and a person is being asked" (ssh_helpertarget.go's own
// resolveCredential), and it must be accepted rather than refused for the
// one thing it is defined never to carry.
func TestValidateSSHSpawn_InteractiveIdentityCarriesNoCredential(t *testing.T) {
	p := validSSHSpawnParams()
	p.Destination.Identity = proto.SSHIdentity{Auth: proto.SSHAuthInteractive}

	if err := validateSSHSpawn(p); err != nil {
		t.Fatalf("validateSSHSpawn refused an interactive identity that carries no credential by design: %v", err)
	}
}

// TestValidateSSHSpawn_PasswordIdentityStillNeedsAReference is the control:
// a PASSWORD (or key) identity with no reference is still the malformed
// request ErrBadSSHParams exists to catch, and the interactive carve-out must
// not swallow it.
func TestValidateSSHSpawn_PasswordIdentityStillNeedsAReference(t *testing.T) {
	p := validSSHSpawnParams()
	p.Destination.Identity = proto.SSHIdentity{Auth: proto.SSHAuthPassword}

	err := validateSSHSpawn(p)
	if !errors.Is(err, ErrBadSSHParams) {
		t.Fatalf("validateSSHSpawn accepted a password identity with no credential reference; want ErrBadSSHParams, got %v", err)
	}
}
