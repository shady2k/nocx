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
//
// The LOCAL tab is the one branch that does send a path, and it is the
// branch that mints nothing: a drop there uploads no bytes, it puts the
// dropped file's path at the person's own prompt (D9), which is what every
// terminal does and what a bare base name only looks like. Read the
// direction rather than the noun — R2's threat is a renderer that can NAME
// a source inbound, because a path it can spell is a file it can ask this
// process to read and send to a host of its choosing. files.upload has no
// such parameter and its decoder refuses unknown fields, so the request
// cannot express one. Handing a path outward, to the same human who chose
// the file, for their own command line on the machine the file is already
// on, is the opposite motion and there is no wire field that takes it back.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/session"
)

// SourcePick is everything the renderer learns about a file a human chose
// on the backend's machine: an opaque ticket, a display name, a size. The
// JSON tags are the wire shape of dialog.openFileForUpload's result and of
// each entry in a files.dropped notification — both declared in contracts/.
type SourcePick struct {
	// Ticket is the opaque id, 32 lowercase hex characters, or "" — which
	// is not an error in either of the two ways it happens. The person
	// cancelled the picker, and the renderer reads it as "no change" the
	// way dialog.openFile's empty path already does; or the drop landed on
	// a LOCAL tab, where nothing is minted because nothing may be uploaded
	// onto the machine the file is already on (D9) and the renderer is
	// being told the path for the prompt insert and nothing else.
	Ticket string `json:"sourceTicket"`
	// Name is the file's BASE name — never a path, and never the directory
	// it came from.
	Name string `json:"name"`
	// LocalPath is the absolute path of a file dropped on a LOCAL tab, and
	// nothing else ever sets it: not the picker, not a drop on a remote tab.
	// It is what D9 promised and Name could not keep — the renderer inserts
	// it, shell-quoted, at the prompt, and `report.pdf` is not a path, it is
	// a string that resolves against whatever the shell's cwd happens to be.
	//
	// omitempty is load-bearing rather than tidy: a pick that carries no
	// path marshals without the KEY, so the one shape R2 cares about — the
	// tab where a credential exists and bytes will move — cannot be misread
	// as an empty path, and dialog.openFileForUpload's contract, which
	// forbids additional properties, keeps rejecting one outright.
	LocalPath string `json:"localPath,omitempty"`
	// Size is the file's size in bytes at mint time. Advisory, like every
	// stat: the transfer's own read is what actually moves.
	Size int64 `json:"size"`
}

// SourceFile is what a claimed ticket resolves to, inside the backend: an
// ALREADY-OPEN handle on the bytes a human chose, and never a pathname. It
// never crosses the wire.
//
// The handle rather than the name, because a name is not an identity.
// Between a person choosing a file and the transfer reading it, the name
// can be renamed, replaced, or be a symlink whose target moved, and a
// ticket that handed a string back for somebody else to open would then
// name different bytes from the ones the person chose. On a machine where
// the renderer is not trusted with paths at all — the whole premise of R2 —
// a re-resolvable name gives back most of what the rule took away.
//
// An open handle cannot be raced at all: the inode is pinned from the
// moment of the gesture. The alternative the review offered, re-attesting
// device+inode with os.SameFile at redemption, narrows the window and does
// not close it — the file can be swapped back and forth around the check,
// and it costs a second stat and a refusal path a person cannot act on.
//
// What it costs instead: one file descriptor per outstanding ticket, at
// most maxSourceTickets of them, held for at most sourceTicketTTL; and, if
// the person deletes the file inside that window, its blocks stay allocated
// until the ticket is claimed, expires or the server stops. Those three are
// the closing events, and every one of them closes the handle.
//
// CLAIMING TRANSFERS OWNERSHIP. Once Claim has answered, the store no
// longer knows about the handle and the caller closes it on every path.
type SourceFile struct {
	// File is the open, read-only handle. Never nil in a SourceFile the
	// store returned.
	File *os.File
	Name string
	Size int64
	// Session is the tab the file was DROPPED on, and redemption refuses a
	// binding on any other one (R1: the wrong pairing is not expressible,
	// as a property of the addressing rather than as a check a renderer
	// remembered to make). Empty for a picker ticket, which named no tab —
	// see Mint.
	Session session.ID
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
	errDropTabNotOpen    = errors.New("the drop target names a tab that is not open")
	errDropNoRenderer    = errors.New("no renderer is attached to receive the dropped files")
)

