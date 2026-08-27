package deploy_test

// The platform probe and artifact-selection tests (plan Task 9, D20): the
// probe parses exactly one bounded command's output into a Platform, and
// the artifact source answers the three build targets with the
// still-compressed bytes and the decompressed content hash, refusing
// darwin/amd64 and anything else the matrix does not contain. The refusal
// tests use the DEFAULT source (the embedded binaries) — unsupported-
// platform is a fact about the matrix, independent of whether `make
// helpers` has run — and exactly one test needs the real embedded bytes to
// exist, so only that one is gated on them.

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/helper/deploy"
)

// requireArtifacts gates the ONE test in this package that asserts the
// real embedded binaries: a fresh checkout compiles without them (the
// artifacts directory embeds its .gitignore), and that test is about the
// build output itself, so it can only run where the build ran. Nothing
// else in the package is gated on them — the install semantics run on
// synthetic bytes everywhere.
func requireArtifacts(t *testing.T) {
	t.Helper()
	if _, _, err := deploy.DefaultSource.Artifact(deploy.Platform{GOOS: "linux", GOARCH: "amd64"}); errors.Is(err, deploy.ErrArtifactsNotBuilt) {
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

// TestArtifactDarwinAMD64IsUnsupported is D20: darwin/amd64 is deliberately
// NOT built, and asking for it is a distinct refusal — a fact about the
// matrix, independent of whether `make helpers` has run.
func TestArtifactDarwinAMD64IsUnsupported(t *testing.T) {
	if _, _, err := deploy.DefaultSource.Artifact(deploy.Platform{GOOS: "darwin", GOARCH: "amd64"}); !errors.Is(err, deploy.ErrUnsupportedPlatform) {
		t.Fatalf("Artifact(darwin/amd64) error = %v, want ErrUnsupportedPlatform", err)
	}
}

// TestArtifactUnknownPlatformIsUnsupported: a platform outside the matrix
// (including one no probe can produce) is unsupported, never a build gap.
func TestArtifactUnknownPlatformIsUnsupported(t *testing.T) {
	for _, p := range []deploy.Platform{
		{GOOS: "windows", GOARCH: "amd64"},
		{GOOS: "linux", GOARCH: "386"},
		{GOOS: "freebsd", GOARCH: "arm64"},
	} {
		if _, _, err := deploy.DefaultSource.Artifact(p); !errors.Is(err, deploy.ErrUnsupportedPlatform) {
			t.Fatalf("Artifact(%+v) error = %v, want ErrUnsupportedPlatform", p, err)
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
	data, contentHash, err := deploy.DefaultSource.Artifact(deploy.Platform{GOOS: "linux", GOARCH: "amd64"})
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
