// Package sftp is the remote-machine filesystem provider (spec §5.1): the
// same filesystem.Provider contract as the local provider, over the SFTP
// protocol, reached through an ssh.FSConn lease (spec D3).
//
// # Paths are POSIX, always
//
// This package uses path, never path/filepath. The SFTP protocol specifies
// POSIX-style paths regardless of the OS nocx runs on; under filepath rules a
// remote C:\Users\me would read as relative and its backslashes as ordinary
// characters. filepath must not appear in this package.
//
// # The transport seam
//
// The provider consumes fsConn, a narrow consumer interface declared here the
// way internal/discovery declares its Connector (discovery.go:113): it is the
// SFTP lease surface the provider needs, satisfied structurally by
// ssh.FSConn, and declared narrow so the provider can be tested against a
// double without a live connection — the reason the lease is an interface at
// all. The provider owns the lease: New takes it, Close releases it.
//
// Cancellation is split exactly as the lease splits it: listing is natively
// cancellable (the lease's ReadDir runs ReadDirContext, which checks the
// context on every READDIR packet), so the D14 elapsed-time cap is enforced
// through a deadline derived from the caller's context. Everything else —
// Stat, Lstat, RealPath, ReadFile — is not context-cancellable by the
// protocol, and for those the lease already provides close-to-cancel and a
// bounded lane with a hard-timeout poison; the provider calls them directly
// and does not wrap them in goroutines (that is the lease's job, proven in
// internal/ssh).
//
// # The caps are the same product limits as local, over a different transport
//
// Total, the deterministic ordering and the whole-directory Rev all require
// enumerating the complete directory before any page can be returned, so
// remote work is proportional to directory size, not to rows displayed. All
// three D14 bounds apply with the same distinct outcomes as the local
// provider, and partial results are discarded, never returned as if
// complete.
package sftp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	filesystem "github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/transfer"
)

// maxReadBytes is the read ceiling of spec §5.1: the effective limit is
// min(requested, 2 MiB), so the parameter can only lower the ceiling. It is a
// number rather than a knob because the viewer must never present more than
// this much of a file; it is a starting number, to be tuned once the panel is
// in daily use (spec §9). The lease's own read cap is the same ceiling, so
// the bound the provider passes never exceeds the transport's.
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

// defaultListTimeout is the D14 operational limit for the sftp provider: an
// elapsed-time backstop against a laggy or non-replying server. Unlike the
// local provider — where a readdir cannot be interrupted and the cap is
// measured after the fact — the sftp cap is enforced natively inside the
// lease's ReadDir (ReadDirContext checks the context on every packet), and
// re-checked between entries for the work after the enumeration. Starting
// number (spec §9).
const defaultListTimeout = 10 * time.Second

// fsReader is the read half of the narrow transport seam this provider
// consumes: the SFTP lease surface (spec D3). ssh.FSConn satisfies it
// structurally; the interface is declared here, in the feature package, so
// the provider is testable against a double without a live connection.
// ReadLink is the one operation the lease surface must gain to serve the
// symlink entries of §5.1 (LinkTarget and LinkKind are required fields of
// Rev): pkg/sftp's *Client has it (client.go:497), the committed FSConn
// interface does not expose it yet.
type fsReader interface {
	// ReadDir lists the directory at path. Natively cancellable: the lease
	// implements it with ReadDirContext, which checks the context on each
	// READDIR packet, so the provider's elapsed-time cap is enforced through
	// a derived context.
	ReadDir(ctx context.Context, path string) ([]os.FileInfo, error)
	// Stat returns the file info for path, following symlinks.
	Stat(path string) (os.FileInfo, error)
	// Lstat returns the file info for path without following symlinks.
	Lstat(path string) (os.FileInfo, error)
	// RealPath resolves path to the server's canonical absolute form.
	RealPath(path string) (string, error)
	// ReadFile opens path and reads at most maxBytes bytes, returning
	// truncated when more data remains. The whole open-read-close sequence
	// runs as ONE lease-lane call.
	ReadFile(ctx context.Context, path string, maxBytes int64) (data []byte, truncated bool, err error)
	// ReadLink reads the target of a symbolic link.
	ReadLink(path string) (string, error)
	// Close releases the lease: the pooled reference is dropped and no call
	// from this provider is in flight after Close returns (the lease's own
	// close-to-cancel guarantee).
	Close() error
}

