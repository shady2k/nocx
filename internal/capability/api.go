package capability

// The api.* domain: the API-testing collection, the requests inside it, and
// the two import entrances.
//
// # Its own conflict domain, and why not the config one
//
// Snippets, notes and UI state hold the CONFIG gate, and the reason is
// written on the snippet handler (ws_snippet_handlers.go:9): each is one
// document under the profile directory that backup/restore also writes, so
// a restore replacing the document underneath a mutation is exactly the
// two-writer race the config gate serialises.
//
// A collection is an arbitrary folder the user chose (design §6.1). Backup
// and restore do not touch it, nothing else in the app writes it, and no
// config-domain operation can be mid-write in it. So the snippet analogy
// does not transfer and copying its gate would buy exclusion nobody needs
// at the price of one product area blocking another — a collection read
// waiting behind a settings write. Collections get GateAPI, their own
// domain.
//
// # The send does not hold a gate across the dial
//
// api.request.send is the one method here that performs network I/O, and
// the latency is a remote server's rather than ours. Snapshot takes
// everything the send needs — the request exactly as the file has it, and
// the cookie scope — under the api gate, and returns; the handler dials
// AFTER Run has released it (ws_api_handlers.go).
//
// The interval, both ends named, because an invariant with only a start
// buys a test that guards only the start: the api gate is held from before
// the request file is opened until the request value is in the handler's
// hand, and it is not held again for the rest of the send — not during the
// resolve, the dial, the exchange or the body read. Holding a whole domain
// behind arbitrary remote latency would block every other collection
// operation for as long as a hung server cared to wait.
//
// # The opened-folder list lives here, and it is not a second handle table
//
// apicoll's own table maps a handle to the folder it names, so that every
// call can re-validate the root it was opened on. It answers "where does
// this handle point"; it has no close and no enumeration, because those are
// not questions about a path.
//
// This registry answers a different question — WHICH folders the user
// currently has open, in the order they opened them — which is design
// §6.1's "the app remembers the list of opened folders, never their
// contents". It is also the authority on whether a handle is still open: a
// closed handle is refused here before apicoll is asked anything.
//
// One consequence is worth naming rather than leaving to be discovered.
// apicoll.Service has no Close, so closing a collection removes the entry
// here while apicoll's own table keeps its row until the process exits. The
// handle is unreachable — every method resolves through this registry first
// — so it is a table that does not shrink, not a folder that stays
// readable.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/apiimport"
	"github.com/shady2k/nocx/internal/transport/control"
)

// OpenCollection is one folder the user has open: the handle that addresses
// it, the path they named, and the collection as it is on disk RIGHT NOW.
//
// Contents are re-read on every listing and never cached (§6.1): the app
// remembers the list of folders, not what is in them, so a request file
// added by a colleague's git pull appears without anything being told about
// it.
//
// Err is the failure for this one folder — a root replaced or removed since
// it was opened. It is a field ON the entry rather than an error beside the
// listing, for the same reason apicoll.Collection.Malformed is: a caller
// that returned early on the first failure would let one dead folder hide
// every live one, and a folder silently dropped is the soft degrade that
// makes a broken feature look absent.
type OpenCollection struct {
	Handle     apicoll.HandleID
	Path       string
	Collection apicoll.Collection
	Err        error
}

// SendInputs is what api.request.send takes out from under the gate. It
// exists so that the gate's hold is a snapshot rather than a whole request
// lifetime — see the file comment.
type SendInputs struct {
	// Request is the request with the environment's plain variables
	// resolved into it, and nothing else: a body naming a file and an auth
	// naming a variable are still the caller's to resolve, and apisend
	// refuses both rather than guessing. With no environment named it is
	// the request exactly as the file has it.
	//
	// The substitution happens HERE rather than in the sender because it
	// needs the environment, which needs the folder, which is what the gate
	// is held for. The file is untouched: a resolved request is a
	// projection of it (§6.4), so nothing written back can carry a value
	// the user did not type.
	Request apicoll.Request
	// CookieScope is the collection's identity, and it is what keys the
	// sender's client instance so two collections never share a jar. The
	// path the user named is the stable identity across handles; a handle
	// is minted fresh every run.
	CookieScope string
	// Environment is the environment's NAME as its file declares it —
	// empty when no environment was named. It is carried out of the
	// snapshot because it is half of the binding key (collection,
	// environment, variable) the send path needs to resolve an auth
	// variable, and it is READ HERE for the same reason the route is: the
	// name and the values must come from one record at one moment, or a
	// request resolves its address from one environment and its credential
	// from another.
	//
	// It is the name in the file and never the file's path. apiimport binds
	// under the name, so deriving one from the other at the send would be a
	// second answer to "which environment is this" — and the two would agree
	// until somebody renamed an environment without renaming its file.
	Environment string
	// Route is the environment's answer to "how do I get there" (§6.5). It
	// comes from the SAME record as the address that was just substituted,
	// which is the whole reason the route lives on the environment: the two
	// cannot drift, so a production request cannot go out around its
	// bastion. With no environment named it is the direct route.
	//
	// It is the domain value rather than the sender's route id: mapping one
	// onto the other is apisend's (RouteIDFor), and this layer does not
	// learn a second spelling of it.
	Route apicoll.Route
}

