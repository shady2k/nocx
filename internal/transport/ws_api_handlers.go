package transport

// The api.* control handlers as constructed types: each handler holds its
// operation and the Responder — never the *WSServer, and never
// apicoll.Service directly. The service is handed to the callback inside
// op.Run, guard-bound, so a handler cannot reach the folder outside the
// operation that gates it (capability.ErrOperationInactive).
//
// Two things here are deliberately NOT copied from ws_snippet_handlers.go,
// which is otherwise the template.
//
// The gate. Snippets hold the CONFIG gate because the snippet library is one
// document under the profile directory that backup/restore also writes
// (ws_snippet_handlers.go:9). A collection is an arbitrary folder the user
// chose (design §6.1); backup/restore does not touch it, so that reasoning
// does not transfer and collections get their own conflict domain
// (capability.GateAPI). See internal/capability/api.go.
//
// The send. api.request.send performs network I/O against a server whose
// latency is not ours to bound. It snapshots the request under the api gate
// and dials AFTER Run has returned, so no domain gate is held across the
// exchange — a global gate behind arbitrary remote latency would block every
// unrelated settings, vault and backup operation for as long as a hung
// server cared to wait.
//
// And one rule this file enforces rather than remembers: design §13.1 says
// opening a collection mints a backend-held handle and `root` is never
// accepted again. "Never accepted" is only a rule if a params object
// carrying a path is REFUSED — a tolerant decoder would ignore it, which
// reads identically from the renderer and leaves the property as a habit.
// Every api.* validator below decodes STRICTLY (decodeAPIParams), and
// TestAPIMethods_OnlyOpenAndImportPostmanAcceptAPath asserts it for the
// whole surface.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/apibind"
	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/apiimport"
	"github.com/shady2k/nocx/internal/apisend"
	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/transport/control"
)

// ── handlers ─────────────────────────────────────────────────────────────

// apiCollectionHandlers answers api.collections.* and api.request.*. The
// sender is held beside the operation rather than inside its service on
// purpose: a service method is called inside Run, and a send inside Run
// would hold the api gate across the dial.
type apiCollectionHandlers struct {
	op     capability.APICollectionOperation
	sender apisend.Sender
	// values is the binding document's read half: it answers what a
	// variable is worth and has no parameter through which an identifier
	// could arrive (design §8). nil is a build with no binding store, and
	// then an auth variable resolves to nothing.
	values apibind.ValueResolver
	// bindOp is the write half of a secret variable: the VALUE into the
	// vault, under the binding this collection and environment own. nil is
	// a build with no binding store, and then the method answers -32601
	// rather than accepting a credential it has nowhere to put.
	bindOp capability.APIBindingOperation
	// cancels is which running exchange a token names, and conn is which
	// window minted it. Both are nil/zero for the handlers that cannot
	// send: a read has nothing to stop (ws_api_cancel.go).
	cancels *sendCancels
	conn    uint64
	r       Responder
}

func (h apiCollectionHandlers) handleMethod(ctx context.Context, req jsonrpcRequest) {
	err := h.op.Run(ctx, func(_ context.Context, svc capability.APICollectionService) error {
		switch req.Method {
		case "api.collections.list":
			open, err := svc.ListOpen()
			if err != nil {
				h.fail(req, err)
				return nil
			}
			// Inside the operation the only failure this can have is the
			// guard, which is held — so it is answered rather than
			// swallowed into "", which would report a build with no app
			// directory and a service somebody escaped as the same state.
			root, err := svc.DefaultRoot()
			if err != nil {
				h.fail(req, err)
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(wireOpenCollections(svc, open, root)))
		case "api.collections.open":
			var p apiCollectionsOpenParams
			if !h.decode(req, &p) {
				return nil
			}
			opened, err := svc.Open(p.Path)
			if err != nil {
				h.fail(req, err)
				return nil
			}
			wire, envErr := wireCollectionOf(svc, opened.Handle, opened.Collection)
			if envErr != nil {
				// The folder opened and its environments did not read. That
				// is the folder being unusable rather than a partial answer
				// worth rendering: a panel handed environments:[] for a
				// collection that HAS them would offer "None" as the whole
				// truth and send {{baseUrl}} unresolved (§6.5). The open is
				// refused with the reason instead — and the handle stays
				// registered, so a retry after the permission is fixed needs
				// no second Open. That retry answers alreadyOpen:true, which
				// is the truth: the folder IS open and api.collections.list
				// has been carrying it since the call that failed here.
				h.fail(req, envErr)
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(apiOpenResponse{
				Handle:      string(opened.Handle),
				AlreadyOpen: opened.AlreadyOpen,
				Collection:  wire,
			}))
		case "api.collections.create":
			var p apiCollectionsCreateParams
			if !h.decode(req, &p) {
				return nil
			}
			made, err := svc.Create(p.Name)
			if err != nil {
				h.fail(req, err)
				return nil
			}
			// The handle and the collection api.collections.open answers
			// with, through the same assembler and deliberately so: a create
			// leaves the collection open, so the renderer has one thing to
			// do afterwards rather than two. What it does NOT carry is
			// alreadyOpen — apiCreateResponse says why.
			wire, envErr := wireCollectionOf(svc, made.Handle, made.Collection)
			if envErr != nil {
				h.fail(req, envErr)
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(apiCreateResponse{
				Handle:     string(made.Handle),
				Collection: wire,
			}))
		case "api.collections.createFolder":
			var p apiCollectionsCreateFolderParams
			if !h.decode(req, &p) {
				return nil
			}
			made, err := svc.CreateFolder(apicoll.HandleID(p.Handle), p.ParentRelPath, p.Name)
			if err != nil {
				h.fail(req, err)
				return nil
			}
			// The collection AFTER the folder was made, through the one
			// assembler every api.collections.* result goes through: the
			// caller's next move is to draw the tree, and a listing taken
			// in a second round trip would be a second account of one
			// folder.
			wire, envErr := wireCollectionOf(svc, apicoll.HandleID(p.Handle), made.Collection)
			if envErr != nil {
				h.fail(req, envErr)
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(apiCollectionsCreateFolderResponse{
				RelPath:    made.RelPath,
				Collection: wire,
			}))
		case "api.collections.close":
			var p apiHandleParams
			if !h.decode(req, &p) {
				return nil
			}
			if err := svc.Close(apicoll.HandleID(p.Handle)); err != nil {
				h.fail(req, err)
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(apiEmptyResponse{}))
		case "api.environment.read":
			var p apiEnvironmentParams
			if !h.decode(req, &p) {
				return nil
			}
			env, err := svc.ReadEnvironment(apicoll.HandleID(p.Handle), p.RelPath)
			if err != nil {
				h.fail(req, err)
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(apiEnvironmentReadResponse{
				Environment: wireEnvironment(env),
			}))
		case "api.environment.write":
			var p apiEnvironmentWriteParams
			if !h.decode(req, &p) {
				return nil
			}
			err := svc.WriteEnvironment(
				apicoll.HandleID(p.Handle), p.RelPath, storedEnvironment(p.Environment))
			if err != nil {
				h.fail(req, err)
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(apiEmptyResponse{}))
		case "api.request.delete":
			var p apiRequestParams
			if !h.decode(req, &p) {
				return nil
			}
			if err := svc.DeleteRequest(apicoll.HandleID(p.Handle), p.RelPath); err != nil {
				h.fail(req, err)
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(apiEmptyResponse{}))
		case "api.request.read":
			var p apiRequestParams
			if !h.decode(req, &p) {
				return nil
			}
			r, err := svc.ReadRequest(apicoll.HandleID(p.Handle), p.RelPath)
			if err != nil {
				h.fail(req, err)
				return nil
			}
			wire, err := wireRequest(r)
			if err != nil {
				h.fail(req, err)
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(apiRequestReadResponse{Request: wire}))
		case "api.request.write":
			var p apiRequestWriteParams
			if !h.decode(req, &p) {
				return nil
			}
			if err := svc.WriteRequest(apicoll.HandleID(p.Handle), p.RelPath, storedRequest(p.Request)); err != nil {
				h.fail(req, err)
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(apiEmptyResponse{}))
		case "api.request.move":
			var p apiRequestMoveParams
			if !h.decode(req, &p) {
				return nil
			}
			moved, err := svc.MoveRequest(apicoll.HandleID(p.Handle), p.RelPath, p.ToRelPath)
			if err != nil {
				h.fail(req, err)
				return nil
			}
			// The result carries the new relPath because the caller's next
			// act is to address the file again — a request that is open in
			// the form has to be re-pointed at the new path, and deriving it
			// itself would be the second answer this surface refuses to
			// make.
			_ = h.r.TryResult(req.ID, mustMarshal(apiRequestMoveResponse{RelPath: moved}))
		case "api.folder.read":
			var p apiFolderParams
			if !h.decode(req, &p) {
				return nil
			}
			variables, err := svc.ReadFolderVariables(apicoll.HandleID(p.Handle), p.RelPath)
			if err != nil {
				h.fail(req, err)
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(apiFolderReadResponse{
				Variables: wireParams(variables),
			}))
		case "api.folder.write":
			var p apiFolderWriteParams
			if !h.decode(req, &p) {
				return nil
			}
			variables, err := svc.WriteFolderVariables(
				apicoll.HandleID(p.Handle), p.RelPath, storedParams(p.Variables))
			if err != nil {
				h.fail(req, err)
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(apiFolderWriteResponse{
				Variables: wireParams(variables),
			}))
		case "api.request.scope":
			h.handleRequestScope(ctx, req, svc)
		}
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// handleSend is api.request.send, and its shape is the point: the gate is
// held for the snapshot and released before the dial.
//
// TWO INTERVALS, both stated with both ends.

// The GATE: the api gate is acquired before the request file is opened and
// released when Run returns with the request value in hand; from that moment
// until the response is captured no domain gate is held at all, so a server
// that never answers delays this one request and nothing else.
//
// The TOKEN: registered before anything else happens — before the gate is
// even asked for — and dropped in a defer when this function returns,
// whichever way it returns. It is that early on purpose. The renderer draws
// its Stop the instant a person presses Send, so a registration that waited
// for the snapshot would leave a window in which a visible, stoppable-looking
// run answers "no request is running under this token"; and the window is
// widest exactly when the api gate is busy, which is when a person is most
// likely to be reaching for Stop. Because the snapshot runs under the
// cancellable context too, a Stop pressed during it ends the operation rather
// than being remembered until the dial.
func (h apiCollectionHandlers) handleSend(ctx context.Context, req jsonrpcRequest) {
	var p apiRequestSendParams
	if !h.decode(req, &p) {
		return
	}

	sendCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// A token already in flight refuses the SEND rather than replacing the
	// registration: two exchanges under one name would leave Stop guessing
	// which it meant.
	if taken := h.cancels.register(h.conn, p.Token, cancel); taken != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: taken.Error()})
		return
	}
	defer h.cancels.drop(h.conn, p.Token)

	var inputs capability.SendInputs
	// unresolved is the one snapshot failure that is a RUN rather than an
	// error: a variable with no value. Carried out of the callback rather
	// than answered inside it, because the answer needs the route and the
	// environment the snapshot read, and building it under the gate would
	// hold the whole domain to render a string.
	var unresolved error
	snapshotted := false

	err := h.op.Run(sendCtx, func(ctx context.Context, svc capability.APICollectionService) error {
		got, err := svc.Snapshot(ctx, apicoll.HandleID(p.Handle), p.RelPath, p.EnvRelPath)
		if err != nil && !composeRefusal(err) {
			h.fail(req, err)
			return nil
		}
		inputs = got
		unresolved = err
		snapshotted = true
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
		return
	}
	if !snapshotted {
		return // the callback has already answered
	}

	// A VARIABLE WITH NO VALUE IS A RUN, at the `compose` phase, and it
	// answers with the unresolved reason — which names every missing
	// variable and the field each was used in (apicoll.UnresolvedError) —
	// rather than with a complaint about a URL nobody typed. Before this the
	// substitution simply did not happen when no environment was named, so
	// `{{baseUrl}}/zen` reached the sender as text and came back as
	// `"{{baseUrl}}/zen" is not an absolute URL`.
	//
	// It is answered here rather than sent, because there is nothing to
	// send: the request is the one the file holds, references and all.
	if unresolved != nil {
		_ = h.r.TryResult(req.ID, mustMarshal(wireExchange(
			apisend.Unsent(inputs.Request, apisend.PhaseCompose, unresolved),
			inputs.Environment, inputs.Route)))
		return
	}

	// The route comes off the environment the snapshot read, in the same
	// record as the address it substituted (§6.5) — so a request cannot go
	// out at the production address around its bastion. A route this build
	// cannot name is refused here rather than falling through to the direct
	// one, which would be exactly that send.
	routeID, err := apisend.RouteIDFor(inputs.Route)
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
		return
	}

	// The already-SUBSTITUTED auth text becomes a header HERE — outside the
	// gate, and before the dial. The lookup is gone: since nocx-6hg2w.20
	// the auth is text like every other field, resolved inside
	// apicoll.Substitute at the snapshot, and there is exactly ONE
	// resolver. What Apply still needs is the caller's knowledge of which
	// auth FIELD was answered by the binding document (inputs.AuthSecrets):
	// a variable-sourced credential reaches Send as a NAMED SECRET or it
	// would ride the raw diagnostic in the clear (§11.2). A LITERAL the
	// person typed places nothing and is shown — the decision recorded in
	// nocx-tg9l8.
	//
	// The snapshot computed that fact field-wise, from the FILE's text
	// against the names the binding answered — never from the resolved
	// VALUE — so a literal that happens to equal a vault value is still not
	// elided, which is the point of answering by construction.
	sending, used, err := apisend.Apply(inputs.Request, inputs.AuthSecrets)
	// The values the SUBSTITUTION placed are beside the one the auth block
	// placed; both are handed to the sender for the same reason and end in
	// the same place: it locates them in the text it composes and elides
	// them, so the raw view shows a chip where a token went (§11.2).
	if err != nil {
		// The SAME division as the snapshot's above: an auth block that
		// cannot become a header — a scheme chosen with an EMPTY
		// credential, §6.5's blocked send that must never downgrade to
		// anonymous — is a run at `compose`, because it is a thing that
		// happened to somebody who pressed Send. Anything else Apply
		// refuses — a scheme this build does not implement — is the
		// caller's to fix and stays an error.
		if composeRefusal(err) {
			_ = h.r.TryResult(req.ID, mustMarshal(wireExchange(
				apisend.Unsent(inputs.Request, apisend.PhaseCompose, err),
				inputs.Environment, inputs.Route)))
			return
		}
		_ = h.r.TryError(req.ID, RPCError{Code: apiMethodErrorCode(err), Message: err.Error()})
		return
	}

	for _, secret := range inputs.Secrets {
		used = append(used, apisend.NamedSecret{Name: secret.Name, Value: secret.Value})
	}

	// No gate from here on. The cookie scope is the collection, so two
	// collections never share a jar.
	ex, err := h.sender.Send(sendCtx, sending, apisend.Key{
		RouteID:     routeID,
		CookieScope: inputs.CookieScope,
		// The environment's own declaration, carried into the KEY so a
		// transport that verifies and one that does not are never the same
		// transport (apisend.Key).
		InsecureTLS: inputs.Route.InsecureTLS,
	}, used...)
	if err != nil {
		// An ERROR from the sender is now one thing only: a request it
		// refuses to send at all (apisend.Send). Everything that happened
		// to an attempt — a name that did not resolve, a refused port, a
		// rejected certificate, a stop — comes back as an exchange below,
		// because a person who pressed Send has a run whatever the world
		// did next.
		_ = h.r.TryError(req.ID, RPCError{Code: apiSendErrorCode(err), Message: err.Error()})
		return
	}
	// A SEALED VAULT IS NOT AN EXCHANGE.
	//
	// An environment that routes through a connection needs that connection's
	// credential, and the credential is in the vault. Sealed, the lease
	// cannot be taken — so nothing is dialled, nothing leaves this machine,
	// and what stopped the attempt is a precondition the person can fix
	// rather than anything that happened to a request. The epic's own line
	// already names that case: what stays a JSON-RPC error is what is not an
	// exchange.
	//
	// Answered as one, the seam that already exists runs end to end: the
	// normalizer passes the canonical shape through, the renderer's
	// dispatcher raises ONE unlock dialog and re-sends this request verbatim
	// when the vault answers (vault_sealed.go). The person meets the prompt
	// they would have met from a terminal tab and never learns there is a
	// Vault page. Before this they got a dead row saying "vault is sealed" —
	// a sentence describing a door, with nothing to press.
	//
	// THREE CONDITIONS, and the middle one is what keeps this a precondition
	// rather than a blanket rule:
	//
	//   - the attempt did not answer;
	//   - it stopped at PhaseConnection, which is the route being unusable —
	//     the lease is taken there and nowhere else, so a vault that sealed
	//     MID-EXCHANGE lands at a later phase and stays a run, as it must:
	//     that request did go out;
	//   - and its error chain reaches the vault's own sentinel.
	if ex.Failure != nil && ex.Failure.Phase == apisend.PhaseConnection {
		if sealed, ok := sealedVaultFailure(ex.Failure.Err); ok {
			// The token is dropped HERE rather than only by the deferred
			// drop below, because the renderer re-sends the request VERBATIM
			// — the same token — as soon as the vault answers. The defer
			// runs after this response is enqueued, so releasing the name
			// first is what makes the replay's registration certain rather
			// than merely likely. Dropping twice is a delete of a key that
			// is already gone.
			h.cancels.drop(h.conn, p.Token)
			_ = h.r.TryError(req.ID, sealed)
			return
		}
	}
	_ = h.r.TryResult(req.ID, mustMarshal(wireExchange(ex, inputs.Environment, inputs.Route)))
}

