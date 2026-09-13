package toolendpoint

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/coordinator"
	nocxlog "github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/toolendpoint/panebind"
	"github.com/shady2k/nocx/internal/workers"
)

const (
	toolSocketName    = "tool.sock"
	maxEnvelopeBytes  = 256 << 10
	requestReadWindow = 11 * time.Minute
)

var (
	errOversizedEnvelope  = errors.New("toolendpoint: request envelope exceeds the size bound")
	errIncompleteEnvelope = errors.New("toolendpoint: request did not end with a newline")
	// errUnpublishedAdmission is an authorizer returning a grant without
	// having published the admission through the callback it was handed. The
	// connection it describes is one no interval can close, so it is refused
	// rather than served.
	errUnpublishedAdmission = errors.New("toolendpoint: authorizer admitted a peer without publishing the admission")
	// errPaneRecordRequired is a connection on the helper's lane that did not
	// carry a pane record at all — a helper older than this build, or the pid
	// of one reused by something else. Either way it names no pane, and a
	// connection on that lane is never guessed at: guessing would be exactly
	// the "some enrolled session" answer the record exists to replace.
	errPaneRecordRequired = errors.New("toolendpoint: a connection on the helper's lane carried no pane record")
)

// paneRecordTimeout bounds the wait for a pane record before the connection is
// refused. It is a HANG LIMIT and not a deadline anything waits for: the helper
// writes the record before it pipes a single far byte, so the read has already
// returned by the time the file descriptor is readable at all. A peer that
// connects on this lane and then says nothing is not a helper mid-write, and it
// must not hold a serve goroutine open for the connection's whole life.
const paneRecordTimeout = 5 * time.Second

const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
	rpcInternalError  = -32603
	rpcDomainError    = -32000
	rpcPeerRefused    = -32001
)

// ObservationKind names the endpoint event that the composition root may
// project onto a user-visible session fact.
type ObservationKind string

const (
	ObservationAdmitted  ObservationKind = "admitted"
	ObservationCatalogue ObservationKind = "catalogue"
	ObservationRefusal   ObservationKind = "refusal"
)

// Observation is what the endpoint saw on one launch's tool-surface path.
// SessionID is empty when a peer was refused before the authorizer could bind
// it to a session; an authorizer may implement PeerSessionResolver to provide
// that binding for refusal observations.
type Observation struct {
	SessionID string
	Kind      ObservationKind
	Reason    string
}

// Observer receives endpoint observations. The endpoint reports facts only;
// the composition root owns deadlines, deduplication and presentation.
type Observer interface {
	Observe(Observation)
}

// PeerSessionResolver optionally supplies the session a refused peer was
// attempting to reach. It is separate from Authorizer so existing authorizers
// remain valid and refusals stay fail-closed when no binding is available.
type PeerSessionResolver interface {
	SessionForPeer(Peer) (string, bool)
}

// Config is everything the endpoint needs, supplied by the composition root.
// A nil dependency is an error rather than a permissive default: an endpoint
// without an authorizer would turn same-uid membership into authority.
type Config struct {
	// Dir is the runtime directory shared with the coordinator discovery socket.
	Dir string
	// Peers reads kernel-stamped identity assertions from accepted connections.
	Peers coordinator.PeerCredentials
	// Owner checks ownership of Dir without following its final symlink.
	Owner coordinator.PathOwner
	// SelfUID is the uid that an accepted local peer must have before admission.
	SelfUID uint32
	// Auth binds an accepted peer to a session, grant and run context.
	Auth Authorizer
	// Lane answers whether an accepted peer is this machine's helper daemon —
	// the one process whose connections may name a pane (nocx-50w7p.16). The
	// composition root owns it because the answer is a comparison between the
	// process the coordinator dialed its helper as and the start time that pid
	// has now; the endpoint only asks.
	//
	// NIL MEANS NO LANE, and then NO connection may name a pane: a record
	// arriving anywhere else is not read, not honoured, and not a fallback the
	// endpoint invents. That is the fail-closed direction — a coordinator with
	// no helper admits no far agent, which is exactly today's behaviour.
	Lane func(Peer) bool
	// Dispatch runs the common worker declaration and executor pipeline.
	Dispatch assistant.ToolDispatcher
	// Observer receives admission, refusal and catalogue observations for the
	// composition root's session-level tool-surface monitor.
	Observer Observer
	// Logger receives lifecycle diagnostics and never receives request payloads.
	Logger *slog.Logger
}

