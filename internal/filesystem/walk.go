package filesystem

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Listing a directory and searching the contents of its files are the two
// read-side capabilities the assistant gained last (stage 10, leaf .11.3,
// ADR-anntnxdj §5). Both are methods of ScopedReader rather than a new
// optional provider interface, and that is deliberate: no shipped provider
// can search, the owner's decision was that search happens on our side, and
// the scope check is the same one files.read makes — acquire the pane's
// Handle, canonicalize the named path, refuse anything outside the grant.
// What follows the check is a plain BFS over List and Read, so it behaves the
// same on this machine and on a host behind the helper.
//
// The traversal invents NO limit of its own. What stops it is the caller: the
// tool's declared ResultBound (through ErrStopWalk, once the result is full)
// or the tool's Deadline (the context), which is also what bounds a tree
// whose search finds nothing.

// ErrStopWalk ends a traversal early WITHOUT failing it: the visitor has what
// it came for, and the caller reports what it kept as truncated. It is a
// sentinel rather than a bool return so a visitor that fails for its own
// reason is not mistaken for one that is finished — the two have different
// answers (an error, and a result).
var ErrStopWalk = errors.New("filesystem: stop the walk")

// walkPageLimit is the page size the List API requires (a page with Limit < 1
// is refused by every provider). It is NOT a limit on what a call returns:
// pages are read until the traversal ends, the caller stops it, or its
// deadline fires. The result bound is the only ceiling on the answer.
const walkPageLimit = 512

// WalkStats reports what a traversal deliberately did not enter or read. It
// is counted rather than silently dropped because a listing that hides a
// directory it refused to open is a listing that looks complete when it is
// not.
type WalkStats struct {
	// GitDirs counts `.git` directories the walk skipped.
	GitDirs int
	// Symlinks counts symlinks the walk reported and did not follow.
	Symlinks int
}

// Match is one line of one file that matched a search.
type Match struct {
	Path string // the lexical path the listing gave, as files.read would name it
	Line int    // 1-based line number within the file
	Text string // the line, without its terminator
}

// SearchStats is WalkStats plus what only a content search can count.
type SearchStats struct {
	WalkStats
	// Files counts files whose bytes were read (binary ones included — they
	// are read and then skipped, and a binary file is not free to find).
	Files int
	// BinaryFiles counts files skipped because their bytes are not lines.
	BinaryFiles int
}

// Walk visits every entry under path breadth-first, in the order the provider
// lists each directory, calling visit for one entry at a time. Symlinks are
// reported and never entered; `.git` directories are skipped; with
// recursive false only the named directory's own entries are visited.
//
// visit returning ErrStopWalk ends the walk with no error. Any other error it
// returns, and any provider error, ends it with that error.
func (s *ScopedReader) Walk(ctx context.Context, path string, recursive bool, visit func(Entry) error) (WalkStats, error) {
	h, release, roots, files, err := s.acquire(ctx)
	if err != nil {
		return WalkStats{}, err
	}
	defer release()
	canonical, err := h.Canonical(ctx, path)
	if err != nil {
		return WalkStats{}, err
	}
	if !s.allows(canonical, roots, files) {
		return WalkStats{}, fmt.Errorf("%w: %s", ErrOutOfScope, path)
	}
	return s.walk(ctx, h, canonical, recursive, visit)
}

// walk is the traversal itself, over a handle the caller already holds and a
// start directory it has already proved is inside the scope. Search shares it
// with Walk because a search is a walk that also reads: the scope check and
// the traversal rules must be one answer, not two that agree until one of
// them learns a new rule.
//
// The queue holds CANONICAL directories. Descendants of a non-symlink entry
// of a canonical directory are canonical already, so Canonical is asked once
// per call — for the path the caller named — and never again (leaf .11.3's
// ordering: one List per directory page, one Canonical per call).
func (s *ScopedReader) walk(ctx context.Context, h Handle, canonical string, recursive bool, visit func(Entry) error) (WalkStats, error) {
	var stats WalkStats
	queue := []string{canonical}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		dir := queue[0]
		queue = queue[1:]
		offset := 0
		for {
			listing, err := h.List(ctx, dir, Page{Offset: offset, Limit: walkPageLimit})
			if err != nil {
				return stats, fmt.Errorf("filesystem: list %s: %w", dir, err)
			}
			for _, entry := range listing.Entries {
				// `.git` is not information about the project's files, and
				// descending it costs a listing per object directory. The
				// entry is skipped whole: a hidden `.git` row in every
				// listing would be noise the model pays tokens for.
				if entry.Name == ".git" && entry.Kind == KindDir {
					stats.GitDirs++
					continue
				}
				if entry.Kind == KindSymlink {
					// Reported, never entered: entering one would leave the
					// canonical start directory after the scope check has
					// already been made, and a link loop would never end.
					stats.Symlinks++
				}
				if err := visit(entry); err != nil {
					if errors.Is(err, ErrStopWalk) {
						return stats, nil
					}
					return stats, err
				}
				if recursive && entry.Kind == KindDir {
					queue = append(queue, entry.Path)
				}
			}
			offset += len(listing.Entries)
			if !listing.HasMore || len(listing.Entries) == 0 {
				break
			}
		}
	}
	return stats, nil
}

// Search walks path (recursively — a content search of one directory's own
// files is what a `*` glob already does, and the walk is what makes "search
// the project" one call) and reports every line of every regular file that
// matches pattern, a RE2 expression. Binary files are read and skipped;
// symlinks and `.git` directories are never entered, so search sees exactly
// the tree the listing shows.
//
// A file larger than MaxReadBytes is searched within the bytes Read returned —
// the same whole-file ceiling files.read has, not a second one — so a match
// past that byte is not reported. visit returning ErrStopWalk ends the search
// with no error.
func (s *ScopedReader) Search(ctx context.Context, path, pattern string, visit func(Match) error) (SearchStats, error) {
	// RE2, compiled once per call: the pattern comes from the model, and a
	// pattern the engine cannot express is the model's error to read, not a
	// failed run.
	re, err := regexp.Compile(pattern)
	if err != nil {
		return SearchStats{}, fmt.Errorf("filesystem: search pattern %q: %w", pattern, err)
	}
	h, release, roots, files, err := s.acquire(ctx)
	if err != nil {
		return SearchStats{}, err
	}
	defer release()
	canonical, err := h.Canonical(ctx, path)
	if err != nil {
		return SearchStats{}, err
	}
	if !s.allows(canonical, roots, files) {
		return SearchStats{}, fmt.Errorf("%w: %s", ErrOutOfScope, path)
	}

	var stats SearchStats
	walkStats, err := s.walk(ctx, h, canonical, true, func(entry Entry) error {
		// Directories, symlinks and pipes are not lines. A FIFO in particular
		// would block the read forever, which is why the kind decides here
		// rather than the open's own refusal.
		if entry.Kind != KindRegular {
			return nil
		}
		content, err := h.Read(ctx, entry.Path, MaxReadBytes)
		if err != nil {
			return fmt.Errorf("filesystem: read %s: %w", entry.Path, err)
		}
		stats.Files++
		if content.Binary {
			stats.BinaryFiles++
			return nil
		}
		for i, line := range strings.Split(content.Text, "\n") {
			line = strings.TrimSuffix(line, "\r")
			if !re.MatchString(line) {
				continue
			}
			if err := visit(Match{Path: entry.Path, Line: i + 1, Text: line}); err != nil {
				return err
			}
		}
		return nil
	})
	stats.WalkStats = walkStats
	if err != nil {
		return stats, err
	}
	return stats, nil
}