// composeRefusal reports whether err is a reason the request could not be
// BUILT from what the person has — the `compose` phase of an exchange that
// never went out, and therefore a RUN rather than a JSON-RPC error.
//
// Two members, and they are one concept from two directions. A variable with
// no value is a hole nobody filled; a request row shadowing a name the
// environment declares secret is a hole filled from the wrong place (§8).
// Both are fixed by editing something the person owns — a table, an
// environment, a binding — and both belong on a row beside everything else
// they have sent rather than as a sentence with no request attached to it.
//
// What is NOT here is what is not an exchange at all: an unknown handle, a
// file that will not read, a vault that is sealed. Those stay errors, which
// is the line the epic drew and this predicate keeps in one place rather
// than at each of the two call sites.
func composeRefusal(err error) bool {
	return errors.Is(err, apicoll.ErrUnresolvedVariable) || errors.Is(err, apicoll.ErrSecretShadowed)
}

// handleCancel is api.request.cancel: stop the exchange running under this
// connection's token.
//
// It holds NO gate and takes no operation. There is nothing here to
// serialise — it fires a context cancel and touches no folder, no file and
// no vault — and putting it behind the api gate would be worse than
// pointless: the gate is capacity one, so a Stop would queue behind whatever
// the api domain was doing, which on a busy panel is the very send it is
// trying to stop.
func (h apiCollectionHandlers) handleCancel(_ context.Context, req jsonrpcRequest) {
	var p apiRequestCancelParams
	if !h.decode(req, &p) {
		return
	}
	if !h.cancels.stop(h.conn, p.Token) {
		// -32602 and not a success: the caller asked to stop something, and
		// "there was nothing to stop" is an answer it needs to be able to
		// tell apart from "it is stopped".
		_ = h.r.TryError(req.ID, RPCError{
			Code:    -32602,
			Message: fmt.Errorf("%w (token %q)", errUnknownToken, p.Token).Error(),
		})
		return
	}
	// EMPTY, deliberately. The stopped exchange reports itself, on the
	// api.request.send result of the very request that was stopped, as
	// outcome "stopped" — two methods reporting one exchange's end would be
	// two accounts of it (contracts/api.request.cancel.schema.json).
	_ = h.r.TryResult(req.ID, mustMarshal(apiEmptyResponse{}))
}

// handleBindSecret is api.environment.bindSecret: give a secret variable its
// value.
//
// THIS IS THE ONE api.* METHOD THAT CARRIES A CREDENTIAL INBOUND, and the
// shape is written around that. Three properties, each with what it stops:
//
//  1. THE VALUE APPEARS IN NO MESSAGE THIS FUNCTION CAN PRODUCE. Every
//     refusal below names the variable, the environment or the handle —
//     never `p.Value`. An error is written to a log the person did not
//     choose, and a credential in one is a credential leaked (§11 states
//     the same rule for the diagnostic).
//  2. TWO OPERATIONS, NOT ONE. The collection operation derives the binding
//     key — the collection's root and the environment's NAME, read out of
//     the file, which is what the READ half is keyed by — and the binding
//     operation writes the vault under [vault, api], the same pair
//     api.import.postman holds, so the two writers of the binding document
//     exclude each other.
//  3. NOTHING GOES INTO THE COLLECTION FOLDER. The environment file is not
//     rewritten here at all: it declares the variable's NAME, which the
//     editor's own save put there, and there is no field in that format a
//     value could be written into (design §8).
func (h apiCollectionHandlers) handleBindSecret(ctx context.Context, req jsonrpcRequest) {
	var p apiEnvironmentBindSecretParams
	if !h.decode(req, &p) {
		return
	}

	// 1. The key, under the collection's own gate.
	var collection, environment string
	derived := false
	err := h.op.Run(ctx, func(_ context.Context, svc capability.APICollectionService) error {
		root, name, keyErr := svc.BindingKeyFor(apicoll.HandleID(p.Handle), p.RelPath)
		if keyErr != nil {
			h.fail(req, keyErr)
			return nil
		}
		collection, environment = root, name
		derived = true
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
		return
	}
	if !derived {
		return // the callback has already answered
	}

	// 2. The value, under the vault's.
	err = h.bindOp.Run(ctx, func(ctx context.Context, svc capability.APIBindingService) error {
		bindErr := svc.BindSecret(ctx, apibind.Key{
			Collection:  collection,
			Environment: environment,
			Variable:    p.Variable,
		}, []byte(p.Value))
		if bindErr != nil {
			h.fail(req, bindErr)
			return nil
		}
		_ = h.r.TryResult(req.ID, mustMarshal(apiEmptyResponse{}))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// decode reads the method's params and answers -32602 itself when they will
// not decode. The registered validator has already checked every field, so
// reaching this is a shape the validator accepted and the handler could
// not use; answering here keeps the handler from acting on a zero value.
func (h apiCollectionHandlers) decode(req jsonrpcRequest, dst any) bool {
	if msg := decodeAPIParams(req.Params, dst); msg != "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: msg})
		return false
	}
	return true
}

func (h apiCollectionHandlers) fail(req jsonrpcRequest, err error) {
	_ = h.r.TryError(req.ID, RPCError{Code: apiMethodErrorCode(err), Message: err.Error()})
}

// apiImportHandlers answers api.import.*. postman writes and holds an
// operation; curl converts a line into a value, touches nothing, and holds
// none — giving it one would serialise a pure parse behind whatever the api
// domain happened to be doing.
type apiImportHandlers struct {
	op capability.APIImportOperation
	r  Responder
}

func (h apiImportHandlers) handlePostman(ctx context.Context, req jsonrpcRequest) {
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.APIImportService) error {
		var p apiImportPostmanParams
		if msg := decodeAPIParams(req.Params, &p); msg != "" {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: msg})
			return nil
		}
		// One import, three ways in, and the choice is already made: the
		// validator refused several-and-none, so exactly one of these is
		// set and there is no precedence rule here to disagree with it.
		var (
			unsup []apiimport.Unsupported
			err   error
		)
		switch {
		case p.URL != "":
			unsup, err = svc.ImportPostmanURL(ctx, p.URL, storedRoute(p.Route), p.Dest)
		case p.Document != "":
			unsup, err = svc.ImportPostmanDocument(ctx, p.Document, p.Dest)
		default:
			unsup, err = svc.ImportPostman(ctx, p.Path, p.Dest)
		}
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: apiMethodErrorCode(err), Message: err.Error()})
			return nil
		}
		_ = h.r.TryResult(req.ID, mustMarshal(apiImportPostmanResponse{Unsupported: wireUnsupported(unsup)}))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

