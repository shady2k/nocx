package spawn

import (
	"strings"
	"testing"
)

// The listings here are git 2.55's own bytes, captured from repositories
// built for the purpose (the worktree operations' acceptance tests build the
// same shapes live). A parser tested against a hand-written approximation of
// git's output only proves the approximation.

// TestParseWorktreeListReadsEveryRecord: the whole record table — the main
// worktree first, a linked one on a branch, a detached one, a locked one and
// a prunable one — with the branch NAME recovered from the ref git prints.
func TestParseWorktreeListReadsEveryRecord(t *testing.T) {
	data := `worktree /tmp/wtprobe/main
HEAD 02f20efb4bc1e18aef38e79fee6ed2e28ec3f874
branch refs/heads/main

worktree /tmp/wtprobe/wt-det
HEAD 02f20efb4bc1e18aef38e79fee6ed2e28ec3f874
detached
locked

worktree /tmp/wtprobe/wt-gone
HEAD 02f20efb4bc1e18aef38e79fee6ed2e28ec3f874
branch refs/heads/gone
prunable gitdir file points to non-existent location

worktree /tmp/wtprobe/wt sp2
HEAD 02f20efb4bc1e18aef38e79fee6ed2e28ec3f874
branch refs/heads/sp2

`
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

// TestParseWorktreeListKeepsANewlineInAPath: measured on git 2.55, the
// porcelain form prints a worktree path verbatim even when it contains a
// newline, so the rest of the path arrives as a line that looks like data.
// The parser recovers it because the path is everything up to the next
// ATTRIBUTE — /tmp/wtprobe/wt, then "nl" on the next line, is one path.
func TestParseWorktreeListKeepsANewlineInAPath(t *testing.T) {
	data := "worktree /tmp/wtprobe/wt\nnl\nHEAD 02f20efb4bc1e18aef38e79fee6ed2e28ec3f874\nbranch refs/heads/nlbr\n\n"
	recs, err := ParseWorktreeList([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("records = %d, want 1: %+v", len(recs), recs)
	}
	if want := "/tmp/wtprobe/wt\nnl"; recs[0].Path != want {
		t.Errorf("Path = %q, want %q", recs[0].Path, want)
	}
	if recs[0].Branch != "nlbr" {
		t.Errorf("Branch = %q, want nlbr", recs[0].Branch)
	}
}

// TestParseWorktreeListRejectsAMalformedListing: the first line of a record
// is the only line whose shape the parser owns — anything that is not
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
// lines the format did not define, and there are none here.
func TestParseWorktreeListEmpty(t *testing.T) {
	for _, data := range []string{"", "\n"} {
		recs, err := ParseWorktreeList([]byte(data))
		if err != nil {
			t.Fatalf("ParseWorktreeList(%q): %v", data, err)
		}
		if len(recs) != 0 {
			t.Fatalf("ParseWorktreeList(%q) = %+v, want none", data, recs)
		}
	}
}

// TestWorktreeArgsPlaceThePathAfterDashes: the path is the one value these
// invocations take that a caller could begin with '-'. Measured on git 2.55:
// `worktree add -b <br> -- <path> <base>` and `worktree remove -- <path>` are
// both accepted and act on the path as written, so the guard costs nothing.
func TestWorktreeArgsPlaceThePathAfterDashes(t *testing.T) {
	cases := []struct {
		name string
		got  []string
		want []string
	}{
		{"list", WorktreeListArgs(), []string{"--no-optional-locks", "worktree", "list", "--porcelain"}},
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