// Endpoint owns the local worker JSON-RPC socket.
type Endpoint struct {
	cfg    Config
	socket string

	mu       sync.Mutex
	listener *net.UnixListener
	closed   bool
	conns    map[*net.UnixConn]struct{}
	// admitted is the same set keyed by the session each connection was
	// admitted for, carrying the EPOCH it was admitted under. ADR-0058 makes
	// the admission interval the unit of authority and names its closing
	// events; a connection outlives a single call, so the interval has to be
	// closable by session or a withdrawn answer keeps working until the socket
	// happens to end. The epoch is what makes the closing exact: a session can
	// hold two intervals with identical approval, and only the epoch says which
	// of them a live connection belongs to.
	admitted map[string]map[*net.UnixConn]AdmissionEpoch
	// retired is the highest epoch per session whose interval has ENDED, and
	// it only ever moves forward. A publication naming an epoch at or below it
	// is refused: the interval that decided the admission is over, so a
	// connection arriving late from it must not be recorded. It is the one
	// piece of state that makes the publication and the retirement
	// linearizable — both take the lock below, so the two orders that exist
	// are "published then retired", which closes the connection, and "retired
	// then published", which refuses it.
	retired map[string]AdmissionEpoch
	wait    sync.WaitGroup
}

// New validates the endpoint configuration and computes its socket path. It
// touches no filesystem; failures against the runtime directory happen in
// Start, after the caller is ready to handle them.
func New(cfg Config) (*Endpoint, error) {
	switch {
	case cfg.Dir == "":
		return nil, errors.New("toolendpoint: no runtime directory")
	case !filepath.IsAbs(cfg.Dir):
		return nil, fmt.Errorf("toolendpoint: runtime directory %q is not absolute", cfg.Dir)
	case cfg.Peers == nil:
		return nil, errors.New("toolendpoint: no peer credentials")
	case cfg.Owner == nil:
		return nil, errors.New("toolendpoint: no path owner")
	case isNilDependency(cfg.Auth):
		return nil, errors.New("toolendpoint: no authorizer")
	case isNilDependency(cfg.Dispatch):
		return nil, errors.New("toolendpoint: no worker dispatcher")
	// The catalogue is what lets a caller obey "an unreachable tool is not
	// offered": without it the endpoint can execute calls it cannot enumerate,
	// and a caller has to guess its own eligibility. Required at composition
	// rather than discovered on the first tools.catalogue, because a
	// capability found missing at call time is a soft degrade nobody sees —
	// the caller gets one refused method and goes on believing the surface is
	// whole.
	case !implementsCatalogue(cfg.Dispatch):
		return nil, errors.New("toolendpoint: dispatcher cannot enumerate what a grant admits")
	case cfg.Logger == nil:
		return nil, errors.New("toolendpoint: no logger")
	}
	endpoint := &Endpoint{
		cfg:      cfg,
		socket:   filepath.Join(cfg.Dir, toolSocketName),
		conns:    make(map[*net.UnixConn]struct{}),
		admitted: make(map[string]map[*net.UnixConn]AdmissionEpoch),
		retired:  make(map[string]AdmissionEpoch),
	}
	// An authorizer whose own authority can end while a connection is open
	// takes the endpoint's admissions here, at construction, rather than
	// through a second wiring step some composition root has to remember: the
	// endpoint and the authorizer are joined by this call in every build,
	// tests included.
	if binder, ok := cfg.Auth.(SessionAdmissionBinder); ok {
		binder.BindSessionAdmissions(endpoint)
	}
	return endpoint, nil
}

// SessionAdmissions is the endpoint's view of what it has admitted, handed to
// an authorizer that needs to end an interval early: the epoch of one session's
// authority, and the way to retire it.
type SessionAdmissions interface {
	// RetireAdmissions ends the authority an interval carried. Every live
	// connection admitted for the session under an epoch at or before epoch is
	// closed — which cancels the request contexts in flight on it — and every
	// later publication naming one of those epochs is refused. It returns how
	// many connections it closed.
	//
	// It is idempotent in the sense that matters: retiring an epoch that was
	// already retired closes nothing and refuses everything it refused before.
	// A session's epochs only move forward, so a retirement is never undone
	// and a later interval is never caught by an earlier retirement.
	RetireAdmissions(session string, epoch AdmissionEpoch) int
}

// SessionAdmissionBinder is implemented by an Authorizer that owns authority
// which can be withdrawn mid-connection. toolendpoint binds at construction.
type SessionAdmissionBinder interface {
	BindSessionAdmissions(SessionAdmissions)
}

// AdmittedSessions names the sessions with a live admitted connection, in a
// stable order so a caller comparing them sees a set rather than a map walk.
func (e *Endpoint) AdmittedSessions() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	sessions := make([]string, 0, len(e.admitted))
	for session := range e.admitted {
		sessions = append(sessions, session)
	}
	sort.Strings(sessions)
	return sessions
}

