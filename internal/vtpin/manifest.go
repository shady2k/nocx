// Package vtpin owns the pin of nocx's one statically linked C dependency:
// ghostty's libghostty-vt (ADR-0065, "Holding an unstable dependency").
//
// The pin is a committed document, third_party/libghostty-vt/MANIFEST.json: the
// upstream commit, the exact Zig, the build flags, and — per target — the
// sha256 of the archive and of its matched headers. Everything that has to
// agree about those bytes reads that file: the fetch that puts them in the
// tree, the recipe that rebuilds them, and the link probe that proves they
// link. Nothing here is a second answer to a question the manifest already
// answers.
//
// The manifest is data, and this package is the only code that knows its
// shape. Two decisions in it are worth stating where a reader of the file will
// not see them:
//
//   - There are TWO Linux archives per architecture, not one. The shipped
//     helper must be static, which on Linux means the musl ABI; the ordinary
//     native build (go build -tags gtk3, go test -race) is glibc. An archive's
//     libc is baked into its objects — the pair is the same size and different
//     bytes, and a glibc-linked helper carries PT_INTERP (nocx-cm1ac,
//     .internal/spikes/buildmatrix). So the manifest names six artifacts for
//     four Go targets, and a target's Libc says which one it is for.
//
//   - The hashes are the CANONICAL builder's: a Linux host, Zig 0.16.0,
//     the recorded flags. Measured on 2026-09-13: the same recipe on macOS
//     produces darwin archives of a different size, so "reproducible" here
//     means reproducible on the builder that published them, and
//     RecipeResult says so rather than pretending otherwise.
package vtpin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Schema is the manifest format this package reads and writes.
const Schema = 1

// ManifestPath is where the pin lives, relative to the repository root. It is
// stated here rather than in each caller because the Makefile, CI, the release
// workflow and the recipe all have to name the same file.
const ManifestPath = "third_party/libghostty-vt/MANIFEST.json"

// Manifest is the whole of third_party/libghostty-vt/MANIFEST.json.
type Manifest struct {
	Schema     int       `json:"schema"`
	Dependency string    `json:"dependency"`
	Upstream   Upstream  `json:"upstream"`
	Toolchain  Toolchain `json:"toolchain"`
	Build      Build     `json:"build"`
	Release    Release   `json:"release"`
	Licenses   Asset     `json:"licenses"`
	Targets    []Target  `json:"targets"`
}

// Upstream identifies the source the archives are built from: a FORK of
// ghostty, at a commit that carries nocx's patches on top of an upstream one.
//
// THERE IS NO SOURCE TARBALL ASSET ANY MORE, and this is where that is decided.
// ADR-0065 point 1 asks for a controlled copy of the source rather than a SHA
// on somebody else's host; the fork's git history at the pinned tag IS that
// copy, in a repository the archives are published from, so a second 39 MB
// tarball in the same release would be a copy of a copy — and the one thing a
// reader cannot diff against anything is the file nobody builds from.
type Upstream struct {
	// Repository is the fork the archives are built from.
	Repository string `json:"repository"`
	// Commit is the FORK commit the archives are built from. It carries the
	// patches below on top of BaseCommit.
	Commit string `json:"commit"`
	// BaseCommit is the upstream commit the fork's tag pointed at, recorded
	// separately so a reader can see what nocx changed and diff it.
	BaseCommit string `json:"baseCommit"`
	// Patch describes what the fork adds, in one line. It exists so that a
	// patched pin cannot be read as an unpatched one: a test refuses a commit
	// that differs from BaseCommit without saying what the difference is.
	Patch   string `json:"patch"`
	Version string `json:"version"`
	License string `json:"license"`
}

// Toolchain is the compiler that produced the archives. It is pinned because
// ghostty pins it: build.zig.zon declares minimum_zig_version, and a different
// Zig is different bytes.
type Toolchain struct {
	Zig string `json:"zig"`
}

