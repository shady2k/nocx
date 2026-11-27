package filesystem_test

// The traversal half of the assistant's read capability (stage 10, leaf
// .11.3): listing and content search over the pane's Handle. ADR-anntnxdj §5
// — the capability acquires the Handle and the provider does the I/O, so
// "list a directory" and "search its files" are the same code on this
// machine and on a host behind the helper.
//
// What these tests pin is the WALK ITSELF, and every one of them is a
// property the provider cannot be trusted to give:
//
//   - a symlink is REPORTED and never entered or read. Entering one would
//     leave the canonical start directory entirely — the scope check is made
//     once, on the directory the call named — so a link out of the granted
//     tree would hand the model files the grant does not cover, and a link
//     loop would hang the call.
//   - `.git` is skipped: it is not information about the project's files,
//     and it costs a page of RPCs per repository.
//   - a binary file is listed but never searched: its bytes are not lines.
//   - the traversal is breadth-first, so a truncated listing shows the
//     shallow names a person recognises before it shows depth.
//   - every external call has a failure test (List fails mid-walk, Read
//     fails) paired with the success path that proves the fixture is real.
//   - a scope escape armed AFTER the start check — a symlink retargeted
//     between the check and the call — is refused per operation, on the
//     canonical identity of the object the handle actually read: a page is
//     refused before a row of it is visited, a read before a line of it is
//     matched.
//   - a cancelled context is never reported as a finished walk/search: the
//     check runs before every page, every read, every matched line, and
//     before the final result is handed back.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	filesystem "github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/local"
)

// walkFixture is one granted directory plus a sibling OUTSIDE it, so a
// symlink escape is a real escape rather than a picture of one.
type walkFixture struct {
	root    string
	outside string
}

