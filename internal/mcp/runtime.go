package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shady2k/nocx/internal/httppolicy"
	"github.com/shady2k/nocx/internal/profile"
)

var (
	ErrProtocol          = errors.New("MCP protocol failure")
	ErrSchema            = errors.New("MCP schema validation failed")
	ErrServerUnavailable = errors.New("MCP server is unavailable")
	ErrServerFailure     = errors.New("MCP server operation failed")
)

// IsRecoverableError is an allowlist, not a catch-all non-context conversion.
// Unknown errors and protocol/schema/bounds failures remain ledger failures.
func IsRecoverableError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrProtocol) || errors.Is(err, ErrSchema) || errors.Is(err, ErrFrameTooLarge) || errors.Is(err, ErrResponseTooLarge) {
		return false
	}
	for _, sentinel := range []error{ErrClosed, ErrActivationChanged, ErrCatalogStale, ErrDestinationRefused, ErrSecretUnavailable, ErrOAuthReconnectRequired, ErrServerUnavailable, ErrServerFailure} {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}

type (
	ActivationVerifier func(context.Context, Activation) error
	ManagerOption      func(*Manager)
)

func WithActivationVerifier(verifier ActivationVerifier) ManagerOption {
	return func(m *Manager) { m.verifier = verifier }
}

// MutationRunner is required at the management composition boundary.
type MutationRunner interface {
	RunServerMutations(context.Context, []string, func(context.Context) error) error
}

// OperationRunner lets explicit OAuth work share cancellation and drain ownership.
type OperationRunner interface {
	RunServerOperation(context.Context, string, func(context.Context) error) error
	VerifyActivation(context.Context, Activation) error
}

type (
	sessionKey struct{ runID, serverID string }
	operation  struct {
		key    sessionKey
		cancel context.CancelFunc
		done   chan struct{}
	}
)

type Manager struct {
	mu               sync.Mutex
	sessions         map[sessionKey]*pooledSession
	operations       map[*operation]struct{}
	barriers         map[string]int
	runBarriers      map[string]int
	changed          chan struct{}
	closed           bool
	closeDone        chan struct{}
	resolver         SecretResolver
	verifier         ActivationVerifier
	oauthCoordinator *oauthRefreshCoordinator
}

// All pooled state except gate is owned by Manager.mu. gate serializes calls;
// acquiring it is cancellable and never precedes operation registration.
type pooledSession struct {
	key            sessionKey
	identity       string
	gate           chan struct{}
	live           *liveSession
	lifetime       context.Context
	cancel         context.CancelFunc
	active         int
	idle           *time.Timer
	idleGeneration uint64
}

type sensitiveValues struct {
	mu     sync.RWMutex
	values []string
}

func (s *sensitiveValues) add(values ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, value := range values {
		if value == "" {
			continue
		}
		found := false
		for _, old := range s.values {
			if old == value {
				found = true
				break
			}
		}
		if !found {
			// Longest first prevents an overlapping shorter secret exposing a
			// suffix of a longer credential during replacement.
			at := sort.Search(len(s.values), func(i int) bool { return len(s.values[i]) < len(value) })
			s.values = append(s.values, "")
			copy(s.values[at+1:], s.values[at:])
			s.values[at] = value
		}
	}
}

func (s *sensitiveValues) snapshot() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.values...)
}

type liveSession struct {
	client     *sdk.ClientSession
	cancel     context.CancelFunc
	cleanup    func()
	beginClose func()
	sensitive  *sensitiveValues
	stderr     *boundedStderr
	closeOnce  sync.Once
}

func (s *liveSession) sensitiveValues() []string { return s.sensitive.snapshot() }
func (s *liveSession) close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		if s.beginClose != nil {
			s.beginClose()
		}
		s.cancel()
		if s.client != nil {
			_ = s.client.Close()
		}
		if s.cleanup != nil {
			s.cleanup()
		}
	})
}

func NewManager(resolver SecretResolver, options ...ManagerOption) *Manager {
	m := &Manager{
		sessions: make(map[sessionKey]*pooledSession), operations: make(map[*operation]struct{}),
		barriers: make(map[string]int), runBarriers: make(map[string]int), changed: make(chan struct{}),
		closeDone: make(chan struct{}), resolver: resolver, oauthCoordinator: newOAuthRefreshCoordinator(),
	}
	for _, option := range options {
		if option != nil {
			option(m)
		}
	}
	return m
}