// RetireAdmissions ends the authority of a session's interval. It is the
// closing event ADR-0058 names for a shared connection: the interval's grant is
// immutable, so ending the interval is the only way a withdrawal takes effect
// before the session itself ends.
//
// The epoch is what makes it exact rather than approximate. A session can hold
// two intervals the approval cannot tell apart — withdrawn and enrolled again —
// and a retirement that closed "whatever is live for this session" would take
// the new interval's connection with the old one's. Only the connections whose
// epoch has ENDED are closed here; the higher floor left behind is what refuses
// the late publication of a decision taken under the ended epoch.
func (e *Endpoint) RetireAdmissions(session string, epoch AdmissionEpoch) int {
	if session == "" || epoch == 0 {
		return 0
	}
	e.mu.Lock()
	if epoch > e.retired[session] {
		e.retired[session] = epoch
	}
	// The floor, not the argument, decides what is closed: an epoch retired
	// earlier has already been refused, and a connection stamped with it
	// cannot exist. Reading it back keeps the two in one place.
	floor := e.retired[session]
	var connections []*net.UnixConn
	for conn, admitted := range e.admitted[session] {
		if admitted <= floor {
			connections = append(connections, conn)
		}
	}
	e.mu.Unlock()
	for _, conn := range connections {
		_ = conn.Close()
	}
	return len(connections)
}

func isNilDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

// SocketPath returns the endpoint path before or after Start.
func (e *Endpoint) SocketPath() string { return e.socket }

// Start prepares the shared runtime directory, publishes tool.sock atomically
// through the coordinator's path-security answer, and begins accepting peers.
func (e *Endpoint) Start() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return errors.New("toolendpoint: endpoint is closed")
	}
	if e.listener != nil {
		return errors.New("toolendpoint: endpoint is already started")
	}
	if err := coordinator.PrepareRuntimeDir(e.cfg.Dir, e.cfg.Owner, e.cfg.SelfUID); err != nil {
		return err
	}
	listener, err := coordinator.BindSocket(e.cfg.Dir, toolSocketName)
	if err != nil {
		return err
	}
	e.listener = listener
	e.wait.Add(1)
	go e.accept(listener)
	return nil
}

// Close stops accepting, closes active connections so their request contexts
// are canceled, waits for handlers, and removes the published socket. It is
// idempotent.
func (e *Endpoint) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	listener := e.listener
	started := listener != nil
	e.listener = nil
	connections := make([]*net.UnixConn, 0, len(e.conns))
	for conn := range e.conns {
		connections = append(connections, conn)
	}
	e.mu.Unlock()

	if listener != nil {
		_ = listener.Close()
	}
	for _, conn := range connections {
		_ = conn.Close()
	}
	e.wait.Wait()
	if !started {
		return nil
	}
	if err := os.Remove(e.socket); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("toolendpoint: remove socket: %w", err)
	}
	return nil
}

func (e *Endpoint) accept(listener *net.UnixListener) {
	defer e.wait.Done()
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			e.cfg.Logger.Warn("toolendpoint: accept failed")
			return
		}

		// Kernel identity is read synchronously in the accept loop, before the
		// connection is handed to a request parser. No request bytes are read
		// until both uid and (when available) pid are established.
		peer, err := e.peer(conn)
		if err != nil {
			e.observePeerRefusal(Peer{}, "peer credentials unavailable")
			e.writeError(conn, nil, rpcPeerRefused, "peer credentials unavailable", "peer credentials unavailable")
			_ = conn.Close()
			continue
		}
		if peer.UID != e.cfg.SelfUID {
			e.cfg.Logger.Warn("toolendpoint: refusing foreign peer")
			e.observePeerRefusal(peer, "peer uid is not permitted")
			e.writeError(conn, nil, rpcPeerRefused, "peer uid is not permitted", "peer uid is not permitted")
			_ = conn.Close()
			continue
		}
		if !e.track(conn) {
			_ = conn.Close()
			return
		}
		// WHETHER THIS PEER MAY NAME A PANE, decided here and not in the serve
		// goroutine: it is a comparison against a fact the coordinator recorded,
		// it never touches the peer's bytes, and a connection that is NOT the
		// lane must be served exactly as it was before this bead — no record
		// read, no preamble, the same JSON ahead of it.
		onLane := e.cfg.Lane != nil && e.cfg.Lane(peer)
		go func() {
			defer e.wait.Done()
			e.serve(conn, peer, onLane)
		}()
	}
}

func (e *Endpoint) peer(conn *net.UnixConn) (Peer, error) {
	uid, err := e.cfg.Peers.PeerUID(conn)
	if err != nil {
		return Peer{}, err
	}
	if uid != e.cfg.SelfUID {
		return Peer{UID: uid}, nil
	}
	pid := 0
	if process, ok := e.cfg.Peers.(coordinator.PeerProcess); ok {
		pid, err = process.PeerPID(conn)
		if err != nil {
			return Peer{}, err
		}
	}
	return Peer{UID: uid, PID: pid}, nil
}

