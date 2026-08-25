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
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/shady2k/nocx/internal/apibind"
	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/apifetch"
	"github.com/shady2k/nocx/internal/apiimport"
	"github.com/shady2k/nocx/internal/apisend"
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
	// resolved into it, and nothing else: a body naming a file is still the
	// caller's to resolve, and apisend refuses it rather than guessing.
	// With no environment named it is the request exactly as the file has
	// it.
	//
	// The substitution happens HERE rather than in the sender because it
	// needs the environment, which needs the folder, which is what the gate
	// is held for. The file is untouched: a resolved request is a
	// projection of it (§6.4), so nothing written back can carry a value
	// the user did not type.
	Request apicoll.Request
	// RawRequest is the request exactly as the FILE has it, before
	// substitution. The sender maps a resolved auth block back onto it by
	// FIELD, so it can know whether an auth credential came from the
	// binding document without ever comparing resolved text against a
	// recorded value: `auth.token` in the raw file is `{{name}}` exactly
	// when the file says so, and the binding document answers a name
	// exactly when it answered that name. There is no heuristic, because
	// neither comparison is about the VALUE.
	RawRequest apicoll.Request
	// CookieScope is the collection's identity, and it is what keys the
	// sender's client instance so two collections never share a jar. The
	// path the user named is the stable identity across handles; a handle
	// is minted fresh every run.
	CookieScope string
	// Environment is the environment's NAME as its file declares it —
	// empty when no environment was named. It is carried out of the
	// snapshot because it is half of the binding key (collection,
	// environment, variable) the send path resolves variables under, and
	// it is READ HERE for the same reason the route is: the name and the
	// values must come from one record at one moment, or a request
	// resolves its address from one environment and its credential from
	// another.
	//
	// It is the name in the file and never the file's path. apiimport binds
	// under the name, so deriving one from the other at the send would be a
	// second answer to "which environment is this" — and the two would agree
	// until somebody renamed an environment without renaming its file.
	Environment string
	// AuthSecrets records which of the request's auth FIELDS resolved
	// through the binding document, by variable name — the field-to-source
	// map apisend.Apply needs to elide by construction (nocx-6hg2w.20). A
	// zero AuthSecrets means every auth field is a literal the person
	// typed, which the product sends and shows.
	AuthSecrets apisend.SecretSource
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
	// Secrets are the values the binding document answered for this
	// substitution — the ones now sitting inside Request's URL, headers,
	// query or body. Empty when the environment declared none.
	//
	// The caller hands them to the sender so they can be elided from the
	// diagnostic. They are recorded by WHICH LOOKUP ANSWERED rather than by
	// guessing at the text: the environment's plain values are tried first
	// and the binding document second (apicoll.Chain), so a name the second
	// one answers is a secret by construction — there is no heuristic here
	// and nothing that could mistake a plain value for a credential or the
	// other way round.
	Secrets []PlacedSecret
}

// SecretValues is the binding document's READ half, narrowed to the one call
// the snapshot makes. Declared here as a consumer contract, like Secrets and
// SecretVault beside it: this layer asks what a variable is worth and has no
// parameter through which an identifier could travel in either direction
// (design §8, apibind.ValueResolver).
type SecretValues interface {
	Variables(ctx context.Context, collection, environment string) apicoll.Lookup
}

// PlacedSecret is one secret value that WAS substituted into the request, by
// name.
//
// It exists so the sender can find those bytes in the text it composes and
// elide them (apisend.MarkRequest, locate) — which is what makes the raw view
// show a chip where a token went rather than the token. Without it a secret
// resolved into a URL would cross to the renderer in the clear inside the
// diagnostic whose entire purpose is to show everything (§11.2).
//
// It carries the VALUE, and it is the one thing in SendInputs that does.
// That is not a new exposure: the resolved request beside it already has the
// value substituted into its URL, its headers or its body — that is what
// substitution IS. What this adds is the sender's ability to know which
// bytes those are.
type PlacedSecret struct {
	Name  string
	Value string
}

