package skill_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/shady2k/nocx/internal/skill"
)

// The person's read path goes through the SAME containment the agent tool
// does, so the cases that refuse Read refuse File: an absolute path, a
// traversal, a traversal hidden behind a legitimate first segment, and a
// symlink pointing out of the skill — which is the only one no amount of
// lexical cleaning can see.
func TestFileRefusesEscapes(t *testing.T) {
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
		got, err := skill.File(roots, "deploy", rel)
		if err == nil || got.Text != "" {
			t.Errorf("File(%q) = %+v, err %v; want a refusal with no text", rel, got, err)
		}
	}
}

func TestFileAnswersWithTheWholeFileIncludingFrontmatter(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "deploy", "name: deploy\ndescription: d", "Run make release.")
	roots := []skill.Root{{Dir: root, Provenance: skill.ProvenanceAuthored}}

	got, err := skill.File(roots, "deploy", "SKILL.md")
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	if got.Refusal != skill.FileRefusalNone {
		t.Fatalf("refusal = %q, want none", got.Refusal)
	}
	if !strings.Contains(got.Text, "description: d") || !strings.Contains(got.Text, "Run make release.") {
		t.Fatalf("text = %q, want the file as it is on disk, frontmatter included", got.Text)
	}
	if got.Name != "deploy" || got.Path != "SKILL.md" || got.Provenance != skill.ProvenanceAuthored {
		t.Fatalf("got %+v, want the skill it came from named", got)
	}
	if got.MaxBytes != skill.MaxReadBytes {
		t.Fatalf("maxBytes = %d, want the read budget %d", got.MaxBytes, skill.MaxReadBytes)
	}
}

// Reading is not writing: a builtin skill's bytes are ours and the person may
// read what the assistant reads, so an embedded root answers exactly as a
// directory root does.
func TestFileAnswersForAnEmbeddedRoot(t *testing.T) {
	roots := []skill.Root{{
		FS: fstest.MapFS{
			"authoring/SKILL.md": {Data: []byte("---\nname: authoring\ndescription: d\n---\nshipped body\n")},
		},
		Provenance: skill.ProvenanceBuiltin,
	}}

	got, err := skill.File(roots, "authoring", "SKILL.md")
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	if got.Provenance != skill.ProvenanceBuiltin || !strings.Contains(got.Text, "shipped body") {
		t.Fatalf("got %+v, want the builtin bytes", got)
	}
}

// Not text is an ANSWER, not an error: the file exists, it is inside the
// skill, and "this is not a text file" is a true sentence about it.
func TestFileRefusesToShowWhatIsNotText(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "deploy", "name: deploy\ndescription: d", "body")
	if err := os.WriteFile(filepath.Join(root, "deploy", "diagram.png"), []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0xff, 0xfe}, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := skill.File([]skill.Root{{Dir: root, Provenance: skill.ProvenanceAuthored}}, "deploy", "diagram.png")
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	if got.Refusal != skill.FileRefusalNotText {
		t.Fatalf("refusal = %q, want %q", got.Refusal, skill.FileRefusalNotText)
	}
	if got.Text != "" {
		t.Fatalf("text = %q, want nothing: bytes that are not text are not shown as text", got.Text)
	}
	if got.Path != "diagram.png" {
		t.Fatalf("path = %q, want the file that was refused named", got.Path)
	}
}

// Larger than the budget is an ANSWER for the same reason, and it carries the
// budget so the viewer's sentence can name the limit without a second copy of
// the number.
func TestFileRefusesToShowWhatIsLargerThanTheBudget(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "deploy", "name: deploy\ndescription: d", "body")
	big := strings.Repeat("x", skill.MaxReadBytes+1)
	if err := os.WriteFile(filepath.Join(root, "deploy", "dump.log"), []byte(big), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := skill.File([]skill.Root{{Dir: root, Provenance: skill.ProvenanceAuthored}}, "deploy", "dump.log")
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	if got.Refusal != skill.FileRefusalTooLarge {
		t.Fatalf("refusal = %q, want %q", got.Refusal, skill.FileRefusalTooLarge)
	}
	if got.Text != "" {
		t.Fatalf("text = %q, want nothing: a truncated file read as though it were whole is a lie", got.Text)
	}
	if got.MaxBytes != skill.MaxReadBytes {
		t.Fatalf("maxBytes = %d, want the budget that refused it", got.MaxBytes)
	}
}

// A file exactly at the budget is readable: the bound is a ceiling the file
// may touch, and getting this wrong shows a file as refused for being one
// byte too big when it is not.
func TestFileReadsAFileExactlyAtTheBudget(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "deploy", "name: deploy\ndescription: d", "body")
	exact := strings.Repeat("x", skill.MaxReadBytes)
	if err := os.WriteFile(filepath.Join(root, "deploy", "notes.md"), []byte(exact), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := skill.File([]skill.Root{{Dir: root, Provenance: skill.ProvenanceAuthored}}, "deploy", "notes.md")
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	if got.Refusal != skill.FileRefusalNone || len(got.Text) != skill.MaxReadBytes {
		t.Fatalf("got refusal %q and %d bytes, want the whole file", got.Refusal, len(got.Text))
	}
}

// The external call this code makes is the open, and here it fails: the file
// named is not there. There is no subject to describe, so it is an error.
func TestFileErrsWhenTheFileIsGone(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "deploy", "name: deploy\ndescription: d", "body")

	got, err := skill.File([]skill.Root{{Dir: root, Provenance: skill.ProvenanceAuthored}}, "deploy", "references/gone.md")
	if err == nil {
		t.Fatalf("File = %+v, want an error naming the file that is not there", got)
	}
	if !strings.Contains(err.Error(), "gone.md") {
		t.Errorf("error = %q, want the path named", err)
	}
}

func TestFileErrsWhenTheSkillIsNotThere(t *testing.T) {
	root := t.TempDir()
	if _, err := skill.File([]skill.Root{{Dir: root, Provenance: skill.ProvenanceAuthored}}, "absent", "SKILL.md"); err == nil {
		t.Fatal("File accepted a skill that does not exist")
	}
}

// A directory is not a file, and answering with a directory's bytes is not a
// thing that exists.
func TestFileErrsWhenThePathIsADirectory(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "deploy", "name: deploy\ndescription: d", "body")
	if err := os.MkdirAll(filepath.Join(root, "deploy", "references"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := skill.File([]skill.Root{{Dir: root, Provenance: skill.ProvenanceAuthored}}, "deploy", "references"); err == nil {
		t.Fatal("File accepted a directory")
	}
}

func TestFileErrsWhenNoPathIsNamed(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "deploy", "name: deploy\ndescription: d", "body")
	if _, err := skill.File([]skill.Root{{Dir: root, Provenance: skill.ProvenanceAuthored}}, "deploy", ""); err == nil {
		t.Fatal("File accepted an empty path; the person's read path names one file")
	}
}
