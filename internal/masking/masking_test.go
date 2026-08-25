package masking

// The service's contract: both consumers — the durable path and the egress
// gate — call here, and a detection failure surfaces as an error the caller
// fails closed on, never a panic and never a raw text.

import (
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/secrets"
)

// TestDetect_FindsSecretShapedText pins the pass-through: the findings are
// the recognizer's own, byte offsets into the input, and the value never
// appears in a finding.
func TestDetect_FindsSecretShapedText(t *testing.T) {
	const line = "curl -H \"Authorization: Bearer sk-proj-abcdefghijklmnopqrstuvwx\" https://api.example.com"
	findings, err := Detect(line)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("Detect found nothing in a credential-bearing line")
	}
	for _, f := range findings {
		if f.Kind == "" {
			t.Errorf("finding %+v has no kind", f)
		}
		if f.Start < 0 || f.End <= f.Start || f.End > len(line) {
			t.Errorf("finding %+v has offsets outside %q", f, line)
		}
	}
}

// TestDetect_CleanTextHasNoFindings is the paired end: ordinary text —
// including text that merely NAMES a secret — must pass through with nothing
// to report.
func TestDetect_CleanTextHasNoFindings(t *testing.T) {
	clean := []string{
		"the file's contents",
		"ls -la /home/dev/projects",
		"export GITHUB_TOKEN=$GITHUB_TOKEN", // a reference, not a secret
		"echo 'no secrets here'",
	}
	for _, s := range clean {
		findings, err := Detect(s)
		if err != nil {
			t.Fatalf("Detect(%q): %v", s, err)
		}
		if len(findings) != 0 {
			t.Errorf("Detect(%q) = %d findings, want none", s, len(findings))
		}
	}
}

// TestMaskWithSegments_IsTheDurableShape pins the durable pass: masked text
// with the replacements, and findings/segments parallel to exactly those
// replacements — the shape the history row keeps (ADR-0021).
func TestMaskWithSegments_IsTheDurableShape(t *testing.T) {
	const line = "TOKEN=sk-proj-abcdefghijklmnopqrstuvwx ./run.sh"
	masked, findings, segs, err := MaskWithSegments(line)
	if err != nil {
		t.Fatalf("MaskWithSegments: %v", err)
	}
	if len(findings) == 0 || len(findings) != len(segs) {
		t.Fatalf("findings %d != segments %d (both must describe the same replacements)", len(findings), len(segs))
	}
	if strings.Contains(masked, "sk-proj-abcdefghijklmnopqrstuvwx") {
		t.Fatalf("masked text still carries the value: %q", masked)
	}
	if strings.Contains(masked, line) {
		t.Fatalf("masked text is unchanged: %q", masked)
	}
	for _, f := range findings {
		// The finding is offsets and a kind; the value itself must never
		// appear in it.
		if strings.Contains(string(f.Kind), "sk-proj") {
			t.Fatalf("a finding carried material: %+v", f)
		}
	}
}

// TestMaskWithSegments_CleanTextPassesUnchanged is the durable path's paired
// end: no finding, no segment, and the text comes back byte for byte.
func TestMaskWithSegments_CleanTextPassesUnchanged(t *testing.T) {
	const line = "echo hello"
	masked, findings, segs, err := MaskWithSegments(line)
	if err != nil {
		t.Fatalf("MaskWithSegments: %v", err)
	}
	if masked != line {
		t.Fatalf("masked = %q, want the input unchanged", masked)
	}
	if len(findings) != 0 || len(segs) != 0 {
		t.Fatalf("clean text produced findings %v / segments %v", findings, segs)
	}
}

// TestDetect_KindVocabularyIsSecretsOwn pins "one recognizer": the service
// reports the recognizer's closed kinds, not a vocabulary of its own.
func TestDetect_KindVocabularyIsSecretsOwn(t *testing.T) {
	findings, err := Detect("Authorization: Bearer sk-proj-abcdefghijklmnopqrstuvwx")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("no finding for a Bearer token")
	}
	if findings[0].Kind != secrets.KindOpenAI && findings[0].Kind != secrets.KindAuthHeader {
		t.Fatalf("kind = %q, want one of the recognizer's own kinds (openai/auth-header)", findings[0].Kind)
	}
}
