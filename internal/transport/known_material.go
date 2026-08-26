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
	"fmt"
	"strings"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/vault"
)

// NewVaultKnownMaterial adapts the vault to assistant.KnownMaterial: the
// egress gate's seam for "does this text contain a value the vault holds".
// The composition root wires it where it wires the vault; nil is never
// passed where a grant-carrying run may execute tools — the middleware
// fails closed on nil (assistant.newPolicyMiddleware), and that rule is
// deliberately not weakened here.
func NewVaultKnownMaterial(v *vault.Vault) assistant.KnownMaterial {
	return &vaultKnownMaterial{v: v}
}

// vaultKnownMaterial is the adapter. It reads the vault's catalogue fresh on
// every call: a secret replaced mid-run must be visible to the next
// screening, and nothing here is cached long enough to go stale.
type vaultKnownMaterial struct {
	v *vault.Vault
}

// FindKnown reports the byte spans of text that match a secret the vault
// holds, with the catalogue name of each matched secret. Errors — a sealed
// vault, a provider that refuses a read, an inventory row that cannot be
// resolved — are returned, never skipped: a row the adapter cannot compare
// is a secret the gate cannot see, and a miss off-machine is invisible and
// permanent.
func (k *vaultKnownMaterial) FindKnown(ctx context.Context, text string) ([]assistant.KnownMatch, error) {
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
		sec, err := k.v.Get(ctx, id)
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