// Build is the recipe's input: the flags, and where the build leaves the two
// artifacts a consumer needs.
type Build struct {
	// Command is the build spelled out, {zigTarget} standing where the one
	// per-target input goes. It is prose for a reader AND the recipe's
	// reference: the recipe passes Flags verbatim.
	Command string `json:"command"`
	// Flags are the flags after `zig build`, verbatim and in order.
	Flags []string `json:"flags"`
	// Archive and Headers are build outputs, relative to the source checkout.
	Archive string `json:"archive"`
	Headers string `json:"headers"`
	// CanonicalHost names the builder the hashes came from, and nothing more
	// than that: "linux/amd64, Zig 0.16.0".
	//
	// THERE ARE DELIBERATELY NO PATHS HERE, and no promise that a rebuild
	// reproduces these bytes. Measured 2026-09-13: two builds of one archive
	// from the same build root, the same Zig and the same flags differ in 60
	// bytes, all of them inside Zig's own .zig-cache/o/<key> object directory
	// names, which the objects embed and which source + toolchain + flags do
	// not determine. A pinned path would name a configuration under which the
	// bytes STILL differ, so it would read as a reproduction recipe while
	// being none. What the pin guarantees is the FILE: the fetch verifies this
	// sha256 or refuses. README.md, "Reproducibility, stated exactly".
	CanonicalHost string `json:"canonicalHost"`
}

// Release is where the published artifacts live. The tag is fixed per pin, so
// a fetch is a URL a reader can construct by hand.
//
// THERE IS NO PRERELEASE FLAG ANY MORE (nocx-ygxjv.14). It existed for one
// reason: the archives used to be published as a release of `nocx` itself, and
// the updater resolves https://github.com/shady2k/nocx/releases/latest/download
// (internal/update), where GitHub's `latest` skips prereleases. The archives
// live in the FORK now, which the updater does not look at, so the flag guarded
// nothing — and a field that guards nothing is a field the next reader has to
// re-derive the truth of.
type Release struct {
	Tag string `json:"tag"`
	// AssetURLTemplate uses {asset} in place of an asset name.
	AssetURLTemplate string `json:"assetURLTemplate"`
}

