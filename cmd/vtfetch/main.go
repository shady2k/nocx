// Command vtfetch holds nocx's pin of libghostty-vt: it verifies and downloads
// the pinned archives, names the files a release must carry, records the
// hashes a recipe produced, and answers what the resulting binaries are linked
// against.
//
// It is one binary rather than four scripts because every one of those jobs
// reads the same document — third_party/libghostty-vt/MANIFEST.json — and a
// shell implementation would either duplicate its fields or depend on jq and
// on a sha256sum/shasum spelling that differs between macOS and Linux.
//
// Usage:
//
//	vtfetch fetch    --root build/libghostty-vt [--base URL|DIR]
//	vtfetch plan     [--manifest P]
//	vtfetch pin      [--manifest P] --dist DIR
//	vtfetch pack-headers --dir DIR --out FILE
//	vtfetch licenses --source DIR --dist DIR --out FILE
//	vtfetch cc       --target GOOS/GOARCH [--zig BIN] [--stubs DIR]
//	vtfetch zig      [--bin BIN]
//	vtfetch inspect  [--require-static] [--symbol NAME] FILE...
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/shady2k/nocx/internal/vtpin"
)

// DefaultManifest is where the pin lives, relative to the repository root. Every
// command resolves it from the working directory, which is the module root for
// `go run` and for make. It is the package's constant, not a second copy of it:
// the Makefile, CI and the recipe all name this file too.
const DefaultManifest = vtpin.ManifestPath

// DefaultStubs is the macOS cross-link stub directory: two empty .tbd files
// that satisfy the linker's lookup for libresolv and CoreFoundation, which
// Go's own darwin runtime puts on every link line and a Linux host has no
// macOS SDK to supply. See third_party/libghostty-vt/README.md.
const DefaultStubs = "third_party/libghostty-vt/stubs"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "fetch":
		err = cmdFetch(args)
	case "plan":
		err = cmdPlan(args)
	case "meta":
		err = cmdMeta(args)
	case "pin":
		err = cmdPin(args)
	case "verify":
		err = cmdVerify(args)
	case "pack-headers":
		err = cmdPackHeaders(args)
	case "licenses":
		err = cmdLicenses(args)
	case "cc":
		err = cmdCC(args)
	case "zig":
		err = cmdZig(args)
	case "inspect":
		err = cmdInspect(args)
	case "-h", "--help", "help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "vtfetch:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: vtfetch <command> [flags]

  fetch         verify, and download what is missing, into --root
  plan          print the target table a recipe needs (TSV)
  meta          print the pin's scalar fields as key=value lines
  pin           record the hashes of --dist into the manifest
  verify        check --dist against the manifest, without writing
  pack-headers  pack a headers directory into a deterministic bundle
  licenses      generate THIRD_PARTY_LICENSES from the pin and the built archives
  cc            print the C compiler for one helper target
  zig           resolve the pinned Zig and refuse a different version
  inspect       report linkage (and symbols) of built files

