package skill_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/skill"
)

type logCapture struct {
	records []slog.Record
}

func (l *logCapture) Enabled(context.Context, slog.Level) bool {
	return true
}

func (l *logCapture) Handle(_ context.Context, record slog.Record) error {
	l.records = append(l.records, record)
	return nil
}

func (l *logCapture) WithAttrs([]slog.Attr) slog.Handler {
	return l
}

func (l *logCapture) WithGroup(string) slog.Handler {
	return l
}

func TestReadRefusesEscapes(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "deploy", "name: deploy\ndescription: d", "body")
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "deploy", "link.md")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	roots := []skill.Root{{Dir: root, Provenance: skill.ProvenanceAuthored}}

	for _, rel := range []string{"/etc/passwd", "../../etc/passwd", "references/../../../etc/passwd", "link.md"} {
		if got, err := skill.Read(roots, "deploy", rel); err == nil || len(got.Bytes) != 0 {
			t.Errorf("Read(%q) = bytes %q, err %v; want refusal with no bytes", rel, got.Bytes, err)
		}
	}
}

func TestReadReturnsTheBodyAndAReferenceFile(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "deploy", "name: deploy\ndescription: d", "Run make release.")
	refs := filepath.Join(root, "deploy", "references")
	if err := os.MkdirAll(refs, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(refs, "hosts.md"), []byte("prod is eu-1"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	roots := []skill.Root{{Dir: root, Provenance: skill.ProvenanceAuthored}}

	body, err := skill.Read(roots, "deploy", "")
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if strings.TrimSpace(string(body.Bytes)) != "Run make release." {
		t.Fatalf("body must be the body with frontmatter stripped, got %q", body.Bytes)
	}
	if body.Provenance != skill.ProvenanceAuthored {
		t.Fatalf("provenance = %q, want authored", body.Provenance)
	}
	ref, err := skill.Read(roots, "deploy", "references/hosts.md")
	if err != nil || string(ref.Bytes) != "prod is eu-1" {
		t.Fatalf("ref = %q, err = %v", ref.Bytes, err)
	}
}

func TestReadUsesFirstRootAndCarriesProvenance(t *testing.T) {
	authored := t.TempDir()
	managed := t.TempDir()
	writeSkill(t, authored, "deploy", "description: authored", "authored body")
	writeSkill(t, managed, "deploy", "description: managed", "managed body")

	got, err := skill.Read([]skill.Root{
		{Dir: authored, Provenance: skill.ProvenanceAuthored},
		{Dir: managed, Provenance: skill.ProvenanceManaged},
	}, "deploy", "")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got.Bytes) != "authored body\n" || got.Provenance != skill.ProvenanceAuthored {
		t.Fatalf("got %+v, want authored body and provenance", got)
	}
}

func TestReadBoundsReturnedContent(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "large", "description: large", strings.Repeat("x", 70<<10))

	got, err := skill.Read([]skill.Root{{Dir: root, Provenance: skill.ProvenanceAuthored}}, "large", "")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got.Bytes) > 64<<10 {
		t.Fatalf("read returned %d bytes, want at most %d", len(got.Bytes), 64<<10)
	}
}

func TestReadUsesDiscoveryValidationLogging(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "broken")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: broken\ndescription: missing closing\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	capture := &logCapture{}
	previous := slog.Default()
	slog.SetDefault(slog.New(capture))
	t.Cleanup(func() { slog.SetDefault(previous) })

	if _, err := skill.Read([]skill.Root{{Dir: root, Provenance: skill.ProvenanceAuthored}}, "broken", ""); err == nil {
		t.Fatal("Read accepted malformed skill")
	}
	for _, record := range capture.records {
		if record.Message == "skill: invalid frontmatter" {
			return
		}
	}
	t.Fatalf("Read emitted no discovery warning: %+v", capture.records)
}
