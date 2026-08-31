// Package local is the local-machine filesystem provider (spec §5.1). It
// implements filesystem.Provider with path/filepath rules; the SFTP provider
// lives in its own package and uses path, because SFTP specifies POSIX-style
// paths regardless of the OS nocx runs on. filepath must not appear in
// transport or in code shared by both providers.
package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	filesystem "github.com/shady2k/nocx/internal/filesystem"
)

// maxReadBytes is the read ceiling of spec §5.1: the effective limit is
// min(requested, 2 MiB), so the parameter can only lower the ceiling. It is a
// number rather than a knob because the viewer must never present more than
// this much of a file; it is a starting number, to be tuned once the panel is
// in daily use (spec §9).
const maxReadBytes = 2 << 20 // 2 MiB

// defaultEntryCap is the D14 product limit: a directory with more entries
// than this is a tooLarge state, not a listing. Total, the deterministic
// ordering and the whole-directory digest all require enumerating the
// complete directory before any page can be returned, so the cap bounds that
// work; it is also the one a user can reason about — "nocx does not display
// directories this large". No pagination is offered above it and polling is
// disabled for such a directory. Starting number (spec §9).
const defaultEntryCap = 10_000

// defaultSizeCap is the §5.1 response-size ceiling, the third bound: an
// estimate of the JSON bytes of a complete listing. The entry cap bounds the
// count; this bounds what equal counts can cost — 5,000 entries with 4 KB
// symlink targets are a different object from 5,000 short names. Above it
// the directory is refused and partial results are discarded, like tooLarge.
// Starting number (spec §9).
const defaultSizeCap = 8 << 20 // 8 MiB

// defaultListTimeout is the D14 operational limit for the local provider: an
// elapsed-time backstop against pathological local filesystems (a hung NFS
// mount, a misbehaving FUSE filesystem). Unlike SFTP — where ReadDirContext
// makes enumeration cancellable — a local readdir cannot be interrupted, so
// the cap is measured after the fact and the result discarded; it cannot
// preempt a truly hung mount, and that is the honest limit of the local case.
// Starting number (spec §9).
const defaultListTimeout = 10 * time.Second

// Option configures a Provider.
type Option func(*Provider)

// WithRoot sets the verified navigation root — the composition layer's OSC 7
// override (spec D2). Root() returns it when it is still usable at call time
// and falls back to the home directory otherwise; without this option, Root()
// returns the home directory, inferred and labelled.
func WithRoot(path string) Option {
	return func(p *Provider) { p.root = path }
}

// WithEntryCap overrides the directory entry cap. A non-positive value
// disables the cap. Tests and tuning only.
func WithEntryCap(n int) Option {
	return func(p *Provider) { p.entryCap = n }
}

// WithSizeCap overrides the listing response-size ceiling (spec §5.1: equal
// entry counts do not cost equal bytes). A non-positive value disables the
// cap. Tests and tuning only.
func WithSizeCap(n int) Option {
	return func(p *Provider) { p.sizeCap = n }
}

// WithListTimeout overrides the enumeration elapsed-time cap. A non-positive
// value disables the cap. Tests and tuning only.
func WithListTimeout(d time.Duration) Option {
	return func(p *Provider) { p.listTimeout = d }
}

// Provider is a read-only view of the local machine's filesystem.
type Provider struct {
	root        string // verified root override; "" means home, inferred
	entryCap    int
	sizeCap     int
	listTimeout time.Duration

	// Test seams: each models a change arriving between two steps of a real
	// operation, so a defect in the interval shows up deterministically.
	afterRead    func() // runs between the read and the after-stat
	afterReadDir func() // runs after the readdir, before entry processing
	beforeEntry  func() // runs before each entry's metadata is read
	beforeOpen   func() // runs after path resolution, before the open
}

// New creates a local provider. Without WithRoot the root is the user's home
// directory, inferred; with it, the verified path is used while it remains
// usable.
func New(opts ...Option) *Provider {
	p := &Provider{entryCap: defaultEntryCap, sizeCap: defaultSizeCap, listTimeout: defaultListTimeout}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Root computes the navigation root (spec D2). The verified override is
// re-checked at call time — the composition layer can pass a cwd that is
// gone by the time the panel opens — and falls back to the home directory,
// labelled inferred, when it is absent or unusable.
func (p *Provider) Root(ctx context.Context) (filesystem.Root, error) {
	if p.root != "" {
		if filepath.IsAbs(p.root) {
			if fi, err := os.Stat(p.root); err == nil && fi.IsDir() {
				return newRoot(p.root, false, ""), nil
			}
		}
		return p.fallbackRoot(fmt.Sprintf("requested root %q unusable — using home", p.root))
	}
	return p.fallbackRoot("no verified working directory — using home")
}

func (p *Provider) fallbackRoot(reason string) (filesystem.Root, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return filesystem.Root{}, fmt.Errorf("filesystem local: home root: %w", err)
	}
	return newRoot(home, true, reason), nil
}