func (h apiImportHandlers) handleCurl(_ context.Context, req jsonrpcRequest) {
	var p apiImportCurlParams
	if msg := decodeAPIParams(req.Params, &p); msg != "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: msg})
		return
	}
	r, unsup, err := apiimport.FromCurl(p.Line)
	if err != nil {
		// A line this package could not parse is the caller's line, not the
		// server's fault: refused rather than guessed at (design §10).
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: err.Error()})
		return
	}
	wire, err := wireRequest(r)
	if err != nil {
		// The converter produced a kind this build does not declare, which
		// is an inconsistency inside the backend rather than anything the
		// caller can rephrase.
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(apiImportCurlResponse{
		Request:     wire,
		Unsupported: wireUnsupported(unsup),
	}))
}

// apiMethodErrorCode maps a collection or import error to a JSON-RPC code.
//
// The split is about who can act on it. An error that names something the
// CALLER supplied — a handle nobody minted, a path leaving the folder, a
// file that is not there, a destination already occupied — is -32602, and
// the caller's move is to name something else. An error that describes the
// WORLD — the root replaced under an open handle, a request file that is
// malformed on disk — is -32603 carrying its own sentence, because the
// caller cannot fix it by asking differently and the user needs to read
// what happened.
func apiMethodErrorCode(err error) int {
	switch {
	case errors.Is(err, apicoll.ErrUnknownHandle),
		errors.Is(err, apicoll.ErrPathOutsideCollection),
		errors.Is(err, apicoll.ErrNotARequestPath),
		errors.Is(err, apicoll.ErrRequestNotFound),
		errors.Is(err, apicoll.ErrNoManifest),
		errors.Is(err, apicoll.ErrCollectionExists),
		errors.Is(err, apicoll.ErrInvalidCollectionName),
		errors.Is(err, apicoll.ErrInvalidFolderName),
		errors.Is(err, apicoll.ErrFolderExists),
		errors.Is(err, apicoll.ErrFolderNotFound),
		errors.Is(err, apicoll.ErrRequestExists),
		errors.Is(err, capability.ErrImportNotAFile):
		return -32602
	default:
		return -32603
	}
}

// apiSendErrorCode maps a send failure. A body that names a file and an auth
// that names an unbound variable are both requests the sender REFUSES to
// send rather than sending something plausible and wrong (§6.5), and both
// are fixed by editing the request — so they are the caller's error. A
// transport failure is not.
func apiSendErrorCode(err error) int {
	if errors.Is(err, apisend.ErrFileBody) ||
		errors.Is(err, apisend.ErrAuthUnresolved) ||
		// An http:// URL naming a host through a connection route: the far
		// side resolves the name, so this end cannot check what will be
		// reached and refuses rather than guessing. Fixed by editing the
		// request — an address, or https — so it is the caller's.
		errors.Is(err, apisend.ErrNameResolvedRemotely) {
		return -32602
	}
	// apisend.ErrNoConnection falls through to -32603 on purpose: the
	// environment is right and the world is not, so the user's move is to
	// open the connection rather than to ask differently.
	return -32603
}

// ── params ───────────────────────────────────────────────────────────────

type apiCollectionsOpenParams struct {
	Path string `json:"path"`
}

type apiHandleParams struct {
	Handle string `json:"handle"`
}

type apiRequestParams struct {
	Handle  string `json:"handle"`
	RelPath string `json:"relPath"`
}

// apiRequestMoveParams names the request's two places, and both are addressed
// exactly as every other api.* method addresses a file: the backend-held
// handle plus a path relative to it, never a root (§13.1). There is no
// second handle and no way to name another collection: the move is between
// two paths inside the ONE collection the handle names.
//
// toRelPath is spelled out rather than derived on the caller's side, for the
// same reason every other path is carried: a renderer joining a folder and a
// stem itself would be a second answer to "where does this land now".
type apiRequestMoveParams struct {
	Handle    string `json:"handle"`
	RelPath   string `json:"relPath"`
	ToRelPath string `json:"toRelPath"`
}

// apiCollectionsCreateParams carries a NAME and not a path. That is the
// whole difference from api.collections.open: a name is a single folder
// name, the location is derived from it inside apicoll, and this method
// therefore does not join the two that accept a root (§13.1).
type apiCollectionsCreateParams struct {
	Name string `json:"name"`
}

// apiCollectionsCreateFolderParams carries a NAME and the folder to put it
// in, and neither is a root.
//
// name is a single component — api.collections.create's grammar one level
// down. parentRelPath is an EXISTING folder inside the collection,
// addressed the way every other thing here is addressed: the backend-held
// handle plus a path relative to it (§13.1). Absent is the collection root,
// which is where a folder made with no parent chosen goes.
//
// Why not one relative path with the intermediate folders made along the
// way: MkdirAll succeeds for a request nobody made — a misspelled month
// would mint the misspelling and there would be no moment at which the
// caller could be told the parent is not there. Nesting is repeated calls,
// each naming a parent that already exists.
type apiCollectionsCreateFolderParams struct {
	Handle        string `json:"handle"`
	ParentRelPath string `json:"parentRelPath"`
	Name          string `json:"name"`
}

// apiRequestSendParams is api.request.send's own params rather than
// apiRequestParams with a field added, because the extra field is a send's
// and not a read's: strict decoding refuses a field a method does not
// declare, and sharing the struct would quietly teach api.request.read to
// accept an environment it has no use for.
//
// envRelPath names the environment the request is sent UNDER, addressed
// inside the collection exactly like the request itself. It is optional:
// absent is no environment, which is the request as written on the direct
// route — a collection with no environments is a collection (§6.2).
type apiRequestSendParams struct {
	Handle     string `json:"handle"`
	RelPath    string `json:"relPath"`
	EnvRelPath string `json:"envRelPath"`
	// Token is the caller's OWN name for this exchange, and it is required:
	// a send that could not be named is a run with no Stop, and "usually
	// stoppable" is the kind of capability a surface cannot draw a button
	// for. It is the renderer's rather than the JSON-RPC id because the id
	// belongs to the dispatcher and is never handed to the caller that
	// asked (ws_api_cancel.go).
	Token string `json:"token"`
}

// apiRequestCancelParams names an exchange and nothing else. No handle and
// no path: the token already identifies exactly one running exchange, and a
// second way to address it would be a second answer to "which run is this".
type apiRequestCancelParams struct {
	Token string `json:"token"`
}

// apiEnvironmentBindSecretParams carries a credential INBOUND, which no
// other params struct on this surface does.
//
// Value is the only field in the whole api.* wire format that holds one, and
// it goes one way: in. Nothing echoes it, no result carries it, and the
// binding document answers values only through apibind's own resolver, which
// has no parameter an identifier could arrive through (design §8).
//
// The variable is named rather than addressed by index: a row's position in
// an editor is not a name, and the binding key is a triple of names.
type apiEnvironmentBindSecretParams struct {
	Handle   string `json:"handle"`
	RelPath  string `json:"relPath"`
	Variable string `json:"variable"`
	Value    string `json:"value"`
}

// apiEnvironmentParams addresses one environment file the way every other
// api.* method addresses a file: the backend-held handle plus a path inside
// it, never a root (§13.1).
type apiEnvironmentParams struct {
	Handle  string `json:"handle"`
	RelPath string `json:"relPath"`
}

type apiEnvironmentWriteParams struct {
	Handle      string             `json:"handle"`
	RelPath     string             `json:"relPath"`
	Environment apiEnvironmentWire `json:"environment"`
}

type apiRequestWriteParams struct {
	Handle  string         `json:"handle"`
	RelPath string         `json:"relPath"`
	Request apiRequestWire `json:"request"`
}

// apiImportPostmanParams names the export THREE ways, and exactly one of
// them may be given (validateAPIImportPostmanRaw).
//
// Path names a file on the machine running THIS process. In the desktop app
// that is also the person's machine, which is why it reads naturally there;
// reached over a forwarded port (`make dev-web` forwards both ports over
// SSH) it names a file on the server, which is almost never the export a
// person just downloaded. Document carries the export's bytes instead, and
// bytes reach a backend wherever it runs.
//
// This is NOT ws_upload.go's R2 in reverse. R2 says the renderer may name an
// upload's DESTINATION and may never name its SOURCE path, because a
// renderer that could spell a backend path could have the backend read
// ~/.ssh/id_ed25519 and send it somewhere. `path` here is an existing
// parameter of an existing method — design §13.1's second and last path,
// unchanged and not widened by this — and `document` names no path at all,
// which is strictly the safer of the two routes rather than a new way to
// spell a source.
type apiImportPostmanParams struct {
	Path     string `json:"path"`
	Document string `json:"document"`
	// URL is the third and most general source: the document is neither on
	// the backend's disk nor in the renderer's hands, because it is behind a
	// network the renderer may not be on. Route says how to reach it, and
	// means nothing without it.
	URL   string        `json:"url"`
	Route *apiRouteWire `json:"route"`
	Dest  string        `json:"dest"`
}

type apiImportCurlParams struct {
	Line string `json:"line"`
}

// ── wire results ─────────────────────────────────────────────────────────
//
// Hand-written wire structs rather than the domain types marshalled
// directly, and the reason is in each of them: apicoll's own JSON tags carry
// `omitempty`, which is right for a file on disk — an absent `headers` key
// is a request with no headers — and wrong on the wire, where the renderer's
// first .map on an absent list throws. Every list here is forced non-nil.

type apiEmptyResponse struct{}

type apiCollectionsListResponse struct {
	Collections []apiOpenCollectionWire `json:"collections"`
	// DefaultRoot is where a collection made with no place named goes, so an
	// ask can PROPOSE a destination rather than demand one. It rides the
	// listing because it is a fact about this build rather than about any
	// one folder, and the listing is the call every surface already makes.
	// "" is a build with no app directory to derive it from.
	DefaultRoot string `json:"defaultRoot"`
}

type apiOpenCollectionWire struct {
	Handle     string            `json:"handle"`
	Path       string            `json:"path"`
	Collection apiCollectionWire `json:"collection"`
	// Error is why this one folder could not be re-read — a root replaced
	// or removed since it was opened — and "" when it could. It is on the
	// entry rather than in an error beside the listing so one dead folder
	// cannot hide every live one.
	Error string `json:"error"`
}

