package transport

// The vault adapter's own tests (design §7.1): the BuildInventory →
// ResolveRow → Get → match round trip against a REAL vault, the spans and
// the catalogue names, and the sealed-vault fail-closed. The e2e readScreen
// tests prove the adapter screens clean text without false positives; these
// prove it finds the material when it is there.

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vault/file"
)

// adapterVault builds a real, unsealed vault in a temp dir — the same
// recipe the ask harness uses, so the adapter is exercised against the
// production provider, not a fake.
func adapterVault(t *testing.T) *vault.Vault {
	t.Helper()
	dir := t.TempDir()
	docStore := storage.NewDocumentStore(dir)
	reg, err := vault.NewRegistry(file.New(docStore, "vault-blob.json"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(nil, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, err := vault.New(docStore, reg, logger)
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	t.Cleanup(v.Close)
	if _, setupErr := v.Setup(t.Context(), vault.SetupRequest{Passphrase: "test"}); setupErr != nil {
		t.Fatalf("Setup: %v", setupErr)
	}
	return v
}

func createSecret(t *testing.T, v *vault.Vault, name, value string) {
	t.Helper()
	if _, err := v.CreateNamed(t.Context(), credential.NewSecret(value), vault.SecretMeta{Name: name, Kind: "password"}); err != nil {
		t.Fatalf("CreateNamed(%s): %v", name, err)
	}
}

// TestVaultKnownMaterial_FindsValueAtItsSpan is the heart of the adapter:
// a tool result containing a value the vault holds comes back as one match
// at the value's exact byte span, named with the vault's catalogue name
// (ADR-0016). The span slicing proves the finding LOCATES the value without
// carrying it — the matched text is the thing being withheld.
func TestVaultKnownMaterial_FindsValueAtItsSpan(t *testing.T) {
	v := adapterVault(t)
	createSecret(t, v, "github-token", "correct-horse-battery-9")

	k := NewVaultKnownMaterial(v)
	const text = "deploy key: correct-horse-battery-9 end"
	matches, err := k.FindKnown(t.Context(), text)
	if err != nil {
		t.Fatalf("FindKnown: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %+v, want exactly one", matches)
	}
	m := matches[0]
	if m.SecretName != "github-token" {
		t.Fatalf("match names %q, want the vault's catalogue name", m.SecretName)
	}
	want := strings.Index(text, "correct-horse-battery-9")
	if m.Start != want || m.End != want+len("correct-horse-battery-9") {
		t.Fatalf("match span = [%d,%d), want [%d,%d)", m.Start, m.End, want, want+len("correct-horse-battery-9"))
	}
	if got := text[m.Start:m.End]; got != "correct-horse-battery-9" {
		t.Fatalf("the span locates %q, not the value", got)
	}
}

// TestVaultKnownMaterial_AllOccurrencesReported: the same value twice in one
// result is two findings — the gate shows where EACH occurrence was.
func TestVaultKnownMaterial_AllOccurrencesReported(t *testing.T) {
	v := adapterVault(t)
	createSecret(t, v, "staging-key", "shh-secret-42")

	k := NewVaultKnownMaterial(v)
	const text = "a shh-secret-42 then shh-secret-42 again"
	matches, err := k.FindKnown(t.Context(), text)
	if err != nil {
		t.Fatalf("FindKnown: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("matches = %+v, want both occurrences", matches)
	}
	if matches[0].Start >= matches[1].Start {
		t.Fatalf("occurrences out of order: %+v", matches)
	}
}

// TestVaultKnownMaterial_CleanTextHasNoMatches is the paired end: text with
// no vault value reports nothing, and nothing is invented by the scan.
func TestVaultKnownMaterial_CleanTextHasNoMatches(t *testing.T) {
	v := adapterVault(t)
	createSecret(t, v, "github-token", "correct-horse-battery-9")

	k := NewVaultKnownMaterial(v)
	matches, err := k.FindKnown(t.Context(), "the file's contents, nothing else")
	if err != nil {
		t.Fatalf("FindKnown: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("clean text matched: %+v", matches)
	}
}

// TestVaultKnownMaterial_EmptyVaultReportsNothing: no secrets, no matches —
// the adapter does not fail a run for an empty catalogue.
func TestVaultKnownMaterial_EmptyVaultReportsNothing(t *testing.T) {
	v := adapterVault(t)
	k := NewVaultKnownMaterial(v)
	matches, err := k.FindKnown(t.Context(), "any text")
	if err != nil {
		t.Fatalf("FindKnown on an empty vault: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("empty vault matched: %+v", matches)
	}
}

// TestVaultKnownMaterial_SealedVaultFailsClosed: a sealed vault cannot be
// compared, and the adapter says so instead of pretending there is nothing
// to compare — the gate withholds the result, never lets it leave
// unscreened.
func TestVaultKnownMaterial_SealedVaultFailsClosed(t *testing.T) {
	v := adapterVault(t)
	createSecret(t, v, "github-token", "correct-horse-battery-9")
	v.Seal()

	k := NewVaultKnownMaterial(v)
	if _, err := k.FindKnown(t.Context(), "deploy key: correct-horse-battery-9"); err == nil {
		t.Fatal("sealed vault: FindKnown succeeded — the gate would have screened against nothing")
	}
}