func newRoot(path string, inferred bool, reason string) filesystem.Root {
	return filesystem.Root{Path: path, Display: displayOf(path), Inferred: inferred, InferredReason: reason}
}

// displayOf abbreviates paths under the user's home with a tilde (spec §5.1:
// Display, for the header). Paths outside home are returned as-is.
func displayOf(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + path[len(home):]
	}
	return path
}

// List returns one page of a complete, deterministically ordered listing.
//
// The provider resolves the canonical directory and then lists that, in that
// order (spec §5.1): a symlink retargeted between the two calls returns the
// identity of A with the entries of B, which is exactly the incoherence this
// ordering prevents. Entry paths use the lexical parent the caller asked
// about, not the canonical one, so the tree the client renders keeps the
// shape it asked for.
//
// All three D14 caps apply, with distinct outcomes: tooLarge carries the
// exact observed count — the local enumeration is a single complete readdir,
// so the count is paid for — timedOut discards everything, never returning a
// complete-looking prefix, and the size cap refuses when the complete
// listing would cost too many bytes. The elapsed-time deadline is derived
// once at entry and re-checked at every stage boundary — the cap exists
// because everything after the readdir (per-entry lstat and symlink
// resolution, sorting, the digest) is also work that can hold the slot, and
// bounding only the readdir would leave the expensive half unbounded.
func (p *Provider) List(ctx context.Context, path string, page filesystem.Page) (filesystem.Listing, error) {
	if err := checkPath(path); err != nil {
		return filesystem.Listing{}, err
	}
	if page.Offset < 0 || page.Limit < 1 {
		return filesystem.Listing{}, &filesystem.ErrInvalidPage{Offset: page.Offset, Limit: page.Limit}
	}
	start := time.Now()
	var deadline time.Time
	if p.listTimeout > 0 {
		deadline = start.Add(p.listTimeout)
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return filesystem.Listing{}, wrapPathErr("list", path, err)
	}
	dirents, err := os.ReadDir(canonical)
	if err != nil {
		return filesystem.Listing{}, wrapPathErr("list", path, err)
	}
	if p.timedOut(deadline) {
		return filesystem.Listing{}, &filesystem.ErrTimedOut{Timeout: p.listTimeout}
	}
	if p.entryCap > 0 && len(dirents) > p.entryCap {
		return filesystem.Listing{}, &filesystem.ErrTooLarge{ObservedCount: len(dirents), Limit: p.entryCap}
	}
	if p.afterReadDir != nil {
		p.afterReadDir()
	}
	entries := make([]filesystem.Entry, 0, len(dirents))
	for _, de := range dirents {
		if p.beforeEntry != nil {
			p.beforeEntry()
		}
		if p.timedOut(deadline) {
			return filesystem.Listing{}, &filesystem.ErrTimedOut{Timeout: p.listTimeout}
		}
		// The metadata is read through the canonical parent, the directory
		// actually listed, never the lexical one: a lexical parent that is a
		// symlink can be retargeted mid-operation, and then names and
		// DirEntry.Info come from A while LinkTarget and LinkKind come from
		// B. Rev is computed over the mixture. Only Entry.Path keeps the
		// lexical shape (spec §5.1: "internal coherence of one operation").
		entries = append(entries, buildEntry(de, canonical, path))
	}
	if p.timedOut(deadline) {
		return filesystem.Listing{}, &filesystem.ErrTimedOut{Timeout: p.listTimeout}
	}
	if p.sizeCap > 0 {
		var totalBytes int64
		for _, e := range entries {
			totalBytes += entryWireCost(e)
		}
		if totalBytes > int64(p.sizeCap) {
			return filesystem.Listing{}, &filesystem.ErrTooLargeSize{ObservedBytes: totalBytes, Limit: int64(p.sizeCap)}
		}
	}
	sortEntries(entries)
	if p.timedOut(deadline) {
		return filesystem.Listing{}, &filesystem.ErrTimedOut{Timeout: p.listTimeout}
	}
	total := len(entries)
	lo := page.Offset
	if lo > total {
		lo = total
	}
	// Saturating arithmetic: a Limit near MaxInt with a nonzero Offset would
	// wrap negative and the slice below would panic. page.Limit < total-lo
	// cannot overflow, and when it is false the answer is total either way.
	hi := total
	if page.Limit < total-lo {
		hi = lo + page.Limit
	}
	return filesystem.Listing{
		Path:      path,
		Canonical: canonical,
		Entries:   entries[lo:hi],
		Offset:    page.Offset,
		Total:     total,
		HasMore:   hi < total,
		Rev:       filesystem.ComputeRev(canonical, entries),
	}, nil
}

