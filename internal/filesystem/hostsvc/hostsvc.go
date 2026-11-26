// Package hostsvc is the far helper's `fs` service: the read half of the
// filesystem surface a coordinator reaches over a connection this helper holds
// (ADR-727g63tr, design §5.10).
//
// # It maps, it does not re-derive
//
// Root, stat, list and canonical are internal/filesystem/local's own answers,
// mapped onto the wire. The provider owns path syntax, the entry caps, the
// deterministic order and the whole-directory digest; this package holds a
// *local.Provider and asks it, because a second implementation of any of those
// would agree everywhere anybody looked and disagree somewhere nobody did.
//
// # read is the one op with work of its own
//
// The plan's whole-file read is not local.Provider.Read: it samples the open
// descriptor before and after the bytes, retries while the file moves, and
// answers a version over exactly the bytes it returns. The range read answers
// the object's identity instead. Both open with O_NONBLOCK and fstat the SAME
// descriptor, so a FIFO swapped in for a regular file is answered rather than
// blocked on — the interval local.Read closed for the panel, kept here because
// this is a second open and would otherwise be a second window.
//
// The write half (write.go) and its journal (journal.go) are the other place
// this package does more than map. A write is a session the caller holds across
// a lost reply, the temporary it streams into belongs to this process, and the
// journal is what makes that temporary recoverable after this process is gone.
// `inventory` reports what a previous generation could not remove, so a person
// is told about a `.nocx-write-…` file instead of finding one.
//
// # The artifact it runs inside
//
// This package is compiled into the helper written to hosts we do not own, so
// it names no ssh client, no sftp, no hostconn and no deploy: the dependency
// tests in internal/filesystem and internal/helper/deploy are the fence, and
// they walk `go list -deps` rather than reading imports by eye.
package hostsvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"

	filesystem "github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// maxReadLength is the bound fs.read accepts: the files.read ceiling plus one,
// so a caller can ask for "the ceiling and a bit" without a second bound. The
// extra byte is what a truncation probe needs; the op's shape has no truncation
// flag because a whole-file read refuses a file it cannot return in one piece
// (too-large) rather than returning a labelled prefix.
const maxReadLength = local.MaxReadBytes + 1

// wholeReadAttempts is the retry budget of a whole-file read. Three attempts
// is the plan's number: one to detect movement, two to let a writer finish,
// and a refusal rather than a wrong version after that. A quiet file costs one.
const wholeReadAttempts = 3

// Service is the helper's `fs` service.
//
// ONE SERVICE PER DAEMON, NOT PER CONNECTION. The read ops are stateless, but a
// write session is not: its temporary belongs to the process, and an outcome
// has to survive the connection that asked for it — a commit whose reply was
// lost is answered on the NEXT connection, and an id this process never held
// must answer `unknown-outcome` rather than a second write. The service is
// therefore constructed once, like the session service, and registered on every
// host the daemon accepts.
type Service struct {
	provider *local.Provider

	// journal is this daemon's durable record of the write sessions it has
	// started (journal.go). It is required: a service without one could answer
	// every read op, but a write session it opened would leave a temporary
	// nobody could name after a crash — the state the journal exists to end.
	journal *journal

	// afterRead is the test seam for the interval a whole-file read guards:
	// it runs after the bytes were read and BEFORE the after-stat that decides
	// whether the file moved. A test rewrites the file there, which models
	// "somebody wrote during the read" without a sleeping writer goroutine.
	afterRead func(attempt int)

	// seams fail one step of a commit deterministically; every nil field is
	// the real call (see writeSeams).
	seams writeSeams

	// mu guards sessions and closed. locks serialises installs that land on one
	// resolved path, so the conditional check and the rename are one critical
	// section for everybody in this process.
	mu       sync.Mutex
	closed   bool
	sessions map[string]*writeSession
	locks    *pathLocks

	// strandedMu guards the snapshot fs.inventory answers with: the
	// temporaries this daemon found on start and the ones it could not remove
	// itself, minus the ones it has since removed. It is a snapshot rather than
	// a fresh disk walk, because a temporary that this daemon already knows is
	// unreachable is not made reachable by asking again — and a walk on every
	// inventory would answer a question about the journal's readability that
	// nobody asked.
	strandedMu    sync.Mutex
	stranded      []proto.FSStrandedTemp
	strandedPaths map[string]bool
}

