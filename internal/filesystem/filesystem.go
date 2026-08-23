// Package filesystem is the session-aware file tree backend: the Provider
// contract, the binding registry that guards it, and the errors transport
// switches on.
//
// # Why a second directory lister (spec D11)
//
// internal/completion already lists remote directories, through SSHCompleter
// running bash over a DiscoveryConn. Under the AGENTS.md "one owner per
// behaviour" rule this must be justified rather than duplicated silently.
//
// They answer different questions. Completion asks "what does the shell think
// completes this prefix" — a question only a shell can answer, because it
// includes bash's own completion rules. The tree asks "what does this
// directory contain, with sizes, modes and mtimes" — a filesystem question,
// and SFTP answers it structurally, works when remote exec is forbidden while
// SFTP is allowed, and does not spend a shell. Parsing ls output for the tree
// would be the second implementation, not this one.
//
// # The structural guarantee (spec D1)
//
// A bound filesystem is unreachable except through Registry.Acquire. Binding
// holds its provider in an unexported field, so "every handler must remember
// to check" is not a discipline anybody has to keep: a handler cannot forget
// a check it never performs. Acquire itself performs the one authorisation
// check — the caller must Own the binding's session (D15) — and takes the
// use-guard that keeps the binding alive for the call's duration.
//
// # Path syntax
//
// Path syntax belongs to the provider. local uses path/filepath; sftp uses
// path, because SFTP specifies POSIX-style paths regardless of the OS nocx
// runs on. Nothing in this package (shared by both) interprets a path.
//
// # Watching
//
// Watch, WatchKind and WatchMode are part of the Provider and Handle
// contracts (spec §5.1), but the watching wave — fsnotify locally, polling
// over SFTP — is a later step of the design's sequence (§6 step 5). Until
// then the local provider's Watch refuses with ErrWatchUnavailable; the
// Handle's set-replacement semantics are already live.
package filesystem

import (
	"context"
	"time"

	"github.com/shady2k/nocx/internal/transfer"
)

// Provider is a read-only view of one machine's filesystem.
//
// The interface has no mutating method, so mutation cannot be added to one
// provider without changing the contract for both (spec §5.1). It is a rule
// about symmetry, not a permanent ban: the mutation epic adds methods, and
// adds them to both providers.
type Provider interface {
	Root(ctx context.Context) (Root, error)
	List(ctx context.Context, path string, page Page) (Listing, error)
	Read(ctx context.Context, path string, maxBytes int64) (Content, error)
	Watch(ctx context.Context, path string) (Watch, error)
	Canonical(ctx context.Context, path string) (string, error)
	Close() error
}

// Uploader is the optional write seam (upload design D7). A provider that
// can write a file onto the machine it views implements it. BOTH providers
// do: sftp writes over the tab's lease, local writes through os.
//
// The seam is still optional, and rule R1 — "a file can only be uploaded to
// the machine the tab is actually on" — is still its absence rather than a
// check somebody performs: a provider that cannot write must not implement
// it, and the binding it is registered on then refuses with
// *ErrUploadUnsupported because it holds no sink.
//
// D7 first withheld this method from the local provider, on the reasoning
// that a local tab inserts a path and never copies (D9), so a local write
// would be a path with no caller — the nocx-rtg0 failure. That was true of
// the DESKTOP build only. In a browser a "local" tab is a shell on the
// backend's machine while the dropped file is on the browser's, so there is
// no path to insert and the upload is the only thing the gesture can mean.
// The caller exists; the seam is implemented rather than dead.
//
// It is not part of Provider because Provider is read-only by contract and
// a mutating method there would have to land on BOTH providers (§5.1) —
// which is now what happened, deliberately and on both, rather than by one
// provider quietly growing a write.
//
// The assertion is performed once, where the endpoint attester's already is:
// in the composition of files.open, the last moment the provider is in hand
// before Register. Binding.provider is unexported and Acquire returns a
// Handle, so nothing downstream could perform it — which is the structural
// guarantee working as designed, not an obstacle to route around.
type Uploader interface {
	// Sink returns the write half of this provider's view. It is never
	// nil: a provider that implements Uploader can write, and one that
	// cannot must not implement it, because a nil sink from a live
	// Uploader would be a capability that refuses without saying why.
	Sink() transfer.Sink
}