// timedOut reports whether the D14 deadline has passed. A zero deadline means
// the cap is disabled.
func (p *Provider) timedOut(deadline time.Time) bool {
	return !deadline.IsZero() && time.Now().After(deadline)
}

// entryWireCost estimates the JSON bytes one Entry contributes to a listing
// response. The variable parts are Name, Path and LinkTarget; the fixed
// fields (kind, size, mtime, mode) cost about 200 bytes after escaping. It
// deliberately over-counts — JSON escaping can only expand — so the ceiling
// errs toward refusal.
func entryWireCost(e filesystem.Entry) int64 {
	return int64(len(e.Name)+len(e.Path)+len(e.LinkTarget)) + 200
}

// buildEntry derives one Entry from a directory entry. Both parents matter,
// and the split is deliberate: the canonical parent is where the entry's
// metadata is read — the directory actually listed — while the lexical
// parent is what Entry.Path is built from, keeping the shape the client
// rendered. A symlink gets LinkTarget and its resolved LinkKind — required
// fields of Rev, so a retarget to a file of the same size and kind still
// changes the digest.
//
// Metadata failures are distinguishable states, never fabricated data: an
// entry whose Info cannot be read is KindUnreadable (it exists; we could not
// learn its mode), and a symlink whose target cannot be statted is
// KindUnreadable only when the failure is something other than a missing
// target — permission denied and a genuinely broken link are different
// facts, and both must survive on the wire.
func buildEntry(de fs.DirEntry, canonicalParent, lexicalParent string) filesystem.Entry {
	e := filesystem.Entry{Name: de.Name(), Path: filepath.Join(lexicalParent, de.Name())}
	info, err := de.Info() // lstat
	if err != nil {
		// Vanished between readdir and here, or permission denied, or I/O.
		// It is presented as unreadable — unopenable and unexpandable — not
		// as empty plausible data, and not dropped from a count the digest
		// already committed to.
		e.Kind = filesystem.KindUnreadable
		return e
	}
	e.Kind = filesystem.KindOf(info.Mode())
	e.Size = info.Size()
	e.ModTime = info.ModTime()
	e.Mode = uint32(info.Mode())
	if e.Kind == filesystem.KindSymlink {
		canonicalPath := filepath.Join(canonicalParent, de.Name())
		target, err := os.Readlink(canonicalPath)
		if err != nil {
			e.LinkKind = filesystem.KindUnreadable
			return e
		}
		e.LinkTarget = target
		if tgt, err := os.Stat(canonicalPath); err == nil {
			e.LinkKind = filesystem.KindOf(tgt.Mode())
		} else if errors.Is(err, fs.ErrNotExist) {
			e.LinkKind = filesystem.KindOther // genuinely broken
		} else {
			e.LinkKind = filesystem.KindUnreadable // exists, unreachable
		}
	}
	return e
}

// isDirLike buckets entries for the deterministic order of spec §5.1:
// directories first, then files, each by UTF-8 byte order of the name,
// case-sensitive, applied BEFORE pagination. A symlink to a directory
// belongs with the directories — it expands, and the frontend must not
// re-sort, or "show next N" would duplicate and skip rows.
func isDirLike(e filesystem.Entry) bool {
	return e.Kind == filesystem.KindDir || (e.Kind == filesystem.KindSymlink && e.LinkKind == filesystem.KindDir)
}

func sortEntries(es []filesystem.Entry) {
	sort.Slice(es, func(i, j int) bool {
		di, dj := isDirLike(es[i]), isDirLike(es[j])
		if di != dj {
			return di
		}
		return es[i].Name < es[j].Name // Go string ordering is bytewise, case-sensitive
	})
}