// DropHost is what a window drop needs from the transport: what kind of tab
// the drop landed on, and the route to that tab's renderer. Implemented by
// *WSServer (TabKind and EmitFilesDropped, below); an interface because the
// store's own tests need to watch what a drop emits without a socket, and
// because a store constructed for a picker-only host has nothing to emit
// through.
//
// TabKind is here rather than left to the renderer because Go knows the
// answer and the renderer's answer is not binding: the drop target's
// session id is renderer-authored DOM, and what it names is a session this
// process opened. A local tab does not copy (design D9) — every terminal
// inserts the dropped file's path at the prompt instead — so it needs no
// upload credential, and one minted for it would be a credential nobody
// redeems.
type DropHost interface {
	// TabKind is the kind of the session a drop target named, and whether
	// that session is open at all. False refuses the drop whole.
	TabKind(sessionID string) (session.Kind, bool)
	// EmitFilesDropped tells that tab's renderer what was dropped on it.
	EmitFilesDropped(sessionID string, picks []SourcePick) error
}

type sourceTicketEntry struct {
	file     *os.File
	name     string
	size     int64
	session  session.ID
	mintedAt time.Time
}

// close lets go of the entry's handle. Every path that removes an entry
// without handing it to a claimant goes through here, and that is the whole
// list: the TTL sweep, the check inside Claim, and the take-back for a drop
// nobody heard.
func (e *sourceTicketEntry) close() { _ = e.file.Close() }

// SourceTicketStore is the mint. It is created once by the composition root
// and shared by the two mint sites; the transport reaches it through the
// dialog seam, and the window drop calls Dropped directly.
type SourceTicketStore struct {
	mu      sync.Mutex
	entries map[string]*sourceTicketEntry
	emit    DropHost
	// now is the clock, injectable so the TTL can be tested as an interval
	// rather than by sleeping.
	now func() time.Time
}

// NewSourceTicketStore builds the store. emit may be nil on a host with no
// window drop (a picker-only or test host); Dropped then refuses rather
// than minting tickets nobody will ever be told about.
func NewSourceTicketStore(emit DropHost) *SourceTicketStore {
	return &SourceTicketStore{
		entries: make(map[string]*sourceTicketEntry),
		emit:    emit,
		now:     time.Now,
	}
}

// Mint is the PICKER's mint: a file a human just chose in the native
// dialog, named by a ticket that is bound to no tab.
//
// That is a decision and not an omission, and the drop's opposite decision
// is what makes the argument. A drop carries a destination intent the
// renderer cannot author — Wails says which element the bytes landed on —
// so binding the ticket to that element's session is a fact being recorded,
// and pairing it with another tab contradicts what the person did. The
// picker carries no such fact: the gesture is "choose a file", not "drop it
// there", so any session id at mint time would be one the renderer wrote,
// exactly as the bindingId at redemption is. Binding to it would look like
// authorisation and would authorise nothing, which is worse than not doing
// it: a check that cannot fail is read by the next person as a check.
//
// So a picker ticket is bound by the tab that redeems it, once. What makes
// that safe is not the binding but the claim: one-shot, TTL-bounded, and
// answered only to a connection that owns the destination binding, which is
// re-checked by Registry.Acquire on the same call.
func (s *SourceTicketStore) Mint(path string) (SourcePick, error) {
	return s.mint(path, "")
}