// New builds the fs service over a local provider and opens its write-session
// journal. The provider is the caller's — the composition root constructs it,
// and a test injects one with local.WithRoot, WithEntryCap or WithListTimeout —
// the same division git/hostsvc.New(factory) keeps.
//
// The journal is opened HERE rather than lazily before the first write: opening
// it is also the start-up sweep, and a helper that has only ever been read from
// still owes the temporaries of the generation before it. A journal that cannot
// be opened fails the construction, because a helper that silently served
// writes with no crash recovery would be a helper whose product promise is a
// log line nobody reads.
func New(provider *local.Provider, journal JournalConfig) (*Service, error) {
	j, stranded, err := openJournal(journal)
	if err != nil {
		return nil, err
	}
	s := &Service{
		provider:      provider,
		journal:       j,
		sessions:      map[string]*writeSession{},
		locks:         newPathLocks(),
		strandedPaths: map[string]bool{},
	}
	for _, temp := range stranded {
		s.recordStranded(temp.Path, temp.Reason)
	}
	return s, nil
}

// Name is the reserved service name the host registers it under.
func (s *Service) Name() string { return proto.ServiceFS }

// Ops is the closed set of operations this service serves: the five read ops,
// the four that make one write a session, and the inventory of what an earlier
// generation could not clean up. An op this generation does not have is one a
// coordinator learns about at hello, not one it learns about by writing.
func (s *Service) Ops() []string {
	return []string{
		proto.OpRoot, proto.OpStat, proto.OpList, proto.OpCanonical, proto.OpRead,
		proto.OpWriteOpen, proto.OpWrite, proto.OpWriteCommit, proto.OpWriteAbort,
		proto.OpInventory,
	}
}

// ParamsSchema declares each op's params type; the host decodes a request's
// params through it and audits it for argv at registration (D3). None of them
// takes a command, a script or a bare string list — a write carries bytes and a
// destination, never a way to run them.
func (s *Service) ParamsSchema(op string) *host.Schema {
	switch op {
	case proto.OpRoot:
		return host.SchemaFor(proto.FSRootParams{})
	case proto.OpStat, proto.OpCanonical:
		return host.SchemaFor(proto.FSPathParams{})
	case proto.OpList:
		return host.SchemaFor(proto.FSListParams{})
	case proto.OpRead:
		return host.SchemaFor(proto.FSReadParams{})
	case proto.OpWriteOpen:
		return host.SchemaFor(proto.FSWriteOpenParams{})
	case proto.OpWrite:
		return host.SchemaFor(proto.FSWriteParams{})
	case proto.OpWriteCommit:
		return host.SchemaFor(proto.FSWriteCommitParams{})
	case proto.OpWriteAbort:
		return host.SchemaFor(proto.FSWriteAbortParams{})
	case proto.OpInventory:
		return host.SchemaFor(proto.FSInventoryParams{})
	}
	return nil
}

// Call dispatches one named operation. Every op runs under the request
// context; a cancelled request is answered `timed-out` rather than left to
// finish work nobody is waiting for.
func (s *Service) Call(ctx context.Context, op string, params json.RawMessage) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch op {
	case proto.OpRoot:
		return s.root(ctx, params)
	case proto.OpStat:
		return s.stat(ctx, params)
	case proto.OpList:
		return s.list(ctx, params)
	case proto.OpCanonical:
		return s.canonical(ctx, params)
	case proto.OpRead:
		return s.read(ctx, params)
	case proto.OpWriteOpen:
		return s.writeOpen(ctx, params)
	case proto.OpWrite:
		return s.writeChunk(ctx, params)
	case proto.OpWriteCommit:
		return s.writeCommit(ctx, params)
	case proto.OpWriteAbort:
		return s.writeAbort(ctx, params)
	case proto.OpInventory:
		return s.inventory(ctx)
	}
	return nil, fmt.Errorf("hostsvc: no op %q", op)
}

// root answers the navigation root (spec D2). With no path it is the provider
// this service holds; with one it is a provider over that path, which is the
// same verified-override re-check local.Provider.Root performs — absolute and
// still a directory, or the home directory inferred and labelled with why.
// There is no new inference and no new fallback here.
func (s *Service) root(ctx context.Context, raw json.RawMessage) (any, error) {
	var p proto.FSRootParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	provider := s.provider
	if p.Path != "" {
		provider = local.New(local.WithRoot(p.Path))
	}
	r, err := provider.Root(ctx)
	if err != nil {
		return nil, err
	}
	return proto.FSRootResult{
		Path:           r.Path,
		Display:        r.Display,
		Inferred:       r.Inferred,
		InferredReason: r.InferredReason,
	}, nil
}

