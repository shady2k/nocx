package transport

// Source tickets — the other half of R2.
//
// "The renderer may name the destination. It may never name the source."
// files.upload has no sourcePath and no source discriminator, and its
// decoder refuses unknown fields, so a renderer cannot ask the backend to
// read a file on the backend's own disk. That rule is what makes the wire
// safe; on its own it also makes a file that genuinely IS on the backend's
// disk unuploadable.
//
// A source ticket is the answer. It is minted backend-side at the moment a
// HUMAN chose the file — in the native picker, or by dropping it on the
// window — and handed to the renderer as an opaque id it can echo but never
// author. What the renderer cannot spell, it cannot ask for.
//
// The two mint sites are therefore the only two gestures that exist: the
// native picker handler (ws_dialog.go, behind the UploadPicker seam) and
// Dropped below. Neither is reachable from a JSON-RPC parameter, and
// TestSourceTicket_CannotBeMintedFromTheWire calls every registered method
// to say so.
//
// # What the ticket is, and is not (design D4)
//
// It is a bearer credential: possession names bytes the backend will read
// and send. It is therefore crypto/rand at 128 bits, one-shot, TTL-bounded,
// held in a bounded map, and it is never logged and never put in an error
// string. It is NOT an authorisation to write anywhere: the destination is
// still addressed by a bindingId the backend issued, re-checked against the
// requesting connection's session set. A ticket names a source and nothing
// else.
//
// And the ticket never carries a path outward. The renderer learns a base
// name and a size; the directory stays in this process. That holds for the
// error channel too — every refusal below is worded without the path,
// because "the file /home/dev/.ssh/id_ed25519 is not readable" hands back
// exactly what the ticket exists to withhold. The caller has the path and
// may log it; this store never does.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SourcePick is everything the renderer learns about a file a human chose
// on the backend's machine: an opaque ticket, a display name, a size. The
// JSON tags are the wire shape of dialog.openFileForUpload's result and of
// each entry in a files.dropped notification — both declared in contracts/.
type SourcePick struct {
	// Ticket is the opaque id, 32 lowercase hex characters, or "" when the
	// person cancelled the picker. Empty is not an error: the renderer
	// treats it as "no change", the way dialog.openFile's empty path
	// already works.
	Ticket string `json:"sourceTicket"`
	// Name is the file's BASE name — never a path, and never the directory
	// it came from.
	Name string `json:"name"`
	// Size is the file's size in bytes at mint time. Advisory, like every
	// stat: the transfer's own read is what actually moves.
	Size int64 `json:"size"`
}

// SourceFile is what a claimed ticket resolves to, inside the backend. It
// never crosses the wire.
type SourceFile struct {
	Path string
	Name string
	Size int64
}

const (
	// sourceTicketTTL bounds how long an unclaimed ticket lives. It spans a
	// person picking a file, being asked the collision question and
	// answering it — the same order as planTTL, which bounds the same kind
	// of "a human is deciding" gap.
	sourceTicketTTL = 10 * time.Minute
	// maxSourceTickets bounds the map. A multi-file drop is the largest
	// legitimate burst, and 256 is far above any drop a person makes by
	// hand while still being a bound.
	maxSourceTickets = 256
	// sourceTicketBytes is the ticket's entropy: 128 bits, the floor D4
	// sets for both tickets.
	sourceTicketBytes = 16
	// dropSessionAttr is the attribute the drop target carries to say which
	// tab it is. Wails delivers every attribute of the element that carries
	// data-file-drop-target; this is the one we read.
	dropSessionAttr = "data-session-id"
)

// Refusals, worded without the path (see the file comment). The caller
// holds the path and may log it; these strings reach the renderer.
var (
	errSourceNotAbsolute = errors.New("the chosen file must be named by an absolute path")
	errSourceUnreadable  = errors.New("the chosen file cannot be read")
	errSourceNotRegular  = errors.New("only a regular file can be uploaded")
	errSourceStoreFull   = errors.New("too many files are waiting to be uploaded")
	errDropNamesNoTab    = errors.New("the drop target does not name a session")
	errDropNoRenderer    = errors.New("no renderer is attached to receive the dropped files")
)

