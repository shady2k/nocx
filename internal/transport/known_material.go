package transport

// The vault adapter for the egress gate's known-material comparison (design
// §7.1, bead nocx-0p7y2): "the vault knows the real values, and a
// comparison beats any pattern." The comparison happens in the backend and
// nothing leaves — the values cross only into the comparison, inside
// credential.Secret.Use, and no further (ADR-0011 §2); the name reported is
// the vault's own catalogue name (ADR-0016). A failure here makes the
// assistant gate withhold the result — the run fails closed rather than let
// unscreened material leave for a provider.
import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/vault"
)

// NewVaultKnownMaterial adapts the vault to assistant.KnownMaterial: the
// egress gate's seam for "does this text contain a value the vault holds".
// The composition root wires it where it wires the vault; nil is never
// passed where a grant-carrying run may execute tools — the middleware
// fails closed on nil (assistant.newPolicyMiddleware), and that rule is
// deliberately not weakened here.
//
// resolver is the stanced material-read seam. unsealer is the matching
// operation-unlock seam needed before the metadata inventory can be read.
// Both are explicit so a headless resolver remains headless.
func NewVaultKnownMaterial(
	v *vault.Vault, resolver credential.Resolver, unsealer credential.Unsealer,
) assistant.KnownMaterial {
	return &vaultKnownMaterial{v: v, resolver: resolver, unsealer: unsealer}
}

// vaultKnownMaterial is the adapter. It reads the vault's catalogue fresh on
// every call: a secret replaced mid-run must be visible to the next
// screening, and nothing here is cached long enough to go stale.
type vaultKnownMaterial struct {
	v        *vault.Vault
	resolver credential.Resolver
	unsealer credential.Unsealer
}

// Material screening is an operation: if the vault is sealed, the person must
// be able to unlock it before the result can be compared. This call is outside
// any capability admission — the assistant stream task owns the only
// admission, and it is not held while the middleware runs a tool or screens
// its result (ADR-0032 amendment).
const knownMaterialReason = "screen the tool result"

// FindKnown reports the byte spans of text that match a secret the vault
// holds, with the catalogue name of each matched secret. Errors — a sealed
// vault, a provider that refuses a read, an inventory row that cannot be
// resolved — are returned, never skipped: a row the adapter cannot compare
// is a secret the gate cannot see, and a miss off-machine is invisible and
// permanent.
func (k *vaultKnownMaterial) FindKnown(ctx context.Context, text string) ([]assistant.KnownMatch, error) {
	if k.v == nil || k.resolver == nil {
		return nil, errors.New("known material: stanced vault resolver is not wired")
	}
	if k.unsealer != nil {
		if err := k.unsealer.EnsureUnsealed(ctx, knownMaterialReason); err != nil {
			return nil, err
		}
	}
	inventory, err := k.v.BuildInventory(ctx, nil)
	if err != nil {
		return nil, err
	}
	var matches []assistant.KnownMatch
	for _, entry := range inventory {
		id, ok := k.v.ResolveRow(entry.ID, nil)
		if !ok {
			// Unreachable for the catalogue BuildInventory enumerates (a
			// record's row always resolves to itself), but fail closed
			// rather than let listed material screen as compared when it
			// was not.
			return nil, fmt.Errorf("known material: inventory row %q does not resolve — the listed secret could not be compared", entry.ID)
		}
		sec, err := k.resolver.Resolve(ctx, id, credential.Operation(knownMaterialReason))
		if err != nil {
			return nil, err
		}
		if err := sec.Use(func(b []byte) error {
			if len(b) == 0 {
				return nil // an empty value matches everything; it is nothing to compare
			}
			value := string(b)
			from := 0
			for {
				i := strings.Index(text[from:], value)
				if i < 0 {
					return nil
				}
				start := from + i
				matches = append(matches, assistant.KnownMatch{
					Start:      start,
					End:        start + len(value),
					SecretName: entry.Name,
				})
				from = start + len(value)
			}
		}); err != nil {
			return nil, err
		}
	}
	if len(matches) == 0 {
		return nil, nil
	}
	return matches, nil
}