func (m *Manager) signalLocked() { close(m.changed); m.changed = make(chan struct{}) }

func (m *Manager) begin(ctx context.Context, key sessionKey) (context.Context, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if m.closed {
		return nil, nil, ErrClosed
	}
	if m.barriers[key.serverID] > 0 || m.runBarriers[key.runID] > 0 {
		return nil, nil, ErrActivationChanged
	}
	work, cancel := context.WithCancel(ctx)
	op := &operation{key: key, cancel: cancel, done: make(chan struct{})}
	m.operations[op] = struct{}{}
	return work, func() {
		cancel()
		m.mu.Lock()
		delete(m.operations, op)
		close(op.done)
		m.signalLocked()
		m.mu.Unlock()
	}, nil
}

func (m *Manager) RunServerOperation(ctx context.Context, serverID string, work func(context.Context) error) error {
	if serverID == "" || work == nil {
		return ErrInvalidActivation
	}
	ctx, finish, err := m.begin(ctx, sessionKey{serverID: serverID})
	if err != nil {
		return err
	}
	defer finish()
	return work(ctx)
}

func (m *Manager) VerifyActivation(ctx context.Context, activation Activation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.verifier != nil {
		if err := m.verifier(ctx, activation); err != nil {
			return safeOperationError("activation check", err, nil)
		}
	}
	return nil
}

func (m *Manager) Refresh(ctx context.Context, activation Activation) (Catalog, error) {
	activation = activation.Clone()
	if err := activation.validate(false); err != nil {
		return Catalog{}, err
	}
	ctx, finish, err := m.begin(ctx, sessionKey{serverID: activation.ServerID})
	if err != nil {
		return Catalog{}, err
	}
	defer finish()
	if verificationErr := m.VerifyActivation(ctx, activation); verificationErr != nil {
		return Catalog{}, verificationErr
	}
	live, err := m.connect(ctx, ctx, activation, nil)
	if err != nil {
		return Catalog{}, err
	}
	defer live.close()
	if verificationErr := m.VerifyActivation(ctx, activation); verificationErr != nil {
		return Catalog{}, verificationErr
	}
	listCtx, cancel := context.WithTimeout(ctx, activation.Limits.CallTimeout)
	defer cancel()
	closed := make(chan struct{})
	stop := context.AfterFunc(listCtx, func() { live.close(); close(closed) })
	defer func() {
		if !stop() {
			<-closed
		}
	}()
	catalog, err := discoverCatalog(listCtx, live.client)
	if err != nil {
		return Catalog{}, safeOperationError("tool discovery", preferContext(listCtx, err), live.sensitiveValues(), live.stderr)
	}
	// A descriptor containing material supplied to this server must never be
	// persisted or projected to a provider, even in schema literals or names.
	if catalogContainsSecret(catalog, live.sensitiveValues()) {
		return Catalog{}, ErrSchema
	}
	return catalog, nil
}

func (m *Manager) acquire(ctx context.Context, key sessionKey, activation Activation) (*pooledSession, error) {
	identity := activation.identity()
	m.mu.Lock()
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if m.closed || m.barriers[key.serverID] > 0 || m.runBarriers[key.runID] > 0 {
		m.mu.Unlock()
		return nil, ErrActivationChanged
	}
	s := m.sessions[key]
	if s != nil && s.identity != identity {
		m.mu.Unlock()
		m.dropSession(key, s)
		return nil, ErrActivationChanged
	}
	if s == nil {
		lifetime, cancel := context.WithCancel(context.WithoutCancel(ctx))
		s = &pooledSession{key: key, identity: identity, gate: make(chan struct{}, 1), lifetime: lifetime, cancel: cancel}
		m.sessions[key] = s
	}
	s.active++
	s.idleGeneration++
	if s.idle != nil {
		s.idle.Stop()
		s.idle = nil
	}
	m.mu.Unlock()
	select {
	case s.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-s.gate
			m.release(s, 0)
			return nil, err
		}
		return s, nil
	case <-ctx.Done():
		m.release(s, 0)
		return nil, ctx.Err()
	}
}

func (m *Manager) release(s *pooledSession, idle time.Duration) {
	m.mu.Lock()
	s.active--
	if m.sessions[s.key] != s || s.active != 0 {
		m.mu.Unlock()
		return
	}
	if idle == 0 {
		m.mu.Unlock()
		m.dropSession(s.key, s)
		return
	}
	s.idleGeneration++
	generation := s.idleGeneration
	s.idle = time.AfterFunc(idle, func() { m.expireIdle(s, generation) })
	m.mu.Unlock()
}