// stat classifies one path and reports the metadata a row shows. The path is
// resolved through the provider first, which is where the provider's path
// syntax and its typed refusals live; the stat itself is the same call local's
// Stat makes, taking the fields local.Stat deliberately does not carry.
func (s *Service) stat(ctx context.Context, raw json.RawMessage) (any, error) {
	var p proto.FSPathParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	canonical, err := s.provider.Canonical(ctx, p.Path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return nil, err
	}
	return proto.FSStatResult{
		Kind:    filesystem.KindOf(info.Mode()),
		Size:    info.Size(),
		MtimeNs: info.ModTime().UnixNano(),
		Mode:    uint32(info.Mode()), //nolint:gosec // os.FileMode is 32 bits of defined flags; this is its wire form
	}, nil
}

// list answers one page of the provider's complete, deterministically ordered
// listing. A page past the directory's total is answered empty with the real
// total and rev, exactly as local.List answers it: deleting an entry between
// two pages produces that page, and the total the caller already holds is what
// resolves the race. A refusal here would make this op disagree with the
// provider on the same tree for a page the provider can express.
func (s *Service) list(ctx context.Context, raw json.RawMessage) (any, error) {
	var p proto.FSListParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	listing, err := s.provider.List(ctx, p.Path, filesystem.Page{Offset: p.Offset, Limit: p.Limit})
	if err != nil {
		return nil, err
	}
	entries := make([]proto.FSListEntry, 0, len(listing.Entries))
	for _, e := range listing.Entries {
		entries = append(entries, proto.FSListEntry{
			Name:       e.Name,
			Path:       e.Path,
			Kind:       e.Kind,
			LinkTarget: e.LinkTarget,
			LinkKind:   e.LinkKind,
			Size:       e.Size,
			MtimeNs:    e.ModTime.UnixNano(),
			Mode:       e.Mode,
		})
	}
	return proto.FSListResult{
		Path:      listing.Path,
		Canonical: listing.Canonical,
		Entries:   entries,
		Offset:    listing.Offset,
		Total:     listing.Total,
		HasMore:   listing.HasMore,
		Rev:       listing.Rev,
	}, nil
}

// canonical answers the provider-canonical identity of a path.
func (s *Service) canonical(ctx context.Context, raw json.RawMessage) (any, error) {
	var p proto.FSPathParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	c, err := s.provider.Canonical(ctx, p.Path)
	if err != nil {
		return nil, err
	}
	return proto.FSCanonicalResult{Canonical: c}, nil
}