// mint records a file a human just chose and returns what the renderer may
// know about it. The path is absolute and comes from the platform picker or
// the window drop — never from the wire. sid is the tab the file was
// dropped on, empty for the picker.
func (s *SourceTicketStore) mint(path string, sid session.ID) (SourcePick, error) {
	if !filepath.IsAbs(path) {
		return SourcePick{}, errSourceNotAbsolute
	}
	// The pre-check is not the authority — the fstat below is — but it has
	// to happen first all the same, because opening is not a harmless
	// question to ask of every path: os.Open on a fifo with no writer
	// BLOCKS, and a drop must not be able to wedge the handler that took
	// it. So the kind is asked by name first, cheaply, and then asked again
	// of the thing actually opened. A path that becomes a fifo between the
	// two is the one shape this ordering does not cover, and it is a race a
	// person would have to run against their own picker.
	if _, err := describeSource(path); err != nil {
		return SourcePick{}, err
	}

	// Open before taking the lock: the filesystem call is the slow part and
	// it needs nothing the lock guards.
	// #nosec G304 — path came from the platform picker or the window drop,
	// never from the wire; that is the whole of R2.
	f, err := os.Open(path) //nolint:gosec // see above
	if err != nil {
		return SourcePick{}, errSourceUnreadable
	}
	// And now the authoritative question, asked of the OPEN FILE rather
	// than of a name: this is the identity the ticket will carry, so it is
	// the one that has to answer. The size comes from here too — it
	// measures the bytes we are holding, not the bytes some name resolved
	// to a moment ago.
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return SourcePick{}, errSourceUnreadable
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return SourcePick{}, errSourceNotRegular
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictExpiredLocked()
	if len(s.entries) >= maxSourceTickets {
		_ = f.Close()
		return SourcePick{}, errSourceStoreFull
	}

	var buf [sourceTicketBytes]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand failing is an external call failing, and the honest
		// answer is a refusal — never a weaker id.
		_ = f.Close()
		return SourcePick{}, errors.New("cannot mint an upload ticket")
	}
	ticket := hex.EncodeToString(buf[:])
	name := filepath.Base(path)
	s.entries[ticket] = &sourceTicketEntry{
		file: f, name: name, size: info.Size(), session: sid, mintedAt: s.now(),
	}
	return SourcePick{Ticket: ticket, Name: name, Size: info.Size()}, nil
}

// describeSource is what the renderer may learn about a chosen file when
// nothing is being minted for it: the base name, the size, the absolute
// path, and no ticket. It is the local tab's answer (D9 — the PATH goes to
// the prompt, no bytes move) and it is also Mint's pre-check, so the two
// agree about what is choosable by construction rather than by two lists
// staying in step.
//
// Mint takes the pre-check and discards this result, which is what keeps
// the path on the one branch entitled to it: a minted pick is built from
// scratch above, with a ticket and no path.
//
// The error is DISCARDED rather than wrapped — an *os.PathError prints the
// path, and these errors travel to the renderer.
func describeSource(path string) (SourcePick, error) {
	if !filepath.IsAbs(path) {
		return SourcePick{}, errSourceNotAbsolute
	}
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
	return SourcePick{Name: filepath.Base(path), Size: info.Size(), LocalPath: path}, nil
}

// Claim resolves a ticket to the file it names and forgets it. One-shot:
// a second claim of the same ticket finds nothing, so a leaked ticket
// cannot be replayed. An unknown or expired ticket names nothing.
//
// Its one production caller is files.upload's handler, redeeming the
// optional sourceTicket param (ws_upload.go). It reaches this through
// sourceClaimer — Claim and nothing else — so the wire can spend a ticket a
// human minted and can never mint one.
//
// The claim happens AFTER every refusal that costs no bytes, and the
// collision question is the one that makes that ordering load-bearing: a
// collision answer starts no transfer and asks the renderer to call again,
// so a ticket spent before the probe could never be redeemed by the second
// call.
func (s *SourceTicketStore) Claim(ticket string) (SourceFile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[ticket]
	if !ok {
		return SourceFile{}, false
	}
	delete(s.entries, ticket)
	if s.now().Sub(e.mintedAt) > sourceTicketTTL {
		// Expired between the mint and here: nobody receives the handle, so
		// this is one of the ends the interval closes at.
		e.close()
		return SourceFile{}, false
	}
	return SourceFile{File: e.file, Name: e.name, Size: e.size, Session: e.session}, true
}

