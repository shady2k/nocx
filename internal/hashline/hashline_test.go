package hashline

// The engine is pure by bytes (nocx-a62cl.11.1). It used to own the local file
// I/O — EvalSymlinks, os.ReadFile, the temporary file and the rename — which
// made it the reason the assistant's edit path could only ever touch this
// machine's disk. These tests assert the bytes contract directly: a snapshot is
// described from bytes the caller already holds, and a patch returns the bytes
// to install rather than installing them.

import (
	"errors"
	"testing"
)

// canonical is what the provider answered for the path; the engine does not
// resolve anything itself, so any stable string is a valid identity here.
const canonical = "/tmp/example.go"

func snapshot(t *testing.T, data string, maxBytes int64) Snapshot {
	t.Helper()
	return Read(canonical, []byte(data), maxBytes)
}

func TestApplyRefusesStaleRead(t *testing.T) {
	s := snapshot(t, "package old\n", 64<<10)
	_, _, err := Apply(canonical, []byte("package changed\n"), s.Revision, "PUT 1.=1:\n+package new")
	if !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("Apply error = %v, want ErrStaleRevision", err)
	}
}

// A revision minted for one canonical identity cannot be replayed against
// another: the token binds the identity as well as the bytes.
func TestApplyRefusesARevisionOfAnotherPath(t *testing.T) {
	s := Read("/tmp/other.go", []byte("package old\n"), 64<<10)
	_, _, err := Apply(canonical, []byte("package old\n"), s.Revision, "PUT 1.=1:\n+package new")
	if !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("Apply error = %v, want ErrStaleRevision", err)
	}
}

func TestApplySupportsAllOperationsAndPreservesLineEndings(t *testing.T) {
	data := "one\r\ntwo\r\nthree\r\n"
	s := snapshot(t, data, 64<<10)
	patch := "PUT 1.=1:\n+ONE\nPUT >3:\n+THREE-AND-A-HALF\nPUT <1:\n+zero\nCUT 2.=2"
	got, result, err := Apply(canonical, []byte(data), s.Revision, patch)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := "zero\r\nONE\r\nthree\r\nTHREE-AND-A-HALF\r\n"
	if string(got) != want {
		t.Fatalf("edited bytes = %q, want %q", string(got), want)
	}
	// The result names the bytes that were produced and shows NOTHING: the
	// window was a fact about the content before the patch, and this patch cut
	// a line out of it. A second edit therefore has to start from a fresh
	// files.read — under the result's revision not one line is displayed, not
	// even the ones this patch addressed.
	if result.Canonical != canonical {
		t.Fatalf("result canonical = %q, want %q", result.Canonical, canonical)
	}
	if result.Revision == s.Revision {
		t.Fatal("the result kept the old revision; it must name the produced bytes")
	}
	if _, _, err := Apply(canonical, got, result.Revision, "PUT 1.=1:\n+zero"); !errors.Is(err, ErrLineNotDisplayed) {
		t.Fatalf("edit under the result's revision = %v, want ErrLineNotDisplayed", err)
	}
}

// TestApplyResultRevisionDoesNotAuthorizeALineTheCutHid: the read displayed
// lines 1-3, the patch cut two of them, and line 3 of the RESULT is a line the
// caller never saw. Carrying the read's window forward would have authorized
// overwriting it by its old number (nocx-a62cl.11.1 review, ALX-1821).
func TestApplyResultRevisionDoesNotAuthorizeALineTheCutHid(t *testing.T) {
	data := "one\ntwo\nthree\nfour\nfive\n"
	s := Read(canonical, []byte(data), int64(len("one\ntwo\nthree\n")))
	if s.SeenStart != 1 || s.SeenEnd != 3 {
		t.Fatalf("window = %d..%d, want 1..3", s.SeenStart, s.SeenEnd)
	}
	cut, result, err := Apply(canonical, []byte(data), s.Revision, "CUT 1.=2")
	if err != nil {
		t.Fatalf("CUT 1.=2: %v", err)
	}
	if want := "three\nfour\nfive\n"; string(cut) != want {
		t.Fatalf("cut bytes = %q, want %q", string(cut), want)
	}
	// Line 3 of these bytes is "four" — never displayed — and it sits inside
	// the window the read reported, which is exactly what the result must not
	// pass on.
	again, _, err := Apply(canonical, cut, result.Revision, "PUT 3.=3:\n+FOUR")
	if !errors.Is(err, ErrLineNotDisplayed) {
		t.Fatalf("edit of a hidden line = %v (bytes %q), want ErrLineNotDisplayed", err, string(again))
	}
}

func TestApplyRefusesLinesOutsideDisplayedWindow(t *testing.T) {
	s := snapshot(t, "first\nsecond\n", int64(len("first\n")))
	if s.SeenEnd != 1 {
		t.Fatalf("SeenEnd = %d, want 1", s.SeenEnd)
	}
	_, _, err := Apply(canonical, []byte("first\nsecond\n"), s.Revision, "PUT 2.=2:\n+changed")
	if !errors.Is(err, ErrLineNotDisplayed) {
		t.Fatalf("Apply error = %v, want ErrLineNotDisplayed", err)
	}
}

func TestReadRevisionIncludesTrailingWhitespaceAndLineEndings(t *testing.T) {
	s := snapshot(t, "one  \r\ntwo\n", 64<<10)
	if s.Text != "1:one  \r\n2:two\n" {
		t.Fatalf("numbered text = %q", s.Text)
	}
	_, _, err := Apply(canonical, []byte("one\r\ntwo\n"), s.Revision, "PUT 1.=1:\n+ONE")
	if !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("Apply error = %v, want ErrStaleRevision", err)
	}
}

func TestApplyRejectsUnsupportedOperations(t *testing.T) {
	s := snapshot(t, "one\n", 64<<10)
	_, _, err := Apply(canonical, []byte("one\n"), s.Revision, "PUT 1*:\n+whole function")
	if !errors.Is(err, ErrInvalidPatch) {
		t.Fatalf("Apply error = %v, want ErrInvalidPatch", err)
	}
}

func TestApplyRefusesBinarySnapshot(t *testing.T) {
	original := []byte("one\x00two\n")
	s := Read(canonical, original, 64<<10)
	if !s.Binary {
		t.Fatal("Read did not report binary content")
	}
	_, _, err := Apply(canonical, original, s.Revision, "PUT <1:\n+prefix")
	if !errors.Is(err, ErrBinaryFile) {
		t.Fatalf("Apply error = %v, want ErrBinaryFile", err)
	}
}

// Revision names bytes a caller generated rather than read: files.create hands
// its new content back with a token, and that token still binds the canonical
// identity to the exact bytes. It carries no displayed window, because none was
// displayed — the write is not a read.
func TestRevisionNamesGeneratedBytes(t *testing.T) {
	data := []byte("created\n")
	r := Revision(canonical, data)
	if r == "" {
		t.Fatal("Revision returned an empty token")
	}
	if r != Revision(canonical, data) {
		t.Fatal("the same bytes and identity produced two revisions")
	}
	if r == Revision(canonical, []byte("other\n")) {
		t.Fatal("two different bodies produced the same revision")
	}
	if r == Revision("/tmp/other.go", data) {
		t.Fatal("two different identities produced the same revision")
	}
}
