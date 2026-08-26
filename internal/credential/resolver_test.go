package credential_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
)

var errSealed = errors.New("sealed")

type stancedStore struct {
	sealed      bool
	ensureCalls int
	secret      credential.Secret
}

func (s *stancedStore) Create(context.Context, credential.Secret) (credential.SecretID, error) {
	return "sec:v1:file:test", nil
}
func (s *stancedStore) Delete(context.Context, credential.SecretID) error { return nil }
func (s *stancedStore) Exists(context.Context, credential.SecretID) (bool, error) {
	return true, nil
}

func (s *stancedStore) Get(context.Context, credential.SecretID) (credential.Secret, error) {
	if s.sealed {
		return credential.Secret{}, errSealed
	}
	return s.secret, nil
}

func (s *stancedStore) EnsureUnsealed(ctx context.Context, reason string) error {
	return s.ensure(ctx, reason)
}

func (s *stancedStore) ensure(context.Context, string) error {
	s.ensureCalls++
	s.sealed = false
	return nil
}

func TestResolverOperationEnsuresThenReads(t *testing.T) {
	store := &stancedStore{sealed: true, secret: credential.NewSecret("value")}
	resolver := credential.NewResolver(store, nil, store)

	got, err := resolver.Resolve(t.Context(), "sec:v1:file:test", credential.Operation("answer the ask"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if store.ensureCalls != 1 {
		t.Fatalf("EnsureUnsealed calls = %d, want 1", store.ensureCalls)
	}
	if got.IsEmpty() {
		t.Fatal("operation lost the resolved secret")
	}
}

func TestResolverReportNeverEnsures(t *testing.T) {
	store := &stancedStore{sealed: true}
	resolver := credential.NewResolver(store, func(err error) bool {
		return errors.Is(err, errSealed)
	}, store)

	_, err := resolver.Resolve(t.Context(), "sec:v1:file:test", credential.Report())
	if !errors.Is(err, credential.ErrSealedQuiet) {
		t.Fatalf("Resolve error = %v, want ErrSealedQuiet", err)
	}
	if store.ensureCalls != 0 {
		t.Fatalf("report raised unlock %d times", store.ensureCalls)
	}
}

func TestResolverRejectsUndeclaredStance(t *testing.T) {
	store := &stancedStore{secret: credential.NewSecret("value")}
	resolver := credential.NewResolver(store, nil, store)

	_, err := resolver.Resolve(t.Context(), "sec:v1:file:test", credential.Stance{})
	if !errors.Is(err, credential.ErrStanceUndeclared) {
		t.Fatalf("Resolve error = %v, want ErrStanceUndeclared", err)
	}
	if store.ensureCalls != 0 {
		t.Fatalf("undeclared stance raised unlock %d times", store.ensureCalls)
	}
}

func TestResolverCallWithoutStanceDoesNotCompile(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	source, err := os.ReadFile("testdata/stanceless/stanceless.go.txt")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	dir := t.TempDir()
	goMod := "module github.com/shady2k/nocx/internal/stancelessfixture\n\ngo 1.26\n\n" +
		"require github.com/shady2k/nocx v0.0.0\n" +
		"replace github.com/shady2k/nocx => " + root + "\n"
	if writeErr := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o600); writeErr != nil {
		t.Fatalf("write fixture go.mod: %v", writeErr)
	}
	goSum, err := os.ReadFile(filepath.Join(root, "go.sum")) //nolint:gosec // root is the fixed repository root
	if err != nil {
		t.Fatalf("read repo go.sum: %v", err)
	}
	if writeErr := os.WriteFile(filepath.Join(dir, "go.sum"), goSum, 0o600); writeErr != nil {
		t.Fatalf("write fixture go.sum: %v", writeErr)
	}
	if writeErr := os.WriteFile(filepath.Join(dir, "stanceless.go"), source, 0o600); writeErr != nil {
		t.Fatalf("write fixture source: %v", writeErr)
	}
	cmd := exec.Command("go", "test", "-mod=mod", ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("stanceless fixture compiled")
	}
	if !strings.Contains(string(out), "not enough arguments in call to r.Resolve") {
		t.Fatalf("fixture failed for the wrong reason:\n%s", out)
	}
}

func TestProductDoesNotTellThePersonToFindTheVault(t *testing.T) {
	const forbidden = "the vault is locked — unlock it and ask again"
	root := filepath.Clean("../..")
	for _, rel := range []string{"internal", filepath.Join("frontend", "src")} {
		err := filepath.WalkDir(filepath.Join(root, rel), func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			if strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, ".test.ts") {
				return nil
			}
			body, readErr := os.ReadFile(path) //nolint:gosec // WalkDir confines path to product source roots
			if readErr != nil {
				return readErr
			}
			if strings.Contains(string(body), forbidden) {
				t.Errorf("forbidden unlock instruction survives in %s", path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", rel, err)
		}
	}
}