type apiOpenResponse struct {
	Handle string `json:"handle"`
	// AlreadyOpen is whether the folder was open before this call. It is on
	// the wire because one folder has ONE handle for as long as it is open
	// (apicoll.Opened): a path that is already open answers with the handle
	// that exists, and a surface has to be able to tell "I opened it" from
	// "you already had it" in order to reveal the row it has rather than
	// add a second one. Reading it off the tree instead would put a second
	// reader of collection identity in the renderer (nocx-ghuq3).
	AlreadyOpen bool              `json:"alreadyOpen"`
	Collection  apiCollectionWire `json:"collection"`
}

// apiCreateResponse is api.collections.create's answer: the same handle and
// collection an open carries, and no alreadyOpen.
//
// It is a separate type rather than apiOpenResponse for exactly that field.
// A folder minted a moment ago in a location the backend chose cannot be one
// somebody already had open, so the question has no answer here worth
// sending; a hard-coded false on the wire would be a field the renderer
// could branch on and never see change. What the two results DO share — the
// handle and the collection, assembled by one wireCollectionOf — is what the
// create schema means by "the shape is api.collections.open's on purpose",
// and that has not moved.
type apiCreateResponse struct {
	Handle     string            `json:"handle"`
	Collection apiCollectionWire `json:"collection"`
}

// apiCollectionsCreateFolderResponse is the folder that was made and the
// collection it is in.
//
// relPath is carried rather than left to be reassembled from the params:
// the caller passes it straight back as the parent of the next folder, and
// a renderer joining a parent and a name itself would be a second answer to
// "what is this folder called from the root".
type apiCollectionsCreateFolderResponse struct {
	RelPath    string            `json:"relPath"`
	Collection apiCollectionWire `json:"collection"`
}

// apiRequestMoveResponse is a move's whole answer: the new relPath. The
// bytes that moved were the bytes at the source — the file is the truth
// (§6.4), and nothing here re-reads them — and the tree is re-read by the
// caller through the listing, so echoing anything else back would be a
// second account of one act.
type apiRequestMoveResponse struct {
	RelPath string `json:"relPath"`
}

type apiCollectionWire struct {
	Name     string              `json:"name"`
	Requests []apiRequestRefWire `json:"requests"`
	// Folders is every directory inside the collection, parents before
	// their children. It is on the wire because a folder holding nothing
	// yet is invisible in Requests, and that is the state a folder is in
	// for as long as it takes the person to fill it — a tree drawing itself
	// from the request paths alone would lose the folder they just made.
	//
	// It is also the ONE answer to "what folders are there": a renderer
	// deriving them from the request paths as well would agree with this
	// list about every folder that holds a request and disagree about every
	// folder that does not. Never nil.
	Folders []string `json:"folders"`
	// VariableFolders names the folders carrying `.variables.json`, plus ""
	// for the collection root. It carries presence only; values remain on
	// disk. It is part of this listing rather than a per-folder read so the
	// tree does not ask a second question about folders it already has.
	VariableFolders []string `json:"variableFolders"`
	// Malformed carries the unreadable files from BOTH halves of the folder
	// — requests and environments — in one list, because "a file in here
	// that cannot be read" is one concept and a second list would be a
	// second owner of it. Never nil: the renderer's first .map on a null
	// throws.
	Malformed []apiMalformedRefWire `json:"malformed"`
	// Environments is every environment in `environments/` (§6.2). It rides
	// on the collection because it is part of what the folder IS; a second
	// method answering "which environments does this folder have" would be
	// two accounts of one folder, read a round trip apart.
	Environments []apiEnvironmentRefWire `json:"environments"`
}

// apiEnvironmentRefWire is one environment, and it carries NO VALUES and no
// route — deliberately, and it is the field list that enforces it rather
// than a redaction step.
//
// apicoll.EnvironmentRef embeds the whole Environment, values included, so
// marshalling the domain type here would put every environment's addresses
// on the wire for a panel whose entire need is to NAME one. Beyond the size
// of that, §6.4 says the file is the truth and every surface is a projection
// of it: a copy of the values in the renderer would be a second truth that
// drifts the moment somebody edits the file on disk.
//
// RelPath is what api.request.send's envRelPath names; Name is the name the
// FILE declares, which is a label here and is never sent back — the binding
// key's environment is read out of the file at send time (capability.
// SendInputs), and a renderer that supplied it would be the second answer to
// "which environment is this" that this path must not have.
type apiEnvironmentRefWire struct {
	RelPath string `json:"relPath"`
	Name    string `json:"name"`
}

type apiRequestRefWire struct {
	RelPath string `json:"relPath"`
	Name    string `json:"name"`
	Method  string `json:"method"`
}

type apiMalformedRefWire struct {
	RelPath string `json:"relPath"`
	Reason  string `json:"reason"`
}

type apiEnvironmentReadResponse struct {
	Environment apiEnvironmentWire `json:"environment"`
}

// apiEnvironmentWire is ONE environment whole: what it is called, what it
// answers, which of its variables are secret by name, and how a request
// under it gets there.
//
// It carries the values that apiEnvironmentRefWire deliberately does not,
// and the difference is the question being asked. The listing names
// environments so a person can CHOOSE one, and a copy of every address in
// every environment would be a second truth beside the files (§6.4). This is
// the editor's read of ONE file, taken at the moment it is opened for
// editing and written straight back — so the copy lives exactly as long as
// the ask that made it.
//
// SecretVars holds NAMES and never values, which is §8 restated in a field
// list: there is no field here in which a secret or an identifier for one
// can be spelled, so the wire cannot carry one in either direction.
type apiEnvironmentWire struct {
	Name string `json:"name"`
	// Values is never nil — an environment that declares none is {}, because
	// the renderer's first Object.entries on a null throws.
	Values map[string]string `json:"values"`
	// SecretVars is never nil — none is [].
	SecretVars []string     `json:"secretVars"`
	Route      apiRouteWire `json:"route"`
}

// apiRouteWire is the environment's answer to "how do I get there" (§6.5).
// ProfileID is the connection a `connection` route leases and is empty on a
// `direct` one — the two states the closed kind vocabulary already names.
type apiRouteWire struct {
	Kind      string `json:"kind"`
	ProfileID string `json:"profileId"`
	// InsecureTLS — the environment sends without verifying the server's
	// certificate. On the wire in BOTH directions: the editor writes it, and
	// a send REPORTS it, so a run that went out unverified says so on the
	// run rather than only in the file that caused it.
	InsecureTLS bool `json:"insecureTls"`
}

// apiRouteKinds is the closed set the wire declares, and it is apicoll's own
// constants rather than a second list of strings beside them.
var apiRouteKinds = []string{apicoll.RouteDirect, apicoll.RouteConnection}

// wireRoute renders a route for the wire, spelling the zero value out as
// `direct` — the same normalisation wireEnvironment does for the same
// reason: a renderer meets one spelling of one state.
func wireRoute(r apicoll.Route) apiRouteWire {
	kind := r.Kind
	if kind == "" {
		kind = apicoll.RouteDirect
	}
	return apiRouteWire{Kind: kind, ProfileID: r.ProfileID, InsecureTLS: r.InsecureTLS}
}

// storedRoute is wireRoute's inverse for a route that may be ABSENT: no
// route is the direct one, which is the same normalisation wireRoute writes
// in the other direction, so a renderer meets one spelling of one state
// whichever way the value is travelling.
//
// InsecureTLS is deliberately NOT carried. It is per-ENVIRONMENT on purpose
// (apicoll/collection.go:126) — a person turns it on for the dev environment
// and cannot thereby turn it on for production — and a fetch is not an
// environment. The import ask has no such control either, so a value here
// could only have been invented by a caller.
func storedRoute(w *apiRouteWire) apicoll.Route {
	if w == nil {
		return apicoll.Route{Kind: apicoll.RouteDirect}
	}
	return apicoll.Route{Kind: w.Kind, ProfileID: w.ProfileID}
}

// wireEnvironment renders one environment for the renderer, filling in the
// two collections the stored form is allowed to omit. A file may leave
// `values` and `secretVars` out entirely — `omitempty` is right for a file —
// and null is not what a panel can iterate.
func wireEnvironment(env apicoll.Environment) apiEnvironmentWire {
	values := env.Values
	if values == nil {
		values = map[string]string{}
	}
	secrets := env.SecretVars
	if secrets == nil {
		secrets = []string{}
	}
	return apiEnvironmentWire{
		Name:       env.Name,
		Values:     values,
		SecretVars: secrets,
		Route:      wireRoute(env.Route),
	}
}

// storedEnvironment is the other direction: what the file will hold. The
// empty map and the empty slice go back to nil so the stored form is the one
// `omitempty` produces — the same normalisation storedRequest states, and
// for the same reason: one canonical file whoever wrote it.
func storedEnvironment(w apiEnvironmentWire) apicoll.Environment {
	env := apicoll.Environment{
		Name: w.Name,
		Route: apicoll.Route{
			Kind:        w.Route.Kind,
			ProfileID:   w.Route.ProfileID,
			InsecureTLS: w.Route.InsecureTLS,
		},
	}
	if len(w.Values) > 0 {
		env.Values = w.Values
	}
	if len(w.SecretVars) > 0 {
		env.SecretVars = w.SecretVars
	}
	return env
}

type apiRequestReadResponse struct {
	Request apiRequestWire `json:"request"`
}

type apiRequestWire struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Method  string          `json:"method"`
	URL     string          `json:"url"`
	Headers []apiHeaderWire `json:"headers"`
	Query   []apiParamWire  `json:"query"`
	// Variables are the request's own. Never null on the wire, like every
	// other list here: the file may omit the key — `omitempty` is right for
	// a file — and the renderer's first .map on a null throws.
	Variables []apiParamWire `json:"variables"`
	Body      apiBodyWire    `json:"body"`
	Auth      apiAuthWire    `json:"auth"`
}

type apiHeaderWire struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Enabled bool   `json:"enabled"`
}

type apiParamWire struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Enabled bool   `json:"enabled"`
}

type apiBodyWire struct {
	Kind    string `json:"kind"`
	Text    string `json:"text"`
	FileRef string `json:"fileRef"`
}

// apiAuthWire is TEXT like every other field the wire carries: the token,
// the password and the username are what the file holds — a literal the
// person typed, or a `{{variable}}` reference — never an identifier for a
// stored secret. Design §8 still holds: there is no syntax in which a file
// names a secret, and the binding from a name to a stored value lives in
// the binding document, nowhere in this request.
type apiAuthWire struct {
	Kind string `json:"kind"`
	User string `json:"user"`
	// Token is the bearer token or the api-key value.
	Token string `json:"token"`
	// Password is the basic-auth password.
	Password string `json:"password"`
}