// APICollectionService is the collection surface: the opened-folder list,
// the open/close lifecycle, the two request accessors, and the send's
// snapshot. It is what an APICollectionOperation hands its callback, and
// every method checks the operation's guard.
type APICollectionService interface {
	// ListOpen returns every folder the user has open, contents re-read.
	ListOpen() ([]OpenCollection, error)
	// Open adds a folder to the list and returns its handle, the
	// collection, and whether the folder was ALREADY open. It is the ONLY
	// method here that accepts a root (§13.1).
	//
	// One folder is one handle for as long as it is open, and that is
	// apicoll's rule rather than this list's: a path already open answers
	// with the identity that exists, so re-opening it adds no row here and
	// draws no second collection in the tree (nocx-ghuq3).
	Open(root string) (apicoll.Opened, error)
	// Create mints an empty collection under a NAME — never a path — in the
	// default location apicoll decides, and leaves it OPEN: it lands in
	// this list exactly as Open's folder does, so a caller has one thing to
	// do afterwards rather than two.
	Create(name string) (apicoll.Created, error)
	// BindingKeyFor answers the pair a binding under this environment is
	// keyed by: the collection's canonical root and the environment's NAME
	// as its file declares it.
	//
	// It exists so the write half of a binding is keyed by exactly what the
	// READ half is keyed by. Snapshot reports the same two values for the
	// send (CookieScope and Environment), and apibind.Key is a triple for a
	// reason — two collections with a variable of one name must not share a
	// value. A handler that assembled the pair itself would be a second
	// derivation of the binding key, and the two would agree until somebody
	// renamed an environment without renaming its file.
	BindingKeyFor(h apicoll.HandleID, envRelPath string) (collection, environment string, err error)
	// DefaultRoot is where that default location IS, so a surface can show
	// a person where a collection is about to land rather than asking them
	// to name a place with nothing proposed. "" is a build with no app
	// directory (apicoll.Creator says the rest).
	//
	// It is guard-checked like everything else here, although it reaches no
	// folder and could not be made to: the property this interface has is
	// that a service which escaped its operation is USELESS, and one method
	// that still answered would be an exception somebody has to remember.
	DefaultRoot() (string, error)
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
	// ReadEnvironment reads ONE environment whole — the values and the
	// route, not the ref the listing carries. The listing answers "which
	// environments are there"; this answers "what does this one say", which
	// is the question an editor asks and the only way a person can be shown
	// what they are about to change.
	ReadEnvironment(h apicoll.HandleID, relPath string) (apicoll.Environment, error)
	// WriteEnvironment writes one back, creating the file when nothing
	// occupies the name. It is how an environment is configured from the
	// product at all: before this, `environments/` could be read by the
	// sender and written by nothing, so every environment in existence had
	// been typed into a file by hand or landed by the Postman importer.
	WriteEnvironment(h apicoll.HandleID, relPath string, env apicoll.Environment) error
	// ReadFolderVariables reads the variables declared by one folder. Values
	// are returned only for the folder editor, never on collection listing.
	ReadFolderVariables(h apicoll.HandleID, relPath string) ([]apicoll.Param, error)
	// WriteFolderVariables replaces one folder's declarations and returns the
	// canonical rows that were persisted.
	WriteFolderVariables(h apicoll.HandleID, relPath string, variables []apicoll.Param) ([]apicoll.Param, error)
	// ReadRequest reads one request by its path within the collection.
	ReadRequest(h apicoll.HandleID, relPath string) (apicoll.Request, error)
	// WriteRequest writes one request back.
	WriteRequest(h apicoll.HandleID, relPath string, r apicoll.Request) error
	// DeleteRequest removes one request file. It is the only method here
	// that takes something away, and it takes away exactly one file: a
	// collection is closed through Close and never emptied through this.
	DeleteRequest(h apicoll.HandleID, relPath string) error
	// MoveRequest moves one request file to another path inside the SAME
	// collection, and answers the new relPath. It is addressed by the
	// handle plus two paths relative to it, exactly like the accessors
	// beside it, so §13.1 holds — and it is one operation on the backend:
	// a no-replace rename, never a write-then-delete. The destination
	// folder must already exist; CreateFolder makes it.
	MoveRequest(h apicoll.HandleID, fromRelPath, toRelPath string) (string, error)
	// CreateFolder makes ONE folder inside an open collection: a NAME, and
	// the existing folder to put it in ("" is the collection root).
	//
	// It is Create's grammar one level down — a component the backend turns
	// into a location, never a location the caller supplies — so it joins
	// neither of the two api.* methods that accept a root (§13.1). Nesting
	// is repeated calls, each naming a parent that already exists, which is
	// what lets "that folder is not there" be answered at all
	// (apicoll/createfolder.go).
	//
	// The collection comes back with it because the caller's next move is
	// to draw the tree: a folder made and then listed at a second moment is
	// two accounts of one folder, and the first thing a person does after
	// making one is look for it.
	CreateFolder(h apicoll.HandleID, parentRelPath, name string) (apicoll.FolderCreated, error)
	// Snapshot takes what a send needs and nothing else, so the gate can be
	// released before the dial. envRelPath names the environment to send
	// under, addressed inside the collection like everything else; "" is no
	// environment, which is the request as written on the direct route.
	//
	// It takes a context because resolving a secret variable reads the
	// vault, which is a call that can be cancelled — the only thing in the
	// snapshot that is not a file read.
	Snapshot(ctx context.Context, h apicoll.HandleID, relPath, envRelPath string) (SendInputs, error)
	// RequestScope returns the same request-to-vault resolution order as
	// Snapshot, with every row and its source exposed for the Variables tab.
	// variables is the draft's request layer; it replaces only the stored
	// request rows for this call.
	RequestScope(
		ctx context.Context,
		h apicoll.HandleID,
		relPath, envRelPath string,
		variables []apicoll.Param,
	) (RequestScopeResult, error)
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
//
// values is the binding document's read half, and it is a parameter rather
// than a field set afterwards because the snapshot needs it on the first
// call: a build wired without one resolves no secret variable at all, which
// is a coherent half of the feature and says so by leaving them unresolved.
func NewAPICollectionOperation(apiGate, lane control.Admission, svc apicoll.Collections, values SecretValues) APICollectionOperation {
	g := &guard{}
	return newOperation[APICollectionService](
		control.NewComposite(apiGate, lane),
		g,
		newAPICollectionService(g, svc, values),
	)
}

func newAPICollectionService(g *guard, svc apicoll.Collections, values SecretValues) *apiCollectionService {
	return &apiCollectionService{guard: g, svc: svc, values: values}
}

type apiCollectionService struct {
	guard *guard
	svc   apicoll.Collections
	// values is the binding document's read half. nil is a build with no
	// binding store, and then a secret variable resolves to nothing — which
	// is the same answer a misspelled variable gets, and an honest one.
	values SecretValues

	// mu guards the opened-folder list. The api gate already serialises
	// every caller at capacity 1, so this is the lock that keeps the list
	// correct if that capacity is ever raised — the gate's grain is an
	// implementation detail of this package and this list must not depend
	// on it.
	mu   sync.Mutex
	open []openEntry
	// starterDone is set by the first ListOpen of the process, whatever it
	// found: the built-in collection is opened ONCE per session, so a user
	// who closes it is not arguing with the panel on the next refresh. It
	// returns on the next start, which is what makes it built-in rather
	// than a seeding nobody can get back.
	starterDone bool
	// starterErr is why the built-in collection is not in the list, kept so
	// the listing can SAY so. A first run that could not write to the app
	// directory is a panel with no collections and no reason given — the
	// silent degrade AGENTS.md names — and the row below is what a person
	// sees instead.
	starterErr error
	starterAt  string
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
	s.ensureStarter()
	s.mu.Lock()
	entries := make([]openEntry, len(s.open))
	copy(entries, s.open)
	s.mu.Unlock()

	out := make([]OpenCollection, 0, len(entries)+1)
	// The built-in collection could not be opened. It is a ROW with the
	// reason on it rather than an error that replaces the listing: the
	// folders the user opened themselves are still there and still readable,
	// and one folder that would not open must not hide them — apicoll's own
	// rule for a malformed file, one level up.
	if at, err := s.starterFailure(); err != nil {
		out = append(out, OpenCollection{Path: at, Err: err})
	}
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

// ensureStarter opens the built-in collection, once per process, before the
// first listing answers.
//
// Here rather than in the composition root because this is the layer that
// owns the opened-folder LIST: a starter opened in app.go would exist on
// disk, hold a handle nothing had registered, and not appear in
// api.collections.list at all. The once-per-process flag is taken under the
// same mutex as the list for the reason the list has one — the api gate
// serialises callers at capacity 1 today, and this must stay correct if that
// capacity is ever raised.
func (s *apiCollectionService) ensureStarter() {
	s.mu.Lock()
	already := s.starterDone
	s.starterDone = true
	s.mu.Unlock()
	if already {
		return
	}

	made, err := s.svc.EnsureStarter()
	if err != nil {
		s.mu.Lock()
		s.starterErr = err
		s.starterAt = made.Root
		s.mu.Unlock()
		return
	}
	s.register(made.Handle, made.Root)
}

// starterFailure is where the built-in collection should be and why it is
// not there, or "" and nil. Both facts come back from one locked read: they
// are one row, and a Path taken outside the lock would be a second read of
// state this mutex exists to protect.
func (s *apiCollectionService) starterFailure() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starterAt, s.starterErr
}

func (s *apiCollectionService) Open(root string) (apicoll.Opened, error) {
	if err := s.guard.check(); err != nil {
		return apicoll.Opened{}, err
	}
	op, err := s.svc.Open(root)
	if err != nil {
		return apicoll.Opened{}, err
	}
	s.register(op.Handle, root)
	return op, nil
}

// register puts one folder in the opened list, and leaves the row that is
// already there exactly as it is.
//
// One folder is one entry however many times it is opened, and the row is
// found by its HANDLE. That is the whole of the matching rule here now,
// because the handle IS the folder's identity: apicoll answers an open of a
// folder that is already open with the handle that exists, so two calls
// naming one directory arrive here with one id.
//
// It used to match on the path AS NAMED, and called itself best-effort for
// it: two spellings of one directory — a symlink, a trailing slash, an
// importer's destination and the same folder chosen by hand — listed as two
// folders and drew the collection twice in the tree (nocx-ghuq3). That was a
// second, weaker owner of collection identity sitting a layer above the one
// that mints it; the fix was to delete it rather than to strengthen it.
//
// The row keeps the path it was FIRST opened under, which is the name
// apicoll re-validates that handle against — so the list and the table name
// the folder the same way rather than two ways.
//
// One case is deliberately left as two rows: a folder DELETED and re-created
// at the same path, then re-opened. It is a different directory, so it gets
// a different handle, and the row for the one that is gone stays — carrying
// the reason ListOpen reads off it. Path-matching used to replace it
// silently, which is the listing quietly dropping a dead folder it promises
// to REPORT (TestAPICollectionService_ListOpenReportsADeadFolderBesideALiveOne).
// The two rows name two directories, only one of which exists, and closing
// the dead one is a click.
func (s *apiCollectionService) register(h apicoll.HandleID, root string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.open {
		if s.open[i].handle == h {
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
func (s *apiCollectionService) BindingKeyFor(h apicoll.HandleID, envRelPath string) (string, string, error) {
	if err := s.guard.check(); err != nil {
		return "", "", err
	}
	scope, err := s.pathFor(h)
	if err != nil {
		return "", "", err
	}
	env, err := s.svc.ReadEnvironment(h, envRelPath)
	if err != nil {
		return "", "", err
	}
	return scope, env.Name, nil
}

func (s *apiCollectionService) DefaultRoot() (string, error) {
	if err := s.guard.check(); err != nil {
		return "", err
	}
	return s.svc.DefaultRoot(), nil
}

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
			// The table that MINTED the handle forgets it first, and the
			// row goes only if that succeeded. The order is what keeps the
			// two accounts of "which folders are open" from disagreeing: a
			// close that half-happened leaves the folder open in both
			// places, which the user can close again, rather than gone from
			// the list while the next Open of it still answers "already
			// open" with a handle no row holds.
			if err := s.svc.Close(h); err != nil {
				return err
			}
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

// ReadEnvironment and WriteEnvironment answer out of a folder the user still
// has open, and refuse one they have closed — the same rule ListEnvironments
// above keeps, and for the same reason: this registry is the authority on
// whether a handle is still live, and apicoll's own table would go on
// resolving it.
func (s *apiCollectionService) ReadEnvironment(h apicoll.HandleID, relPath string) (apicoll.Environment, error) {
	if err := s.guard.check(); err != nil {
		return apicoll.Environment{}, err
	}
	if err := s.stillOpen(h); err != nil {
		return apicoll.Environment{}, err
	}
	return s.svc.ReadEnvironment(h, relPath)
}

func (s *apiCollectionService) WriteEnvironment(h apicoll.HandleID, relPath string, env apicoll.Environment) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	if err := s.stillOpen(h); err != nil {
		return err
	}
	return s.svc.WriteEnvironment(h, relPath, env)
}

func (s *apiCollectionService) ReadFolderVariables(h apicoll.HandleID, relPath string) ([]apicoll.Param, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	if err := s.stillOpen(h); err != nil {
		return nil, err
	}
	return s.svc.ReadFolderVariables(h, relPath)
}

func (s *apiCollectionService) WriteFolderVariables(h apicoll.HandleID, relPath string, variables []apicoll.Param) ([]apicoll.Param, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	if err := s.stillOpen(h); err != nil {
		return nil, err
	}
	return s.svc.WriteFolderVariables(h, relPath, variables)
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
// Since nocx-6hg2w.20 the AUTH is not an exception: its fields are text
// like every other, resolved by the same substitution below, and the
// field-wise provenance of which one the binding document answered is
// carried out (AuthSecrets) so the sender can elide those bytes and show a
// literal (§11.2). What this snapshot also contributes is the
// environment's NAME, below — the half of the binding key that only the
// record just read can supply.
func (s *apiCollectionService) DeleteRequest(h apicoll.HandleID, relPath string) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	if err := s.stillOpen(h); err != nil {
		return err
	}
	return s.svc.DeleteRequest(h, relPath)
}

// CreateFolder answers out of a folder the user still has open, and
// refuses one they have closed — the rule every method beside it keeps,
// and it binds hardest on the one that WRITES: apicoll's own table would go
// on resolving a closed handle, and a folder made through it would land in
// a collection the user believes they have shut.
func (s *apiCollectionService) CreateFolder(h apicoll.HandleID, parentRelPath, name string) (apicoll.FolderCreated, error) {
	if err := s.guard.check(); err != nil {
		return apicoll.FolderCreated{}, err
	}
	if err := s.stillOpen(h); err != nil {
		return apicoll.FolderCreated{}, err
	}
	return s.svc.CreateFolder(h, parentRelPath, name)
}

// MoveRequest answers out of a folder the user still has open, and refuses
// one they have closed — the rule every method beside it keeps, and it
// binds here as on the write half: a move is the one act that changes
// WHERE a file is, and doing it in a collection the user believes they
// have shut would move a file in a folder they are not looking at.
func (s *apiCollectionService) MoveRequest(h apicoll.HandleID, fromRelPath, toRelPath string) (string, error) {
	if err := s.guard.check(); err != nil {
		return "", err
	}
	if err := s.stillOpen(h); err != nil {
		return "", err
	}
	return s.svc.MoveRequest(h, fromRelPath, toRelPath)
}

func (s *apiCollectionService) Snapshot(ctx context.Context, h apicoll.HandleID, relPath, envRelPath string) (SendInputs, error) {
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

	// The request as the FILE has it, with where it would go out. Filled in
	// before the substitution rather than after, because it is what a run
	// that CANNOT be substituted has to be built from — see the unresolved
	// branch below.
	inputs := SendInputs{RawRequest: req, Request: req, CookieScope: scope, Route: apicoll.Route{Kind: apicoll.RouteDirect}}

	// No environment is a LOOKUP THAT ANSWERS NOTHING, not a substitution
	// skipped. Skipping it is what made `{{baseUrl}}/zen` with nothing bound
	// reach the sender as text, so the sender complained about the only
	// thing it could see — "is not an absolute URL", a sentence about a URL
	// nobody typed, naming neither the variable nor the environment. A
	// collection with no environments is still a collection (§6.2): the
	// request goes out exactly as written when it HAS no references, and
	// says which ones it cannot resolve when it has.
	var look apicoll.Lookup
	var secrets apicoll.Lookup
	var env apicoll.Environment
	if envRelPath != "" {
		read, envErr := s.svc.ReadEnvironment(h, envRelPath)
		if envErr != nil {
			return SendInputs{}, envErr
		}
		env = read
		inputs.Environment = env.Name
		inputs.Route = env.Route
		look = env.Lookup()
		// THE SECOND LOOKUP, which is what Chain has always existed for and
		// what was never built. A vault-held value used to reach exactly
		// one field — the auth variable, resolved by the sender — so a
		// token that goes in a PATH could only be sent by typing it into a
		// file that goes into git. Telegram is the shape that names the
		// gap: `/bot<TOKEN>/sendMessage`.
		//
		// BEHIND the environment, never in front of it. Environment.Value
		// already refuses a name the file declares secret, so the two halves
		// cannot both answer one name; the ORDER is what makes a collection
		// arriving in a pull request unable to choose what a reader's
		// request sends (§8) — a file's plain value can never shadow a
		// binding, and a binding is only reached for a name the file did not
		// answer.
		if s.values != nil {
			secrets = recordPlaced(s.values.Variables(ctx, scope, env.Name), &inputs.Secrets)
		}
	}

	// THE REQUEST'S OWN VARIABLES GO IN FRONT, and the environment's are
	// inherited: a name the request answers wins, and everything else falls
	// through exactly as it did. That is the whole shape of the feature —
	// `id` in `/users/{{id}}` belongs to the request, because two requests
	// want different ones, while `baseUrl` belongs to the environment
	// because every request under it wants the same one.
	//
	// The one case the order does NOT decide is a name the environment
	// declares secret: the request loses, loudly (ErrSecretShadowed). A
	// request file goes into git and a credential belongs in the vault, so
	// the two meeting is refused rather than resolved silently in either
	// direction.
	look, lookupErr := requestLookup(req, env, look, secrets)
	if lookupErr != nil {
		return inputs, lookupErr
	}

	resolved, err := apicoll.Substitute(req, look)
	if err != nil {
		// THE INPUTS COME BACK WITH THE ERROR, and that is the one thing
		// this signature does that a plain failure would not. An unresolved
		// variable is a run — the `compose` phase of an exchange that never
		// went out — and a run needs the request to show and the route to
		// say where it would have gone. Returning the zero value here would
		// leave the handler with a sentence and nothing to attach it to,
		// which is the shape this whole epic replaced.
		//
		// The request is the UNSUBSTITUTED one, so the reference a person
		// has to fix is visible in the text they are shown.
		return inputs, err
	}

	// WHICH AUTH FIELD CAME FROM THE BINDING DOCUMENT, answered by
	// FIELD-WISE comparison of the file's text against the names the
	// binding just answered — never by inspecting a RESOLVED VALUE. A field
	// the file wrote as exactly one `{{name}}` is named by a binding when
	// that name is one of Secrets; a literal the person typed is not a
	// reference at all; `Bearer {{name}}` text is a reference not covering
	// the whole field, which the file has no way to be bound AS a secret —
	// it is resolved like any other mixed text and shown like any other
	// text. A name that resolves to a PLAIN environment value is also
	// absent here: the plain values are not secrets, which is the point of
	// the construction (apicoll.Chain: env first, binding second).
	inputs.AuthSecrets.Token = authFieldSource(req.Auth.Token, inputs.Secrets)
	inputs.AuthSecrets.Password = authFieldSource(req.Auth.Password, inputs.Secrets)
	inputs.Request = resolved
	return inputs, nil
}

// authFieldSource answers which variable name a raw auth FIELD resolved
// through the binding document, or "" when it is a literal (or mixed text,
// or an environment-plain value). See the Snapshot comment above for why
// this is by construction rather than by inspecting resolved text.
func authFieldSource(raw string, placed []PlacedSecret) string {
	name, ok := apicoll.ExactReference(raw)
	if !ok {
		return ""
	}
	for _, p := range placed {
		if p.Name == name {
			return name
		}
	}
	return ""
}

// recordPlaced wraps a lookup so the caller learns WHICH values it answered.
//
// It is a decorator rather than a change to apicoll.Substitute, and that is
// the point: substitution's contract is that it knows nothing about where an
// answer came from, so "which of these came from the vault" is a question
// only the composer of the Chain can answer. Wrapping the second lookup
// alone means the answer is exact — no scan of the text, no heuristic about
// what a credential looks like.
//
// BY NAME, ONCE. A variable referenced three times is answered three times
// and recorded once: the sender locates every occurrence of a value itself,
// so a second identical placement would be dropped by collapse anyway, and
// recording it would only make the list say something it does not mean.
func recordPlaced(look apicoll.Lookup, into *[]PlacedSecret) apicoll.Lookup {
	if look == nil {
		return nil
	}
	return func(name string) (string, bool, error) {
		value, found, err := look(name)
		if err != nil || !found {
			return value, found, err
		}
		for _, already := range *into {
			if already.Name == name {
				return value, true, nil
			}
		}
		*into = append(*into, PlacedSecret{Name: name, Value: value})
		return value, true, nil
	}
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

// ErrImportURLUnavailable — this build has no fetcher, so the URL entrance
// is not offered. Absence is the capability (the rule the pickers follow);
// the renderer draws the entrance from what the backend answers rather than
// from what it hopes.
var ErrImportURLUnavailable = errors.New("capability: this build cannot fetch an import by URL")

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
	//
	// srcPath is a path on the machine running THIS process, which is the
	// person's machine only when the desktop app is what is calling. It is
	// therefore the NARROW route; ImportPostmanDocument is the general one.
	ImportPostman(ctx context.Context, srcPath, dest string) ([]apiimport.Unsupported, error)

	// ImportPostmanDocument writes the same collection from the export's
	// BYTES, which the caller already holds. It is not a second import: the
	// writer takes an io.Reader (apiimport.ImportInto), and ImportPostman
	// differs only by opening a file to get one. A renderer reached over a
	// forwarded port has the bytes and cannot name a file the backend can
	// see, which is the case the path route cannot serve.
	ImportPostmanDocument(ctx context.Context, document, dest string) ([]apiimport.Unsupported, error)

	// ImportPostmanURL fetches the export over route and writes the same
	// collection. It is the general route in the other direction from
	// ImportPostmanDocument: there the renderer had the bytes, here NOBODY
	// on this side has them yet, because the document lives on a network
	// the backend can reach and the renderer may not.
	//
	// route is how to REACH the document, and the environment the import
	// mints inherits it (apiimport.ImportInto): a collection fetched from
	// behind a bastion must not arrive routed direct.
	ImportPostmanURL(ctx context.Context, rawURL string, route apicoll.Route, dest string) ([]apiimport.Unsupported, error)
}

// SecretBinder is the binding document's WRITE half, narrowed to the one
// call this operation makes. Declared here as a consumer contract, the way
// apiimport declares its own BindWriter for the same method: this layer
// needs to put one value away and has no part in the document's lifecycle.
type SecretBinder interface {
	Bind(ctx context.Context, k apibind.Key, value []byte) error
}

// APIBindingService is the write a person makes when they give a secret
// variable its value: the VALUE goes to the vault under a
// collection-and-environment binding, and the environment FILE keeps only
// the NAME (design §8).
//
// Until this, only an IMPORT could mint one — apiimport's BindWriter — so a
// variable a person declared secret in the editor had no way to be given a
// value at all, and the only way to send a token in a URL was to type it
// into a file that goes into git.
type APIBindingService interface {
	// BindSecret stores value under k. It takes the key rather than a
	// handle and a path because the key is the collection service's to
	// derive (BindingKeyFor) — this operation writes the vault and knows
	// nothing about collection folders.
	BindSecret(ctx context.Context, k apibind.Key, value []byte) error
}

// APIBindingOperation is the typed operation for the binding write. Its
// gates are [vault, api]: it puts a value in the vault, and it writes the
// binding document that api.import.postman also writes — the two must
// exclude each other, and the vault gate is what makes them.
type APIBindingOperation interface {
	Run(context.Context, func(context.Context, APIBindingService) error) error
}

// NewAPIBindingOperation builds it over the binding store.
func NewAPIBindingOperation(vaultGate, apiGate, lane control.Admission, bindings SecretBinder) APIBindingOperation {
	g := &guard{}
	return newOperation[APIBindingService](
		control.NewComposite(vaultGate, apiGate, lane),
		g,
		&apiBindingService{guard: g, bindings: bindings},
	)
}

type apiBindingService struct {
	guard    *guard
	bindings SecretBinder
}

// BindSecret hands the value to the binding store and nothing else.
//
// IT LOGS NOTHING AND WRAPS NOTHING WITH THE VALUE IN IT. The one argument
// that is a credential appears in no message this function can produce: a
// failure names the VARIABLE, which is what the person has to fix, and the
// store's own errors do the same (apibind).
func (s *apiBindingService) BindSecret(ctx context.Context, k apibind.Key, value []byte) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.bindings.Bind(ctx, k, value)
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
//
// fetch acquires a document by URL and may be nil, which is a build without
// the URL entrance rather than a build with a broken one: ImportPostmanURL
// then refuses by name (ErrImportURLUnavailable) and the other two
// entrances are untouched.
func NewAPIImportOperation(
	vaultGate, apiGate, lane control.Admission,
	fsys apiimport.FS,
	bindings apiimport.BindWriter,
	fetch apifetch.Fetcher,
) APIImportOperation {
	g := &guard{}
	return newOperation[APIImportService](
		control.NewComposite(vaultGate, apiGate, lane),
		g,
		&apiImportService{guard: g, fsys: fsys, bindings: bindings, fetch: fetch},
	)
}

type apiImportService struct {
	guard    *guard
	fsys     apiimport.FS
	bindings apiimport.BindWriter
	fetch    apifetch.Fetcher
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
	return apiimport.ImportInto(ctx, s.fsys, s.bindings, dest, f, apicoll.Route{Kind: apicoll.RouteDirect})
}

// ImportPostmanDocument runs the same import over the bytes it was given.
//
// There is no Lstat half here and nothing is missing: the two refusals above
// are about a FILE the process was asked to open — a fifo that would block
// the read while both domain gates are held, a directory with nothing in it
// to parse — and a document that arrived as bytes opens nothing. What
// bounds this route is the caller's: the transport refuses a document over
// maxAPIImportDocumentRunes before the call, and apiimport's own 16 MiB cap
// governs the parse whichever way the document arrived.
func (s *apiImportService) ImportPostmanDocument(ctx context.Context, document, dest string) ([]apiimport.Unsupported, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	return apiimport.ImportInto(ctx, s.fsys, s.bindings, dest, strings.NewReader(document), apicoll.Route{Kind: apicoll.RouteDirect})
}

// ImportPostmanURL fetches the export and imports it over the same route.
//
// The order is the whole of what this layer adds. The document is fetched
// COMPLETELY and only then handed to the writer: ImportInto's arrival is
// atomic, so from before the fetch until the collection is whole there is
// nothing at dest, and a fetch that failed halfway leaves no half-collection
// for the person to wonder about. The cost is one document in memory,
// bounded by the ceiling the parse already uses.
//
// The refusals are apifetch's own and are passed through unwrapped — a
// scheme this cannot GET, a body over the ceiling, an address whose bytes
// are not a document. Restating them here would be a second refusal
// vocabulary for one act.
func (s *apiImportService) ImportPostmanURL(ctx context.Context, rawURL string, route apicoll.Route, dest string) ([]apiimport.Unsupported, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	if s.fetch == nil {
		return nil, ErrImportURLUnavailable
	}
	doc, err := s.fetch.Fetch(ctx, rawURL, route)
	if err != nil {
		return nil, err
	}
	return apiimport.ImportInto(ctx, s.fsys, s.bindings, dest, bytes.NewReader(doc), route)
}