// APICollectionService is the collection surface: the opened-folder list,
// the open/close lifecycle, the two request accessors, and the send's
// snapshot. It is what an APICollectionOperation hands its callback, and
// every method checks the operation's guard.
type APICollectionService interface {
	// ListOpen returns every folder the user has open, contents re-read.
	ListOpen() ([]OpenCollection, error)
	// Open adds a folder to the list and returns its handle plus the
	// collection. It is the ONLY method here that accepts a root (§13.1).
	Open(root string) (apicoll.HandleID, apicoll.Collection, error)
	// Create mints an empty collection under a NAME — never a path — in the
	// default location apicoll decides, and leaves it OPEN: it lands in
	// this list exactly as Open's folder does, so a caller has one thing to
	// do afterwards rather than two.
	Create(name string) (apicoll.Created, error)
	// Close removes a folder from the list. Every later call naming that
	// handle is refused.
	Close(h apicoll.HandleID) error
	// ListEnvironments reads every environment in one open folder, the
	// malformed files NAMED alongside the good ones rather than replacing
	// them with an error — apicoll's own rule, and it holds for the same
	// reason it does for requests: one bad file must not hide the rest.
	//
	// It is here rather than folded into ListOpen because the two are read
	// by different methods for different results, and every api.collections.*
	// method needs it: a renderer cannot offer a choice of environment
	// without knowing which ones exist, and before this the only caller of
	// apicoll.ListEnvironments anywhere in the tree was apicoll's own tests
	// — a read path that existed and was reachable from nothing (nocx-pnvnn).
	ListEnvironments(h apicoll.HandleID) ([]apicoll.EnvironmentRef, []apicoll.MalformedRef, error)
	// ReadRequest reads one request by its path within the collection.
	ReadRequest(h apicoll.HandleID, relPath string) (apicoll.Request, error)
	// WriteRequest writes one request back.
	WriteRequest(h apicoll.HandleID, relPath string, r apicoll.Request) error
	// Snapshot takes what a send needs and nothing else, so the gate can be
	// released before the dial. envRelPath names the environment to send
	// under, addressed inside the collection like everything else; "" is no
	// environment, which is the request as written on the direct route.
	Snapshot(h apicoll.HandleID, relPath, envRelPath string) (SendInputs, error)
}

// APICollectionOperation is the typed operation for every api.collections.*
// and api.request.* method. Its gate is [api] alone: a collection is a
// folder of the user's, so it conflicts with other collection work and with
// nothing else.
type APICollectionOperation interface {
	Run(context.Context, func(context.Context, APICollectionService) error) error
}

// NewAPICollectionOperation builds the collection operation over the folder
// service, acquiring the api gate and then the execution lane.
//
// It takes the WHOLE folder surface — requests, environments and creation —
// because all three are reached from this one domain and all three must go
// through one handle table and one root re-validation.
func NewAPICollectionOperation(apiGate, lane control.Admission, svc apicoll.Collections) APICollectionOperation {
	g := &guard{}
	return newOperation[APICollectionService](
		control.NewComposite(apiGate, lane),
		g,
		newAPICollectionService(g, svc),
	)
}

func newAPICollectionService(g *guard, svc apicoll.Collections) *apiCollectionService {
	return &apiCollectionService{guard: g, svc: svc}
}

type apiCollectionService struct {
	guard *guard
	svc   apicoll.Collections

	// mu guards the opened-folder list. The api gate already serialises
	// every caller at capacity 1, so this is the lock that keeps the list
	// correct if that capacity is ever raised — the gate's grain is an
	// implementation detail of this package and this list must not depend
	// on it.
	mu   sync.Mutex
	open []openEntry
}

// openEntry is one row of the opened-folder list.
type openEntry struct {
	handle apicoll.HandleID
	path   string
}

func (s *apiCollectionService) ListOpen() ([]OpenCollection, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	entries := make([]openEntry, len(s.open))
	copy(entries, s.open)
	s.mu.Unlock()

	out := make([]OpenCollection, 0, len(entries))
	for _, e := range entries {
		oc := OpenCollection{Handle: e.handle, Path: e.path}
		coll, err := s.svc.List(e.handle)
		if err != nil {
			oc.Err = err
		} else {
			oc.Collection = coll
		}
		out = append(out, oc)
	}
	return out, nil
}