func (m *Manager) expireIdle(s *pooledSession, generation uint64) {
	m.mu.Lock()
	if m.sessions[s.key] != s || s.active != 0 || s.idleGeneration != generation {
		m.mu.Unlock()
		return
	}
	delete(m.sessions, s.key)
	s.cancel()
	live := s.live
	closing := m.registerCloseLocked(s)
	m.mu.Unlock()
	live.close()
	m.finishClose(closing)
}

func (m *Manager) dropSession(key sessionKey, s *pooledSession) {
	m.mu.Lock()
	if m.sessions[key] == s {
		delete(m.sessions, key)
	}
	s.idleGeneration++
	if s.idle != nil {
		s.idle.Stop()
		s.idle = nil
	}
	s.cancel()
	live := s.live
	closing := m.registerCloseLocked(s)
	m.mu.Unlock()
	live.close()
	m.finishClose(closing)
}

// Cleanup remains registered after removal from the pool, so an idle expiry
// racing a mutation/shutdown cannot hide a still-closing process or connection.
func (m *Manager) registerCloseLocked(s *pooledSession) *operation {
	op := &operation{key: s.key, cancel: s.cancel, done: make(chan struct{})}
	m.operations[op] = struct{}{}
	return op
}

func (m *Manager) finishClose(op *operation) {
	m.mu.Lock()
	delete(m.operations, op)
	close(op.done)
	m.signalLocked()
	m.mu.Unlock()
}

func (m *Manager) Invoke(ctx context.Context, invocation Invocation) (Result, error) {
	invocation.Activation = invocation.Activation.Clone()
	invocation.Arguments = append(json.RawMessage(nil), invocation.Arguments...)
	if len(invocation.Arguments) == 0 {
		invocation.Arguments = json.RawMessage(`{}`)
	}
	if err := validateInvocation(invocation); err != nil {
		return Result{}, err
	}
	key := sessionKey{runID: invocation.RunID, serverID: invocation.Activation.ServerID}
	ctx, finish, err := m.begin(ctx, key)
	if err != nil {
		return Result{}, err
	}
	defer finish()
	if verificationErr := m.VerifyActivation(ctx, invocation.Activation); verificationErr != nil {
		return Result{}, verificationErr
	}
	s, err := m.acquire(ctx, key, invocation.Activation)
	if err != nil {
		return Result{}, err
	}
	idle := time.Duration(0)
	defer func() { <-s.gate; m.release(s, idle) }()
	// Cancel a pooled transport on every active-call cancellation, not merely
	// the SDK's request. Stop and drain this callback before returning success.
	cancelDone := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { m.dropSession(key, s); close(cancelDone) })
	defer func() {
		if !stop() {
			<-cancelDone
		}
	}()
	m.mu.Lock()
	live := s.live
	valid := m.sessions[key] == s
	m.mu.Unlock()
	if !valid {
		return Result{}, ErrActivationChanged
	}
	if live == nil {
		live, err = m.connect(ctx, s.lifetime, invocation.Activation, func() { go m.dropSession(key, s) })
		if err != nil {
			m.dropSession(key, s)
			return Result{}, err
		}
		m.mu.Lock()
		valid = m.sessions[key] == s && ctx.Err() == nil && s.lifetime.Err() == nil
		if valid {
			s.live = live
		}
		m.mu.Unlock()
		if !valid {
			live.close()
			return Result{}, preferContext(ctx, ErrActivationChanged)
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, invocation.Activation.Limits.CallTimeout)
	defer cancel()
	deadlineDone := make(chan struct{})
	stopDeadline := context.AfterFunc(callCtx, func() { m.dropSession(key, s); close(deadlineDone) })
	defer func() {
		if !stopDeadline() {
			<-deadlineDone
		}
	}()
	catalog, err := discoverCatalog(callCtx, live.client)
	if err != nil {
		m.dropSession(key, s)
		return Result{}, safeOperationError("live tool check", preferContext(callCtx, err), live.sensitiveValues())
	}
	var selected *ToolDescriptor
	for i := range catalog.Tools {
		if catalog.Tools[i].Name == invocation.RemoteTool {
			selected = &catalog.Tools[i]
			break
		}
	}
	if selected == nil || selected.DescriptorDigest != invocation.DescriptorDigest {
		m.dropSession(key, s)
		return Result{}, ErrCatalogStale
	}
	if argumentsErr := validateToolArguments(invocation.Arguments, selected.InputSchema); argumentsErr != nil {
		m.dropSession(key, s)
		return Result{}, ErrSchema
	}
	if verificationErr := m.VerifyActivation(callCtx, invocation.Activation); verificationErr != nil {
		m.dropSession(key, s)
		return Result{}, verificationErr
	}
	response, err := live.client.CallTool(callCtx, &sdk.CallToolParams{Name: invocation.RemoteTool, Arguments: invocation.Arguments})
	if err != nil {
		m.dropSession(key, s)
		return Result{}, safeOperationError("tool call", preferContext(callCtx, err), live.sensitiveValues())
	}
	if response == nil {
		m.dropSession(key, s)
		return Result{}, ErrProtocol
	}
	if len(selected.OutputSchema) != 0 && !response.IsError && response.StructuredContent == nil {
		m.dropSession(key, s)
		return Result{}, ErrSchema
	}
	if response.StructuredContent != nil {
		raw, err := json.Marshal(response.StructuredContent)
		if err != nil || validateStructuredContent(raw, selected.OutputSchema) != nil {
			m.dropSession(key, s)
			return Result{}, ErrSchema
		}
	}
	if err := callCtx.Err(); err != nil {
		m.dropSession(key, s)
		return Result{}, err
	}
	result := boundResult(key.serverID, invocation.RemoteTool, response, invocation.Activation.Limits.MaxResultBytes, live.sensitiveValues())
	idle = invocation.Activation.Limits.IdleTimeout
	return result, nil
}

