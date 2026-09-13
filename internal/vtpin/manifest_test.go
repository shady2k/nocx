package vtpin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validManifest() *Manifest {
	return &Manifest{
		Schema:     Schema,
		Dependency: "libghostty-vt",
		Upstream: Upstream{
			Repository: "https://example.invalid/ghostty",
			Commit:     "e2e53f861482e080bf45054ba49ef471f9849937",
			Version:    "1.3.2-dev",
			License:    "MIT",
		},
		Toolchain: Toolchain{Zig: "0.16.0"},
		Build: Build{
			Command:       "zig build {flags} -Dtarget={zigTarget}",
			Flags:         []string{"-Demit-lib-vt=true"},
			Archive:       "zig-out/lib/libghostty-vt.a",
			Headers:       "zig-out/include/ghostty",
			CanonicalHost: "linux/amd64, Zig 0.16.0",
		},
		Release: Release{
			Tag:              "libghostty-vt-e2e53f86",
			AssetURLTemplate: "https://example.invalid/releases/download/libghostty-vt-e2e53f86/{asset}",
			Prerelease:       true,
		},
		Source: Asset{Name: "ghostty-e2e53f861482.tar.gz", SHA256: hex64("a"), Bytes: 1},
		Targets: []Target{
			{
				Name: "linux-amd64", GOOS: "linux", GOARCH: "amd64", Libc: LibcMusl, ZigTarget: "x86_64-linux-musl",
				Archive: Asset{Name: "linux-amd64.a", SHA256: hex64("b"), Bytes: 2},
				Headers: Asset{Name: "linux-amd64-headers.tar.gz", SHA256: hex64("c"), Bytes: 3},
			},
			{
				Name: "linux-amd64-gnu", GOOS: "linux", GOARCH: "amd64", Libc: LibcGlibc, ZigTarget: "x86_64-linux",
				Archive: Asset{Name: "linux-amd64-gnu.a", SHA256: hex64("d"), Bytes: 4},
				Headers: Asset{Name: "linux-amd64-gnu-headers.tar.gz", SHA256: hex64("e"), Bytes: 5},
			},
			{
				Name: "darwin-arm64", GOOS: "darwin", GOARCH: "arm64", Libc: LibcSystem, ZigTarget: "aarch64-macos",
				Archive: Asset{Name: "darwin-arm64.a", SHA256: hex64("f"), Bytes: 6},
				Headers: Asset{Name: "darwin-arm64-headers.tar.gz", SHA256: hex64("0"), Bytes: 7},
			},
		},
	}
}

func hex64(c string) string {
	out := make([]byte, 64)
	for i := range out {
		out[i] = c[0]
	}
	return string(out)
}

func TestValidateRejectsIncompleteManifests(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Manifest)
	}{
		{"wrong schema", func(m *Manifest) { m.Schema = 99 }},
		{"short commit", func(m *Manifest) { m.Upstream.Commit = "e2e53f8" }},
		{"no zig", func(m *Manifest) { m.Toolchain.Zig = "" }},
		{"no flags", func(m *Manifest) { m.Build.Flags = nil }},
		{"url without placeholder", func(m *Manifest) { m.Release.AssetURLTemplate = "https://example.invalid/" + "x" }},
		{"unsourced headers", func(m *Manifest) { m.Targets[0].Headers = Asset{} }},
		{"bad hash", func(m *Manifest) { m.Targets[0].Archive.SHA256 = "not-a-hash" }},
		{"zero bytes", func(m *Manifest) { m.Targets[0].Archive.Bytes = 0 }},
		{"unknown libc", func(m *Manifest) { m.Targets[0].Libc = "uclibc" }},
		{"no canonical host", func(m *Manifest) { m.Build.CanonicalHost = "" }},
		{"duplicate target", func(m *Manifest) { m.Targets[1].Name = m.Targets[0].Name }},
		{"no targets", func(m *Manifest) { m.Targets = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := validManifest()
			tc.change(m)
			if err := m.Validate(); err == nil {
				t.Fatalf("%s: Validate accepted the manifest", tc.name)
			}
		})
	}
}

func TestTargetPicksTheStaticArchiveForTheHelper(t *testing.T) {
	m := validManifest()

	helper, err := m.Target("linux", "amd64")
	if err != nil {
		t.Fatalf("Target(linux, amd64): %v", err)
	}
	if helper.Libc != LibcMusl {
		t.Fatalf("the helper's linux/amd64 library has libc %q; a helper on an unknown host must link the static one", helper.Libc)
	}

	host, err := m.HostTarget("linux", "amd64")
	if err != nil {
		t.Fatalf("HostTarget(linux, amd64): %v", err)
	}
	if host.Libc != LibcGlibc {
		t.Fatalf("a native linux/amd64 build has libc %q, want glibc: the native toolchain is glibc and the two archives are different bytes", host.Libc)
	}
	if host.Name == helper.Name {
		t.Fatalf("one archive serves both the helper and the native build (%s) — the static property is not a build flag", host.Name)
	}

	darwin, err := m.Target("darwin", "arm64")
	if err != nil {
		t.Fatalf("Target(darwin, arm64): %v", err)
	}
	if darwin.Libc != LibcSystem {
		t.Fatalf("darwin library has libc %q, want system", darwin.Libc)
	}

	if _, err := m.Target("windows", "amd64"); err == nil {
		t.Fatal("Target accepted a platform the manifest has no build for")
	}
}