// apiSendResponse is ONE EXCHANGE — what was attempted, how far it got, and
// what came back if anything did. It is not "the response", and the field
// list is where that stops being a slogan: `request`, `remoteAddr`,
// `timings` and `certificates` are on the exchange rather than inside
// `response`, so a run that never got an answer still carries every one of
// them that the attempt reached.
type apiSendResponse struct {
	// Outcome is the one field that says how the rest reads: `answered`,
	// `failed` or `stopped`.
	Outcome string `json:"outcome"`
	// Request is what went out, segmented — present whatever the outcome,
	// because apisend composes it and places its spans before it dials.
	Request apiRawWire `json:"request"`
	// Response is null unless the outcome is `answered`. A POINTER and not
	// a value: a zeroed response marshals as an HTTP 0 with an empty body,
	// which the renderer cannot tell from a real one.
	Response *apiSendResponseWire `json:"response"`
	// Failure is null exactly when the outcome is `answered` — present for
	// a stop as well as for a failure, because "how far did it get" is a
	// question a stop answers too.
	Failure *apiSendFailureWire `json:"failure"`
	// RemoteAddr is what answered the dial, "" when nothing did.
	RemoteAddr string `json:"remoteAddr"`
	// DNSAddresses is what the resolver answered for the host, in its own
	// order. Never null — the schema says [] for "no lookup" and for "the
	// lookup failed" alike.
	DNSAddresses []string `json:"dnsAddresses"`
	// Timings are the phases as far as the attempt got; one never reached
	// is 0.
	Timings apiTimingsWire `json:"timings"`
	// Certificates is the chain the server presented, leaf first, described
	// by the side that saw the bytes — never DER for the renderer to parse
	// (apisend.Certificate says why). Never null.
	Certificates []apiCertificateWire `json:"certificates"`
	// Environment is the NAME of the environment the exchange went out
	// under, as the file declares it, and "" when none was named. It is the
	// backend's own account rather than an echo of the caller's envRelPath:
	// the snapshot reads the name off the same record it took the address
	// and the route from, so this is what says WHICH record answered. A run
	// list drawn from what the renderer believed it asked for would be the
	// vault.status defect in reverse.
	Environment string `json:"environment"`
	// Route is HOW this exchange got there — the route the snapshot took
	// off the same record the address came from (§6.5), never an echo of
	// anything the caller sent. A run that said only which environment it
	// used would leave the panel unable to answer the question this whole
	// feature exists for: did this request leave from THIS machine, or
	// through the connection the environment names?
	//
	// The profile ID and not a name: an id is a fact this layer holds, and
	// the name belongs to whoever owns connections (AD-8). The renderer
	// already has that list and turns one into the other for display.
	Route apiRouteWire `json:"route"`
}

// apiSendResponseWire is what CAME BACK, and only that. Everything an
// attempt reaches whether or not it is answered has moved up to the
// exchange; what is left here has no meaning without a response behind it.
type apiSendResponseWire struct {
	Status  int             `json:"status"`
	Headers []apiHeaderWire `json:"headers"`
	// Text is empty when Binary. Binary, Lossy and Truncated are three
	// separate facts because they are three separate sentences in the run,
	// and collapsing any two of them loses one (apisend.Response).
	Text       string `json:"text"`
	Binary     bool   `json:"binary"`
	Lossy      bool   `json:"lossy"`
	Truncated  bool   `json:"truncated"`
	Size       int64  `json:"size"`
	TLSVersion string `json:"tlsVersion"`
	// TLSCipherSuite is the negotiated suite, "" off TLS.
	TLSCipherSuite string `json:"tlsCipherSuite"`
	// Trust is what VERIFICATION says about the chain that was accepted —
	// never the environment's setting, which is what apiRouteWire's
	// InsecureTLS is. Beside the version and the suite because it is the
	// same kind of fact: a completed handshake's.
	Trust apiTrustWire `json:"trust"`
	// Raw is the RESPONSE SIDE of the segmented text. The request side is
	// on the exchange, because the sender has it before it dials
	// (apisend/spans.go).
	Raw apiRawWire `json:"raw"`
}

// apiTrustWire is the verifier's answer, as a closed state plus its own
// sentence. A state rather than a boolean: "verification was off" and
// "something untrusted was accepted" are different facts, and a surface
// given one flag drew the warning for both.
type apiTrustWire struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

// apiSendFailureWire is how an attempt ended when it did not answer: WHERE
// it stopped, as a closed vocabulary the renderer picks its own sentence
// from, and the backend's own words for what went wrong.
type apiSendFailureWire struct {
	Phase  string `json:"phase"`
	Reason string `json:"reason"`
}

// apiCertificateWire is one certificate of the presented chain, in the fields
// a person reads when deciding whether to trust it. Strings, all of them: the
// renderer does not parse X.509 and must not learn to.
type apiCertificateWire struct {
	Subject     string   `json:"subject"`
	Issuer      string   `json:"issuer"`
	NotBefore   string   `json:"notBefore"`
	NotAfter    string   `json:"notAfter"`
	DNSNames    []string `json:"dnsNames"`
	IPAddresses []string `json:"ipAddresses"`
	SelfSigned  bool     `json:"selfSigned"`
	Fingerprint string   `json:"fingerprint"`
}

// wireCertificates renders the chain, filling in the two lists the domain
// type is allowed to leave nil — the renderer's first .map on a null throws.
func wireCertificates(in []apisend.Certificate) []apiCertificateWire {
	out := make([]apiCertificateWire, 0, len(in))
	for _, c := range in {
		names := c.DNSNames
		if names == nil {
			names = []string{}
		}
		ips := c.IPAddresses
		if ips == nil {
			ips = []string{}
		}
		out = append(out, apiCertificateWire{
			Subject:     c.Subject,
			Issuer:      c.Issuer,
			NotBefore:   c.NotBefore,
			NotAfter:    c.NotAfter,
			DNSNames:    names,
			IPAddresses: ips,
			SelfSigned:  c.SelfSigned,
			Fingerprint: c.Fingerprint,
		})
	}
	return out
}

// apiRawWire and apiRawSpanWire carry §11's segmented text. A secret's
// VALUE is in neither: the bytes are elided by the sender and a placeholder
// naming the secret takes their place, so this side has nothing to redact
// and no field in which a value could ride.
type apiRawWire struct {
	Text string `json:"text"`
	// Spans is never null: a renderer walking null is a crash rather than
	// an empty view, which is why the schema says [].
	Spans []apiRawSpanWire `json:"spans"`
}