func (e *Endpoint) track(conn *net.UnixConn) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return false
	}
	e.conns[conn] = struct{}{}
	e.wait.Add(1)
	return true
}

func (e *Endpoint) untrack(conn *net.UnixConn) {
	e.mu.Lock()
	delete(e.conns, conn)
	e.mu.Unlock()
}

// publishAdmission records which session — and which INTERVAL of it — this
// connection was admitted for. It is called BY the authorizer from inside the
// decision that admitted the connection, never by the endpoint after that
// decision returned, so a retirement ordered between the two cannot miss it:
// both take this mutex, and the two orders that exist are "published then
// retired", where the retirement finds this connection and closes it, and
// "retired then published", where the epoch is at or below the floor and this
// returns false.
//
// The record is removed by the serve loop that owns the connection, or — when
// the decision published and then failed — by the refusal path that owns that
// failure. Either way a record and a live serve loop are the same thing.
func (e *Endpoint) publishAdmission(session string, epoch AdmissionEpoch, conn *net.UnixConn) bool {
	if session == "" || epoch == 0 {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return false
	}
	if epoch <= e.retired[session] {
		// The interval this decision was taken under has ended. A connection
		// admitted from it now would carry a grant nobody holds, which is the
		// state the epoch exists to make impossible.
		return false
	}
	if e.admitted == nil {
		e.admitted = make(map[string]map[*net.UnixConn]AdmissionEpoch)
	}
	if e.admitted[session] == nil {
		e.admitted[session] = make(map[*net.UnixConn]AdmissionEpoch)
	}
	e.admitted[session][conn] = epoch
	return true
}

func (e *Endpoint) forgetSession(session string, conn *net.UnixConn) {
	if session == "" {
		return
	}
	e.mu.Lock()
	delete(e.admitted[session], conn)
	if len(e.admitted[session]) == 0 {
		delete(e.admitted, session)
	}
	e.mu.Unlock()
}

func (e *Endpoint) observe(observation Observation) {
	if e.cfg.Observer != nil {
		e.cfg.Observer.Observe(observation)
	}
}

func (e *Endpoint) observePeerRefusal(peer Peer, reason string) {
	observation := Observation{Kind: ObservationRefusal, Reason: reason}
	if resolver, ok := e.cfg.Auth.(PeerSessionResolver); ok {
		if sid, found := resolver.SessionForPeer(peer); found {
			observation.SessionID = sid
		}
	}
	e.observe(observation)
}

// readPaneRecord reads the one frame a helper writes ahead of a far agent's
// own bytes: which pane the connection arrived on (nocx-50w7p.16).
//
// The deadline is set and CLEARED around this read rather than left on the
// connection. What follows it is a long-lived request loop, and a read deadline
// that outlived the record would end every idle connection a helper holds —
// which is every far agent that is thinking rather than calling.
func (e *Endpoint) readPaneRecord(conn *net.UnixConn) (string, error) {
	if err := conn.SetReadDeadline(time.Now().Add(paneRecordTimeout)); err != nil {
		return "", err
	}
	pane, err := panebind.Read(conn)
	// Cleared whether or not the read worked: on failure the connection is
	// closed, and on success the request loop below must be able to wait.
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		return "", err
	}
	if pane == "" {
		// Unreachable through panebind.Read, which refuses an empty session.
		// Answered rather than assumed, because a reader that could return ""
		// would hand the authorizer an empty pane, and "" is the spelling of
		// "this connection named no pane" — the opposite fact.
		return "", errPaneRecordRequired
	}
	return pane, nil
}