func TestAssetNamesAreDistinctAndStable(t *testing.T) {
	m := validManifest()
	seen := map[string]string{}
	for _, target := range m.Targets {
		for _, name := range []string{m.ArchiveAssetName(target), m.HeadersAssetName(target)} {
			if other, dup := seen[name]; dup {
				t.Fatalf("asset %s names both %s and %s", name, other, target.Name)
			}
			seen[name] = target.Name
		}
	}
	if got, want := m.SourceAssetName(), "ghostty-e2e53f861482.tar.gz"; got != want {
		t.Fatalf("source asset name %q, want %q", got, want)
	}
	if got, want := len(m.DistFiles()), 2*len(m.Targets)+1; got != want {
		t.Fatalf("DistFiles lists %d files for %d targets; a published release would be missing one", got, len(m.Targets))
	}
}

func TestRefreshFromDistRecordsWhatTheRecipeBuilt(t *testing.T) {
	m := validManifest()
	dist := t.TempDir()
	// The names are the manifest's to derive: the recipe writes what the
	// manifest will name, so a file under any other name is a mismatch by
	// definition, not a silent absence.
	if err := os.WriteFile(filepath.Join(dist, m.SourceAssetName()), []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, target := range m.Targets {
		archive := []byte("archive-" + target.Name)
		if err := os.WriteFile(filepath.Join(dist, m.ArchiveAssetName(target)), archive, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dist, m.HeadersAssetName(target)), []byte("headers-"+target.Name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.RefreshFromDist(dist); err != nil {
		t.Fatalf("RefreshFromDist: %v", err)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("a manifest filled from a complete dist must validate: %v", err)
	}
	for _, target := range m.Targets {
		want, _, err := HashFile(filepath.Join(dist, m.ArchiveAssetName(target)))
		if err != nil {
			t.Fatal(err)
		}
		if target.Archive.SHA256 != want {
			t.Fatalf("%s archive hash %s, want %s", target.Name, target.Archive.SHA256, want)
		}
	}

	// And the negative: one file missing is a failure, never a manifest with
	// a hash of nothing in it.
	missing := validManifest()
	if _, err := missing.RefreshFromDist(t.TempDir()); err == nil {
		t.Fatal("RefreshFromDist accepted an empty dist directory")
	}
}

func TestAssetByNamesEveryMismatch(t *testing.T) {
	m := validManifest()
	dir := t.TempDir()
	for _, target := range m.Targets {
		if err := os.WriteFile(filepath.Join(dir, target.Archive.Name), []byte("archive"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, target.Headers.Name), []byte("headers"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Fill the manifest with the hashes of what is actually there: nothing
	// should be reported.
	for i := range m.Targets {
		for _, a := range []*Asset{&m.Targets[i].Archive, &m.Targets[i].Headers} {
			sum, size, err := HashFile(filepath.Join(dir, a.Name))
			if err != nil {
				t.Fatal(err)
			}
			a.SHA256, a.Bytes = sum, size
		}
	}
	mismatches, err := m.AssetBy(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(mismatches) != 0 {
		t.Fatalf("a directory holding exactly the pinned bytes reported %v", mismatches)
	}

	tampered := m.Targets[2].Archive.Name
	if writeErr := os.WriteFile(filepath.Join(dir, tampered), []byte("not the archive"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	mismatches, err = m.AssetBy(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(mismatches) != 1 || mismatches[0].Name != tampered {
		t.Fatalf("tampering with %s reported %v; want exactly that one", tampered, mismatches)
	}
}

// TestCommittedManifestIsComplete reads the real pin. It is the check that a
// pin can never be committed half-made — a target without a hash is a fetch
// that verifies nothing, and the file is hand-maintained apart from `pin`.
func TestCommittedManifestIsComplete(t *testing.T) {
	path := filepath.Join("..", "..", filepath.FromSlash(ManifestPath))
	m, err := Load(path)
	if err != nil {
		t.Fatalf("the committed manifest is not loadable: %v", err)
	}
	if len(m.Targets) != 6 {
		t.Fatalf("%d targets in the pin, want 6: two Linux architectures in glibc AND musl, plus both darwin ones", len(m.Targets))
	}
	for _, goarch := range []string{"amd64", "arm64"} {
		helper, err := m.Target("linux", goarch)
		if err != nil {
			t.Fatalf("linux/%s has no helper (static) build: %v", goarch, err)
		}
		if helper.ZigTarget == "" || !strings.HasSuffix(helper.ZigTarget, "-musl") {
			t.Fatalf("linux/%s helper archive builds for %q, which is not a musl triple", goarch, helper.ZigTarget)
		}
		if _, err := m.HostTarget("linux", goarch); err != nil {
			t.Fatalf("linux/%s has no native (glibc) build: %v", goarch, err)
		}
		if _, err := m.Target("darwin", goarch); err != nil {
			t.Fatalf("darwin/%s is missing: %v", goarch, err)
		}
	}
	if !m.Release.Prerelease {
		t.Fatal("the archive release is not marked prerelease: GitHub's releases/latest would then offer it as a product update (internal/update)")
	}
	if !strings.Contains(m.Release.AssetURLTemplate, m.Release.Tag) {
		t.Fatalf("asset URL template %q does not name the release tag %q", m.Release.AssetURLTemplate, m.Release.Tag)
	}
	if !strings.Contains(m.Build.Command, "{zigTarget}") {
		t.Fatalf("build command %q does not say where the per-target triple goes", m.Build.Command)
	}
}