// Read returns at most the effective limit — min(requested, 2 MiB) — of the
// file at path, streaming: it reads at most limit+1 bytes and never the whole
// file, so the memory guard holds for a 40 GB file. Truncated is true iff
// the extra byte was readable. Openability is enforced from metadata read at
// call time (spec §5.1), never from a kind a caller was handed: the path is
// resolved and opened here, so a symlink retargeted — or a regular file
// swapped for a FIFO — since the last list is refused, not opened. Size and
// mtime are sampled before and after; a difference sets Changed.
//
// The openability check and the open refer to the same object, not the same
// path. The original code statted the path and then opened it in two calls,
// leaving a window in which a regular file could become a FIFO or a device —
// and os.Open on a FIFO blocks forever, which is precisely the hang the
// openability table exists to prevent. Opening with O_NONBLOCK (a no-op for
// regular files, and it makes a swapped-in FIFO's open return instead of
// block) and Fstat-ing the descriptor closes the window: the kind, the size
// and the bytes all come from the one object actually opened.
func (p *Provider) Read(ctx context.Context, path string, maxBytes int64) (filesystem.Content, error) {
	if err := checkPath(path); err != nil {
		return filesystem.Content{}, err
	}
	limit := maxBytes
	if limit <= 0 || limit > maxReadBytes {
		limit = maxReadBytes
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return filesystem.Content{}, wrapPathErr("read", path, err)
	}
	if p.beforeOpen != nil {
		p.beforeOpen()
	}
	f, err := os.OpenFile(canonical, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return filesystem.Content{}, wrapPathErr("read", path, err)
	}
	defer func() { _ = f.Close() }()
	before, err := f.Stat()
	if err != nil {
		return filesystem.Content{}, wrapPathErr("read", path, err)
	}
	kind := filesystem.KindOf(before.Mode())
	// link is dead for a resolved kind — stat follows symlinks, so kind is
	// never KindSymlink here — and CanOpen ignores it in that case.
	if !filesystem.CanOpen(kind, kind) {
		return filesystem.Content{}, &filesystem.ErrNotRegular{Path: path, Kind: kind}
	}
	buf := make([]byte, limit+1)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return filesystem.Content{}, wrapPathErr("read", path, err)
	}
	if p.afterRead != nil {
		p.afterRead()
	}
	after, err := f.Stat()
	if err != nil {
		return filesystem.Content{}, wrapPathErr("read", path, err)
	}
	truncated := int64(n) == limit+1
	data := buf[:n]
	if truncated {
		data = data[:int(limit)]
	}
	binary := bytes.IndexByte(data, 0) >= 0 // the NUL heuristic of spec §5.1
	c := filesystem.Content{
		Path:      path,
		Canonical: canonical,
		Size:      int64(len(data)),
		ModTime:   after.ModTime(),
		Truncated: truncated,
		Binary:    binary,
		Changed:   after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()),
	}
	if !binary {
		c.Text = string(data)
		if !utf8.Valid(data) {
			c.Lossy = true
			c.Text = strings.ToValidUTF8(c.Text, "\uFFFD")
		}
	}
	return c, nil
}

// Watch is declared by the Provider contract (spec §5.1), but watching is a
// later wave of the design's sequence (§6 step 5): local watching lands with
// fsnotify, SFTP polling with the sftp provider. Until then a watch cannot be
// established honestly — a Watch whose Events never fired would be a silent
// lie the product could not surface — so the provider refuses with
// ErrWatchUnavailable.
func (p *Provider) Watch(ctx context.Context, path string) (filesystem.Watch, error) {
	return nil, &filesystem.ErrWatchUnavailable{}
}

// Stat classifies one path by metadata without enumerating its parent. os.Stat
// follows symlinks so the result describes the object a link would open or
// expand, while wrapPathErr preserves the provider's typed path failures.
func (p *Provider) Stat(ctx context.Context, path string) (filesystem.Stat, error) {
	if err := checkPath(path); err != nil {
		return filesystem.Stat{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return filesystem.Stat{}, wrapPathErr("stat", path, err)
	}
	return filesystem.Stat{Kind: filesystem.KindOf(info.Mode())}, nil
}

// Canonical resolves a path to its provider-canonical identity. It exists
// because the Provider contract declares it; no wire method reaches it today
// — Listing.Canonical and Content.Canonical carry identity on every list and
// read — and it is what a future files.canonical would call.
func (p *Provider) Canonical(ctx context.Context, path string) (string, error) {
	if err := checkPath(path); err != nil {
		return "", err
	}
	c, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", wrapPathErr("canonical", path, err)
	}
	return c, nil
}

// Close releases provider-level resources. The local provider holds none
// until the watching wave; the method exists so both providers keep the same
// lifecycle (spec §5.1).
func (p *Provider) Close() error { return nil }

// checkPath enforces the provider's path syntax (spec §5.2: paths absolute
// and cleaned by the provider's rules). The provider owns path syntax and
// rejects rather than silently rewriting a caller's path.
func checkPath(path string) error {
	if !filepath.IsAbs(path) {
		return &filesystem.ErrInvalidPath{Path: path, Reason: "path is not absolute"}
	}
	if filepath.Clean(path) != path {
		return &filesystem.ErrInvalidPath{Path: path, Reason: "path is not clean"}
	}
	return nil
}

// wrapPathErr maps os errors to the package's typed markers. ENOENT includes
// broken symlinks; ENOTDIR is a List on a file. Anything else is returned
// as-is.
func wrapPathErr(op, path string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return &filesystem.ErrNotFound{Path: path, Err: err}
	case errors.Is(err, fs.ErrPermission):
		return &filesystem.ErrPermission{Path: path, Err: err}
	case errors.Is(err, syscall.ENOTDIR):
		return &filesystem.ErrNotDir{Path: path, Err: err}
	default:
		return err
	}
}