// read opens the object ONCE, classifies the descriptor, and then answers
// either the whole file with a version or a range with an identity.
//
// O_NONBLOCK is not a flag for regular files; it is what makes the open of a
// FIFO somebody swapped in return instead of block. The type check happens on
// the descriptor, so the object classified and the object read are the same
// one — statting the path first would leave the window this closes.
func (s *Service) read(ctx context.Context, raw json.RawMessage) (any, error) {
	var p proto.FSReadParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.Offset < 0 || p.Length <= 0 {
		return nil, &invalidPage{reason: fmt.Sprintf("offset %d and length %d are not a readable page", p.Offset, p.Length)}
	}
	if p.Length > maxReadLength {
		return nil, &invalidPage{reason: fmt.Sprintf("length %d exceeds the %d-byte read ceiling", p.Length, maxReadLength)}
	}
	if p.Whole && p.Offset != 0 {
		return nil, &invalidPage{reason: fmt.Sprintf("a whole-file read starts at 0, not %d", p.Offset)}
	}
	canonical, err := s.provider.Canonical(ctx, p.Path)
	if err != nil {
		return nil, err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	// #nosec G304 — a caller-supplied path is the feature of this service. It
	// reached here through the provider's own Canonical, which is where the
	// path syntax and the typed refusals live, and it names an object on THIS
	// machine — the one the helper exists to read.
	f, err := os.OpenFile(canonical, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		// A unix socket cannot be opened at all: open(2) answers ENXIO on
		// Linux and EOPNOTSUPP on Darwin, in both cases before any descriptor
		// exists, so the fstat that names every other special object never
		// runs. The two errnos are one condition spelled twice and neither is
		// portable to the other platform, so both are accepted — a branch that
		// knew only Linux's spelling answered a macOS read as an unclassified
		// `internal`. lstat is the ONLY way to name this object, and it is
		// used for nothing else — the descriptor-is-the-object rule below
		// stands for every open that succeeds, and either errno on anything
		// that is not a socket is returned unchanged rather than turned into a
		// refusal this op did not observe.
		if errors.Is(err, syscall.ENXIO) || errors.Is(err, syscall.EOPNOTSUPP) {
			if info, lerr := os.Lstat(canonical); lerr == nil && info.Mode()&os.ModeSocket != 0 {
				return nil, &notRegular{path: p.Path, kind: kindName(info.Mode())}
			}
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, &notRegular{path: p.Path, kind: kindName(info.Mode())}
	}
	identity := fileIdentity(info)
	if !p.Whole {
		return readRange(f, p, canonical, identity, info)
	}
	return s.readWhole(ctx, f, p, canonical, identity)
}

// readWhole reads the entire file and returns the version of exactly the bytes
// returned. A file bigger than the caller's bound is `too-large` — the whole
// file was asked for, and a prefix is not it. Stability is decided on the open
// descriptor: size and mtime sampled before and after must agree, or somebody
// wrote during the read and the bytes are a mixture nobody can name. Three
// attempts, then `changed-during-read`.
func (s *Service) readWhole(ctx context.Context, f *os.File, p proto.FSReadParams, canonical, identity string) (any, error) {
	for attempt := 0; attempt < wholeReadAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		before, err := f.Stat()
		if err != nil {
			return nil, err
		}
		if before.Size() > p.Length {
			return nil, &tooLarge{observed: before.Size(), limit: p.Length}
		}
		if _, seekErr := f.Seek(0, io.SeekStart); seekErr != nil {
			return nil, seekErr
		}
		data, err := io.ReadAll(io.LimitReader(f, p.Length))
		if err != nil {
			return nil, err
		}
		if s.afterRead != nil {
			s.afterRead(attempt)
		}
		after, err := f.Stat()
		if err != nil {
			return nil, err
		}
		if after.Size() != before.Size() || after.ModTime().UnixNano() != before.ModTime().UnixNano() {
			continue
		}
		if data == nil {
			data = []byte{}
		}
		sum := sha256.Sum256(data)
		return proto.FSReadResult{
			Canonical: canonical,
			Identity:  identity,
			Data:      data,
			Size:      int64(len(data)),
			MtimeNs:   after.ModTime().UnixNano(),
			Changed:   attempt > 0,
			Version: &proto.FSVersion{
				Size:    int64(len(data)),
				MtimeNs: after.ModTime().UnixNano(),
				SHA256:  hex.EncodeToString(sum[:]),
			},
		}, nil
	}
	return nil, &changedDuringRead{path: p.Path, attempts: wholeReadAttempts}
}

// readRange returns at most Length bytes from Offset, with the object's
// identity and no version. An append keeps the inode and therefore the
// identity; a replacement changes it, which is the only way a caller can tell
// "the file grew" from "this is a different file now". An offset past the end
// answers no bytes rather than an error: the range exists, it is empty.
func readRange(f *os.File, p proto.FSReadParams, canonical, identity string, info os.FileInfo) (any, error) {
	if _, err := f.Seek(p.Offset, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, p.Length))
	if err != nil {
		return nil, err
	}
	if data == nil {
		data = []byte{}
	}
	return proto.FSReadResult{
		Canonical: canonical,
		Identity:  identity,
		Data:      data,
		Size:      int64(len(data)),
		MtimeNs:   info.ModTime().UnixNano(),
		Changed:   false,
	}, nil
}

// inventory answers the temporaries this helper knows it could not remove: the
// ones a previous generation left and the ones this one failed to delete. See
// journal.go for why the list exists at all.
//
// Stranded is built with make, so an empty answer is [] and never null: the
// caller is a coordinator that warns per entry, and a decoder forced to tell
// "nothing stranded" from "no answer" is a decoder that will one day warn
// about nothing or stay silent about something.
func (s *Service) inventory(ctx context.Context) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.strandedMu.Lock()
	defer s.strandedMu.Unlock()
	out := make([]proto.FSStrandedTemp, len(s.stranded))
	copy(out, s.stranded)
	return proto.FSInventoryResult{Stranded: out}, nil
}