func (s *apiCollectionService) Open(root string) (apicoll.HandleID, apicoll.Collection, error) {
	if err := s.guard.check(); err != nil {
		return "", apicoll.Collection{}, err
	}
	h, coll, err := s.svc.Open(root)
	if err != nil {
		return "", apicoll.Collection{}, err
	}
	s.register(h, root)
	return h, coll, nil
}

// register puts one folder in the opened list, or replaces the row that is
// already there.
//
// One folder is one entry however many times it is opened: re-opening
// replaces the row rather than listing the same folder twice, and the
// previous handle stops resolving. The match is on the path AS NAMED, which
// is best-effort — apicoll canonicalises the root internally and does not
// expose the canonical form, so two different names for one directory list
// as two folders. That is a duplicate row in a list, not a folder anybody
// loses.
func (s *apiCollectionService) register(h apicoll.HandleID, root string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.open {
		if s.open[i].path == root {
			s.open[i].handle = h
			return
		}
	}
	s.open = append(s.open, openEntry{handle: h, path: root})
}

// Create mints a collection and registers it exactly as Open does.
//
// The registration is deliberately Open's own bookkeeping rather than a
// second copy of it: apicoll.Create opens the folder it made, so what comes
// back here is an opened folder and the only question left is which list it
// belongs in. A folder that could not be created registers nothing — there
// is no row naming a collection nobody made.
func (s *apiCollectionService) Create(name string) (apicoll.Created, error) {
	if err := s.guard.check(); err != nil {
		return apicoll.Created{}, err
	}
	made, err := s.svc.Create(name)
	if err != nil {
		return apicoll.Created{}, err
	}
	s.register(made.Handle, made.Root)
	return made, nil
}

