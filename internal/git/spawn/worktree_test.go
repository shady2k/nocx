package spawn

import (
	"strings"
	"testing"
)

// The listings here are git 2.55's own bytes, captured from repositories
// built for the purpose (the worktree operations' acceptance tests build the
// same shapes live). A parser tested against a hand-written approximation of
// git's output only proves the approximation.

// TestParseWorktreeListReadsEveryRecord: the whole record table in the NUL
// form — the main worktree first, a linked one on a branch, a detached one, a
// locked one and a prunable one — with the branch NAME recovered from the ref
// git prints.
func TestParseWorktreeListReadsEveryRecord(t *testing.T) {
	data := "worktree /tmp/wtprobe/main\x00HEAD 02f20efb4bc1e18aef38e79fee6ed2e28ec3f874\x00branch refs/heads/main\x00\x00" +
		"worktree /tmp/wtprobe/wt-det\x00HEAD 02f20efb4bc1e18aef38e79fee6ed2e28ec3f874\x00detached\x00locked\x00\x00" +
		"worktree /tmp/wtprobe/wt-gone\x00HEAD 02f20efb4bc1e18aef38e79fee6ed2e28ec3f874\x00branch refs/heads/gone\x00prunable gitdir file points to non-existent location\x00\x00" +
		"worktree /tmp/wtprobe/wt sp2\x00HEAD 02f20efb4bc1e18aef38e79fee6ed2e28ec3f874\x00branch refs/heads/sp2\x00\x00"

	recs, err := ParseWorktreeList([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	want := []WorktreeRecord{
		{Path: "/tmp/wtprobe/main", Branch: "main"},
		{Path: "/tmp/wtprobe/wt-det", Detached: true},
		{Path: "/tmp/wtprobe/wt-gone", Branch: "gone"},
		{Path: "/tmp/wtprobe/wt sp2", Branch: "sp2"},
	}
	if len(recs) != len(want) {
		t.Fatalf("records = %d, want %d: %+v", len(recs), len(want), recs)
	}
	for i, w := range want {
		if recs[i] != w {
			t.Errorf("record %d = %+v, want %+v", i, recs[i], w)
		}
	}
}

// TestParseWorktreeListKeepsEveryByteOfAPath is what the NUL form is FOR, and
// the reason it is the parser the seam uses wherever git offers it: a field is
// NUL-terminated, so a path is one field whatever bytes it holds. The path
// here is built from the three things the line form cannot survive — a
// newline, a blank line, and a line that begins like an attribute — none of
// which this parser has to know about.
func TestParseWorktreeListKeepsEveryByteOfAPath(t *testing.T) {
	path := "/tmp/wtprobe/wt\nnl\n\nHEAD deadbeef\nbranch refs/heads/nope"
	data := "worktree " + path + "\x00HEAD 02f20efb4bc1e18aef38e79fee6ed2e28ec3f874\x00branch refs/heads/hostile\x00\x00"

	recs, err := ParseWorktreeList([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("records = %d, want 1: %+v", len(recs), recs)
	}
	if recs[0].Path != path {
		t.Errorf("Path = %q, want %q", recs[0].Path, path)
	}
	if recs[0].Branch != "hostile" {
		t.Errorf("Branch = %q, want hostile — a path that looks like an attribute must not become one", recs[0].Branch)
	}
}

// TestParseWorktreeListRejectsAMalformedListing: the field that opens a
// record is the only one whose shape the parser owns — anything that is not
// "worktree <path>" there means this is not the output the argv asked for,
// and reporting a worktree at the wrong path would be worse than reporting
// nothing.
func TestParseWorktreeListRejectsAMalformedListing(t *testing.T) {
	_, err := ParseWorktreeList([]byte("fatal: not a git repository\n"))
	if err == nil {
		t.Fatal("expected an error for a listing that is not a listing")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("the error must quote what it saw, got %q", err)
	}
}

// TestParseWorktreeListEmpty: an empty read (a git that answered nothing) is
// an empty list and not an error — the malformed-record rule above is about
// fields the format did not define, and there are none here.
func TestParseWorktreeListEmpty(t *testing.T) {
	for _, data := range []string{"", "\x00"} {
		recs, err := ParseWorktreeList([]byte(data))
		if err != nil {
			t.Fatalf("ParseWorktreeList(%q): %v", data, err)
		}
		if len(recs) != 0 {
			t.Fatalf("ParseWorktreeList(%q) = %+v, want none", data, recs)
		}
	}
}

// TestParseWorktreeListLinesReadsTheOlderForm is the same table for the
// newline-terminated listing a git below 2.36 answers — the fallback the seam
// uses only there. A record's path is recovered from a path containing a
// newline (the rule is "up to the next attribute"), which is as far as this
// encoding goes: TestParseWorktreeListKeepsEveryByteOfAPath is the same path
// with a blank line and an attribute-looking line in it, and it is the NUL
// form that survives that.
func TestParseWorktreeListLinesReadsTheOlderForm(t *testing.T) {
	data := `worktree /tmp/wtprobe/main
HEAD 02f20efb4bc1e18aef38e79fee6ed2e28ec3f874
branch refs/heads/main

worktree /tmp/wtprobe/wt-det
HEAD 02f20efb4bc1e18aef38e79fee6ed2e28ec3f874
detached
locked

worktree /tmp/wtprobe/wt
nl
HEAD 02f20efb4bc1e18aef38e79fee6ed2e28ec3f874
branch refs/heads/nlbr

worktree /tmp/wtprobe/wt-gone
HEAD 02f20efb4bc1e18aef38e79fee6ed2e28ec3f874
branch refs/heads/gone
prunable gitdir file points to non-existent location

`
	recs, err := ParseWorktreeListLines([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	want := []WorktreeRecord{
		{Path: "/tmp/wtprobe/main", Branch: "main"},
		{Path: "/tmp/wtprobe/wt-det", Detached: true},
		{Path: "/tmp/wtprobe/wt\nnl", Branch: "nlbr"},
		{Path: "/tmp/wtprobe/wt-gone", Branch: "gone"},
	}
	if len(recs) != len(want) {
		t.Fatalf("records = %d, want %d: %+v", len(recs), len(want), recs)
	}
	for i, w := range want {
		if recs[i] != w {
			t.Errorf("record %d = %+v, want %+v", i, recs[i], w)
		}
	}

	if _, err := ParseWorktreeListLines([]byte("fatal: not a git repository\n")); err == nil {
		t.Error("expected an error for a listing that is not a listing")
	}
	for _, empty := range []string{"", "\n"} {
		recs, err := ParseWorktreeListLines([]byte(empty))
		if err != nil || len(recs) != 0 {
			t.Errorf("ParseWorktreeListLines(%q) = %+v, %v; want none and no error", empty, recs, err)
		}
	}
}

// TestWorktreeArgsPlaceThePathAfterDashes: the path is the one value these
// invocations take that a caller could begin with '-'. Measured on git 2.55:
// `worktree add -b <br> -- <path> <base>` and `worktree remove -- <path>` are
// both accepted and act on the path as written, so the guard costs nothing.
// The two listing forms are pinned too, because which one is asked for is a
// version decision the caller makes and this is the only place it is visible.
func TestWorktreeArgsPlaceThePathAfterDashes(t *testing.T) {
	cases := []struct {
		name string
		got  []string
		want []string
	}{
		{"list, NUL form", WorktreeListArgs(), []string{"--no-optional-locks", "worktree", "list", "--porcelain", "-z"}},
		{"list, line form", WorktreeListLinesArgs(), []string{"--no-optional-locks", "worktree", "list", "--porcelain"}},
		{"checkout", WorktreeCheckoutArgs("/tmp/wt", "br"), []string{"worktree", "add", "--", "/tmp/wt", "br"}},
		{"create", WorktreeCreateArgs("/tmp/wt", "br", "main"), []string{"worktree", "add", "-b", "br", "--", "/tmp/wt", "main"}},
		{"remove", WorktreeRemoveArgs("/tmp/wt"), []string{"worktree", "remove", "--", "/tmp/wt"}},
		{"verify", VerifyCommitArgs("main"), []string{"rev-parse", "--verify", "--quiet", "main^{commit}"}},
		{"tip", RefTipArgs("refs/heads/br"), []string{"rev-parse", "--verify", "--quiet", "refs/heads/br"}},
		{"count", RevListCountArgs("abc", "HEAD"), []string{"--no-optional-locks", "rev-list", "--count", "abc..HEAD"}},
		{"delete", DeleteRefArgs("refs/heads/br", "abc"), []string{"update-ref", "-d", "refs/heads/br", "abc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Join(tc.got, " ") != strings.Join(tc.want, " ") {
				t.Errorf("argv = %q, want %q", tc.got, tc.want)
			}
		})
	}
}