type apiRawSpanWire struct {
	From   int    `json:"from"`
	To     int    `json:"to"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Damage string `json:"damage"`
}

// apiTimingsWire carries milliseconds. A Go duration has no JSON form, and
// milliseconds is what the run shows; a float keeps sub-millisecond phases
// from all reading as zero on a fast loopback exchange.
type apiTimingsWire struct {
	DNSMs     float64 `json:"dnsMs"`
	ConnectMs float64 `json:"connectMs"`
	TLSMs     float64 `json:"tlsMs"`
	TTFBMs    float64 `json:"ttfbMs"`
	TotalMs   float64 `json:"totalMs"`
}

type apiImportPostmanResponse struct {
	Unsupported []apiUnsupportedWire `json:"unsupported"`
}

type apiImportCurlResponse struct {
	Request     apiRequestWire       `json:"request"`
	Unsupported []apiUnsupportedWire `json:"unsupported"`
}

// apiUnsupportedWire is one thing the import did not carry over. What names
// the feature and NEVER an argument's value — a refused --oauth2-bearer
// would otherwise itemise the token it refused (apiimport.Unsupported).
type apiUnsupportedWire struct {
	What string `json:"what"`
	Why  string `json:"why"`
}

// ── domain ⇄ wire ────────────────────────────────────────────────────────

// wireOpenCollections renders the opened-folder list, reading each folder's
// environments as it goes.
//
// A folder whose environments will not read does NOT take the listing down,
// and it does not silently list as a collection with no environments either:
// the reason joins that one entry's Error, which is the field this listing
// already has for "this one folder is in trouble" (capability.OpenCollection).
// A dead folder hiding every live one is the defect that field exists to
// prevent, and a soft degrade the UI cannot see is the one AGENTS.md names.
func wireOpenCollections(svc capability.APICollectionService, open []capability.OpenCollection, defaultRoot string) apiCollectionsListResponse {
	out := make([]apiOpenCollectionWire, 0, len(open))
	for _, c := range open {
		row := apiOpenCollectionWire{
			Handle: string(c.Handle),
			Path:   c.Path,
		}
		if c.Err != nil {
			row.Error = c.Err.Error()
		}
		wire, err := wireCollectionOf(svc, c.Handle, c.Collection)
		if err != nil {
			// The requests half may still have read; render what there is
			// and say what did not. joinReasons keeps both sentences when
			// the folder had already reported one.
			wire = wireCollection(c.Collection, nil, nil)
			row.Error = joinReasons(row.Error, err.Error())
		}
		row.Collection = wire
		out = append(out, row)
	}
	return apiCollectionsListResponse{Collections: out, DefaultRoot: defaultRoot}
}

// joinReasons puts two failures in one sentence, and answers the one that
// exists when only one does. A folder can be in trouble twice.
func joinReasons(first, second string) string {
	switch {
	case first == "":
		return second
	case second == "":
		return first
	default:
		return first + "; " + second
	}
}

// wireCollectionOf is the ONE assembler every api.collections.* result goes
// through: it reads the folder's environments and hands the whole collection
// to wireCollection. list, open and create all call it, so the three results
// cannot disagree about what a collection is — which is exactly what the
// create schema means by "the shape is api.collections.open's on purpose".
//
// The read happens inside the operation's callback, so it is under the same
// api-gate hold as the listing or the open it belongs to: one folder, one
// moment, one answer.
func wireCollectionOf(svc capability.APICollectionService, h apicoll.HandleID, c apicoll.Collection) (apiCollectionWire, error) {
	envs, bad, err := svc.ListEnvironments(h)
	if err != nil {
		return apiCollectionWire{}, err
	}
	return wireCollection(c, envs, bad), nil
}

// wireCollection turns one collection plus its environments into the wire
// shape. badEnvs joins the collection's own malformed list rather than
// getting a list of its own: the renderer's question is "which files in this
// folder could not be read", and it is one question.
func wireCollection(c apicoll.Collection, envs []apicoll.EnvironmentRef, badEnvs []apicoll.MalformedRef) apiCollectionWire {
	reqs := make([]apiRequestRefWire, 0, len(c.Requests))
	for _, r := range c.Requests {
		reqs = append(reqs, apiRequestRefWire{RelPath: r.RelPath, Name: r.Name, Method: r.Method})
	}
	mal := make([]apiMalformedRefWire, 0, len(c.Malformed)+len(badEnvs))
	for _, m := range c.Malformed {
		mal = append(mal, apiMalformedRefWire{RelPath: m.RelPath, Reason: m.Reason})
	}
	for _, m := range badEnvs {
		mal = append(mal, apiMalformedRefWire{RelPath: m.RelPath, Reason: m.Reason})
	}
	// The NAME off the file and the path that addresses it — and nothing
	// else off EnvironmentRef, which also holds every value the environment
	// declares.
	out := make([]apiEnvironmentRefWire, 0, len(envs))
	for _, e := range envs {
		out = append(out, apiEnvironmentRefWire{RelPath: e.RelPath, Name: e.Environment.Name})
	}
	folders := c.Folders
	if folders == nil {
		// apicoll already answers [] rather than null; this is the wire's
		// own guarantee rather than a trust in the layer below, because the
		// renderer's first .map on a null throws.
		folders = []string{}
	}
	variableFolders := c.VariableFolders
	if variableFolders == nil {
		variableFolders = []string{}
	}
	return apiCollectionWire{
		Name:            c.Name,
		Requests:        reqs,
		Folders:         folders,
		VariableFolders: variableFolders,
		Malformed:       mal,
		Environments:    out,
	}
}

// apiBodyKinds and apiAuthKinds are the closed sets the wire declares, and
// they are apicoll's own constants rather than a second list of strings
// beside them.
var (
	apiBodyKinds = []string{
		apicoll.BodyNone,
		apicoll.BodyRaw,
		apicoll.BodyJSON,
		apicoll.BodyForm,
		apicoll.BodyFile,
	}
	apiAuthKinds = []string{apicoll.AuthNone, apicoll.AuthBearer, apicoll.AuthBasic, apicoll.AuthAPIKey}
)

// apiWireKind maps a stored request kind onto the wire's closed set. Named
// for its domain because ws_files.go already owns a wireKind, and that one
// maps a filesystem entry kind — a different concept that happens to share
// the word.
//
// The empty string is the ZERO VALUE and means "none". Two producers hand
// it over: a request file that simply omits the auth object, and
// apiimport.FromCurl for a line carrying no credential. Spelling that ""
// on the wire would put two spellings of one state in front of the
// renderer, which is the shape a closed enum exists to prevent.
//
// Anything else is REFUSED rather than mapped onto the nearest thing. A
// collection folder can arrive from a pull request, and an auth kind of
// "Bearer" quietly read as "none" would send the request unauthenticated —
// a plausible-looking request that teaches the wrong lesson about why it
// was rejected, which is exactly what design §6.5 refuses to do with an
// unresolved variable.
func apiWireKind(field, kind, zero string, allowed []string) (string, error) {
	if kind == "" {
		return zero, nil
	}
	for _, a := range allowed {
		if kind == a {
			return kind, nil
		}
	}
	return "", fmt.Errorf("%w: %s is %q, which is not a kind this build knows",
		apicoll.ErrMalformedRequest, field, kind)
}

func wireRequest(r apicoll.Request) (apiRequestWire, error) {
	bodyKind, err := apiWireKind("body.kind", r.Body.Kind, apicoll.BodyNone, apiBodyKinds)
	if err != nil {
		return apiRequestWire{}, err
	}
	authKind, err := apiWireKind("auth.kind", r.Auth.Kind, apicoll.AuthNone, apiAuthKinds)
	if err != nil {
		return apiRequestWire{}, err
	}
	return apiRequestWire{
		ID:        r.ID,
		Name:      r.Name,
		Method:    r.Method,
		URL:       r.URL,
		Headers:   wireHeaders(r.Headers),
		Query:     wireParams(r.Query),
		Variables: wireParams(r.Variables),
		Auth:      apiAuthWire{Kind: authKind, User: r.Auth.User, Token: r.Auth.Token, Password: r.Auth.Password},
		Body:      apiBodyWire{Kind: bodyKind, Text: r.Body.Text, FileRef: r.Body.FileRef},
	}, nil
}

// storedRequest is the other direction: what api.request.write puts on disk.
func storedRequest(r apiRequestWire) apicoll.Request {
	headers := make([]apicoll.Header, 0, len(r.Headers))
	for _, h := range r.Headers {
		headers = append(headers, apicoll.Header{Name: h.Name, Value: h.Value, Enabled: h.Enabled})
	}
	query := make([]apicoll.Param, 0, len(r.Query))
	for _, q := range r.Query {
		query = append(query, apicoll.Param{Name: q.Name, Value: q.Value, Enabled: q.Enabled})
	}
	variables := make([]apicoll.Param, 0, len(r.Variables))
	for _, v := range r.Variables {
		variables = append(variables, apicoll.Param{Name: v.Name, Value: v.Value, Enabled: v.Enabled})
	}
	return apicoll.Request{
		ID:        r.ID,
		Name:      r.Name,
		Method:    r.Method,
		URL:       r.URL,
		Headers:   headers,
		Query:     query,
		Variables: variables,
		Body:      apicoll.Body{Kind: r.Body.Kind, Text: r.Body.Text, FileRef: r.Body.FileRef},
		Auth:      apicoll.Auth{Kind: r.Auth.Kind, User: r.Auth.User, Token: r.Auth.Token, Password: r.Auth.Password},
	}
}

func wireHeaders(hs []apicoll.Header) []apiHeaderWire {
	out := make([]apiHeaderWire, 0, len(hs))
	for _, h := range hs {
		out = append(out, apiHeaderWire{Name: h.Name, Value: h.Value, Enabled: h.Enabled})
	}
	return out
}

func wireParams(ps []apicoll.Param) []apiParamWire {
	out := make([]apiParamWire, 0, len(ps))
	for _, p := range ps {
		out = append(out, apiParamWire{Name: p.Name, Value: p.Value, Enabled: p.Enabled})
	}
	return out
}

// wireExchange renders one exchange for the renderer. It is the ONE place a
// send result is built, so the contract's invariants — a request block
// whatever the outcome, a never-null certificate list, `response` and
// `failure` mutually exclusive — hold here or nowhere.
// neverNull is the wire's rule for a list, applied where a Go nil would
// otherwise marshal as `null`: the schema says [] for an empty answer, and a
// renderer that has to check for null is a renderer that will forget once.
// The same defect shipped before in vault.status's `providers`.
func neverNull(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func wireExchange(ex apisend.Exchange, environment string, route apicoll.Route) apiSendResponse {
	out := apiSendResponse{
		Outcome:      string(ex.Outcome),
		Request:      wireRaw(ex.Request),
		RemoteAddr:   ex.RemoteAddr,
		DNSAddresses: neverNull(ex.DNSAddresses),
		Timings:      wireTimings(ex.Timings),
		Certificates: wireCertificates(ex.Certificates),
		Environment:  environment,
		Route:        wireRoute(route),
	}
	if ex.Response != nil {
		out.Response = wireSendResponse(*ex.Response)
	}
	if ex.Failure != nil {
		out.Failure = &apiSendFailureWire{
			Phase:  string(ex.Failure.Phase),
			Reason: ex.Failure.Reason,
		}
	}
	return out
}

func wireSendResponse(r apisend.Response) *apiSendResponseWire {
	return &apiSendResponseWire{
		Status:         r.Status,
		Headers:        wireHeaders(r.Headers),
		Text:           r.Text,
		Binary:         r.Binary,
		Lossy:          r.Lossy,
		Truncated:      r.Truncated,
		Size:           r.Size,
		TLSVersion:     r.TLSVersion,
		TLSCipherSuite: r.TLSCipherSuite,
		Trust:          apiTrustWire{State: string(r.Trust.State), Reason: r.Trust.Reason},
		Raw:            wireRaw(r.Raw),
	}
}

// wireTimings carries milliseconds. A Go duration has no JSON form, and a
// float keeps sub-millisecond phases from all reading as zero on a fast
// loopback exchange.
func wireTimings(t apisend.Timings) apiTimingsWire {
	return apiTimingsWire{
		DNSMs:     float64(t.DNS.Microseconds()) / 1000,
		ConnectMs: float64(t.Connect.Microseconds()) / 1000,
		TLSMs:     float64(t.TLS.Microseconds()) / 1000,
		TTFBMs:    float64(t.TTFB.Microseconds()) / 1000,
		TotalMs:   float64(t.Total.Microseconds()) / 1000,
	}
}

func wireRaw(raw apisend.Raw) apiRawWire {
	spans := make([]apiRawSpanWire, 0, len(raw.Spans))
	for _, s := range raw.Spans {
		spans = append(spans, apiRawSpanWire{
			From: s.From, To: s.To, Kind: s.Kind, Name: s.Name, Damage: s.Damage,
		})
	}
	return apiRawWire{Text: raw.Text, Spans: spans}
}

func wireUnsupported(us []apiimport.Unsupported) []apiUnsupportedWire {
	out := make([]apiUnsupportedWire, 0, len(us))
	for _, u := range us {
		out = append(out, apiUnsupportedWire{What: u.What, Why: u.Why})
	}
	return out
}

// ── ingress bounds and validators ────────────────────────────────────────

const (
	// maxAPIHandleRunes bounds a collection handle before it is looked up.
	// apicoll mints 32 lowercase hex characters; the shape check is
	// isLowerHex, which every backend-minted id on this control plane gets
	// (files.*, git.*), and this is the bound that stops an enormous string
	// reaching it.
	maxAPIHandleRunes = 128
	// maxAPICurlLineRunes bounds a pasted curl line. apiimport is the owner
	// of the refusal — its tokenizer caps the line at 1 MiB — and this is
	// the wire-cost ceiling before the parse, at the same order of
	// magnitude so nothing legitimate sits between them.
	maxAPICurlLineRunes = 1 << 20
	// maxAPIImportDocumentRunes bounds a Postman export carried INLINE, as
	// the `document` parameter of api.import.postman. It is deliberately
	// the same 1 MiB as maxAPICurlLineRunes above, and the two are ONE
	// decision stated twice rather than two opinions: this is what a text
	// parameter may cost this control plane, whether the text is a curl
	// line somebody pasted or an export the renderer read.
	//
	// Over the bound the import still happens — by `path`, which names the
	// file on the machine running Go and carries no bytes over the socket
	// at all — and the refusal says so rather than leaving a person with an
	// export and nowhere to put it. apiimport's own 16 MiB cap
	// (maxDocumentBytes) is a different bound in a different place: it
	// governs a document already being read, however it arrived.
	maxAPIImportDocumentRunes = 1 << 20
	// maxAPIBodyTextRunes bounds a request body written back through
	// api.request.write. A body too large for a line lives in a file that
	// the request NAMES (§6.4), so an inline body is a phrase a person
	// typed, generously bounded.
	maxAPIBodyTextRunes = 1 << 18
	// maxAPITokenRunes bounds the client-minted token that names one
	// running exchange. The renderer sends a UUID; this is generous enough
	// for any identifier a caller might reasonably choose and far below
	// anything that makes a map key expensive.
	maxAPITokenRunes = 128
	// maxAPISecretValueRunes bounds a secret value on its way in. Generous:
	// a bearer token, a signed JWT and a PEM private key all fit, and the
	// bound is a wire-cost ceiling rather than an opinion about what a
	// credential may look like.
	maxAPISecretValueRunes = 1 << 14
	// maxAPIRequestRows bounds how many header and query rows one request
	// may carry. Each row is bounded individually; this bounds the count,
	// which is the other half of the per-call work bound.
	maxAPIRequestRows = 512
)

// decodeAPIParams decodes an api.* params object STRICTLY: a field this
// method does not declare is REFUSED, not ignored.
//
// This is what makes design §13.1 enforceable. Opening a collection mints a
// backend-held handle and `root` is never accepted again — but a tolerant
// decoder would silently drop a `path` bolted onto api.request.read, which
// reads identically from the renderer and leaves "never accepted" as a habit
// somebody has to keep rather than a property of the surface. The strictness
// is recursive, which is also right: the persisted request format is decoded
// strictly on disk too (apicoll's decodeStrict), so a request arriving over
// the wire with a field the format does not declare is refused at both ends.
func decodeAPIParams(raw json.RawMessage, dst any) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return ""
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return "params must be a JSON object carrying only this method's own fields"
	}
	return ""
}

// validateAPIHandle checks a renderer-supplied collection handle. The
// service is the authority on whether a handle is still open; this is the
// wire check before the lookup, and it is the same one every other
// backend-minted 32-hex id on this control plane gets.
func validateAPIHandle(handle string) string {
	if msg := boundedRunes("handle", handle, maxAPIHandleRunes); msg != "" {
		return msg
	}
	if !isLowerHex(handle, 32) {
		return "handle is required and must be the 32-hex id the backend minted"
	}
	return ""
}

// validateAPIRelPath bounds a path INSIDE a collection. Whether it escapes
// the root, names a request file, or resolves through a symlink is
// apicoll's rule and stays there (ErrPathOutsideCollection,
// ErrNotARequestPath) — it needs the folder, which a wire validator does not
// have and must not read. A second copy of that rule here would be the two
// derivations of one fact AGENTS.md is written against.
func validateAPIRelPath(relPath string) string {
	if relPath == "" {
		return "relPath is required"
	}
	return boundedRunes("relPath", relPath, maxPathRunes)
}

// validateAPIFolderPath checks a path the user chose — a collection root or
// an import destination. Absolute and clean, the same contract the files.*
// domain applies, because a relative path here would be resolved against
// whatever directory the backend happened to be started in.
func validateAPIFolderPath(path, what string) string {
	return validateFSPath(path, what)
}

func validateAPICollectionsOpenRaw(raw json.RawMessage) string {
	var p apiCollectionsOpenParams
	if msg := decodeAPIParams(raw, &p); msg != "" {
		return msg
	}
	return validateAPIFolderPath(p.Path, "path")
}

func validateAPIHandleRaw(raw json.RawMessage) string {
	var p apiHandleParams
	if msg := decodeAPIParams(raw, &p); msg != "" {
		return msg
	}
	return validateAPIHandle(p.Handle)
}

// validateAPICollectionsCreateRaw bounds the NAME of a new collection.
// Whether the name is a name at all — not a path, not `.`, not `..`, not
// longer than a filesystem component — is apicoll's rule and stays there
// (validateCollectionName): a second copy here would be two derivations of
// one fact. This is the wire-cost ceiling before the call, and it is the
// same bound every other user-typed name on this control plane gets.
func validateAPICollectionsCreateRaw(raw json.RawMessage) string {
	var p apiCollectionsCreateParams
	if msg := decodeAPIParams(raw, &p); msg != "" {
		return msg
	}
	if p.Name == "" {
		return "name is required"
	}
	return boundedRunes("name", p.Name, maxConfigNameRunes)
}

// validateAPICollectionsCreateFolderRaw bounds the wire cost of a new
// folder's name and of the parent that holds it. Whether the name is one
// path component, and whether the parent is inside the collection, are
// apicoll's rules and stay there (validateComponentName, validateFolderPath)
// — a second copy here would be two derivations of one fact, and the two
// would agree until somebody widened only one.
func validateAPICollectionsCreateFolderRaw(raw json.RawMessage) string {
	var p apiCollectionsCreateFolderParams
	if msg := decodeAPIParams(raw, &p); msg != "" {
		return msg
	}
	if msg := validateAPIHandle(p.Handle); msg != "" {
		return msg
	}
	if p.Name == "" {
		return "name is required"
	}
	if msg := boundedRunes("name", p.Name, maxConfigNameRunes); msg != "" {
		return msg
	}
	// "" is the collection root, which is where a folder with no parent
	// chosen goes — so an absent parent is not a missing parameter.
	if p.ParentRelPath == "" {
		return ""
	}
	return boundedRunes("parentRelPath", p.ParentRelPath, maxPathRunes)
}

// validateAPIRequestSendRaw is validateAPIRequestRaw plus the optional
// environment path. The environment is bounded like every other path inside
// a collection; whether it names an environment file is apicoll's rule.
func validateAPIRequestSendRaw(raw json.RawMessage) string {
	var p apiRequestSendParams
	if msg := decodeAPIParams(raw, &p); msg != "" {
		return msg
	}
	if msg := validateAPIHandle(p.Handle); msg != "" {
		return msg
	}
	if msg := validateAPIRelPath(p.RelPath); msg != "" {
		return msg
	}
	if msg := validateAPISendToken(p.Token); msg != "" {
		return msg
	}
	if p.EnvRelPath == "" {
		return ""
	}
	return boundedRunes("envRelPath", p.EnvRelPath, maxPathRunes)
}

func validateAPIRequestCancelRaw(raw json.RawMessage) string {
	var p apiRequestCancelParams
	if msg := decodeAPIParams(raw, &p); msg != "" {
		return msg
	}
	return validateAPISendToken(p.Token)
}

// validateAPISendToken bounds the caller's token. Presence and a length,
// and deliberately no SHAPE: this is the one identifier on the api surface
// the CLIENT mints, so a spelling rule here would be a format the renderer
// has to satisfy for no gain — the backend only ever uses it as a map key.
// The bound is what stops an enormous string reaching that map.
func validateAPISendToken(token string) string {
	if token == "" {
		return "token is required"
	}
	return boundedRunes("token", token, maxAPITokenRunes)
}

// validateAPIEnvironmentBindSecretRaw bounds the one params object that
// carries a credential.
//
// The VALUE is bounded and never otherwise inspected: its shape is the
// user's business and a validator that said anything about it — a pattern, a
// character class — would be this layer forming an opinion about a
// credential. An empty one is refused, because "bind nothing" is not a
// gesture anybody makes and would leave a variable resolving to the empty
// string, which is the silent degrade §6.5 exists against.
func validateAPIEnvironmentBindSecretRaw(raw json.RawMessage) string {
	var p apiEnvironmentBindSecretParams
	if msg := decodeAPIParams(raw, &p); msg != "" {
		return msg
	}
	if msg := validateAPIHandle(p.Handle); msg != "" {
		return msg
	}
	if msg := validateAPIRelPath(p.RelPath); msg != "" {
		return msg
	}
	if p.Variable == "" {
		return "variable is required"
	}
	if msg := boundedRunes("variable", p.Variable, maxConfigNameRunes); msg != "" {
		return msg
	}
	if p.Value == "" {
		return "value is required"
	}
	// The message says the LIMIT and never the length it saw, which would be
	// a fact about the credential.
	if utf8.RuneCountInString(p.Value) > maxAPISecretValueRunes {
		return fmt.Sprintf("value exceeds %d characters", maxAPISecretValueRunes)
	}
	return ""
}

func validateAPIEnvironmentRaw(raw json.RawMessage) string {
	var p apiEnvironmentParams
	if msg := decodeAPIParams(raw, &p); msg != "" {
		return msg
	}
	if msg := validateAPIHandle(p.Handle); msg != "" {
		return msg
	}
	return validateAPIRelPath(p.RelPath)
}

func validateAPIEnvironmentWriteRaw(raw json.RawMessage) string {
	var p apiEnvironmentWriteParams
	if msg := decodeAPIParams(raw, &p); msg != "" {
		return msg
	}
	if msg := validateAPIHandle(p.Handle); msg != "" {
		return msg
	}
	if msg := validateAPIRelPath(p.RelPath); msg != "" {
		return msg
	}
	return validateAPIEnvironmentBody(p.Environment)
}

// validateAPIEnvironmentBody bounds every field of an environment being
// written. Each one reaches something real: a value is substituted into the
// URL that gets dialled, a secret name becomes a binding key, and the route
// decides which machine the request leaves from.
func validateAPIEnvironmentBody(e apiEnvironmentWire) string {
	if msg := boundedRunes("environment.name", e.Name, maxConfigNameRunes); msg != "" {
		return msg
	}
	if len(e.Values) > maxAPIRequestRows {
		return fmt.Sprintf("environment.values exceeds %d rows", maxAPIRequestRows)
	}
	for name, value := range e.Values {
		if msg := boundedRunes("environment.values.name", name, maxHeaderNameRunes); msg != "" {
			return msg
		}
		if msg := boundedRunes("environment.values.value", value, maxHeaderValueRunes); msg != "" {
			return msg
		}
	}
	if len(e.SecretVars) > maxAPIRequestRows {
		return fmt.Sprintf("environment.secretVars exceeds %d rows", maxAPIRequestRows)
	}
	for _, name := range e.SecretVars {
		if msg := boundedRunes("environment.secretVars", name, maxHeaderNameRunes); msg != "" {
			return msg
		}
	}
	if !slices.Contains(apiRouteKinds, e.Route.Kind) {
		return fmt.Sprintf("environment.route.kind must be one of %v", apiRouteKinds)
	}
	return boundedRunes("environment.route.profileId", e.Route.ProfileID, maxConfigIDRunes)
}

func validateAPIRequestRaw(raw json.RawMessage) string {
	var p apiRequestParams
	if msg := decodeAPIParams(raw, &p); msg != "" {
		return msg
	}
	if msg := validateAPIHandle(p.Handle); msg != "" {
		return msg
	}
	return validateAPIRelPath(p.RelPath)
}

func validateAPIRequestWriteRaw(raw json.RawMessage) string {
	var p apiRequestWriteParams
	if msg := decodeAPIParams(raw, &p); msg != "" {
		return msg
	}
	if msg := validateAPIHandle(p.Handle); msg != "" {
		return msg
	}
	if msg := validateAPIRelPath(p.RelPath); msg != "" {
		return msg
	}
	return validateAPIRequestBody(p.Request)
}

func validateAPIRequestMoveRaw(raw json.RawMessage) string {
	var p apiRequestMoveParams
	if msg := decodeAPIParams(raw, &p); msg != "" {
		return msg
	}
	if msg := validateAPIHandle(p.Handle); msg != "" {
		return msg
	}
	if msg := validateAPIRelPath(p.RelPath); msg != "" {
		return msg
	}
	return validateAPIRelPath(p.ToRelPath)
}

// validateAPIRequestBody bounds every field of a request being written back.
// Each one reaches something real: the URL is dialled on the next send, the
// headers ride the request verbatim, the body is the payload.
func validateAPIRequestBody(r apiRequestWire) string {
	if msg := boundedRunes("request.id", r.ID, maxConfigIDRunes); msg != "" {
		return msg
	}
	if msg := boundedRunes("request.name", r.Name, maxConfigNameRunes); msg != "" {
		return msg
	}
	if msg := boundedRunes("request.method", r.Method, maxEnumRunes); msg != "" {
		return msg
	}
	if msg := boundedRunes("request.url", r.URL, maxEndpointURLRunes); msg != "" {
		return msg
	}
	if len(r.Headers) > maxAPIRequestRows {
		return fmt.Sprintf("request.headers exceeds %d rows", maxAPIRequestRows)
	}
	for _, h := range r.Headers {
		if msg := boundedRunes("request.headers.name", h.Name, maxHeaderNameRunes); msg != "" {
			return msg
		}
		if msg := boundedRunes("request.headers.value", h.Value, maxHeaderValueRunes); msg != "" {
			return msg
		}
	}
	if len(r.Query) > maxAPIRequestRows {
		return fmt.Sprintf("request.query exceeds %d rows", maxAPIRequestRows)
	}
	for _, q := range r.Query {
		if msg := boundedRunes("request.query.name", q.Name, maxHeaderNameRunes); msg != "" {
			return msg
		}
		if msg := boundedRunes("request.query.value", q.Value, maxHeaderValueRunes); msg != "" {
			return msg
		}
	}
	// The request's own variables get the same bounds the query rows get,
	// and for the same reason: a name reaches a substitution and a value
	// reaches the wire. A validator that bounded two of the three lists
	// would leave the newest one as the way in.
	if len(r.Variables) > maxAPIRequestRows {
		return fmt.Sprintf("request.variables exceeds %d rows", maxAPIRequestRows)
	}
	for _, v := range r.Variables {
		if msg := boundedRunes("request.variables.name", v.Name, maxConfigNameRunes); msg != "" {
			return msg
		}
		if msg := boundedRunes("request.variables.value", v.Value, maxHeaderValueRunes); msg != "" {
			return msg
		}
	}
	if msg := boundedRunes("request.body.kind", r.Body.Kind, maxEnumRunes); msg != "" {
		return msg
	}
	if msg := boundedRunes("request.body.text", r.Body.Text, maxAPIBodyTextRunes); msg != "" {
		return msg
	}
	if msg := boundedRunes("request.body.fileRef", r.Body.FileRef, maxPathRunes); msg != "" {
		return msg
	}
	if msg := boundedRunes("request.auth.kind", r.Auth.Kind, maxEnumRunes); msg != "" {
		return msg
	}
	// The token and password are now TEXT that reaches the wire, so they
	// take the same bound as a header value; only the user name stays a
	// name.
	if msg := boundedRunes("request.auth.token", r.Auth.Token, maxHeaderValueRunes); msg != "" {
		return msg
	}
	if msg := boundedRunes("request.auth.password", r.Auth.Password, maxHeaderValueRunes); msg != "" {
		return msg
	}
	return boundedRunes("request.auth.user", r.Auth.User, maxUserRunes)
}

// validateAPIImportPostmanRaw checks the import's two halves: WHICH export,
// and where it goes.
//
// The export is named by `path`, by `document` or by `url`, and several or
// none is refused BY NAME, naming all three. A precedence rule would be the
// cheaper code and the worse answer: a caller that sent two would have one
// of them silently do nothing, and would never learn which one the server
// ignored. The same reasoning is why `route` beside anything but `url` is
// refused rather than dropped — a parameter that quietly does nothing is
// worse than an error, because the caller believes it worked.
//
// There is deliberately NO length or shape check on `url` here. apifetch
// refuses a scheme it cannot GET by name and before any dial, and a second
// URL parser in the validator would be a second answer to "is this a URL",
// agreeing with the first everywhere anyone looked.
func validateAPIImportPostmanRaw(raw json.RawMessage) string {
	var p apiImportPostmanParams
	if msg := decodeAPIParams(raw, &p); msg != "" {
		return msg
	}
	named := 0
	for _, given := range []bool{p.Path != "", p.Document != "", p.URL != ""} {
		if given {
			named++
		}
	}
	switch {
	case named > 1:
		return "path, document and url are three routes to one import — path names the export on the machine running nocx, document carries its bytes, url says where to fetch it; give one of them, not several"
	case named == 0:
		return "an import needs the export: path, naming it on the machine running nocx, document, carrying its bytes, or url, naming where to fetch it"
	}
	if p.Route != nil {
		if p.URL == "" {
			return "route says how to REACH a url and means nothing beside path or document; give it with url or leave it out"
		}
		if !slices.Contains(apiRouteKinds, p.Route.Kind) {
			return fmt.Sprintf("route.kind must be one of %v", apiRouteKinds)
		}
	}
	if p.Document != "" {
		if n := utf8.RuneCountInString(p.Document); n > maxAPIImportDocumentRunes {
			return fmt.Sprintf(
				"document exceeds %d characters; an export this large is imported with path, which names the file on the machine running nocx and sends no bytes over this socket",
				maxAPIImportDocumentRunes)
		}
	}
	if p.Path != "" {
		if msg := validateAPIFolderPath(p.Path, "path"); msg != "" {
			return msg
		}
	}
	return validateAPIFolderPath(p.Dest, "dest")
}

func validateAPIImportCurlRaw(raw json.RawMessage) string {
	var p apiImportCurlParams
	if msg := decodeAPIParams(raw, &p); msg != "" {
		return msg
	}
	if p.Line == "" {
		return "line is required"
	}
	if utf8.RuneCountInString(p.Line) > maxAPICurlLineRunes {
		return fmt.Sprintf("line exceeds %d characters", maxAPICurlLineRunes)
	}
	return ""
}

// ── registration ─────────────────────────────────────────────────────────

// apiSpecs declares the api.* control methods. The two operations are built
// here from the wired seams (composition root for this domain).
//
// Three availability gates, because three things wire independently: the
// collection service backs api.collections.* and api.request.read/write; the
// sender additionally backs api.request.send; the binding store additionally
// backs api.import.postman, which answers -32601 without one rather than
// pretending it can put a secret somewhere. api.import.curl needs none of
// the three: it converts a line into a value.
//
// The binding document's READ half (s.apiVariables) is NOT a fourth gate.
// api.request.send answers with or without it; what changes is whether a
// request whose auth names a variable can resolve it. Without it every such
// send is refused as an unbound variable — which is the same answer a
// misspelled variable gets, and an honest one — so making the whole method
// disappear would report a missing binding store as a missing send.
func (s *WSServer) apiSpecs(lane control.Admission, apiGate, vaultGate control.Admission) []methodSpec {
	collWired := s.apiCollections != nil
	sendWired := collWired && s.apiSender != nil
	importWired := s.apiBindings != nil

	var collOp capability.APICollectionOperation
	if collWired {
		collOp = capability.NewAPICollectionOperation(apiGate, lane, s.apiCollections, s.apiVariables)
	}
	var importOp capability.APIImportOperation
	if importWired {
		// s.apiFetch may be nil, and that is a build without the URL
		// entrance rather than a broken one: the other two entrances are
		// untouched and `url` is refused by name.
		importOp = capability.NewAPIImportOperation(vaultGate, apiGate, lane, apiimport.NewOSFS(), s.apiBindings, s.apiFetch)
	}
	// The binding write shares the import's gates and its store: both put a
	// value in the vault and record it in the one binding document, so they
	// must exclude each other, and the vault gate is what makes them.
	var bindOp capability.APIBindingOperation
	if importWired {
		bindOp = capability.NewAPIBindingOperation(vaultGate, apiGate, lane, s.apiBindings)
	}

	sub := s.operationQueue("api")
	// api.request.cancel gets a submission OF ITS OWN, and that is the
	// point of it rather than tidiness. The api queue is what running sends
	// occupy, and a Stop that queued behind them would be a Stop that could
	// not reach the exchange it exists to end — worst exactly when a panel
	// is busiest. It touches no store, so it shares nothing that would make
	// a second queue unsafe.
	cancelSub := s.operationQueue("api-cancel")
	cancels := newSendCancels()
	collAvailable := func() bool { return collWired }
	collHandlers := func(r Responder) apiCollectionHandlers {
		return apiCollectionHandlers{
			op: collOp, sender: s.apiSender, values: s.apiVariables, bindOp: bindOp, r: r,
		}
	}
	// The sending half additionally needs to know WHICH WINDOW is asking:
	// a token is a name the renderer chose, and two windows may choose the
	// same one, so the registry is keyed by the connection as well.
	sendHandlers := func(w *wsConn, r Responder) apiCollectionHandlers {
		h := collHandlers(r)
		h.cancels = cancels
		h.conn = w.id
		return h
	}

	return []methodSpec{
		whenAvailable(regResponder(sub, "api.collections.list", noParams(), func(r Responder) handlerFunc {
			h := collHandlers(r)
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}), collAvailable, apiCollectionsUnavailable),
		whenAvailable(regResponder(sub, "api.collections.open", params(validateAPICollectionsOpenRaw), func(r Responder) handlerFunc {
			h := collHandlers(r)
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}), collAvailable, apiCollectionsUnavailable),
		whenAvailable(regResponder(sub, "api.collections.create", params(validateAPICollectionsCreateRaw), func(r Responder) handlerFunc {
			h := collHandlers(r)
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}), collAvailable, apiCollectionsUnavailable),
		whenAvailable(regResponder(sub, "api.collections.createFolder", params(validateAPICollectionsCreateFolderRaw), func(r Responder) handlerFunc {
			h := collHandlers(r)
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}), collAvailable, apiCollectionsUnavailable),
		whenAvailable(regResponder(sub, "api.collections.close", params(validateAPIHandleRaw), func(r Responder) handlerFunc {
			h := collHandlers(r)
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}), collAvailable, apiCollectionsUnavailable),
		whenAvailable(regResponder(sub, "api.environment.read", params(validateAPIEnvironmentRaw), func(r Responder) handlerFunc {
			h := collHandlers(r)
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}), collAvailable, apiCollectionsUnavailable),
		whenAvailable(regResponder(sub, "api.environment.write", params(validateAPIEnvironmentWriteRaw), func(r Responder) handlerFunc {
			h := collHandlers(r)
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}), collAvailable, apiCollectionsUnavailable),
		// Available only where the binding store is, and refused by name
		// otherwise: a build with nowhere to put a value must not accept
		// one. It is the same gate api.import.postman answers behind, and
		// for the same reason.
		whenAvailable(regResponder(sub, "api.environment.bindSecret", params(validateAPIEnvironmentBindSecretRaw), func(r Responder) handlerFunc {
			h := collHandlers(r)
			return func(ctx context.Context, req jsonrpcRequest) { h.handleBindSecret(ctx, req) }
		}), func() bool { return collWired && importWired }, "api collection bindings not available"),
		whenAvailable(regResponder(sub, "api.request.read", params(validateAPIRequestRaw), func(r Responder) handlerFunc {
			h := collHandlers(r)
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}), collAvailable, apiCollectionsUnavailable),
		whenAvailable(regResponder(sub, "api.request.delete", params(validateAPIRequestRaw), func(r Responder) handlerFunc {
			h := collHandlers(r)
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}), collAvailable, apiCollectionsUnavailable),
		whenAvailable(regResponder(sub, "api.request.write", params(validateAPIRequestWriteRaw), func(r Responder) handlerFunc {
			h := collHandlers(r)
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}), collAvailable, apiCollectionsUnavailable),
		whenAvailable(regResponder(sub, "api.request.move", params(validateAPIRequestMoveRaw), func(r Responder) handlerFunc {
			h := collHandlers(r)
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}), collAvailable, apiCollectionsUnavailable),
		whenAvailable(reg(sub, "api.request.send", params(validateAPIRequestSendRaw), func(w *wsConn, _ *connState, r Responder) handlerFunc {
			h := sendHandlers(w, r)
			return func(ctx context.Context, req jsonrpcRequest) { h.handleSend(ctx, req) }
		}), func() bool { return sendWired }, "api sending not available"),
		whenAvailable(reg(cancelSub, "api.request.cancel", params(validateAPIRequestCancelRaw), func(w *wsConn, _ *connState, r Responder) handlerFunc {
			h := sendHandlers(w, r)
			return func(ctx context.Context, req jsonrpcRequest) { h.handleCancel(ctx, req) }
		}), func() bool { return sendWired }, "api sending not available"),
		whenAvailable(regResponder(sub, "api.import.postman", params(validateAPIImportPostmanRaw), func(r Responder) handlerFunc {
			h := apiImportHandlers{op: importOp, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handlePostman(ctx, req) }
		}), func() bool { return importWired }, "api collection bindings not available"),
		regResponder(sub, "api.import.curl", params(validateAPIImportCurlRaw), func(r Responder) handlerFunc {
			h := apiImportHandlers{r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleCurl(ctx, req) }
		}),
		whenAvailable(regResponder(sub, "api.folder.read", params(validateAPIFolderReadRaw), func(r Responder) handlerFunc {
			h := collHandlers(r)
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}), collAvailable, apiCollectionsUnavailable),
		whenAvailable(regResponder(sub, "api.folder.write", params(validateAPIFolderWriteRaw), func(r Responder) handlerFunc {
			h := collHandlers(r)
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}), collAvailable, apiCollectionsUnavailable),
		whenAvailable(regResponder(sub, "api.request.scope", params(validateAPIRequestScopeRaw), func(r Responder) handlerFunc {
			h := collHandlers(r)
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}), collAvailable, apiCollectionsUnavailable),
	}
}

// apiCollectionsUnavailable is the sentence a caller gets when no collection
// service is wired. Each domain keeps its own words rather than flattening
// to one string: callers read them.
const apiCollectionsUnavailable = "api collections not available"
