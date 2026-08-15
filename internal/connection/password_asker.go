package connection

import (
	"context"
	"errors"
	"fmt"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/vault"
)

// SecretCreator is the vault's named-create surface the remember path uses:
// a stored password must own a display name (ADR-0016) so the Secrets page
// can show it, and the name is keyed per account. *vault.Vault satisfies it.
// Behind an interface so connection stays replaceable (AD-8), the same way
// the resolver's other stores are.
type SecretCreator interface {
	CreateNamedResolved(ctx context.Context, value credential.Secret, meta vault.SecretMeta) (credential.SecretID, string, error)
}

// passwordAsker implements ssh.ConnectionPasswordRequester for ONE saved
// profile: it asks the renderer over the wire (the transport asker), and on
// remember stores the answer as a vault secret the profile references
// (ADR-0017 §1), keyed per account — the same host with a different user is
// a different secret (tabby's passwordStorage model).
//
// The ask itself is the wire's business (three distinct outcomes: no client
// connected, vault sealed, prompt cancelled); this type owns the remember —
// storing material, binding the profile reference, and refusing to claim
// success until both halves landed.
type passwordAsker struct {
	r         *Resolver
	profileID string
}

// RequestConnectionPassword asks the renderer for the password, then honors
// the answer's Remember request. It returns the answer the auth attempt
// should use; on remember it first makes sure the profile will resolve
// silently next time — the vault secret exists AND the profile references
// it — and fails with a distinct message when it cannot.
func (p *passwordAsker) RequestConnectionPassword(ctx context.Context, req ssh.PasswordRequest) (ssh.PasswordAnswer, error) {
	ans, err := p.r.asker(ctx, req)
	if err != nil {
		// No client connected / prompt cancelled — propagate the wire's
		// own outcome; the connection fails with that reason.
		return ssh.PasswordAnswer{}, err
	}
	if !ans.Remember {
		// Use-once: the answer feeds this auth attempt and nothing else.
		return ans, nil
	}
	if p.r.creator == nil {
		return ssh.PasswordAnswer{}, errors.New("the connection password was not saved: no secret store wired")
	}

	// Per-account keying: the secret's display name is user@host, so the
	// same host under a different user is a different secret. The vault
	// resolves name collisions atomically and reports the name it used.
	id, _, err := p.r.createNamedWithUnlock(ctx, credential.NewSecret(ans.Password), vault.SecretMeta{
		Name: accountName(req.User, req.Host),
		Kind: vault.KindPassword,
	})
	if err != nil {
		return ssh.PasswordAnswer{}, err
	}

	// The closing event of the remember: the profile references the new
	// secret (ADR-0017), so the next open resolves it silently. A secret
	// that exists but nothing references is an orphan, and an asker that
	// reported "remembered" before this landed would be lying.
	if err := p.bindPasswordSecret(ctx, id); err != nil {
		return ssh.PasswordAnswer{}, err
	}
	return ans, nil
}

// bindPasswordSecret points the profile's password binding at the stored
// secret. The profile's OWN options are updated (never a group default: the
// remember is about this connection, not about the group it inherits from).
func (p *passwordAsker) bindPasswordSecret(ctx context.Context, id credential.SecretID) error {
	prof, err := p.r.findProfile(p.profileID)
	if err != nil {
		return fmt.Errorf("the connection password was saved but could not be bound to the connection: %w", err)
	}
	prof.Options.PasswordSecret = string(id)
	if err := p.r.profiles.UpdateProfile(prof); err != nil {
		return fmt.Errorf("the connection password was saved but could not be bound to the connection: %w", err)
	}
	return nil
}

// createNamedWithUnlock creates the secret. The unlock is NOT this
// layer's: a sealed vault is a sealed-vault failure, normalized by the
// transport's dispatcher seam into the canonical error the renderer turns
// into the unlock prompt, and the whole call (the connection open) is
// re-sent once the vault answers (ADR-0032). The error keeps the wrap so
// errors.Is still finds ErrVaultSealed through the ssh auth chain.
func (r *Resolver) createNamedWithUnlock(ctx context.Context, value credential.Secret, meta vault.SecretMeta) (credential.SecretID, string, error) {
	id, name, err := r.creator.CreateNamedResolved(ctx, value, meta)
	if err != nil {
		return "", "", fmt.Errorf("the connection password was not saved: %w", err)
	}
	return id, name, nil
}

// accountName is the per-account key a remembered password is stored under:
// the same host with a different user is a different secret, and the name
// must say which one this is.
func accountName(user, host string) string {
	return user + "@" + host
}

// askerFor binds the shared resolver to one profile so RequestConnectionPassword
// knows which profile to update on remember. The profile id is decided here,
// at resolution time, never re-derived from the wire request.
func (r *Resolver) askerFor(profileID string) ssh.ConnectionPasswordRequester {
	return &passwordAsker{r: r, profileID: profileID}
}

var _ ssh.ConnectionPasswordRequester = (*passwordAsker)(nil)