func validateInvocation(invocation Invocation) error {
	if err := invocation.Activation.validate(true); err != nil {
		return err
	}
	if invocation.RunID == "" || invocation.RemoteTool == "" || invocation.DescriptorDigest == "" {
		return ErrInvalidActivation
	}
	if len(invocation.Arguments) > maxArgumentsBytes {
		return ErrSchema
	}
	// Identity is authoritative and cannot be clipped to make a result fit.
	identity, err := json.Marshal(Result{ServerID: invocation.Activation.ServerID, Tool: invocation.RemoteTool, Text: []string{}, Resources: []Resource{}, Omitted: []Omitted{}})
	if err != nil || len(identity) > invocation.Activation.Limits.MaxResultBytes {
		return ErrSchema
	}
	var object map[string]any
	if json.Unmarshal(invocation.Arguments, &object) != nil || object == nil {
		return ErrSchema
	}
	canonical, err := makeCatalog("", "", "", append([]ToolDescriptor(nil), invocation.Activation.Tools...))
	if err != nil || canonical.Digest != invocation.Activation.CatalogDigest {
		return ErrCatalogStale
	}
	for _, tool := range canonical.Tools {
		if tool.Name == invocation.RemoteTool && tool.DescriptorDigest == invocation.DescriptorDigest {
			return nil
		}
	}
	return ErrCatalogStale
}

func (m *Manager) connect(ctx, lifetime context.Context, activation Activation, toolsChanged func()) (*liveSession, error) {
	startup, startupCancel := context.WithTimeout(ctx, activation.Limits.StartupTimeout)
	defer startupCancel()
	lifetime, cancel := context.WithCancel(lifetime)
	stopStartup := context.AfterFunc(startup, cancel)
	defer stopStartup()
	live := &liveSession{cancel: cancel, sensitive: &sensitiveValues{}}
	var transport sdk.Transport
	switch activation.Transport {
	case TransportStdio:
		config, err := buildStdioTransport(startup, lifetime, activation, m.resolver)
		if err != nil {
			cancel()
			return nil, safeOperationError("stdio activation", preferContext(startup, err), nil)
		}
		transport, live.stderr = config.transport, config.stderr
		live.sensitive.add(config.sensitive...)
	case TransportStreamableHTTP:
		config, err := buildHTTPTransport(startup, lifetime, activation, m.resolver, m.oauthCoordinator, m)
		if err != nil {
			cancel()
			return nil, safeOperationError("HTTP activation", preferContext(startup, err), nil)
		}
		transport, live.cleanup, live.sensitive = config.transport, config.cleanup, config.sensitive
		live.beginClose = config.beginClose
	default:
		cancel()
		return nil, ErrInvalidActivation
	}
	options := &sdk.ClientOptions{Capabilities: &sdk.ClientCapabilities{}, MultiRoundTrip: &sdk.MultiRoundTripOptions{Disabled: true}}
	if toolsChanged != nil {
		options.ToolListChangedHandler = func(context.Context, *sdk.ToolListChangedRequest) { toolsChanged() }
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "nocx", Version: "1"}, options)
	session, err := client.Connect(startup, transport, nil)
	if err != nil {
		live.close()
		return nil, safeOperationError("server activation", preferContext(startup, err), live.sensitiveValues(), live.stderr)
	}
	live.client = session
	if err := startup.Err(); err != nil {
		live.close()
		return nil, err
	}
	if toolsChanged != nil {
		go func() { _ = session.Wait(); toolsChanged() }()
	}
	return live, nil
}