// fsConn is the whole seam: a lease this provider can read AND write
// through. The write half is transfer.RemoteFS verbatim rather than a
// second spelling of the same four calls, because the sink is what consumes
// it and a paraphrase here would be a second owner of that contract.
//
// ssh.FSConn does NOT satisfy this one, and the reason is Go's rather than
// the design's: its Create returns ssh.FSFile where RemoteFS asks for
// transfer.RemoteFile, and interface satisfaction compares result types by
// identity, not by shape. The composition root adapts it — which is also
// where the two error vocabularies are translated, since only it may import
// internal/ssh and internal/transfer at once. TestSSHLeaseSatisfiesSeam
// still pins the read half against the committed lease.
type fsConn interface {
	fsReader
	transfer.RemoteFS
	transfer.RemoteReadFS
}

// Option configures a Provider.
type Option func(*Provider)

// WithRoot sets the verified navigation root — the composition layer's OSC 7
// override (spec D2). Root() returns it, canonicalised by the server, when it
// is still usable at call time and falls back to the remote home otherwise;
// without this option, Root() returns the remote home, inferred and labelled.
func WithRoot(rootPath string) Option {
	return func(p *Provider) { p.root = rootPath }
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

// Provider is a read-only view of a remote machine's filesystem, over SFTP.
type Provider struct {
	conn        fsConn // the SFTP lease; owned, released by Close
	root        string // verified root override; "" means remote home, inferred
	entryCap    int
	sizeCap     int
	listTimeout time.Duration

	// Test seams: each models a change arriving between two steps of a real
	// operation, so a defect in the interval shows up deterministically.
	afterRead    func() // runs between the read and the after-stat
	afterReadDir func() // runs after the readdir, before entry processing
	beforeEntry  func() // runs before each entry's link metadata is read
}

// New creates an sftp provider on the given lease. The provider owns the
// lease: Close releases it.
func New(conn fsConn, opts ...Option) *Provider {
	p := &Provider{conn: conn, entryCap: defaultEntryCap, sizeCap: defaultSizeCap, listTimeout: defaultListTimeout}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Sink returns the write half of this provider's lease — the optional
// filesystem.Uploader seam (design D7). Implementing it is what makes a
// remote binding writable, and the local provider's silence is what makes a
// local one refuse: rule R1 is this method's absence over there, not a check
// performed anywhere.
//
// It is never nil, so a binding that carries one can always be written to.
// The sink is built per call rather than cached because it holds no state of
// its own: it is the lease plus a chunk size, and the lease is the field.
func (p *Provider) Sink() transfer.Sink { return transfer.NewSink(p.conn, transfer.DefaultChunk) }

// Source returns the read-stream half of this provider's lease — the
// optional filesystem.Downloader seam. It is Sink's mirror in construction
// and in lifetime: never nil, built per call because it holds no state of
// its own, and reading through the same lease every other call on this
// provider uses.
//
// It is a separate seam from Read, which is this provider's bounded,
// buffered, text-decoded answer to "show me this file". A download is
// unbounded and never decoded, and one method cannot be both without one of
// the two answers being wrong (filesystem.Downloader says which).
func (p *Provider) Source() transfer.Source { return transfer.NewSource(p.conn, transfer.DefaultChunk) }

// Root computes the navigation root (spec D2) from the provider, never from a
// shell: the server canonicalises "." — the SFTP session's starting
// directory, the remote account's home — where `echo $HOME` over exec would
// depend on remote commands being allowed at all. The verified override is
// canonicalised and re-checked at call time and falls back to the home,
// labelled inferred, when it is absent or unusable.
func (p *Provider) Root(ctx context.Context) (filesystem.Root, error) {
	if p.root != "" {
		if path.IsAbs(p.root) {
			if canonical, err := p.conn.RealPath(p.root); err == nil {
				if fi, err := p.conn.Stat(canonical); err == nil && fi.IsDir() {
					return p.newRoot(canonical, false, ""), nil
				}
			}
		}
		return p.fallbackRoot(fmt.Sprintf("requested root %q unusable — using remote home", p.root))
	}
	return p.fallbackRoot("no verified working directory — using remote home")
}

func (p *Provider) fallbackRoot(reason string) (filesystem.Root, error) {
	home, err := p.conn.RealPath(".")
	if err != nil {
		return filesystem.Root{}, fmt.Errorf("filesystem sftp: home root: %w", err)
	}
	return p.newRoot(home, true, reason), nil
}

// newRoot builds a Root whose Display abbreviates under the remote home with
// a tilde (spec §5.1: Display, for the header).
func (p *Provider) newRoot(rootPath string, inferred bool, reason string) filesystem.Root {
	home := rootPath
	if !inferred {
		// A verified override may sit under the home; resolve the home so the
		// display abbreviates consistently. The remote home is the session's
		// starting directory, canonicalised.
		if h, err := p.conn.RealPath("."); err == nil {
			home = h
		}
	}
	return filesystem.Root{Path: rootPath, Display: displayOf(rootPath, home), Inferred: inferred, InferredReason: reason}
}

// displayOf abbreviates paths under the remote home with a tilde (spec §5.1:
// Display, for the header). Paths outside home are returned as-is.
func displayOf(pathValue, home string) string {
	if pathValue == home {
		return "~"
	}
	if strings.HasPrefix(pathValue, home+"/") {
		return "~" + pathValue[len(home):]
	}
	return pathValue
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
// exact observed count — the sftp enumeration is a complete readdir, so the
// count is paid for — timedOut discards everything, never returning a
// complete-looking prefix, and the size cap refuses when the complete
// listing would cost too many bytes. The elapsed-time deadline is derived
// once at entry and re-checked at every stage boundary — it is enforced
// natively inside the lease's ReadDir (ReadDirContext checks the context on
// each packet) and again between entries, because everything after the
// readdir (per-entry link resolution, sorting, the digest) is also work that
// can hold the slot.
func (p *Provider) List(ctx context.Context, pathValue string, page filesystem.Page) (filesystem.Listing, error) {
	if err := checkPath(pathValue); err != nil {
		return filesystem.Listing{}, err
	}
	if page.Offset < 0 || page.Limit < 1 {
		return filesystem.Listing{}, &filesystem.ErrInvalidPage{Offset: page.Offset, Limit: page.Limit}
	}
	var deadline time.Time
	if p.listTimeout > 0 {
		deadline = time.Now().Add(p.listTimeout)
	}
	canonical, err := p.conn.RealPath(pathValue)
	if err != nil {
		return filesystem.Listing{}, wrapPathErr("list", pathValue, err)
	}
	// The elapsed-time cap rides the context into the lease: ReadDirContext
	// checks it on every READDIR packet, so a laggy server is cancelled
	// natively rather than measured after the fact.
	rdCtx := ctx
	var cancel context.CancelFunc
	if p.listTimeout > 0 {
		rdCtx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	infos, err := p.conn.ReadDir(rdCtx, canonical)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && p.listTimeout > 0 {
			return filesystem.Listing{}, &filesystem.ErrTimedOut{Timeout: p.listTimeout}
		}
		return filesystem.Listing{}, wrapPathErr("list", pathValue, err)
	}
	if p.timedOut(deadline) {
		return filesystem.Listing{}, &filesystem.ErrTimedOut{Timeout: p.listTimeout}
	}
	if p.entryCap > 0 && len(infos) > p.entryCap {
		return filesystem.Listing{}, &filesystem.ErrTooLarge{ObservedCount: len(infos), Limit: p.entryCap}
	}
	if p.afterReadDir != nil {
		p.afterReadDir()
	}
	entries := make([]filesystem.Entry, 0, len(infos))
	for _, fi := range infos {
		if p.beforeEntry != nil {
			p.beforeEntry()
		}
		if p.timedOut(deadline) {
			return filesystem.Listing{}, &filesystem.ErrTimedOut{Timeout: p.listTimeout}
		}
		// The metadata comes from the readdir response itself — one round
		// trip, and name, size, mode and mtime all from the same packet —
		// and link resolution reads through the canonical parent, the
		// directory actually listed, never the lexical one: a lexical parent
		// that is a symlink can be retargeted mid-operation, and then names
		// come from A while LinkTarget and LinkKind come from B. Rev is
		// computed over the mixture, so the mixture is the defect. Only
		// Entry.Path keeps the lexical shape (spec §5.1: "internal coherence
		// of one operation").
		entries = append(entries, p.buildEntry(fi, canonical, pathValue))
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
		Path:      pathValue,
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

// buildEntry derives one Entry from a readdir response's file info. The
// metadata comes with the name in the same packet — the SFTP transport
// answers readdir with full attrs, so unlike the local provider there is no
// separate per-entry stat round trip. Both parents matter, and the split is
// deliberate: the canonical parent is where the entry's link is resolved —
// the directory actually listed — while the lexical parent is what Entry.Path
// is built from, keeping the shape the client rendered. A symlink gets
// LinkTarget and its resolved LinkKind — required fields of Rev, so a
// retarget to a file of the same size and kind still changes the digest.
//
// Link failures are distinguishable states, never fabricated data: a link
// whose target cannot be statted is KindUnreadable only when the failure is
// something other than a missing target — permission denied and a genuinely
// broken link are different facts, and both must survive on the wire.
func (p *Provider) buildEntry(fi os.FileInfo, canonicalParent, lexicalParent string) filesystem.Entry {
	e := filesystem.Entry{Name: fi.Name(), Path: path.Join(lexicalParent, fi.Name())}
	e.Kind = filesystem.KindOf(fi.Mode())
	e.Size = fi.Size()
	e.ModTime = fi.ModTime()
	e.Mode = uint32(fi.Mode())
	if e.Kind == filesystem.KindSymlink {
		p.resolveLink(&e, path.Join(canonicalParent, fi.Name()))
	}
	return e
}

// resolveLink fills LinkTarget and LinkKind for a symlink entry, reading
// through the canonical parent so a lexical parent retargeted mid-operation
// cannot swap the link's facts. LinkTarget is the link text itself, returned
// by the server's READLINK; LinkKind is what the target resolves to, read by
// Stat (which follows symlinks) — a genuinely broken link (no target) is
// KindOther, distinct from a target that exists but cannot be statted, which
// is KindUnreadable.
func (p *Provider) resolveLink(e *filesystem.Entry, canonicalPath string) {
	target, err := p.conn.ReadLink(canonicalPath)
	if err != nil {
		// The link's text could not be read (vanished, permission denied):
		// it exists, we could not learn what it points at.
		e.LinkKind = filesystem.KindUnreadable
		return
	}
	e.LinkTarget = target
	if tgt, err := p.conn.Stat(canonicalPath); err == nil {
		e.LinkKind = filesystem.KindOf(tgt.Mode())
	} else if errors.Is(err, fs.ErrNotExist) {
		e.LinkKind = filesystem.KindOther // genuinely broken
	} else {
		e.LinkKind = filesystem.KindUnreadable // exists, unreachable
	}
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
// file at path, streamed by the lease: the provider passes the bound and the
// lease reads at most bound+1 bytes and never the whole file, so the memory
// guard holds for a 40 GB remote file. Truncated comes back from the lease,
// true iff the extra byte was readable. Openability is enforced from metadata
// read at call time (spec §5.1), never from a kind a caller was handed: the
// path is resolved and statted here, so a symlink retargeted — or a regular
// file swapped for a FIFO — since the last list is refused, not read. Size
// and mtime are sampled before and after; a difference sets Changed.
func (p *Provider) Read(ctx context.Context, pathValue string, maxBytes int64) (filesystem.Content, error) {
	if err := checkPath(pathValue); err != nil {
		return filesystem.Content{}, err
	}
	limit := maxBytes
	if limit <= 0 || limit > maxReadBytes {
		limit = maxReadBytes
	}
	canonical, err := p.conn.RealPath(pathValue)
	if err != nil {
		return filesystem.Content{}, wrapPathErr("read", pathValue, err)
	}
	// Stat follows symlinks, so kind is never KindSymlink here — the path is
	// resolved, and the metadata is of the object the read will open. The
	// between-stat-and-open window (a regular file swapped for a FIFO) is the
	// lease's: its lane's hard timeout is what unblocks a wedged open, proven
	// in internal/ssh — the provider cannot close it, because the lease's
	// ReadFile is one lane call that owns the descriptor.
	before, err := p.conn.Stat(canonical)
	if err != nil {
		return filesystem.Content{}, wrapPathErr("read", pathValue, err)
	}
	kind := filesystem.KindOf(before.Mode())
	if !filesystem.CanOpen(kind, kind) {
		return filesystem.Content{}, &filesystem.ErrNotRegular{Path: pathValue, Kind: kind}
	}
	data, truncated, err := p.conn.ReadFile(ctx, canonical, limit)
	if err != nil {
		return filesystem.Content{}, wrapPathErr("read", pathValue, err)
	}
	if p.afterRead != nil {
		p.afterRead()
	}
	after, err := p.conn.Stat(canonical)
	if err != nil {
		return filesystem.Content{}, wrapPathErr("read", pathValue, err)
	}
	binary := bytes.IndexByte(data, 0) >= 0 // the NUL heuristic of spec §5.1
	c := filesystem.Content{
		Path:      pathValue,
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
// later wave of the design's sequence (§6 step 5): SFTP polling arrives with
// the sftp provider's watching wave. SFTP has no change-notification in the
// protocol at all, and the polling that substitutes for it belongs to that
// wave, not here. Until then a watch cannot be established honestly — a
// Watch whose Events never fired would be a silent lie the product could not
// surface — so the provider refuses with ErrWatchUnavailable.
func (p *Provider) Watch(ctx context.Context, pathValue string) (filesystem.Watch, error) {
	return nil, &filesystem.ErrWatchUnavailable{}
}

// Canonical resolves a path to its provider-canonical identity. It exists
// because the Provider contract declares it; no wire method reaches it today
// — Listing.Canonical and Content.Canonical carry identity on every list and
// read — and it is what a future files.canonical would call.
func (p *Provider) Canonical(ctx context.Context, pathValue string) (string, error) {
	if err := checkPath(pathValue); err != nil {
		return "", err
	}
	c, err := p.conn.RealPath(pathValue)
	if err != nil {
		return "", wrapPathErr("canonical", pathValue, err)
	}
	return c, nil
}

// Close releases the provider-level resource, the SFTP lease. The lease's
// close-to-cancel guarantee (proven in internal/ssh) is what makes the
// interval honest: from New until Close returns, calls reach the connection;
// after Close returns, no call from this provider is in flight and the
// pooled reference is released.
func (p *Provider) Close() error {
	return p.conn.Close()
}

// checkPath enforces the provider's path syntax (spec §5.2: paths absolute
// and cleaned by the provider's rules). The provider owns path syntax and
// rejects rather than silently rewriting a caller's path.
func checkPath(pathValue string) error {
	if !path.IsAbs(pathValue) {
		return &filesystem.ErrInvalidPath{Path: pathValue, Reason: "path is not absolute"}
	}
	if path.Clean(pathValue) != pathValue {
		return &filesystem.ErrInvalidPath{Path: pathValue, Reason: "path is not clean"}
	}
	return nil
}

// wrapPathErr maps transport errors to the package's typed markers. ENOENT
// includes broken symlinks; ENOTDIR is a List on a file. Over the real wire
// the ENOTDIR branch is rarely reached — OpenSSH's sftp-server maps ENOTDIR
// to NO_SUCH_FILE, so List-on-a-file arrives as ErrNotFound — but the branch
// mirrors the local provider and is exercised by transports that do deliver
// it. Anything else is returned as-is.
func wrapPathErr(op, pathValue string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return &filesystem.ErrNotFound{Path: pathValue, Err: err}
	case errors.Is(err, fs.ErrPermission):
		return &filesystem.ErrPermission{Path: pathValue, Err: err}
	case errors.Is(err, syscall.ENOTDIR):
		return &filesystem.ErrNotDir{Path: pathValue, Err: err}
	default:
		return err
	}
}