// Asset is one published file: its sha256 and its size.
//
// THE NAME IS NOT IN THE FILE, deliberately. A name in a manifest is a second
// answer to a question the derivation already answers, and the two drift: the
// first version of this file stored names built from a 7-character commit
// prefix while the derivation used 12, so a recipe wrote files the fetch then
// could not find. Hydrate fills it in from the commit, once, and nothing can
// disagree with it afterwards.
type Asset struct {
	Name   string `json:"-"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// Target is one build of the library.
type Target struct {
	// Name is the artifact's name and the vendor subdirectory.
	Name string `json:"name"`
	// GOOS/GOARCH are the Go target it serves; Libc is what its objects are
	// compiled against: musl (a static helper), glibc (native Linux builds)
	// or system (macOS, where the OS supplies the only libc there is).
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
	Libc      string `json:"libc"`
	ZigTarget string `json:"zigTarget"`
	Archive   Asset  `json:"archive"`
	Headers   Asset  `json:"headers"`
}

// Libc values.
const (
	LibcMusl   = "musl"
	LibcGlibc  = "glibc"
	LibcSystem = "system"
)

var (
	commitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256RE = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Load reads and validates a manifest.
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the manifest is the file the caller named, from a command line or the Makefile
	if err != nil {
		return nil, err
	}
	var m Manifest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	m.hydrate()
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &m, nil
}

// hydrate names every asset from the commit and the target list. It is the only
// assignment of Asset.Name outside RefreshFromDist, which calls it too.
func (m *Manifest) hydrate() {
	m.Licenses.Name = m.LicensesAssetName()
	for i := range m.Targets {
		t := &m.Targets[i]
		t.Archive.Name = m.ArchiveAssetName(*t)
		t.Headers.Name = m.HeadersAssetName(*t)
	}
}

// Save writes the manifest with stable field order and stable formatting. The
// repository formats JSON with prettier, so a pin that changes the file leaves
// a diff a person reads; run the repo's formatter afterwards if it has an
// opinion (third_party/libghostty-vt/README.md says which command).
func (m *Manifest) Save(path string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	// 0644 on purpose: this is a committed document, not a secret, and a
	// repository file another account may not read is a file a tool of theirs
	// cannot verify.
	return os.WriteFile(path, append(data, '\n'), 0o644) //nolint:gosec // a committed document, world-readable on purpose
}

// Validate answers whether the manifest is complete and internally consistent.
// It is deliberately strict: a manifest with a target whose hash is absent is
// a fetch that would verify nothing.
func (m *Manifest) Validate() error {
	if m.Schema != Schema {
		return fmt.Errorf("schema %d, want %d", m.Schema, Schema)
	}
	if m.Dependency == "" {
		return fmt.Errorf("dependency is empty")
	}
	if !commitRE.MatchString(m.Upstream.Commit) {
		return fmt.Errorf("upstream.commit %q is not a full 40-hex commit", m.Upstream.Commit)
	}
	if !commitRE.MatchString(m.Upstream.BaseCommit) {
		return fmt.Errorf("upstream.baseCommit %q is not a full 40-hex commit: the pin has to say which "+
			"upstream commit the fork's patches sit on", m.Upstream.BaseCommit)
	}
	// A patched pin must say what the patch is, and an unpatched one must not
	// claim a patch: this is the pair that makes "what did nocx change" readable
	// from the manifest alone.
	switch {
	case m.Upstream.Commit != m.Upstream.BaseCommit && strings.TrimSpace(m.Upstream.Patch) == "":
		return fmt.Errorf("upstream.commit is %s and baseCommit is %s, so the pin is patched, and upstream.patch "+
			"does not say what the patch is", m.Upstream.Commit, m.Upstream.BaseCommit)
	case m.Upstream.Commit == m.Upstream.BaseCommit && strings.TrimSpace(m.Upstream.Patch) != "":
		return fmt.Errorf("upstream.commit equals baseCommit, so nothing is patched, and upstream.patch describes one: %q",
			m.Upstream.Patch)
	}
	if m.Toolchain.Zig == "" {
		return fmt.Errorf("toolchain.zig is empty")
	}
	if len(m.Build.Flags) == 0 || m.Build.Archive == "" || m.Build.Headers == "" {
		return fmt.Errorf("build is incomplete: flags, archive and headers are all required")
	}
	if m.Build.CanonicalHost == "" {
		return fmt.Errorf("build.canonicalHost is empty: the hashes belong to some builder, and which one is evidence")
	}
	if m.Release.Tag == "" || !strings.Contains(m.Release.AssetURLTemplate, "{asset}") {
		return fmt.Errorf("release needs a tag and an assetURLTemplate containing {asset}")
	}
	if err := checkAsset("licenses", m.Licenses); err != nil {
		return err
	}
	if len(m.Targets) == 0 {
		return fmt.Errorf("no targets")
	}
	seen := make(map[string]bool, len(m.Targets))
	for _, t := range m.Targets {
		if t.Name == "" || t.GOOS == "" || t.GOARCH == "" || t.ZigTarget == "" {
			return fmt.Errorf("target %q is incomplete", t.Name)
		}
		switch t.Libc {
		case LibcMusl, LibcGlibc, LibcSystem:
		default:
			return fmt.Errorf("target %s: libc %q, want %s, %s or %s", t.Name, t.Libc, LibcMusl, LibcGlibc, LibcSystem)
		}
		if seen[t.Name] {
			return fmt.Errorf("target %s is declared twice", t.Name)
		}
		seen[t.Name] = true
		if err := checkAsset(t.Name+".archive", t.Archive); err != nil {
			return err
		}
		if err := checkAsset(t.Name+".headers", t.Headers); err != nil {
			return err
		}
	}
	return nil
}

func checkAsset(what string, a Asset) error {
	if a.Name == "" {
		return fmt.Errorf("%s: no asset name", what)
	}
	if !sha256RE.MatchString(a.SHA256) {
		return fmt.Errorf("%s: sha256 %q is not 64 hex characters", what, a.SHA256)
	}
	if a.Bytes <= 0 {
		return fmt.Errorf("%s: bytes %d", what, a.Bytes)
	}
	return nil
}

// Target returns the build serving a Go target as the SHIPPED HELPER needs it:
// the static archive on Linux, the system one on macOS. The helper is the only
// caller of a cross C compiler in this repository, so this — rather than a
// host build — is what "the target's library" means by default.
func (m *Manifest) Target(goos, goarch string) (Target, error) {
	return m.targetFor(goos, goarch, "")
}

// HostTarget returns the build a NATIVE compile of goos/goarch links: the
// glibc one on Linux, the system one on macOS. Both exist because the native
// toolchain is glibc while a helper that must run on an unknown host is not.
func (m *Manifest) HostTarget(goos, goarch string) (Target, error) {
	return m.targetFor(goos, goarch, LibcGlibc)
}

// TargetWithLibc names one build exactly, and is what a link probe uses: it
// builds BOTH Linux archives for one architecture — the static one to prove
// the helper's property, the glibc one to prove what the native toolchain
// links — and only the explicit libc can ask for the second.
func (m *Manifest) TargetWithLibc(goos, goarch, libc string) (Target, error) {
	return m.targetFor(goos, goarch, libc)
}

func (m *Manifest) targetFor(goos, goarch, libc string) (Target, error) {
	want := libc
	switch want {
	case "":
		// The helper's default, which is the only cross build this
		// repository makes: static on Linux, the system library on macOS.
		want = LibcGlibc
		if goos == "linux" {
			want = LibcMusl
		}
	case LibcMusl, LibcGlibc, LibcSystem:
	default:
		return Target{}, fmt.Errorf("libc %q, want %s, %s or %s", libc, LibcMusl, LibcGlibc, LibcSystem)
	}
	if goos == "darwin" {
		want = LibcSystem
	}
	for _, t := range m.Targets {
		if t.GOOS == goos && t.GOARCH == goarch && t.Libc == want {
			return t, nil
		}
	}
	return Target{}, fmt.Errorf("no %s/%s target with libc %s in the manifest", goos, goarch, want)
}

// ShortCommit is the commit prefix the asset names carry.
func (m *Manifest) ShortCommit() string {
	if len(m.Upstream.Commit) < 12 {
		return m.Upstream.Commit
	}
	return m.Upstream.Commit[:12]
}

// ArchiveAssetName and HeadersAssetName are how a target's two files are named
// in the release. Derived rather than stored so a pin can never name a file it
// did not hash, and a published release can never be missing one a manifest
// names.
func (m *Manifest) ArchiveAssetName(t Target) string {
	return fmt.Sprintf("libghostty-vt-%s-%s.a", m.ShortCommit(), t.Name)
}

// HeadersAssetName is the deterministic tar.gz of the headers installed
// alongside the archive — "its matched headers", so a consumer cannot compile
// one pin's headers against another's objects.
func (m *Manifest) HeadersAssetName(t Target) string {
	return fmt.Sprintf("libghostty-vt-%s-%s-headers.tar.gz", m.ShortCommit(), t.Name)
}

// LicensesAssetName is the generated THIRD_PARTY_LICENSES document — the
// licenses of everything the archives statically link, so a binary that ships
// them can ship them with it. It is derived from the commit like the rest, so
// the release, the fetch and the recipe cannot disagree about its name.
func (m *Manifest) LicensesAssetName() string {
	return fmt.Sprintf("libghostty-vt-%s-THIRD_PARTY_LICENSES.txt", m.ShortCommit())
}

// DistFiles are the files a published release carries, in a stable order: the
// licenses, then each target's archive and headers.
func (m *Manifest) DistFiles() []string {
	names := []string{m.LicensesAssetName()}
	for _, t := range m.Targets {
		names = append(names, m.ArchiveAssetName(t), m.HeadersAssetName(t))
	}
	return names
}

// RefreshFromDist fills in every asset's name, sha256 and size from a
// directory the recipe wrote, and returns the names it saw. It fails on a
// missing file rather than recording a hash of nothing: the manifest is what
// the fetch verifies against, so a hole in it is a hole in the gate.
func (m *Manifest) RefreshFromDist(dist string) ([]string, error) {
	var names []string
	record := func(what, name string, into *Asset) error {
		path := filepath.Join(dist, name)
		sum, bytes, err := HashFile(path)
		if err != nil {
			return fmt.Errorf("%s (%s): %w", what, name, err)
		}
		*into = Asset{Name: name, SHA256: sum, Bytes: bytes}
		names = append(names, name)
		return nil
	}
	if err := record("licenses", m.LicensesAssetName(), &m.Licenses); err != nil {
		return nil, err
	}
	for i := range m.Targets {
		t := &m.Targets[i]
		if err := record("archive", m.ArchiveAssetName(*t), &t.Archive); err != nil {
			return nil, err
		}
		if err := record("headers", m.HeadersAssetName(*t), &t.Headers); err != nil {
			return nil, err
		}
	}
	return names, nil
}

// AssetBy checks the archive and headers of every target against the files in
// a directory, and returns the mismatches. An empty result is the whole
// reproducibility claim: these bytes are the bytes the manifest pins.
func (m *Manifest) AssetBy(dir string) ([]Mismatch, error) {
	var out []Mismatch
	check := func(what string, a Asset) error {
		sum, _, err := HashFile(filepath.Join(dir, a.Name))
		if err != nil {
			return err
		}
		if sum != a.SHA256 {
			out = append(out, Mismatch{Asset: what, Name: a.Name, Want: a.SHA256, Got: sum})
		}
		return nil
	}
	if err := check("licenses", m.Licenses); err != nil {
		return nil, err
	}
	for _, t := range m.Targets {
		if err := check(t.Name+" archive", t.Archive); err != nil {
			return nil, err
		}
		if err := check(t.Name+" headers", t.Headers); err != nil {
			return nil, err
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Asset < out[j].Asset })
	return out, nil
}

// Mismatch is one artifact whose bytes are not the pinned ones.
type Mismatch struct {
	Asset string
	Name  string
	Want  string
	Got   string
}

func (m Mismatch) String() string {
	return fmt.Sprintf("%s (%s): manifest %s, built %s", m.Asset, m.Name, m.Want, m.Got)
}

// Layout — where a verified artifact lands. These three functions are the
// contract the CGo link files name, and they exist here so the path is
// written once: from internal/emulator/ghostty a link file reaches the archive
// as ${SRCDIR}/../../../build/libghostty-vt/vendor/<target>/libghostty-vt.a.
const (
	// DefaultRoot is the tree-local directory the fetch populates. It is
	// under build/, which git, eslint and prettier already ignore — a
	// downloaded artifact is build output, and teaching three walkers about a
	// fourth directory is how a directory ends up in a commit.
	DefaultRoot = "build/libghostty-vt"
	// ArchiveName is the file name inside a target's vendor directory.
	ArchiveName = "libghostty-vt.a"
)

// VendorDir is the directory holding one target's verified archive and headers.
func VendorDir(root, target string) string {
	return filepath.Join(root, "vendor", target)
}

// VendorArchivePath is the archive a CGo link file names.
func VendorArchivePath(root, target string) string {
	return filepath.Join(VendorDir(root, target), ArchiveName)
}

// VendorIncludeDir is the -I a CGo package compiling against it names.
func VendorIncludeDir(root, target string) string {
	return filepath.Join(VendorDir(root, target), "include")
}

// LicensesPath is where the verified THIRD_PARTY_LICENSES document lands. It
// sits beside the vendor directories rather than inside one, because one
// document covers every target's archive.
func (m *Manifest) LicensesPath(root string) string {
	return filepath.Join(root, m.Licenses.Name)
}

// HashFile returns the sha256 and size of a file.
func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path) //nolint:gosec // hashing a file is this function's whole purpose
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
