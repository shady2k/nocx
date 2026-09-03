package deploy_test

// The platform probe and artifact-selection tests (plan Task 9, D20): the
// probe parses exactly one bounded command's output into a Platform, and
// the artifact source answers the four build targets with the
// still-compressed bytes and the decompressed content hash, refusing
// anything the matrix does not contain. The refusal tests use the DEFAULT
// source (the embedded binaries) — unsupported-platform is a fact about
// the matrix, independent of whether `make helpers` has run — and the
// tests that need the real embedded bytes to exist are the only ones gated
// on them.

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/shady2k/nocx/internal/helper/deploy"
	helperartifacts "github.com/shady2k/nocx/internal/helper/deploy/artifacts"
)

// requireArtifacts gates the tests in this package that assert the real
// embedded binaries: a fresh checkout compiles without them (the artifacts
// directory embeds its .gitignore), and those tests are about the build
// output itself, so they can only run where the build ran. Nothing else in
// the package is gated on them — the install semantics run on synthetic
// bytes everywhere.
//
// NOCX_REQUIRE_HELPER_ARTIFACTS turns the skip into a failure: a RELEASE
// build was supposed to run `make helpers`, and a skip there is exactly
// how every published release came to embed nothing (nocx-mchgh). The
// default stays the skip, because a fresh checkout is not a broken build.
func requireArtifacts(t *testing.T) {
	t.Helper()
	if _, _, err := helperartifacts.DefaultSource.Artifact(deploy.Platform{GOOS: "linux", GOARCH: "amd64"}); errors.Is(err, helperartifacts.ErrArtifactsNotBuilt) {
		if os.Getenv("NOCX_REQUIRE_HELPER_ARTIFACTS") != "" {
			t.Fatal("embedded helper artifacts absent while NOCX_REQUIRE_HELPER_ARTIFACTS is set: this build was supposed to run `make helpers` and did not")
		}
		t.Skip("embedded helper artifacts absent — run `make helpers` first")
	}
}

// scriptedExec is a deploy.ExecOnce that answers one canned stdout.
type scriptedExec struct {
	out []byte
	err error
}

func (s scriptedExec) Exec(context.Context, string) ([]byte, error) { return s.out, s.err }