// DropEmitter delivers a window drop to the renderer. Implemented by
// *WSServer (EmitFilesDropped, below); an interface because the store's own
// tests need to watch what a drop emits without a socket, and because a
// store constructed for a picker-only host has nothing to emit through.
type DropEmitter interface {
	EmitFilesDropped(sessionID string, picks []SourcePick) error
}

type sourceTicketEntry struct {
	file     SourceFile
	mintedAt time.Time
}

// SourceTicketStore is the mint. It is created once by the composition root
// and shared by the two mint sites; the transport reaches it through the
// dialog seam, and the window drop calls Dropped directly.
type SourceTicketStore struct {
	mu      sync.Mutex
	entries map[string]*sourceTicketEntry
	emit    DropEmitter
	// now is the clock, injectable so the TTL can be tested as an interval
	// rather than by sleeping.
	now func() time.Time
}

// NewSourceTicketStore builds the store. emit may be nil on a host with no
// window drop (a picker-only or test host); Dropped then refuses rather
// than minting tickets nobody will ever be told about.
func NewSourceTicketStore(emit DropEmitter) *SourceTicketStore {
	return &SourceTicketStore{
		entries: make(map[string]*sourceTicketEntry),
		emit:    emit,
		now:     time.Now,
	}
}

// Mint records a file a human just chose and returns what the renderer may
// know about it. The path is absolute and comes from the platform picker or
// the window drop — never from the wire.
func (s *SourceTicketStore) Mint(path string) (SourcePick, error) {
	if !filepath.IsAbs(path) {
		return SourcePick{}, errSourceNotAbsolute
	}
	// Stat before taking the lock: the filesystem call is the slow part and
	// it needs nothing the lock guards. The error is DISCARDED rather than
	// wrapped — an *os.PathError prints the path, and this error travels to
	// the renderer.
	info, err := os.Stat(path)
	if err != nil {
		return SourcePick{}, errSourceUnreadable
	}
	// Directories are out of scope (design §4): a recursive walk, mkdir,
	// partial failure mid-tree, symlinks and modes are a materially larger
	// problem on the same mechanism. Anything that is not a regular file
	// (a device, a socket, a fifo) has no size a transfer can honour.
	if !info.Mode().IsRegular() {
		return SourcePick{}, errSourceNotRegular
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictExpiredLocked()
	if len(s.entries) >= maxSourceTickets {
		return SourcePick{}, errSourceStoreFull
	}

	var buf [sourceTicketBytes]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand failing is an external call failing, and the honest
		// answer is a refusal — never a weaker id.
		return SourcePick{}, errors.New("cannot mint an upload ticket")
	}
	ticket := hex.EncodeToString(buf[:])
	file := SourceFile{Path: path, Name: filepath.Base(path), Size: info.Size()}
	s.entries[ticket] = &sourceTicketEntry{file: file, mintedAt: s.now()}
	return SourcePick{Ticket: ticket, Name: file.Name, Size: file.Size}, nil
}

// Claim resolves a ticket to the file it names and forgets it. One-shot:
// a second claim of the same ticket finds nothing, so a leaked ticket
// cannot be replayed. An unknown or expired ticket names nothing.
//
// NOTHING IN PRODUCTION CALLS THIS YET, and that is stated here rather than
// left to be discovered:
//
//	deadcode -tags gtk3 -whylive '…transport.SourceTicketStore.Claim' ./...
//	  → "reachable only through reflection"
//
// while Mint and Dropped answer with a path from main(). The caller is
// files.upload accepting its optional sourceTicket param — the sink half,
// in ws_upload.go, which this task deliberately does not touch. Until that
// lands, a source ticket can be minted and handed to the renderer and
// cannot yet be redeemed: the picker and the drop are real, the transfer
// they feed is not. The deadcode ratchet does NOT catch this (the baseline
// is unchanged at 90 either way) — an unwired METHOD is exactly its blind
// spot (AGENTS.md, nocx-re6gk), which is why it is written down.
func (s *SourceTicketStore) Claim(ticket string) (SourceFile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[ticket]
	if !ok {
		return SourceFile{}, false
	}
	delete(s.entries, ticket)
	if s.now().Sub(e.mintedAt) > sourceTicketTTL {
		return SourceFile{}, false
	}
	return e.file, true
}

