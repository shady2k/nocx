package hashline

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyRefusesStaleReadWithoutChangingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "example.go")
	if err := os.WriteFile(path, []byte("package old\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	snapshot, err := Read(path, 64<<10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if writeErr := os.WriteFile(path, []byte("package changed\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	_, err = Apply(path, snapshot.Revision, "PUT 1.=1:\n+package new")
	if !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("Apply error = %v, want ErrStaleRevision", err)
	}
	// #nosec G304 -- path is created under t.TempDir.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "package changed\n" {
		t.Fatalf("file after stale edit = %q, want unchanged bytes", got)
	}
	if strings.Contains(string(got), "package new") {
		t.Fatal("stale edit changed the file")
	}
}

func TestApplySupportsAllOperationsAndPreservesLineEndings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "example.txt")
	original := "one\r\ntwo\r\nthree\r\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Read(path, 64<<10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	patch := "PUT 1.=1:\n+ONE\nPUT >3:\n+THREE-AND-A-HALF\nPUT <1:\n+zero\nCUT 2.=2"
	if _, applyErr := Apply(path, snapshot.Revision, patch); applyErr != nil {
		t.Fatalf("Apply: %v", applyErr)
	}
	// #nosec G304 -- path is created under t.TempDir.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "zero\r\nONE\r\nthree\r\nTHREE-AND-A-HALF\r\n"
	if string(got) != want {
		t.Fatalf("edited bytes = %q, want %q", got, want)
	}
}

func TestApplyRefusesLinesOutsideDisplayedWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "example.txt")
	if err := os.WriteFile(path, []byte("first\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Read(path, int64(len("first\n")))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if snapshot.SeenEnd != 1 {
		t.Fatalf("SeenEnd = %d, want 1", snapshot.SeenEnd)
	}
	_, err = Apply(path, snapshot.Revision, "PUT 2.=2:\n+changed")
	if !errors.Is(err, ErrLineNotDisplayed) {
		t.Fatalf("Apply error = %v, want ErrLineNotDisplayed", err)
	}
}

func TestReadRevisionIncludesTrailingWhitespaceAndLineEndings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "example.txt")
	if err := os.WriteFile(path, []byte("one  \r\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Read(path, 64<<10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if snapshot.Text != "1:one  \r\n2:two\n" {
		t.Fatalf("numbered text = %q", snapshot.Text)
	}
	if writeErr := os.WriteFile(path, []byte("one\r\ntwo\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	_, err = Apply(path, snapshot.Revision, "PUT 1.=1:\n+ONE")
	if !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("Apply error = %v, want ErrStaleRevision", err)
	}
}

func TestApplyRejectsUnsupportedOperations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "example.txt")
	if err := os.WriteFile(path, []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Read(path, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Apply(path, snapshot.Revision, "PUT 1*:\n+whole function")
	if !errors.Is(err, ErrInvalidPatch) {
		t.Fatalf("Apply error = %v, want ErrInvalidPatch", err)
	}
}

func TestApplyRefusesBinarySnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "binary.dat")
	original := []byte("one\x00two\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Read(path, 64<<10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !snapshot.Binary {
		t.Fatal("Read did not report binary content")
	}
	_, err = Apply(path, snapshot.Revision, "PUT <1:\n+prefix")
	if !errors.Is(err, ErrBinaryFile) {
		t.Fatalf("Apply error = %v, want ErrBinaryFile", err)
	}
	// #nosec G304 -- path is created under t.TempDir.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("binary file changed to %q, want unchanged bytes", got)
	}
}