// TestProbeParsesThePlatform: one bounded command, two fields, mapped onto
// Go's GOOS/GOARCH vocabulary.
func TestProbeParsesThePlatform(t *testing.T) {
	for _, tt := range []struct {
		name string
		out  string
		want deploy.Platform
	}{
		{"linux amd64", "Linux x86_64\n", deploy.Platform{GOOS: "linux", GOARCH: "amd64"}},
		{"linux arm64", "Linux aarch64\n", deploy.Platform{GOOS: "linux", GOARCH: "arm64"}},
		{"linux arm64 alt", "Linux arm64\n", deploy.Platform{GOOS: "linux", GOARCH: "arm64"}},
		{"darwin arm64", "Darwin arm64\n", deploy.Platform{GOOS: "darwin", GOARCH: "arm64"}},
		{"darwin amd64", "Darwin x86_64\n", deploy.Platform{GOOS: "darwin", GOARCH: "amd64"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := deploy.Probe(context.Background(), scriptedExec{out: []byte(tt.out)})
			if err != nil {
				t.Fatalf("Probe: %v", err)
			}
			if got != tt.want {
				t.Fatalf("Probe = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestProbePropagatesExecFailure: a refused or failed probe command is a
// probe error, never a guessed platform.
func TestProbePropagatesExecFailure(t *testing.T) {
	want := errors.New("exec refused")
	if _, err := deploy.Probe(context.Background(), scriptedExec{err: want}); !errors.Is(err, want) {
		t.Fatalf("Probe error = %v, want %v", err, want)
	}
}

// TestProbeRejectsUnparseableOutput: output that is not two fields cannot
// name a platform.
func TestProbeRejectsUnparseableOutput(t *testing.T) {
	for _, out := range []string{"", "\n", "Linux\n", "Linux x86_64 extra\n", "\x00\x01"} {
		if _, err := deploy.Probe(context.Background(), scriptedExec{out: []byte(out)}); err == nil {
			t.Fatalf("Probe(%q) succeeded, want an error", out)
		}
	}
}

// TestArtifactUnknownPlatformIsUnsupported: a platform outside the matrix
// (including one no probe can produce) is unsupported, never a build gap.
// (darwin/amd64 was in this list until the matrix went 2x2 on 2026-08-30 —
// D20 amended. The distinction it protected lives on in the platforms
// below, which the matrix genuinely does not contain.)
func TestArtifactUnknownPlatformIsUnsupported(t *testing.T) {
	for _, p := range []deploy.Platform{
		{GOOS: "windows", GOARCH: "amd64"},
		{GOOS: "linux", GOARCH: "386"},
		{GOOS: "freebsd", GOARCH: "arm64"},
	} {
		if _, _, err := helperartifacts.DefaultSource.Artifact(p); !errors.Is(err, deploy.ErrUnsupportedPlatform) {
			t.Fatalf("Artifact(%+v) error = %v, want ErrUnsupportedPlatform", p, err)
		}
	}
}

// TestEveryMatrixPlatformResolves is the release's gate expressed as a
// test: a build that embedded nothing answers ErrArtifactsNotBuilt for
// every platform, which is exactly what every published release did until
// nocx-mchgh — release.yml never ran `make helpers`, and the refusal was
// invisible because a checkout-built binary has them (Makefile:104).
//
// It skips where the artifacts are genuinely absent (a fresh checkout) and
// FAILS where NOCX_REQUIRE_HELPER_ARTIFACTS says a build should have them.
func TestEveryMatrixPlatformResolves(t *testing.T) {
	requireArtifacts(t)
	for _, p := range []deploy.Platform{
		{GOOS: "linux", GOARCH: "amd64"},
		{GOOS: "linux", GOARCH: "arm64"},
		{GOOS: "darwin", GOARCH: "amd64"},
		{GOOS: "darwin", GOARCH: "arm64"},
	} {
		if _, _, err := helperartifacts.DefaultSource.Artifact(p); err != nil {
			t.Fatalf("Artifact(%+v): %v", p, err)
		}
	}
}

// TestArtifactReturnsCompressedBytesAndDecompressedHash is D20/D21 against
// the REAL build output: the embedded helper is still gzip-compressed, the
// content hash is the SHA-256 of the DECOMPRESSED bytes — the key of the
// D7 directory and the hash the helper reports about itself — and the
// decompressed bytes are a plausible executable (ELF or Mach-O magic).
// This is the one test in the package that genuinely needs the real
// embedded binaries, so it alone is gated on them.
func TestArtifactReturnsCompressedBytesAndDecompressedHash(t *testing.T) {
	requireArtifacts(t)
	data, contentHash, err := helperartifacts.DefaultSource.Artifact(deploy.Platform{GOOS: "linux", GOARCH: "amd64"})
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	if len(data) < 2 || data[0] != 0x1f || data[1] != 0x8b {
		t.Fatalf("artifact is not gzip-compressed (magic % x)", data[:min(len(data), 2)])
	}
	decompressed := gunzipBytes(t, data)
	if got := sha256Hex(decompressed); got != contentHash {
		t.Fatalf("content hash %q does not match the decompressed bytes (%q)", contentHash, got)
	}
	if !plausibleExecutable(decompressed) {
		t.Fatalf("decompressed artifact does not look like an executable (first bytes % x)", decompressed[:min(len(decompressed), 4)])
	}
}

// plausibleExecutable reports whether the first bytes are a known
// executable magic: ELF (Linux) or 64-bit little-endian Mach-O (darwin),
// the two OSes the D20 matrix ships.
func plausibleExecutable(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	switch {
	case data[0] == 0x7f && data[1] == 'E' && data[2] == 'L' && data[3] == 'F':
		return true
	case data[0] == 0xcf && data[1] == 0xfa && data[2] == 0xed && data[3] == 0xfe:
		return true
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