// Len is how many tickets are outstanding. It exists for the tests that
// assert nothing was minted — the assertion the whole design rests on.
func (s *SourceTicketStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// evictExpiredLocked drops tickets past their TTL. There is no timer and no
// goroutine: a source ticket names no running transfer, so nothing has to
// be cancelled at the moment it expires, and the sweep on the next mint
// plus the check in Claim close the interval from both ends.
func (s *SourceTicketStore) evictExpiredLocked() {
	now := s.now()
	for k, e := range s.entries {
		if now.Sub(e.mintedAt) > sourceTicketTTL {
			delete(s.entries, k)
		}
	}
}

// Dropped is the window-drop mint site. filenames are the absolute paths
// Wails delivered; attrs are the drop target element's HTML attributes, of
// which data-session-id is the one that matters.
//
// It resolves NO destination. It mints a ticket per file and tells the
// renderer, which then calls files.upload with its own bindingId like any
// other caller — so the native gesture joins the same authorised route
// rather than becoming a second addressing scheme that skips connState
// (design §5.5). The session id is a routing hint for the renderer and
// nothing more: what actually authorises the write is the binding, checked
// against the requesting connection when files.upload arrives.
//
// Nothing is minted unless something will be emitted: a drop with no
// session, a hostile session attribute or no attached renderer leaves the
// store exactly as it found it, because a ticket nobody is told about is a
// live credential waiting out its TTL for no reason.
func (s *SourceTicketStore) Dropped(filenames []string, attrs map[string]string) error {
	sessionID := attrs[dropSessionAttr]
	if sessionID == "" {
		return errDropNamesNoTab
	}
	// The attribute is renderer-authored DOM, so it is held to the shape a
	// server-minted session id has before it goes back out on the wire.
	if msg := validateSessionIDShape(sessionID); msg != "" {
		return fmt.Errorf("the drop target's session id %s", msg)
	}
	if s.emit == nil {
		return errDropNoRenderer
	}
	if len(filenames) == 0 {
		return errors.New("the drop carried no files")
	}

	picks := make([]SourcePick, 0, len(filenames))
	refused := 0
	var firstErr error
	for _, name := range filenames {
		pick, err := s.Mint(name)
		if err != nil {
			refused++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		picks = append(picks, pick)
	}

	if len(picks) == 0 {
		return fmt.Errorf("none of the %d dropped files could be read: %w", len(filenames), firstErr)
	}
	if err := s.emit.EmitFilesDropped(sessionID, picks); err != nil {
		// Undelivered tickets are live credentials nobody can use. Take
		// them back rather than leaving them to time out.
		s.forget(picks)
		return err
	}
	if refused > 0 {
		// A silent skip reads as "all of them arrived". The caller logs
		// this; the renderer sees the files that did make it.
		return fmt.Errorf("%d of %d dropped files could not be read: %w", refused, len(filenames), firstErr)
	}
	return nil
}

// forget drops tickets that were minted for a delivery that did not happen.
func (s *SourceTicketStore) forget(picks []SourcePick) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range picks {
		delete(s.entries, p.Ticket)
	}
}

// EmitFilesDropped sends the files.dropped notification to every connected
// renderer. It is the same broadcast the vault unlock ask uses — one owner
// for "tell every attached client" — and it reports the no-client case
// rather than dropping the drop on the floor, because the tickets it names
// have to be taken back when nobody heard.
func (s *WSServer) EmitFilesDropped(sessionID string, picks []SourcePick) error {
	return s.broadcastAsk("files.dropped", map[string]any{
		"sessionId": sessionID,
		"sources":   picks,
	}, errDropNoRenderer)
}
