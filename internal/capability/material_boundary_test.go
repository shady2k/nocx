package capability_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// NOTHING UNDER A DOMAIN GATE MAY WAIT FOR THE UNLOCK (nocx-o3606).
//
// Every operation in this package runs its callback while holding its
// composite admission — the domain gates and the lane — and the vault gate
// is capacity one. credential.Resolver's operation stance asks the vault to
// become unsealed and BLOCKS until a person answers the dialog, so a resolve
// from in here holds the gate that vault.unseal needs and the dialog cannot
// be satisfied at all.
//
// The rule is therefore structural rather than remembered: this package does
// not hold a Resolver. Material is resolved by the caller, before or after
// the operation, never inside it — the shape endpoints.probe already had and
// the shape the open's PHASE TWO dial was split into for the same reason.
//
// credential.SecretStore is fine and deliberate: it mutates and answers
// existence, and its Get was removed precisely so a stanceless read cannot
// compile.
func TestCapabilityHoldsNoMaterialResolver(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, readErr := os.ReadFile(path) //nolint:gosec // WalkDir confines path to this package's own directory
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(body), "credential.Resolver") {
			t.Errorf("%s holds a credential.Resolver: an operation-stance read from "+
				"inside an admission blocks on the unlock while holding the gate "+
				"vault.unseal needs (nocx-o3606)", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan the package: %v", err)
	}
}
