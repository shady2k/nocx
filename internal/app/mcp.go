package app

import (
	"context"

	"github.com/shady2k/nocx/internal/credential"
)

type mcpSystemTokenStore interface {
	ReplaceSystemSecret(context.Context, credential.SecretID, credential.Secret) error
}

// The runtime registers cancellation before asking for material. Its operation
// stance may raise the existing coalesced unlock, outside capability admissions.
type mcpCredentials struct {
	resolver credential.Resolver
	tokens   mcpSystemTokenStore
}

func (m mcpCredentials) ResolveSecret(ctx context.Context, ref string) (credential.Secret, error) {
	return m.resolver.Resolve(ctx, credential.SecretID(ref), credential.Operation("connect to an MCP server"))
}

// Renewal remains inside the runtime's registered, per-session operation. It
// preserves the opaque ID and must not re-enter its own mutation/drain barrier.
func (m mcpCredentials) ReplaceOAuthToken(ctx context.Context, ref string, material credential.Secret) error {
	return m.tokens.ReplaceSystemSecret(ctx, credential.SecretID(ref), material)
}