func newWalkFixture(t *testing.T) walkFixture {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	outside := filepath.Join(dir, "outside")
	for _, d := range []string{root, filepath.Join(root, "sub"), filepath.Join(root, ".git"), filepath.Join(root, "links"), outside} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	writeFile := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	writeFile(filepath.Join(root, "a.txt"), "alpha\nneedle here\n")
	writeFile(filepath.Join(root, "sub", "b.txt"), "beta\nneedle sub\n")
	writeFile(filepath.Join(root, ".git", "config"), "needle in git\n")
	writeFile(filepath.Join(outside, "secret.txt"), "needle secret\n")
	if err := os.WriteFile(filepath.Join(root, "bin.dat"), []byte{0x00, 0x01, 'n', 'e', 'e', 'd', 'l', 'e'}, 0o600); err != nil {
		t.Fatalf("write bin.dat: %v", err)
	}
	// links/escape leaves the granted tree; links/loop points back at the root
	// (a cycle if the walk ever followed a link); links/inside.txt is a link to
	// a file the walk already visits by name.
	if err := os.Symlink(outside, filepath.Join(root, "links", "escape")); err != nil {
		t.Fatalf("symlink escape: %v", err)
	}
	if err := os.Symlink(root, filepath.Join(root, "links", "loop")); err != nil {
		t.Fatalf("symlink loop: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "a.txt"), filepath.Join(root, "links", "inside.txt")); err != nil {
		t.Fatalf("symlink inside: %v", err)
	}
	return walkFixture{root: root, outside: outside}
}

// readerOver builds the files.read-shaped capability over the fixture's root:
// the same ScopedReader and the same exact-file narrowing the tool gets.
func readerOver(t *testing.T, source filesystem.HandleSource, root, path string) *filesystem.ScopedReader {
	t.Helper()
	return filesystem.NewScopedReaderWithExactFiles(source, []string{root}, []string{path})
}

func localWalkSource(t *testing.T) filesystem.HandleSource {
	t.Helper()
	return newLocalSource(t)
}

// entryNames renders a walked entry the way assertions read it: the path
// relative to the root, so a test says "sub/b.txt" rather than an absolute
// temp path.
func relTo(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return rel
}

func TestWalkIsBreadthFirstAndSkipsGit(t *testing.T) {
	fx := newWalkFixture(t)
	ctx := context.Background()
	reader := readerOver(t, localWalkSource(t), fx.root, fx.root)

	var got []string
	stats, err := reader.Walk(ctx, fx.root, true, func(e filesystem.Entry) error {
		got = append(got, relTo(fx.root, e.Path))
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	want := []string{
		"links", "sub", "a.txt", "bin.dat",
		// links/ sorts like the provider lists it: the two links that resolve
		// to directories first (dir-like), then the one that resolves to a
		// file. The walk keeps the provider's order and adds nothing.
		filepath.Join("links", "escape"), filepath.Join("links", "loop"), filepath.Join("links", "inside.txt"),
		filepath.Join("sub", "b.txt"),
	}
	if len(got) != len(want) {
		t.Fatalf("walked %d entries %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("walk order = %v, want %v (breadth-first: a shallow name before any depth)", got, want)
		}
	}
	if stats.GitDirs != 1 {
		t.Errorf("skipped .git directories = %d, want 1", stats.GitDirs)
	}
	if stats.Symlinks != 3 {
		t.Errorf("reported symlinks = %d, want 3", stats.Symlinks)
	}
	for _, name := range got {
		if name == ".git" || name == filepath.Join(".git", "config") {
			t.Errorf("walk reported %q: a .git directory is skipped, not descended", name)
		}
	}
}

// TestWalkNeverEntersOrReadsASymlink is the escape half of the same property:
// the granted tree contains a link to a directory outside it and a link back
// to the root. Neither may appear as its contents, and neither may hang the
// call.
func TestWalkNeverEntersOrReadsASymlink(t *testing.T) {
	fx := newWalkFixture(t)
	ctx := context.Background()
	reader := readerOver(t, localWalkSource(t), fx.root, fx.root)

	var got []string
	if _, err := reader.Walk(ctx, fx.root, true, func(e filesystem.Entry) error {
		got = append(got, e.Path)
		if e.Kind != filesystem.KindSymlink {
			return nil
		}
		if e.LinkKind == filesystem.KindDir {
			t.Logf("symlink %s reports LinkKind dir; it must still not be entered", e.Path)
		}
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	for _, path := range got {
		if filepath.Base(path) == "secret.txt" {
			t.Fatalf("walk reached %q, which lives outside the granted tree through links/escape", path)
		}
	}
	if len(got) > 32 {
		t.Fatalf("walk produced %d entries: a symlink cycle was followed", len(got))
	}
}

func TestWalkNonRecursiveListsOnlyTheNamedDirectory(t *testing.T) {
	fx := newWalkFixture(t)
	ctx := context.Background()
	reader := readerOver(t, localWalkSource(t), fx.root, fx.root)

	var got []string
	if _, err := reader.Walk(ctx, fx.root, false, func(e filesystem.Entry) error {
		got = append(got, relTo(fx.root, e.Path))
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	want := []string{"links", "sub", "a.txt", "bin.dat"}
	if len(got) != len(want) {
		t.Fatalf("non-recursive walk = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("non-recursive walk = %v, want %v", got, want)
		}
	}
}

func TestSearchFindsLinesAndSkipsBinaryGitAndSymlinks(t *testing.T) {
	fx := newWalkFixture(t)
	ctx := context.Background()
	reader := readerOver(t, localWalkSource(t), fx.root, fx.root)

	var matches []filesystem.Match
	stats, err := reader.Search(ctx, fx.root, "needle", func(m filesystem.Match) error {
		matches = append(matches, m)
		return nil
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	want := []filesystem.Match{
		{Path: filepath.Join(fx.root, "a.txt"), Line: 2, Text: "needle here"},
		{Path: filepath.Join(fx.root, "sub", "b.txt"), Line: 2, Text: "needle sub"},
	}
	if len(matches) != len(want) {
		t.Fatalf("matches = %+v, want %+v", matches, want)
	}
	for i := range want {
		if matches[i] != want[i] {
			t.Fatalf("match %d = %+v, want %+v", i, matches[i], want[i])
		}
	}
	if stats.BinaryFiles != 1 {
		t.Errorf("binary files skipped = %d, want 1 (bin.dat)", stats.BinaryFiles)
	}
	if stats.Files != 3 {
		t.Errorf("files read = %d, want 3 (a.txt, bin.dat, sub/b.txt)", stats.Files)
	}
	if stats.GitDirs != 1 || stats.Symlinks != 3 {
		t.Errorf("skipped = %+v, want 1 .git dir and 3 symlinks", stats.WalkStats)
	}
}

func TestSearchRefusesAPathOutsideTheScope(t *testing.T) {
	fx := newWalkFixture(t)
	ctx := context.Background()
	reader := readerOver(t, localWalkSource(t), fx.root, fx.outside)

	_, err := reader.Search(ctx, fx.outside, "needle", func(filesystem.Match) error { return nil })
	if !errors.Is(err, filesystem.ErrOutOfScope) {
		t.Fatalf("Search outside the scope = %v, want ErrOutOfScope", err)
	}
	// The list half refuses the same path, so a scope escape cannot be
	// expressed through either tool.
	if _, err := reader.Walk(ctx, fx.outside, true, func(filesystem.Entry) error { return nil }); !errors.Is(err, filesystem.ErrOutOfScope) {
		t.Fatalf("Walk outside the scope = %v, want ErrOutOfScope", err)
	}
}

func TestWalkStopsWhenTheVisitorSaysSo(t *testing.T) {
	fx := newWalkFixture(t)
	ctx := context.Background()
	reader := readerOver(t, localWalkSource(t), fx.root, fx.root)

	visited := 0
	full, err := reader.Walk(ctx, fx.root, true, func(filesystem.Entry) error { return filesystem.ErrStopWalk })
	if err != nil {
		t.Fatalf("Walk with a stopping visitor: %v", err)
	}
	if visited != 0 {
		t.Fatalf("visited = %d", visited)
	}
	_ = full

	// The stats of what was skipped BEFORE the stop are still reported:
	// the caller is dropping the tail, not discarding what it learned.
	stopped := 0
	stats, err := reader.Walk(ctx, fx.root, true, func(filesystem.Entry) error {
		stopped++
		if stopped == 2 {
			return filesystem.ErrStopWalk
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Walk stopped after two entries: %v", err)
	}
	if stopped != 2 {
		t.Fatalf("visited %d entries after asking to stop at two", stopped)
	}
	if stats.GitDirs != 1 || stats.Symlinks != 0 {
		t.Fatalf("stats after the stop = %+v, want the .git skip recorded before it", stats)
	}
}

func TestSearchRejectsAnInvalidPattern(t *testing.T) {
	fx := newWalkFixture(t)
	ctx := context.Background()
	reader := readerOver(t, localWalkSource(t), fx.root, fx.root)

	if _, err := reader.Search(ctx, fx.root, "needle(", func(filesystem.Match) error { return nil }); err == nil {
		t.Fatal("Search accepted an invalid RE2 pattern")
	}
	// The same pattern language accepts what RE2 does, so the refusal above is
	// about this pattern and not about search being broken.
	if _, err := reader.Search(ctx, fx.root, `needle\s+here`, func(filesystem.Match) error { return nil }); err != nil {
		t.Fatalf("Search with a valid RE2 pattern: %v", err)
	}
}

func TestSearchReportsCRLFLinesWithoutTheCarriageReturn(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "crlf.txt"), []byte("one\r\nneedle two\r\nthree\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader := readerOver(t, localWalkSource(t), root, root)
	var matches []filesystem.Match
	if _, err := reader.Search(context.Background(), root, "needle", func(m filesystem.Match) error {
		matches = append(matches, m)
		return nil
	}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %+v, want one", matches)
	}
	if matches[0].Line != 2 || matches[0].Text != "needle two" {
		t.Fatalf("match = %+v, want line 2 with no trailing carriage return", matches[0])
	}
}

// scopeEscapeHandle wraps a real handle and retargets one path inside the
// granted tree to a symlink outside it in the instant before the real call
// runs — the window the per-operation canonical check exists to close: the
// start directory was checked once, and a symlink armed between that check
// and the call must not read as a row. The wrapper is the TOCTOU, the check
// under test is the one walk.go makes on Listing.Canonical / Content.Canonical.
type scopeEscapeHandle struct {
	filesystem.Handle
	root, outside string
	t             *testing.T
}

func (h *scopeEscapeHandle) retarget(path, target string) {
	h.t.Helper()
	if err := os.RemoveAll(path); err != nil {
		h.t.Fatalf("retarget: remove %s: %v", path, err)
	}
	if err := os.Symlink(target, path); err != nil {
		h.t.Fatalf("retarget: symlink %s -> %s: %v", path, target, err)
	}
}

func (h *scopeEscapeHandle) List(ctx context.Context, path string, page filesystem.Page) (filesystem.Listing, error) {
	if path == filepath.Join(h.root, "sub") {
		// The granted sub directory is replaced by a link out of the grant in
		// the instant before the provider lists it.
		h.retarget(path, h.outside)
	}
	return h.Handle.List(ctx, path, page)
}

func (h *scopeEscapeHandle) Read(ctx context.Context, path string, maxBytes int64) (filesystem.Content, error) {
	if path == filepath.Join(h.root, "a.txt") {
		// The granted file is replaced by a link to a file outside the grant in
		// the instant before the provider reads it.
		h.retarget(path, filepath.Join(h.outside, "secret.txt"))
	}
	return h.Handle.Read(ctx, path, maxBytes)
}

// scopeEscapeSource acquires the real handle and hands back the wrapper, so
// the walk under test holds exactly the handle whose List/Read can be
// retargeted mid-call.
type scopeEscapeSource struct {
	inner         filesystem.HandleSource
	root, outside string
	t             *testing.T
}

func (s *scopeEscapeSource) Acquire(ctx context.Context) (filesystem.Handle, func(), error) {
	h, release, err := s.inner.Acquire(ctx)
	if err != nil {
		return nil, nil, err
	}
	return &scopeEscapeHandle{Handle: h, root: s.root, outside: s.outside, t: s.t}, release, nil
}

// TestWalkRefusesAnEscapedDirectoryBeforeItsPage is blocker #1's walk half:
// the provider answers the escaped directory's page, and the walk must refuse
// the whole call with ErrOutOfScope before a single row of that page reaches
// the visitor — in particular before the outside file name can.
func TestWalkRefusesAnEscapedDirectoryBeforeItsPage(t *testing.T) {
	fx := newWalkFixture(t)
	ctx := context.Background()
	reader := readerOver(t, &scopeEscapeSource{inner: localWalkSource(t), root: fx.root, outside: fx.outside, t: t}, fx.root, fx.root)

	var paths []string
	visited := 0
	_, err := reader.Walk(ctx, fx.root, true, func(e filesystem.Entry) error {
		visited++
		paths = append(paths, e.Path)
		return nil
	})
	if !errors.Is(err, filesystem.ErrOutOfScope) {
		t.Fatalf("Walk after the sub directory was retargeted = %v, want ErrOutOfScope (the escaped page must refuse the call, not feed its rows)", err)
	}
	for _, path := range paths {
		if filepath.Base(path) == "secret.txt" {
			t.Errorf("visit saw %q: the escaped directory's page reached the visitor", path)
		}
	}
	if visited == 0 {
		t.Fatal("the walk refused before visiting anything: the fixture no longer walks a tree")
	}
}

// TestSearchRefusesAnEscapedFileBeforeItsLines is blocker #1's search half:
// the handle reads the escaped file (its Canonical is the outside file) and
// the search must refuse the call before a line of it is matched — neither
// the escaped file's name nor its line may reach the visitor.
func TestSearchRefusesAnEscapedFileBeforeItsLines(t *testing.T) {
	fx := newWalkFixture(t)
	ctx := context.Background()
	reader := readerOver(t, &scopeEscapeSource{inner: localWalkSource(t), root: fx.root, outside: fx.outside, t: t}, fx.root, fx.root)

	var matches []filesystem.Match
	_, err := reader.Search(ctx, fx.root, "needle", func(m filesystem.Match) error {
		matches = append(matches, m)
		return nil
	})
	if !errors.Is(err, filesystem.ErrOutOfScope) {
		t.Fatalf("Search after a.txt was retargeted = %v, want ErrOutOfScope (the escaped read must refuse the call, not feed its line)", err)
	}
	for _, m := range matches {
		if m.Text == "needle secret" || filepath.Base(m.Path) == "secret.txt" {
			t.Errorf("match %+v: the escaped file's line reached the visitor", m)
		}
	}
}

// TestWalkRefusesSuccessOnACancelMidPage keeps a directory larger than one
// page (512 rows) and cancels inside the first page: the next page must not
// be read and the walk must hand back the cancellation, never a success —
// and the remaining rows of the page the cancel fired in must not be
// visited either: the check at the top of the entries loop refuses them
// before the visitor sees them.
func TestWalkRefusesSuccessOnACancelMidPage(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 600; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d.txt", i)), []byte("needle\n"), 0o600); err != nil {
			t.Fatalf("write f%03d: %v", i, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	reader := readerOver(t, localWalkSource(t), root, root)

	visited := 0
	_, err := reader.Walk(ctx, root, false, func(filesystem.Entry) error {
		visited++
		if visited == 5 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Walk cancelled mid-page = %v, want context.Canceled (a cancelled walk must not report success)", err)
	}
	// The cancel fired on the fifth entry; the entries loop's own check
	// refuses the remaining 507 rows of the same page before visiting them.
	if visited != 5 {
		t.Fatalf("visited = %d, want 5 — the entries after the cancel must not be visited", visited)
	}
}

// TestWalkRefusesSuccessOnACancelInTheLastDirectory cancels on the last entry
// of the last directory's last page: the page loop ends, the queue empties,
// and the walk must still hand back the cancellation rather than success.
func TestWalkRefusesSuccessOnACancelInTheLastDirectory(t *testing.T) {
	fx := newWalkFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	reader := readerOver(t, localWalkSource(t), fx.root, fx.root)

	visited := 0
	_, err := reader.Walk(ctx, fx.root, true, func(filesystem.Entry) error {
		visited++
		// The fixture's walk visits four rows in the root page, three in the
		// links page and one in the sub page: eight is the last entry of the
		// last directory.
		if visited == 8 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Walk cancelled in the last directory = %v, want context.Canceled (a cancelled walk must not report success)", err)
	}
}

// TestSearchRefusesSuccessOnACancel cancels on the first match: the search
// must return the cancellation, never a finished-looking result. The check
// that fires is the one inside the line-matching loop (the visitor returns
// nil), and the final-result check covers a search whose last file was read
// before the cancel fired.
func TestSearchRefusesSuccessOnACancel(t *testing.T) {
	fx := newWalkFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	reader := readerOver(t, localWalkSource(t), fx.root, fx.root)

	_, err := reader.Search(ctx, fx.root, "needle", func(filesystem.Match) error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Search cancelled on the first match = %v, want context.Canceled (a cancelled search must not report success)", err)
	}
}

// TestSearchReportsNoFictitiousLine pins the split's two inventions: the
// empty string a trailing terminator adds ("a\nb\n" splits into
// ["a","b",""]) and the one empty element of an empty file. A "^$" pattern
// matches neither — and a real empty line in the middle of a file still
// does (TestSearchReportsARealEmptyLine).
func TestSearchReportsNoFictitiousLine(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []struct {
		name, body string
	}{
		{"trailing.txt", "alpha\nbeta\n"},
		{"empty.txt", ""},
		{"notrailing.txt", "alpha\nbeta"},
	} {
		if err := os.WriteFile(filepath.Join(root, f.name), []byte(f.body), 0o600); err != nil {
			t.Fatalf("write %s: %v", f.name, err)
		}
	}
	reader := readerOver(t, localWalkSource(t), root, root)

	matches := 0
	if _, err := reader.Search(context.Background(), root, "^$", func(filesystem.Match) error {
		matches++
		return nil
	}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if matches != 0 {
		t.Fatalf("matches = %d: a fictitious line after a terminator (or in an empty file) was reported", matches)
	}
}

// TestSearchReportsARealEmptyLine keeps the split honest: an empty line in
// the middle of a file IS a line and must match, or the fix above dropped
// real lines instead of the invented ones.
func TestSearchReportsARealEmptyLine(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mid.txt"), []byte("alpha\n\nbeta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader := readerOver(t, localWalkSource(t), root, root)

	var matches []filesystem.Match
	if _, err := reader.Search(context.Background(), root, "^$", func(m filesystem.Match) error {
		matches = append(matches, m)
		return nil
	}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 1 || matches[0].Line != 2 || matches[0].Text != "" {
		t.Fatalf("matches = %+v, want exactly line 2, the real empty line between two lines", matches)
	}
}

// TestSearchDoesNotReportATruncatedTailAsALine keeps a file larger than
// MaxReadBytes whose bytes after the last newline are one unterminated tail:
// the tail the read cut in two is not a whole line, so a pattern that would
// match it must find nothing — and the file was still read (Files == 1).
func TestSearchDoesNotReportATruncatedTailAsALine(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(strings.Repeat("z", filesystem.MaxReadBytes*2)), 0o600); err != nil {
		t.Fatal(err)
	}
	reader := readerOver(t, localWalkSource(t), root, root)

	matches := 0
	stats, err := reader.Search(context.Background(), root, "^z+$", func(filesystem.Match) error {
		matches++
		return nil
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if matches != 0 {
		t.Fatalf("matches = %d: the truncated unterminated tail was reported as a line", matches)
	}
	if stats.Files != 1 {
		t.Errorf("files read = %d, want 1 (big.txt was read; only its tail was not a line)", stats.Files)
	}
}

// failingProvider wraps a real one and fails ONE call, so a failure test
// exercises the real walk with a real tree around the failing step rather
// than a stub that answers nothing.
type failingProvider struct {
	filesystem.Provider
	listPath string
	readPath string
	err      error
}

func (p *failingProvider) List(ctx context.Context, path string, page filesystem.Page) (filesystem.Listing, error) {
	if p.listPath != "" && path == p.listPath {
		return filesystem.Listing{}, p.err
	}
	return p.Provider.List(ctx, path, page)
}

func (p *failingProvider) Read(ctx context.Context, path string, maxBytes int64) (filesystem.Content, error) {
	if p.readPath != "" && path == p.readPath {
		return filesystem.Content{}, p.err
	}
	return p.Provider.Read(ctx, path, maxBytes)
}

func TestWalkReportsAListFailureMidWalk(t *testing.T) {
	fx := newWalkFixture(t)
	boom := errors.New("list boom")
	provider := &failingProvider{
		Provider: local.New(),
		listPath: filepath.Join(fx.root, "sub"),
		err:      boom,
	}
	reader := readerOver(t, newSourceWithProvider(t, provider, filesystem.Capabilities{}), fx.root, fx.root)

	// One entry is visited before the failing directory is reached: the walk
	// reaches it, so the failure is mid-walk and not a refusal up front.
	visited := 0
	_, err := reader.Walk(context.Background(), fx.root, true, func(filesystem.Entry) error {
		visited++
		return nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Walk = %v, want the provider's list error", err)
	}
	if visited == 0 {
		t.Fatal("the walk failed before visiting anything: the fixture no longer walks a tree")
	}
}

func TestSearchReportsAReadFailure(t *testing.T) {
	fx := newWalkFixture(t)
	boom := errors.New("read boom")
	provider := &failingProvider{
		Provider: local.New(),
		readPath: filepath.Join(fx.root, "a.txt"),
		err:      boom,
	}
	reader := readerOver(t, newSourceWithProvider(t, provider, filesystem.Capabilities{}), fx.root, fx.root)

	if _, err := reader.Search(context.Background(), fx.root, "needle", func(filesystem.Match) error { return nil }); !errors.Is(err, boom) {
		t.Fatalf("Search = %v, want the provider's read error", err)
	}
}

func TestWalkStopsOnACancelledContext(t *testing.T) {
	fx := newWalkFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader := readerOver(t, localWalkSource(t), fx.root, fx.root)

	if _, err := reader.Walk(ctx, fx.root, true, func(filesystem.Entry) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("Walk on a cancelled context = %v, want context.Canceled", err)
	}
}
