package app

import (
	"context"

	"github.com/shady2k/nocx/internal/credential"
)

// openPasswordFixturePassword and rememberedPassword are untagged because
// stands in both build states spend them: the live password-open stand
// (nocx_local_ssh) and the ssh-pane screen-owner stand (default build).
const openPasswordFixturePassword = "e2e-password-42"

// rememberedPassword is the stored-secret half of ADR-0017: the material a
// remembered connection password resolves to, with no person in the loop.
type rememberedPassword struct{ value string }

func (r rememberedPassword) Resolve(context.Context, credential.SecretID, credential.Stance) (credential.Secret, error) {
	return credential.NewSecret(r.value), nil
}