func (e *Endpoint) serve(conn *net.UnixConn, peer Peer, onLane bool) {
	defer e.untrack(conn)
	defer func() { _ = conn.Close() }()

	// THE PANE RECORD, and it is read FIRST: before the decision, before any
	// request byte, and only on the lane. A far agent's connection is the
	// helper's, so the helper's report of which pane it arrived on is what the
	// authorizer decides on — and a failure to produce one is a refusal with a
	// sentence, never a fall back to the local rule (which would admit the
	// connection as whatever tree the kernel happened to match).
	if onLane {
		pane, err := e.readPaneRecord(conn)
		if err != nil {
			code, message, reason := rpcErrorFor(err)
			e.observePeerRefusal(peer, reason)
			e.writeError(conn, nil, code, message, reason)
			return
		}
		peer.Pane = pane
	}

	// published records that the authorizer admitted this connection through
	// the callback below, and under which session. The endpoint grants nothing
	// it did not publish: an authorizer that returns a grant without publishing
	// has produced a connection the interval cannot close, which is refused
	// rather than served.
	published := false
	publishedSession := ""
	invocation, release, err := e.cfg.Auth.Admit(peer, func(session string, epoch AdmissionEpoch) bool {
		if !e.publishAdmission(session, epoch, conn) {
			return false
		}
		published = true
		publishedSession = session
		return true
	})
	if err == nil && !published {
		err = errUnpublishedAdmission
	}
	if err != nil {
		// A DECISION THAT PUBLISHED AND THEN FAILED LEAVES NOTHING BEHIND. The
		// record is not conditional on the decision succeeding: this serve loop
		// never starts, so nothing else would remove it, and the endpoint would
		// report a session as admitted with no connection behind it until some
		// later retirement closed an interval that has nothing to close.
		if published {
			e.forgetSession(publishedSession, conn)
		}
		code, message, reason := rpcErrorFor(err)
		e.observePeerRefusal(peer, reason)
		e.writeError(conn, nil, code, message, reason)
		return
	}
	base := invocation.Context
	if base == nil {
		base = context.Background()
	}
	// The book is keyed by what the endpoint RECORDED and not by what the
	// invocation says: a connection is forgotten from exactly where it was
	// admitted, so an authorizer whose two answers disagree cannot leave an
	// entry nothing removes.
	defer e.forgetSession(publishedSession, conn)
	e.observe(Observation{
		SessionID: invocation.RunContext.Session,
		Kind:      ObservationAdmitted,
	})
	connectionCtx, cancel := context.WithCancel(base)

	reader := bufio.NewReaderSize(conn, maxEnvelopeBytes)
	var requests sync.WaitGroup
	defer func() {
		cancel()
		requests.Wait()
		if release != nil {
			release()
		}
	}()
	var inFlight atomic.Int32
	for {
		if inFlight.Load() == 0 {
			if err := conn.SetReadDeadline(time.Now().Add(requestReadWindow)); err != nil {
				return
			}
		} else if err := conn.SetReadDeadline(time.Time{}); err != nil {
			return
		}
		line, err := readEnvelope(reader)
		if errors.Is(err, io.EOF) {
			break
		}
		if errors.Is(err, errOversizedEnvelope) {
			e.writeError(conn, nil, rpcInvalidRequest, "invalid request", "request envelope exceeds the size bound")
			break
		}
		if errors.Is(err, errIncompleteEnvelope) {
			e.writeError(conn, nil, rpcParseError, "parse error", "request did not end with a newline")
			break
		}
		if err != nil {
			break
		}

		request, parseErr := decodeRequest(line)
		if parseErr != nil {
			e.writeError(conn, nil, rpcParseError, "parse error", "request is not valid JSON")
			continue
		}
		if requestNotification(request) {
			e.writeError(conn, request.ID, rpcInvalidRequest, "invalid request", "worker notifications are not supported")
			continue
		}
		if validationErr := validateRequest(request); validationErr != nil {
			e.writeError(conn, request.ID, rpcInvalidRequest, "invalid request", "request is not a valid JSON-RPC request")
			continue
		}

		requests.Add(1)
		inFlight.Add(1)
		go func(request rpcRequest) {
			defer requests.Done()
			defer func() {
				if inFlight.Add(-1) == 0 {
					_ = conn.SetReadDeadline(time.Now().Add(requestReadWindow))
				}
			}()
			requestCtx, requestCancel := context.WithCancel(connectionCtx)
			defer requestCancel()
			// The caller's frame becomes this one's parent, and this request
			// gets a span of its own under their trace. Everything below —
			// the dispatch, the refusals, the answer — hangs off it, because
			// the context is what the dispatcher is handed.
			requestCtx = nocxlog.ContinueTrace(requestCtx, request.Traceparent)
			requestCtx = nocxlog.WithRequestID(requestCtx, requestIDTag(request.ID))
			// BOUND, not passed as start-line arguments: the start line is
			// debug and a release build does not write it, so a caller named
			// only there would be absent from the one record that matters —
			// the failure.
			requestCtx, requestLog, endRequest := nocxlog.Start(requestCtx,
				nocxlog.NewSlogAdapter(e.log()).With(
					"session_id", invocation.RunContext.Session,
					"participant", invocation.RunContext.Participant,
				),
				request.Method)
			requestInvocation := invocation
			requestInvocation.Context = requestCtx
			requestInvocation.Method = request.Method
			requestInvocation.RawParams = append(json.RawMessage(nil), request.Params...)
			var result string
			var dispatchErr error
			if request.Method == "tools.catalogue" {
				var catalogueResult []byte
				catalogueResult, dispatchErr = buildCatalogue(e.cfg.Dispatch, requestInvocation.Grant)
				result = string(catalogueResult)
			} else {
				result, dispatchErr = e.cfg.Dispatch.Dispatch(requestInvocation)
			}
			if dispatchErr != nil {
				code, message, reason := rpcErrorFor(dispatchErr)
				if code == rpcInternalError {
					// THE ONE ERROR NOBODY CLASSIFIED, and it used to be the
					// only one that left no trace on either side: the caller
					// got two words naming no cause, and nothing was written
					// here. So the case that most needs diagnosis was the
					// case with the least evidence (nocx-1w3my).
					//
					// The wire answer stays generic on purpose — an
					// unclassified error must not spell a backend's internals
					// to an agent — which is exactly why the log has to carry
					// it.
					// The method, the session and the participant are on the
					// record already — requestLog is bound to the span this
					// request opened — so what this line adds is the error
					// itself, which is the fact the wire deliberately withholds.
					requestLog.Error("tool endpoint: unclassified dispatch failure",
						"error", dispatchErr)
				}
				endRequest(dispatchErr)
				e.writeError(conn, request.ID, code, message, reason)
				return
			}
			if !json.Valid([]byte(result)) {
				// The second unlogged internal error, and it had the same
				// shape as the one nocx-1w3my found: two words on the wire and
				// nothing anywhere naming the dispatcher that produced them.
				invalid := errors.New("dispatcher returned an invalid result")
				requestLog.Error("tool endpoint: " + invalid.Error())
				endRequest(invalid)
				e.writeError(conn, request.ID, rpcInternalError, "internal error", "dispatcher returned an invalid result")
				return
			}
			endRequest(nil)
			if request.Method == "tools.catalogue" {
				e.observe(Observation{
					SessionID: requestInvocation.RunContext.Session,
					Kind:      ObservationCatalogue,
				})
			}
			e.writeResult(conn, request.ID, json.RawMessage(result))
		}(request)

	}
}