Run "vtfetch <command> -h" for the flags of one command.
`)
}

func flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: vtfetch %s [flags]\n\n", name)
		fs.PrintDefaults()
	}
	return fs
}

func loadManifest(fs *flag.FlagSet, path *string) (*vtpin.Manifest, error) {
	abs, err := filepath.Abs(*path)
	if err != nil {
		return nil, err
	}
	m, err := vtpin.Load(abs)
	if err != nil {
		return nil, fmt.Errorf("%s: %w (run from the repository root, or pass --manifest)", *path, err)
	}
	return m, nil
}

func cmdFetch(args []string) error {
	fs := flags("fetch")
	manifest := fs.String("manifest", DefaultManifest, "the pin document")
	root := fs.String("root", vtpin.DefaultRoot, "where verified artifacts land")
	base := fs.String("base", "", "release URL or local directory (default: the manifest's asset URL template)")
	_ = fs.Parse(args)

	m, err := loadManifest(fs, manifest)
	if err != nil {
		return err
	}
	if *base == "" {
		*base = m.Release.AssetURLTemplate
	}
	fetcher := vtpin.NewFetcher(*base)
	opts := vtpin.FetchOptions{
		Log: func(format string, args ...any) { fmt.Printf(format+"\n", args...) },
	}
	if err := m.Fetch(*root, fetcher, opts); err != nil {
		return err
	}
	fmt.Printf("libghostty-vt %s verified in %s\n", m.ShortCommit(), *root)
	return nil
}

func cmdPlan(args []string) error {
	fs := flags("plan")
	manifest := fs.String("manifest", DefaultManifest, "the pin document")
	_ = fs.Parse(args)
	m, err := loadManifest(fs, manifest)
	if err != nil {
		return err
	}
	// TSV, and the recipe is the only reader: one line per target in the
	// manifest's order, so a shell loop can build them all without knowing
	// anything the manifest does not say. The licenses document is not here:
	// it is one file for every target, and the recipe reads its name from
	// `meta` because nothing about it is per-target.
	fmt.Println(strings.Join([]string{"kind", "name", "goos", "goarch", "libc", "zig_target", "archive_asset", "headers_asset"}, "\t"))
	for _, t := range m.Targets {
		fmt.Println(strings.Join([]string{"target", t.Name, t.GOOS, t.GOARCH, t.Libc, t.ZigTarget, m.ArchiveAssetName(t), m.HeadersAssetName(t)}, "\t"))
	}
	return nil
}

// cmdVerify answers the question a rebuild exists to answer: are these bytes
// the bytes the manifest pins? It is separate from `pin` on purpose — one
// writes, the other judges, and a recipe that updated the manifest as a side
// effect of building could never report a difference.
func cmdVerify(args []string) error {
	fs := flags("verify")
	manifest := fs.String("manifest", DefaultManifest, "the pin document")
	dist := fs.String("dist", "", "directory holding the built or downloaded assets")
	_ = fs.Parse(args)
	if *dist == "" {
		return errors.New("verify: --dist is required")
	}
	m, err := loadManifest(fs, manifest)
	if err != nil {
		return err
	}
	mismatches, err := m.AssetBy(*dist)
	if err != nil {
		return err
	}
	checked := 1 + 2*len(m.Targets)
	fmt.Println("ok", m.Licenses.Name)
	for _, t := range m.Targets {
		for _, a := range []vtpin.Asset{t.Archive, t.Headers} {
			fmt.Println("ok", a.Name)
		}
	}
	if len(mismatches) > 0 {
		for _, mismatch := range mismatches {
			fmt.Fprintln(os.Stderr, "MISMATCH", mismatch)
		}
		return fmt.Errorf("%d of %d files are not the pinned bytes", len(mismatches), checked)
	}
	fmt.Fprintf(os.Stderr, "%d files are the pinned bytes\n", checked)
	return nil
}

// cmdMeta prints the pin's scalar fields as key=value lines, for the shell
// scripts that need one of them (the recipe needs the commit and the source
// asset name) and must not grow their own JSON parser to get it. Every value
// is derived from the loaded manifest, so a script cannot read a field the
// manifest does not have.
func cmdMeta(args []string) error {
	fs := flags("meta")
	manifest := fs.String("manifest", DefaultManifest, "the pin document")
	_ = fs.Parse(args)
	m, err := loadManifest(fs, manifest)
	if err != nil {
		return err
	}
	rows := [][2]string{
		{"commit", m.Upstream.Commit},
		{"short", m.ShortCommit()},
		{"version", m.Upstream.Version},
		{"repository", m.Upstream.Repository},
		{"base_commit", m.Upstream.BaseCommit},
		{"patch", m.Upstream.Patch},
		{"zig", m.Toolchain.Zig},
		{"release_tag", m.Release.Tag},
		{"asset_url_template", m.Release.AssetURLTemplate},
		{"licenses_asset", m.LicensesAssetName()},
		{"licenses_sha256", m.Licenses.SHA256},
		{"build_command", m.Build.Command},
		{"build_flags", strings.Join(m.Build.Flags, " ")},
		{"archive_output", m.Build.Archive},
		{"headers_output", m.Build.Headers},
		{"canonical_host", m.Build.CanonicalHost},
	}
	for _, row := range rows {
		fmt.Printf("%s=%s\n", row[0], row[1])
	}
	return nil
}

// cmdLicenses generates THIRD_PARTY_LICENSES: the components the built archives
// statically link, and the text of every license that applies to them. It reads
// the ARCHIVES (which is what makes the component list evidence rather than a
// list somebody maintains) and the SOURCE at the pin (which is where the text
// comes from), and it refuses to write a document it could not complete.
//
// It is a command rather than a shell script for the same reason the rest of
// this binary is: it reads MANIFEST.json, and a shell implementation would
// either duplicate the fields or grow a JSON parser.
func cmdLicenses(args []string) error {
	fs := flags("licenses")
	manifest := fs.String("manifest", DefaultManifest, "the pin document")
	source := fs.String("source", "", "the source checkout at the pin (the recipe's build root)")
	dist := fs.String("dist", "", "the directory holding the archives that were just built")
	out := fs.String("out", "", "the document to write")
	repoRoot := fs.String("repo", ".", "the repository root, for the committed license copies")
	_ = fs.Parse(args)
	if *source == "" || *dist == "" || *out == "" {
		return errors.New("licenses: --source, --dist and --out are required")
	}
	m, err := loadManifest(fs, manifest)
	if err != nil {
		return err
	}
	doc, err := vtpin.GenerateLicenses(m, *source, *repoRoot, *dist)
	if err != nil {
		return err
	}
	f, err := os.Create(*out) //nolint:gosec // the path is the caller's --out, and the recipe is the only caller
	if err != nil {
		return err
	}
	if writeErr := doc.Write(f); writeErr != nil {
		_ = f.Close()
		return writeErr
	}
	if closeErr := f.Close(); closeErr != nil {
		return closeErr
	}
	sum, size, err := vtpin.HashFile(*out)
	if err != nil {
		return err
	}
	for _, e := range doc.Entries {
		fmt.Printf("%-12s %s\n", e.Component, e.License)
	}
	fmt.Fprintf(os.Stderr, "%s: %d components, sha256 %s, %d bytes\n", *out, len(doc.Entries), sum, size)
	return nil
}

// cmdPin records the hashes of --dist into the manifest — a new pin. The
// licenses document has to be IN that directory: it is published with the
// archives and verified like them, so a pin that did not carry its own licenses
// would be a pin whose binaries ship without them.
func cmdPin(args []string) error {
	fs := flags("pin")
	manifest := fs.String("manifest", DefaultManifest, "the pin document")
	dist := fs.String("dist", "", "directory holding the recipe's output, named as the release names it")
	_ = fs.Parse(args)
	if *dist == "" {
		return errors.New("pin: --dist is required")
	}
	m, err := loadManifest(fs, manifest)
	if err != nil {
		return err
	}
	names, err := m.RefreshFromDist(*dist)
	if err != nil {
		return err
	}
	if err := m.Save(*manifest); err != nil {
		return err
	}
	for _, name := range names {
		fmt.Println("pinned", name)
	}
	fmt.Fprintf(os.Stderr, "manifest updated: %s\n", *manifest)
	return nil
}

func cmdPackHeaders(args []string) error {
	fs := flags("pack-headers")
	dir := fs.String("dir", "", "the installed headers directory")
	out := fs.String("out", "", "the bundle to write")
	_ = fs.Parse(args)
	if *dir == "" || *out == "" {
		return errors.New("pack-headers: --dir and --out are required")
	}
	return vtpin.PackHeaders(*dir, *out)
}

// cmdCC answers the one question the helper's build has about a target: which C
// compiler links it. The answer is per target because the archive's libc is
// per target — Zig for the musl triple on Linux (a static helper, which is the
// whole point of musl here), Zig for macOS with the stub search paths that let
// a Linux host cross-link it.
func cmdCC(args []string) error {
	fs := flags("cc")
	manifest := fs.String("manifest", DefaultManifest, "the pin document")
	target := fs.String("target", "", "helper target, GOOS/GOARCH")
	libc := fs.String("libc", "", "which build to link: musl, glibc or system (default: the helper's, i.e. static on Linux)")
	zig := fs.String("zig", "", "the zig binary (default: from PATH)")
	stubs := fs.String("stubs", DefaultStubs, "macOS cross-link stubs directory")
	_ = fs.Parse(args)
	if *target == "" {
		return errors.New("cc: --target is required")
	}
	goos, goarch, ok := strings.Cut(*target, "/")
	if !ok {
		return fmt.Errorf("cc: --target %q, want GOOS/GOARCH", *target)
	}
	m, err := loadManifest(fs, manifest)
	if err != nil {
		return err
	}
	t, err := m.TargetWithLibc(goos, goarch, *libc)
	if err != nil {
		return err
	}
	zigBin := *zig
	if zigBin == "" {
		zigBin = "zig"
	}
	if abs, err := exec.LookPath(zigBin); err == nil {
		zigBin = abs
	}
	parts := []string{zigBin, "cc", "-target", t.ZigTarget}
	if t.Libc == vtpin.LibcSystem {
		abs, err := filepath.Abs(*stubs)
		if err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(abs, "libresolv.tbd")); err != nil {
			return fmt.Errorf("cc: %s: %w", abs, err)
		}
		parts = append(parts, "-L"+abs, "-F"+filepath.Join(abs, "frameworks"))
	}
	fmt.Println(strings.Join(parts, " "))
	return nil
}

// cmdZig resolves the compiler the pin names and refuses anything else. The
// version is not a preference: ghostty's build.zig.zon declares
// minimum_zig_version, and a different Zig is different bytes in an archive
// whose sha256 is committed.
func cmdZig(args []string) error {
	fs := flags("zig")
	manifest := fs.String("manifest", DefaultManifest, "the pin document")
	bin := fs.String("bin", "", "the zig binary (default: from PATH)")
	_ = fs.Parse(args)

	m, err := loadManifest(fs, manifest)
	if err != nil {
		return err
	}
	zigBin := *bin
	if zigBin == "" {
		zigBin = "zig"
	}
	abs, err := exec.LookPath(zigBin)
	if err != nil {
		return fmt.Errorf("zig: %w (the manifest pins %s; install it, or see third_party/libghostty-vt/README.md)", err, m.Toolchain.Zig)
	}
	//nolint:gosec // the binary was resolved above and the version it must report is the pinned one
	out, err := exec.Command(abs, "version").Output()
	if err != nil {
		return fmt.Errorf("zig: %s version: %w", abs, err)
	}
	got := strings.TrimSpace(string(out))
	if got != m.Toolchain.Zig {
		return fmt.Errorf("zig %s at %s, but the manifest pins %s — archives built with another Zig are not the pinned bytes", got, abs, m.Toolchain.Zig)
	}
	fmt.Println(abs)
	return nil
}