// Downloader is the optional READ-STREAM seam — the mirror of Uploader.
// A provider that can stream a file off the machine it views implements it.
// BOTH shipped providers do: sftp reads over the tab's lease, local reads
// through os.
//
// It is separate from Provider.Read, and the separation is the point rather
// than an accident of layering. Read answers "show me this file": it is
// bounded, buffered whole in memory, decoded as text and reported as
// truncated past the bound, because a viewer needs a page and not a stream.
// A download is the other question — "give me this file" — and none of
// those four properties may hold for it: it is unbounded, never buffered,
// never decoded, and a truncated download is a corrupt file rather than a
// labelled excerpt. One method cannot answer both without one of the two
// answers being wrong.
//
// R1 in this direction is the seam's absence, exactly as it is for Uploader:
// a provider that cannot stream must not implement it, and the binding it is
// registered on then refuses with *ErrDownloadUnsupported because it holds
// no source. Reading a file off the WRONG host is as wrong as writing to it,
// and the addressing is what makes it inexpressible — a download names a
// bindingId, and a binding names one session's filesystem and nothing else.
//
// It is not part of Provider for Uploader's reason: Provider is read-only by
// contract, and adding a streaming method there would land it on both
// providers by force rather than by decision. The assertion is performed
// once, where Uploader's already is — in the composition of files.open, the
// last moment the concrete provider is in hand before Register.
type Downloader interface {
	// Source returns the read-stream half of this provider's view. It is
	// never nil: a provider that implements Downloader can stream, and one
	// that cannot must not implement it, because a nil source from a live
	// Downloader would be a capability that refuses without saying why.
	Source() transfer.Source
}

// Root is the provider-computed navigation root (spec D2). The provider
// computes it; a verified OSC 7 cwd overrides it, chosen by the composition
// layer. An inferred root is labelled.
type Root struct {
	Path           string
	Display        string // ~-abbreviated, for the header
	Inferred       bool
	InferredReason string
}

// Page addresses one slice of a complete, sorted listing.
type Page struct {
	Offset int
	Limit  int
}

// Kind classifies one entry. It replaces the draft's IsDir/IsSymlink pair
// because the two encode a lattice the product must not flatten: a FIFO
// blocks forever on read, a device or a procfs pseudo-file has no meaningful
// size and may produce unbounded or ever-changing content (spec §5.1).
// KindUnreadable is not a kind of object but a kind of failure: the entry
// exists (readdir saw it) yet its metadata could not be read — permission
// denied or I/O — and it must not be rendered as empty plausible data, nor
// be confused with a genuinely broken symlink, which is a symlink whose
// target is missing.
type Kind string

const (
	KindRegular    Kind = "regular"
	KindDir        Kind = "dir"
	KindSymlink    Kind = "symlink"
	KindOther      Kind = "other"
	KindUnreadable Kind = "unreadable"
)

// Entry is one row of a Listing. Path is the lexical path — the absolute
// path as spelled, symlinks unresolved — never the canonical identity; the
// frontend derives a copy-relative path from it against the root.
type Entry struct {
	Name, Path string
	Kind       Kind
	LinkTarget string // symlinks only
	LinkKind   Kind   // what the link resolves to; other when broken
	Size       int64
	ModTime    time.Time
	Mode       uint32
}

// Listing is one page of one directory. Canonical is the provider-canonical
// identity of the directory listed (D9) and is returned for every successful
// list, not only for symlinks, so the root and every ordinary ancestor speak
// the same identity vocabulary. Entries is never nil: an empty directory is
// an empty slice, so it marshals as [] rather than null.
type Listing struct {
	Path      string
	Canonical string
	Entries   []Entry
	Offset    int
	Total     int
	HasMore   bool
	Rev       string
}

// Content is the bounded result of reading one file. Size is the number of
// bytes actually returned (len(Text) when textual), not the file's size —
// Truncated is what says a larger file was cut at the effective limit.
type Content struct {
	Path      string
	Canonical string // identity of the object actually read; what singletonKey uses
	Text      string // always valid UTF-8
	Size      int64
	ModTime   time.Time
	Truncated bool
	Binary    bool
	Lossy     bool
	Changed   bool // size or mtime differed before vs after the read
}

// Watch observes one path for change (spec D5). Events are invalidation
// hints, never diffs: the consumer re-lists the directory and compares Rev.
type Watch interface {
	Events() <-chan struct{}
	Mode() WatchMode
	Close() error
}

// WatchKind is how a watch reports its mechanism (spec §5.5).
type WatchKind string

const (
	WatchLive    WatchKind = "live"    // fsnotify locally
	WatchPolling WatchKind = "polling" // periodic re-listing; SFTP's designed mode
)

// WatchMode describes one established watch set. DegradedReason is set only
// when polling is a degradation rather than the designed mode — a local
// watch that could not be established — and is what the product surfaces as
// the persistent "Polling" warning badge.
type WatchMode struct {
	Kind           WatchKind
	DegradedReason string
}