func readEnvelope(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > maxEnvelopeBytes {
			return nil, errOversizedEnvelope
		}
		line = append(line, fragment...)
		if err == nil {
			return line, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(line) == 0 {
				return nil, io.EOF
			}
			return nil, errIncompleteEnvelope
		}
		return nil, err
	}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	// Traceparent is the caller's W3C Trace Context header, and it is the one
	// member here that is not JSON-RPC's. It carries the exchange across the
	// process boundary: a coordinator's MCP call is served by nocx-helper,
	// which asks this socket, and without it those are three sets of log lines
	// with nothing in common — which is exactly how the failure of 2026-09-09
	// had to be assembled, by reading timestamps (nocx-4l2a5.3).
	//
	// Absent or malformed is not a bad request. It means a caller whose
	// telemetry we cannot join, and the call still runs: an observability
	// mechanism that can refuse service is not one.
	Traceparent string `json:"traceparent,omitempty"`
}

func decodeRequest(line []byte) (rpcRequest, error) {
	var request rpcRequest
	if err := json.Unmarshal(line, &request); err != nil {
		return rpcRequest{}, err
	}
	return request, nil
}

func requestNotification(request rpcRequest) bool {
	return len(request.ID) == 0 || bytes.Equal(bytes.TrimSpace(request.ID), []byte("null"))
}

func validateRequest(request rpcRequest) error {
	if request.JSONRPC != "2.0" || request.Method == "" || requestNotification(request) {
		return errors.New("invalid JSON-RPC request")
	}
	var id any
	if err := json.Unmarshal(request.ID, &id); err != nil {
		return err
	}
	switch id.(type) {
	case string, float64:
		return nil
	default:
		return errors.New("JSON-RPC id must be a string or number")
	}
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int           `json:"code"`
	Message string        `json:"message"`
	Data    *rpcErrorData `json:"data,omitempty"`
}

type rpcErrorData struct {
	Reason string `json:"reason"`
}

func (e *Endpoint) writeResult(conn *net.UnixConn, id json.RawMessage, result json.RawMessage) {
	e.write(conn, rpcResponse{JSONRPC: "2.0", ID: append(json.RawMessage(nil), id...), Result: result})
}

func (e *Endpoint) writeError(conn *net.UnixConn, id json.RawMessage, code int, message, reason string) {
	if len(id) == 0 || bytes.Equal(bytes.TrimSpace(id), []byte("null")) {
		id = json.RawMessage("null")
	}
	response := rpcResponse{JSONRPC: "2.0", ID: append(json.RawMessage(nil), id...), Error: &rpcError{Code: code, Message: message}}
	if reason != "" {
		response.Error.Data = &rpcErrorData{Reason: reason}
	}
	e.write(conn, response)
}