// Len is how many tickets are outstanding. It exists for the tests that
// assert nothing was minted — the assertion the whole design rests on.
func (s *SourceTicketStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// evictExpiredLocked drops tickets past their TTL and closes their handles.
// There is no timer and no goroutine: a source ticket names no running
// transfer, so nothing has to be cancelled at the moment it expires, and
// the sweep on the next mint plus the check in Claim close the interval
// from both ends. What a ticket now holds — an open descriptor — is the
// same shape of thing: bounded by maxSourceTickets whether or not a sweep
// has run, so a late sweep costs at most that many descriptors and never
// unboundedly many.
func (s *SourceTicketStore) evictExpiredLocked() {
	now := s.now()
	for k, e := range s.entries {
		if now.Sub(e.mintedAt) > sourceTicketTTL {
			delete(s.entries, k)
			e.close()
		}
	}
}

// Close forgets every outstanding ticket and closes the handles they hold.
// It is the last of the three events that end a ticket's interval — claim,
// expiry, and the server going away — and it exists so a stopped server
// leaves no descriptor behind for a gesture nobody will ever finish.
func (s *SourceTicketStore) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, e := range s.entries {
		delete(s.entries, k)
		e.close()
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
// (design §5.5). What authorises the write is still the binding, checked
// against the requesting connection when files.upload arrives; what the
// ticket's session adds is the other half — that the write goes to the tab
// the file was actually dropped on, and not to some other tab the same
// connection happens to own.
//
// A LOCAL TAB MINTS NOTHING. A local tab does not copy (D9): the terminal
// inserts the dropped file's path at the prompt, and copying a file onto
// the machine it is already on is not a thing anybody asked for. The
// renderer is still told what was dropped — that is what the prompt insert
// reads, and in the Wails window the drop never becomes a DOM event it
// could read instead — but the picks carry no ticket, because a credential
// nobody redeems is still a credential that exists. What they carry
// instead is the path, which is the only branch that does.
//
// Nothing is minted unless something will be emitted: a drop with no
// session, a hostile session attribute, a session that is not open, or no
// attached renderer leaves the store exactly as it found it, because a
// ticket nobody is told about is a live credential waiting out its TTL for
// no reason.
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
	// And then held to something the shape cannot check: that it names a
	// tab this process actually has. A well-formed id for a session that
	// is not open would otherwise mint a ticket bound to nothing, which is
	// the one binding no redemption could ever satisfy.
	kind, open := s.emit.TabKind(sessionID)
	if !open {
		return errDropTabNotOpen
	}
	if len(filenames) == 0 {
		return errors.New("the drop carried no files")
	}

	picks := make([]SourcePick, 0, len(filenames))
	refused := 0
	var firstErr error
	for _, name := range filenames {
		var pick SourcePick
		var err error
		if kind == session.KindLocal {
			pick, err = describeSource(name)
		} else {
			pick, err = s.mint(name, session.ID(sessionID))
		}
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
		if e := s.entries[p.Ticket]; e != nil {
			delete(s.entries, p.Ticket)
			e.close()
		}
	}
}

// UploadSources is the server's source-ticket mint: what the composition
// root hands to the two gestures that mint (the native picker adapter and
// the window drop) and what files.upload redeems from. One store, one
// owner — a second one would mint tickets this server could never claim.
func (s *WSServer) UploadSources() *SourceTicketStore { return s.sources }

// TabKind answers what kind of tab a drop target named, and whether that
// session is open at all. The registry is the authority: the drop's
// data-session-id is renderer-authored DOM, and the only thing that can say
// what it names is the process that opened it.
func (s *WSServer) TabKind(sessionID string) (session.Kind, bool) {
	sess, err := s.registry.Get(session.ID(sessionID))
	if err != nil || sess == nil {
		return 0, false
	}
	return sess.Kind(), true
}

// EmitFilesDropped sends the files.dropped notification to the renderer
// attached to THAT SESSION, resolved at emit time — the addressing
// files.changed already uses (ws_files.go), and for the same reason
// restated on a stronger payload.
//
// It used to broadcast to every connected renderer, which was survivable
// only while nothing could redeem a source ticket: a drop on tab A handed
// its credentials to every attached client, and the only thing keeping the
// wrong pairing from being expressible was that no pairing was. It is not a
// second check beside the ticket's session — the ticket is what refuses the
// wrong redemption — but a notification that hands a credential to somebody
// who was not dropped on has already given away more than the refusal can
// take back.
//
// There is no retention here, unlike files.uploadDone: a ticket has a TTL a
// person is inside, and a drop nobody was attached for is a gesture that
// did not happen. The refusal is reported so the caller can take the
// tickets back.
func (s *WSServer) EmitFilesDropped(sessionID string, picks []SourcePick) error {
	rx := s.getRx(session.ID(sessionID))
	if rx == nil {
		return errDropNoRenderer
	}
	wconn, _ := rx.getSubscriber()
	if wconn == nil {
		return errDropNoRenderer
	}
	return wconn.TryNotify("files.dropped", mustMarshal(map[string]any{
		"sessionId": sessionID,
		"sources":   picks,
	}))
}