func (s *apiCollectionService) Close(h apicoll.HandleID) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.open {
		if s.open[i].handle == h {
			s.open = append(s.open[:i], s.open[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("%w: %q", apicoll.ErrUnknownHandle, h)
}

// ListEnvironments answers out of a folder the user still has open, and
// refuses one they have closed for the same reason ReadRequest does: this
// registry is the authority on whether a handle is still live, and apicoll's
// own table would go on resolving it.
func (s *apiCollectionService) ListEnvironments(h apicoll.HandleID) ([]apicoll.EnvironmentRef, []apicoll.MalformedRef, error) {
	if err := s.guard.check(); err != nil {
		return nil, nil, err
	}
	if err := s.stillOpen(h); err != nil {
		return nil, nil, err
	}
	return s.svc.ListEnvironments(h)
}

func (s *apiCollectionService) ReadRequest(h apicoll.HandleID, relPath string) (apicoll.Request, error) {
	if err := s.guard.check(); err != nil {
		return apicoll.Request{}, err
	}
	if err := s.stillOpen(h); err != nil {
		return apicoll.Request{}, err
	}
	return s.svc.ReadRequest(h, relPath)
}

func (s *apiCollectionService) WriteRequest(h apicoll.HandleID, relPath string, r apicoll.Request) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	if err := s.stillOpen(h); err != nil {
		return err
	}
	return s.svc.WriteRequest(h, relPath, r)
}

// Snapshot reads the request, and — when an environment is named — the
// environment beside it, resolves the request's variables against it and
// carries that environment's route out.
//
// The three reads are one snapshot on purpose. The address and the route
// come from ONE record (§6.5), so taking them at two moments would be the
// drift the design exists to prevent; and doing it here, under the gate,
// keeps every filesystem touch inside the interval the gate covers while
// leaving the dial outside it.
//
// An unresolved variable does not produce a request. It comes back as
// apicoll.ErrUnresolvedVariable naming every reference that has no value —
// not the literal braces on the wire, not an empty string quietly
// substituted, because an `Authorization: Bearer ` header is a
// plausible-looking request that teaches the wrong lesson about why it was
// rejected (§6.5).
//
// A variable the environment declares SECRET is not answered here: its value
// lives in the binding document beside the vault (§8.1), which this service
// is not given. Such a variable in the URL, a header or the body is
// unresolved and blocks the send — the honest state, and the same one the
// user sees for a variable they have not given a value.
//
// The AUTH variable is the exception, and it is resolved by the caller
// rather than here: the auth block names a variable rather than containing
// one, so it is not substitution's business, and the resolved credential
// must reach the sender as a NAMED SECRET or it would appear in the raw
// diagnostic (§11.2). What this snapshot contributes to that is the
// environment's NAME, below — the half of the binding key that only the
// record just read can supply.
func (s *apiCollectionService) Snapshot(h apicoll.HandleID, relPath, envRelPath string) (SendInputs, error) {
	if err := s.guard.check(); err != nil {
		return SendInputs{}, err
	}
	scope, err := s.pathFor(h)
	if err != nil {
		return SendInputs{}, err
	}
	req, err := s.svc.ReadRequest(h, relPath)
	if err != nil {
		return SendInputs{}, err
	}
	if envRelPath == "" {
		// No environment: the request as written, out of this machine. A
		// collection with no environments is a collection (§6.2), so this
		// is an ordinary state rather than a degraded one.
		return SendInputs{Request: req, CookieScope: scope, Route: apicoll.Route{Kind: apicoll.RouteDirect}}, nil
	}
	env, err := s.svc.ReadEnvironment(h, envRelPath)
	if err != nil {
		return SendInputs{}, err
	}
	resolved, err := apicoll.Substitute(req, apicoll.Chain(env.Lookup()))
	if err != nil {
		return SendInputs{}, err
	}
	return SendInputs{Request: resolved, CookieScope: scope, Environment: env.Name, Route: env.Route}, nil
}

// stillOpen refuses a handle the user has closed — or never opened — before
// apicoll is asked anything about it. apicoll's own table would still
// resolve a closed handle, so this check is what makes Close a closing
// event rather than a cosmetic one.
func (s *apiCollectionService) stillOpen(h apicoll.HandleID) error {
	_, err := s.pathFor(h)
	return err
}

func (s *apiCollectionService) pathFor(h apicoll.HandleID) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.open {
		if s.open[i].handle == h {
			return s.open[i].path, nil
		}
	}
	return "", fmt.Errorf("%w: %q", apicoll.ErrUnknownHandle, h)
}

// ── the import entrance that writes ──────────────────────────────────────

// ErrImportNotAFile is a chosen import document that is not a regular file.
// A fifo would make the import block for as long as nothing writes to it,
// while holding two domain gates; a directory has nothing to parse. Both are
// refused by name rather than read.
var ErrImportNotAFile = errors.New("capability: the import document is not a regular file")

// APIImportService is the api.import.postman surface. The curl entrance is
// deliberately absent: it converts a line into a request VALUE, touches no
// store and no filesystem, so giving it an operation would serialise a pure
// parse behind whatever the api domain happened to be doing.
type APIImportService interface {
	// ImportPostman reads the export at srcPath and writes the collection
	// to dest as one atomic arrival, then hands the secret values to the
	// binding store. It is the second and last method on this surface that
	// accepts a path (§13.1) — and it accepts two, because an import names
	// both what to read and where to put it.
	ImportPostman(ctx context.Context, srcPath, dest string) ([]apiimport.Unsupported, error)
}

// APIImportOperation is the typed operation for api.import.postman. Its
// gates are [vault, api], in the canonical order: the import writes secret
// VALUES through the binding store, which is the vault-backed half of
// design §8.1, and it writes a collection folder. It is bounded work — the
// document is capped at 16 MiB by apiimport and every write is local — so
// holding both across it does not repeat the mistake api.request.send
// avoids.
type APIImportOperation interface {
	Run(context.Context, func(context.Context, APIImportService) error) error
}

// NewAPIImportOperation builds the import operation. fsys is every
// filesystem touch the writer makes and bindings is where the secret values
// go; both are apiimport's own narrow contracts, so this constructor names
// no lifecycle the importer has no part in.
func NewAPIImportOperation(
	vaultGate, apiGate, lane control.Admission,
	fsys apiimport.FS,
	bindings apiimport.BindWriter,
) APIImportOperation {
	g := &guard{}
	return newOperation[APIImportService](
		control.NewComposite(vaultGate, apiGate, lane),
		g,
		&apiImportService{guard: g, fsys: fsys, bindings: bindings},
	)
}

type apiImportService struct {
	guard    *guard
	fsys     apiimport.FS
	bindings apiimport.BindWriter
}

func (s *apiImportService) ImportPostman(ctx context.Context, srcPath, dest string) ([]apiimport.Unsupported, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	// Lstat rather than Stat: a symlink at the chosen path is refused, not
	// followed. The document is the user's own choice from a dialog, so
	// this is not §13.1's handle rule — that governs a collection FOLDER —
	// but the two failures a non-regular file causes are real: a fifo
	// blocks the read forever while two domain gates are held, and a
	// directory has nothing to parse.
	fi, err := os.Lstat(srcPath)
	if err != nil {
		return nil, fmt.Errorf("capability: read the import document: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s", ErrImportNotAFile, srcPath)
	}
	f, err := os.Open(srcPath) //nolint:gosec // the import document is the path the user chose; it is Lstat-checked as a regular file just above
	if err != nil {
		return nil, fmt.Errorf("capability: read the import document: %w", err)
	}
	defer func() { _ = f.Close() }()
	return apiimport.ImportInto(ctx, s.fsys, s.bindings, dest, f)
}