func (e *Endpoint) write(conn *net.UnixConn, response rpcResponse) {
	line, err := json.Marshal(response)
	if err != nil {
		return
	}
	line = append(line, '\n')
	// net.UnixConn writes are serialized per connection. Without this mutex,
	// concurrent worker calls could interleave two otherwise valid JSON lines.
	connWriteMu.Lock()
	defer connWriteMu.Unlock()
	_, _ = conn.Write(line)
}

var connWriteMu sync.Mutex

// rpcErrorFor turns a dispatch failure into what an AGENT reads.
//
// THE REASON IS AN INSTRUCTION, NOT A LABEL. Every sentence here says three
// things: what happened, why, and what the caller should do next. That is not
// a preference — it is the standard this product already holds its own model
// to. internal/assistant's refusalResult answers the built-in assistant with
// "REFUSED: the person declined your call to X — it did not run. Say what you
// needed in words instead", and an external coordinator was getting "method is
// not assembled". A caller that cannot tell "you may never do this" from "try
// again" does the wrong one, and the wrong one is usually the retry.
//
// What the model actually sees: on a domain refusal the MCP adapter shows the
// reason (mcpstdio handleCall -> toolError); on everything else it shows the
// message. Both are written for that reader.
func (e *Endpoint) log() *slog.Logger {
	if e != nil && e.cfg.Logger != nil {
		return e.cfg.Logger
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func rpcErrorFor(err error) (code int, message, reason string) {
	switch {
	case errors.Is(err, assistant.ErrUnknownMethod):
		return rpcMethodNotFound, "method not found",
			"nocx has no tool by that name. Call tools.catalogue to see what this session is offered, and use one of those names."
	case errors.Is(err, assistant.ErrInvalidParams):
		return rpcInvalidParams, "invalid params",
			"the arguments do not match this tool's schema. Read the tool's schema in tools.catalogue and call it again with corrected arguments."
	case errors.Is(err, assistant.ErrUnreachableMethod):
		return rpcDomainError, "worker request refused",
			"this tool exists but is not offered to this session, so calling it again will fail the same way. Work with the tools tools.catalogue lists for you, or say in words what you needed it for."
	case errors.Is(err, assistant.ErrInvalidResult):
		return rpcDomainError, "worker request refused",
			"the tool ran and produced a result nocx could not read, so nothing can be reported about it. Do not repeat the call; say what you were trying to find out."
	case errors.Is(err, workers.ErrNotHeld):
		// OWNERSHIP. Split from the state refusal below (nocx-e5e8q): one
		// error used to carry both facts and the wire spelled them with this
		// sentence, so a coordinator refused because a delegation had ended
		// was told the participant belonged to somebody else — which it will
		// not question, and which workers.holdings contradicts on the next
		// call.
		return rpcDomainError, "worker request refused",
			"that participant is held by another session, so this session may not act on it. Call workers.holdings to see the participants that are yours."
	case errors.Is(err, workers.ErrNotDelegated):
		// STATE. The participant IS this caller's; the authority over it is
		// no longer active.
		return rpcDomainError, "worker request refused",
			"that participant is yours, but its delegation is no longer active, so it can no longer be acted on. Call workers.holdings to see its state; a participant that has ended needs nothing further from you."
	case errors.Is(err, workers.ErrTerminal):
		// ENDED (nocx-f545a.4). workers.answer is the first call that is
		// refused for a participant being over rather than tidied up after:
		// there is no menu on a pane that has closed. Mapped rather than left
		// to the default arm, whose sentence would call it a backend fault.
		return rpcDomainError, "worker request refused",
			"that worker has already ended, so there is nothing on its screen to answer. Call workers.holdings to see how it ended."
	case errors.Is(err, workers.ErrPaneNeverTypable):
		var never *workers.PaneNeverTypable
		if errors.As(err, &never) && never.Unreadable() {
			// NOT WORTH RETRYING (nocx-f545a.3). nocx could not read the
			// pane at all, and the error one layer down already said a pane
			// it cannot read will not become typable on its own. This arm
			// used to answer it with the retry sentence below, which sent a
			// coordinator back to do exactly what could not work.
			return rpcDomainError, "worker request refused",
				"the worker started but nocx could not read what its pane was showing, so nothing was typed into it and the worker was not left running unsupervised. Spawning again the same way will fail the same way; say in words what you were trying to start, and that its pane showed something nocx does not recognise."
		}
		// WORTH RETRYING. nocx read the pane and the agent was still busy —
		// a slower machine, a loaded helper. Split from the arm below for the
		// same reason the axis's two refusals are split: a coordinator that
		// cannot tell "not ready yet" from "your task was refused" fixes the
		// wrong half.
		return rpcDomainError, "worker request refused",
			"the worker started but its pane was still busy when the budget ran out, so nothing was typed into it and the worker was not left running unsupervised. Spawning again may succeed if this was transient."
	case errors.Is(err, workers.ErrTaskSubmitRefused):
		// NOT WORTH RETRYING UNMODIFIED. The pane opened for typing and nocx's
		// own gate then refused the write — the screen said something other
		// than "waiting for input" at the moment of the attempt.
		return rpcDomainError, "worker request refused",
			"the worker's pane became ready and nocx's own typing gate then refused to submit the task into it, so the worker was not left running with no task. Do not retry the same call unmodified until you understand why the gate refused it."
	case errors.Is(err, ErrSessionCallerActive):
		return rpcPeerRefused, "worker caller refused",
			"another call from this session is still running, and nocx serves one at a time. Wait for that call to answer, then make this one."
	case errors.Is(err, ErrNotEnrolled):
		return rpcPeerRefused, "worker caller refused",
			"this process is not in a pane nocx has enrolled, so it has no worker tools. Nothing you can call will change that; tell the person their pane is not orchestrated."
	case errors.Is(err, panebind.ErrShortRecord):
		// THE HELPER'S REPORT DID NOT ARRIVE WHOLE. Nothing of the call ran,
		// and retrying on this connection is not possible — but the fact is
		// about the helper that opened the pane rather than about the caller,
		// so the sentence says which of the two is broken.
		return rpcPeerRefused, "worker caller refused",
			"this connection stopped before it said which pane it belongs to, so nocx never learned what it was for and ran none of it. The helper holding this pane is the part that failed; the pane's agent can try again once that helper is restarted."
	case errors.Is(err, panebind.ErrNotAPaneRecord), errors.Is(err, errPaneRecordRequired):
		// A connection on the helper's lane that names no pane. It is refused
		// rather than matched against the local rule: that rule answers for a
		// process tree on THIS machine, and this connection is a far host's.
		return rpcPeerRefused, "worker caller refused",
			"this connection arrived on the lane nocx's helper uses but did not name a pane, so there is no pane whose tools it could be given and none of the call was run. This is a mismatch between the running helper and this build; say so rather than retrying."
	case errors.Is(err, errUnpublishedAdmission):
		// THE ONE ADMISSION THAT IS NOCX'S OWN FAULT. The authorizer granted
		// and did not record, so the connection would have served under an
		// interval nothing can close. Refused rather than retried: the same
		// dial reaches the same authorizer.
		return rpcPeerRefused, "worker caller refused",
			"nocx decided to admit this caller and could not record the admission, so nothing was granted and no call was made. This is a fault in how this pane's tools are wired, not in what you sent; reconnecting will fail the same way, so tell the person their pane is not orchestrated."
	case errors.Is(err, context.Canceled):
		// THE CALLER STOPPED WAITING; NOTHING FAILED INSIDE NOCX. This is
		// what a dispatch reports when its own context ends before it
		// produces an answer — a person interrupting the session that is
		// holding a call (e.g. workers.wait) before it has reported is
		// exactly that (nocx-uhii1). It is neither of the two things the
		// default arm's sentence would tell an agent: not a backend
		// fault, and not a call the agent should stop retrying. The
		// honest fact is the opposite of "do not repeat it" — the call
		// was abandoned before it could answer, and asking again is the
		// normal way to get an answer this time. Split from the deadline
		// arm below only so the two keep distinct sentences; a caller
		// that cannot tell them apart cannot tell "somebody gave up
		// waiting" from "the call's own bound ran out" either.
		return rpcDomainError, "call abandoned",
			"the caller stopped waiting for this call and its context was canceled before it produced an answer, so nocx never got to finish it — nothing here failed and nothing here was refused. Calling it again is legitimate; if you still need the result, ask again and wait for it."
	case errors.Is(err, context.DeadlineExceeded):
		return rpcDomainError, "call abandoned",
			"this call ran past its own bound and its context expired before it produced an answer, so nocx never got to finish it — nothing here failed and nothing here was refused. Calling it again is legitimate; if you still need the result, ask again with time to wait for it."
	default:
		// Deliberately generic on the wire and fully logged at the call site:
		// an error nobody classified must not spell a backend's internals to
		// an agent. What it MUST do is stop the agent from retrying a call
		// that failed for a reason it cannot affect.
		return rpcInternalError, "internal error",
			"nocx failed to complete this call for a reason inside nocx, not in what you sent. Do not repeat it; carry on without this tool and say what you could not do."
	}
}

// requestIDTag is the caller's JSON-RPC id as a log value. It is their token
// and not ours, so it is quoted-string-unwrapped for readability and otherwise
// passed through: a caller that numbered its requests reads "7", one that named
// them reads the name.
func requestIDTag(id json.RawMessage) string {
	trimmed := strings.TrimSpace(string(id))
	if unquoted, err := strconv.Unquote(trimmed); err == nil {
		return unquoted
	}
	return trimmed
}