func discoverCatalog(ctx context.Context, session *sdk.ClientSession) (Catalog, error) {
	initialize := session.InitializeResult()
	if initialize == nil {
		return Catalog{}, ErrProtocol
	}
	serverName, serverVersion := "", ""
	if initialize.ServerInfo != nil {
		serverName, serverVersion = initialize.ServerInfo.Name, initialize.ServerInfo.Version
	}
	tools := make([]ToolDescriptor, 0)
	seenCursors := make(map[string]bool)
	cursor := ""
	total := 0
	for {
		page, err := session.ListTools(ctx, &sdk.ListToolsParams{Cursor: cursor})
		if err != nil {
			return Catalog{}, err
		}
		if page == nil {
			return Catalog{}, ErrProtocol
		}
		for _, tool := range page.Tools {
			if tool == nil {
				return Catalog{}, ErrProtocol
			}
			input, err := json.Marshal(tool.InputSchema)
			if err != nil {
				return Catalog{}, ErrSchema
			}
			var output json.RawMessage
			if tool.OutputSchema != nil {
				output, err = json.Marshal(tool.OutputSchema)
				if err != nil {
					return Catalog{}, ErrSchema
				}
			}
			total += len(tool.Name) + len(tool.Description) + len(input) + len(output)
			if total > profile.MaxMCPCatalogBytes || len(tools) == profile.MaxMCPToolsPerServer {
				return Catalog{}, ErrSchema
			}
			tools = append(tools, ToolDescriptor{Name: tool.Name, Description: tool.Description, InputSchema: input, OutputSchema: output})
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
		if seenCursors[cursor] || len(seenCursors) >= profile.MaxMCPToolsPerServer || len(cursor) > 4096 {
			return Catalog{}, ErrProtocol
		}
		seenCursors[cursor] = true
	}
	catalog, err := makeCatalog(serverName, serverVersion, initialize.ProtocolVersion, tools)
	if err != nil {
		return Catalog{}, ErrSchema
	}
	return catalog, nil
}

func preferContext(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func safeOperationError(operation string, err error, sensitive []string, diagnostics ...*boundedStderr) error {
	if err == nil {
		return nil
	}
	for _, sentinel := range []error{
		context.Canceled, context.DeadlineExceeded, ErrClosed, ErrActivationChanged, ErrCatalogStale, ErrInvalidActivation,
		ErrDestinationRefused, ErrSecretUnavailable, ErrOAuthReconnectRequired, ErrResponseTooLarge, ErrFrameTooLarge, ErrProtocol, ErrSchema, ErrServerUnavailable, ErrServerFailure,
	} {
		if errors.Is(err, sentinel) {
			return fmt.Errorf("MCP %s: %w", operation, sentinel)
		}
	}
	kind := ErrProtocol
	var remote *jsonrpc.Error
	var network *net.OpError
	switch {
	case errors.Is(err, httppolicy.ErrMCPDestination):
		kind = ErrDestinationRefused
	case errors.As(err, &remote):
		// Invalid request/params/method and parse errors are protocol failures.
		if remote.Code <= -32000 && remote.Code >= -32099 || remote.Code == -32603 {
			kind = ErrServerFailure
		}
	case errors.As(err, &network), errors.Is(err, io.EOF), errors.Is(err, sdk.ErrConnectionClosed), errors.Is(err, sdk.ErrSessionMissing):
		kind = ErrServerUnavailable
	}
	message := "MCP " + operation
	if len(diagnostics) > 0 && diagnostics[0] != nil {
		if tail := diagnostics[0].safeTail(sensitive); tail != "" {
			message += ": " + tail
		}
	}
	return fmt.Errorf("%s: %w", message, kind)
}

// cancelMatchingLocked bars have already been installed before this call.
// Cancellation precedes every wait, including a resolver waiting for unlock.
func (m *Manager) cancelMatchingLocked(matches func(sessionKey) bool) ([]*operation, []*pooledSession) {
	var operations []*operation
	for op := range m.operations {
		if matches(op.key) {
			op.cancel()
			operations = append(operations, op)
		}
	}
	var sessions []*pooledSession
	for key, s := range m.sessions {
		if matches(key) {
			delete(m.sessions, key)
			s.cancel()
			s.idleGeneration++
			if s.idle != nil {
				s.idle.Stop()
				s.idle = nil
			}
			sessions = append(sessions, s)
		}
	}
	return operations, sessions
}

func (m *Manager) drain(ctx context.Context, operations []*operation, sessions []*pooledSession) error {
	closed := make(chan struct{})
	go func() {
		for _, s := range sessions {
			m.dropSession(s.key, s)
		}
		close(closed)
	}()
	select {
	case <-closed:
	case <-ctx.Done():
		return ctx.Err()
	}
	for _, op := range operations {
		select {
		case <-op.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return ctx.Err()
}

func (m *Manager) RunServerMutations(ctx context.Context, ids []string, mutation func(context.Context) error) error {
	if mutation == nil {
		return ErrInvalidActivation
	}
	ids = append([]string(nil), ids...)
	sort.Strings(ids)
	unique := ids[:0]
	for _, id := range ids {
		if id == "" {
			return ErrInvalidActivation
		}
		if len(unique) == 0 || unique[len(unique)-1] != id {
			unique = append(unique, id)
		}
	}
	ids = unique
	for {
		m.mu.Lock()
		if err := ctx.Err(); err != nil {
			m.mu.Unlock()
			return err
		}
		if m.closed {
			m.mu.Unlock()
			return ErrClosed
		}
		blocked := false
		for _, id := range ids {
			if m.barriers[id] != 0 {
				blocked = true
				break
			}
		}
		if !blocked {
			break
		}
		changed := m.changed
		m.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	for _, id := range ids {
		m.barriers[id]++
	}
	matches := func(key sessionKey) bool {
		i := sort.SearchStrings(ids, key.serverID)
		return i < len(ids) && ids[i] == key.serverID
	}
	operations, sessions := m.cancelMatchingLocked(matches)
	work, cancel := context.WithCancel(ctx)
	// Track the mutation itself for shutdown, but never include it in its own drain.
	op := &operation{cancel: cancel, done: make(chan struct{})}
	m.operations[op] = struct{}{}
	m.mu.Unlock()
	defer func() {
		cancel()
		m.mu.Lock()
		for _, id := range ids {
			m.barriers[id]--
			if m.barriers[id] == 0 {
				delete(m.barriers, id)
			}
		}
		delete(m.operations, op)
		close(op.done)
		m.signalLocked()
		m.mu.Unlock()
	}()
	if err := m.drain(work, operations, sessions); err != nil {
		return err
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if err := work.Err(); err != nil {
		return err
	}
	return mutation(work)
}

func (m *Manager) CloseRun(runID string) {
	if runID == "" {
		return
	}
	m.mu.Lock()
	m.runBarriers[runID]++
	operations, sessions := m.cancelMatchingLocked(func(key sessionKey) bool { return key.runID == runID && key.serverID != "" })
	m.mu.Unlock()
	_ = m.drain(context.Background(), operations, sessions)
	m.mu.Lock()
	m.runBarriers[runID]--
	if m.runBarriers[runID] == 0 {
		delete(m.runBarriers, runID)
	}
	m.signalLocked()
	m.mu.Unlock()
}

func (m *Manager) CloseServer(serverID string) {
	_ = m.RunServerMutations(context.Background(), []string{serverID}, func(context.Context) error { return nil })
}

func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		done := m.closeDone
		m.mu.Unlock()
		<-done
		return nil
	}
	m.closed = true
	operations, sessions := m.cancelMatchingLocked(func(sessionKey) bool { return true })
	m.signalLocked()
	m.mu.Unlock()
	_ = m.drain(context.Background(), operations, sessions)
	close(m.closeDone)
	return nil
}

var (
	_ Runtime         = (*Manager)(nil)
	_ MutationRunner  = (*Manager)(nil)
	_ OperationRunner = (*Manager)(nil)
)
