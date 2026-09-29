package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type sessionKey struct {
	runID    string
	serverID string
}

type ActivationVerifier func(context.Context, Activation) error

type ManagerOption func(*Manager)

func WithActivationVerifier(verifier ActivationVerifier) ManagerOption {
	return func(manager *Manager) {
		manager.verifier = verifier
	}
}

type Manager struct {
	mu               sync.Mutex
	resetMu          sync.Mutex
	sessions         map[sessionKey]*pooledSession
	serverLocks      map[string]*sync.RWMutex
	discoveryLocks   map[string]*discoveryLifecycle
	oauthCoordinator *oauthRefreshCoordinator
	closed           bool
	resetting        bool
	resetEpoch       uint64
	closeDone        chan struct{}
	resolver         SecretResolver
	verifier         ActivationVerifier
}
type liveSession struct {
	client        *sdk.ClientSession
	cancel        context.CancelFunc
	processCancel context.CancelFunc
	cleanup       func()
	sensitive     []string
	sensitiveRef  *[]string
	stderr        *boundedStderr
}

type discoveryLifecycle struct {
	mutationMu  sync.Mutex
	mu          sync.Mutex
	cond        *sync.Cond
	mutating    bool
	discoveries map[uint64]context.CancelFunc
	nextID      uint64
}

func newDiscoveryLifecycle() *discoveryLifecycle {
	lifecycle := &discoveryLifecycle{discoveries: make(map[uint64]context.CancelFunc)}
	lifecycle.cond = sync.NewCond(&lifecycle.mu)
	return lifecycle
}

func (l *discoveryLifecycle) begin(ctx context.Context) (context.Context, func(), error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.mutating {
		return nil, nil, ErrActivationChanged
	}
	discoveryCtx, cancel := context.WithCancel(ctx)
	l.nextID++
	id := l.nextID
	l.discoveries[id] = cancel
	return discoveryCtx, func() {
		cancel()
		l.mu.Lock()
		delete(l.discoveries, id)
		l.cond.Broadcast()
		l.mu.Unlock()
	}, nil
}

func (l *discoveryLifecycle) beginMutation() func() {
	l.mutationMu.Lock()
	l.mu.Lock()
	l.mutating = true
	for _, cancel := range l.discoveries {
		cancel()
	}
	for len(l.discoveries) != 0 {
		l.cond.Wait()
	}
	l.mu.Unlock()
	return func() {
		l.mu.Lock()
		l.mutating = false
		l.cond.Broadcast()
		l.mu.Unlock()
		l.mutationMu.Unlock()
	}
}

func (s *liveSession) sensitiveValues() []string {
	if s != nil && s.sensitiveRef != nil {
		return *s.sensitiveRef
	}
	if s == nil {
		return nil
	}
	return s.sensitive
}

func (s *liveSession) close() {
	if s == nil {
		return
	}
	s.closeTransport()
	s.clearSensitive()
}

func (s *liveSession) closeTransport() {
	if s.processCancel != nil {
		if s.client != nil {
			_ = s.client.Close()
		}
		s.processCancel()
		if s.cancel != nil {
			s.cancel()
		}
	} else {
		if s.cancel != nil {
			s.cancel()
		}
		if s.client != nil {
			_ = s.client.Close()
		}
	}
	if s.cleanup != nil {
		s.cleanup()
	}
}

func (s *liveSession) clearSensitive() {
	clear(s.sensitiveValues())
	clear(s.sensitive)
}

type pooledSession struct {
	identity   string
	activation Activation

	stateMu       sync.Mutex
	opMu          sync.Mutex
	live          *liveSession
	connecting    bool
	connectCancel context.CancelFunc
	ready         chan struct{}
	connectErr    error
	closed        bool
	idle          *time.Timer
}

type MutationRunner interface {
	RunServerMutation(string, func() error) error
}

func NewManager(resolver SecretResolver, options ...ManagerOption) *Manager {
	manager := &Manager{
		sessions:         make(map[sessionKey]*pooledSession),
		serverLocks:      make(map[string]*sync.RWMutex),
		discoveryLocks:   make(map[string]*discoveryLifecycle),
		oauthCoordinator: newOAuthRefreshCoordinator(),
		closeDone:        make(chan struct{}),
		resolver:         resolver,
	}
	for _, option := range options {
		if option != nil {
			option(manager)
		}
	}
	return manager
}

func (m *Manager) Refresh(ctx context.Context, activation Activation) (Catalog, error) {
	activation = activation.clone()
	if err := activation.validate(false); err != nil {
		return Catalog{}, err
	}
	discoveryCtx, finish, err := m.beginDiscovery(ctx, activation.ServerID)
	if err != nil {
		return Catalog{}, err
	}
	defer finish()
	if err = m.verify(discoveryCtx, activation); err != nil {
		return Catalog{}, err
	}
	live, err := m.connect(discoveryCtx, activation, nil)
	if err != nil {
		return Catalog{}, err
	}
	defer live.close()
	if verifyErr := m.verify(discoveryCtx, activation); verifyErr != nil {
		return Catalog{}, verifyErr
	}
	listCtx, cancel := context.WithTimeout(discoveryCtx, activation.Limits.CallTimeout)
	defer cancel()
	catalog, err := discoverCatalog(listCtx, live.client)
	if err != nil {
		return Catalog{}, safeOperationError("tool discovery", err, live.sensitiveValues(), live.stderr)
	}
	return catalog, nil
}

func (m *Manager) Invoke(ctx context.Context, invocation Invocation) (Result, error) {
	invocation.Activation = invocation.Activation.clone()
	invocation.Arguments = append(json.RawMessage(nil), invocation.Arguments...)
	if err := validateInvocation(invocation); err != nil {
		return Result{}, err
	}
	epoch, err := m.beginInvoke()
	if err != nil {
		return Result{}, err
	}
	if err = m.verify(ctx, invocation.Activation); err != nil {
		return Result{}, err
	}
	serverGate := m.serverGate(invocation.Activation.ServerID)
	serverGate.RLock()
	defer serverGate.RUnlock()
	if err = m.checkInvokeEpoch(epoch); err != nil {
		return Result{}, err
	}
	if err = m.verify(ctx, invocation.Activation); err != nil {
		return Result{}, err
	}
	key := sessionKey{runID: invocation.RunID, serverID: invocation.Activation.ServerID}
	pooled, err := m.sessionFor(key, invocation.Activation)
	if err != nil {
		return Result{}, err
	}
	live, err := pooled.ensure(ctx, func(connectCtx context.Context) (*liveSession, error) {
		return m.connect(connectCtx, invocation.Activation, func() {
			go m.dropSession(key, pooled)
		})
	})
	if err != nil {
		m.dropSession(key, pooled)
		return Result{}, err
	}

	pooled.opMu.Lock()
	defer func() {
		pooled.opMu.Unlock()
		pooled.clearSensitiveIfClosed(live)
	}()
	if !pooled.isLive(live) {
		return Result{}, ErrClosed
	}
	if contextErr := ctx.Err(); contextErr != nil {
		m.dropSession(key, pooled)
		return Result{}, contextErr
	}
	callCtx, cancel := context.WithTimeout(ctx, invocation.Activation.Limits.CallTimeout)
	defer cancel()
	catalog, err := discoverCatalog(callCtx, live.client)
	if err != nil {
		m.dropSession(key, pooled)
		return Result{}, safeOperationError("live tool check", err, live.sensitiveValues())
	}
	var liveTool *ToolDescriptor
	for i := range catalog.Tools {
		if catalog.Tools[i].Name == invocation.RemoteTool {
			liveTool = &catalog.Tools[i]
			break
		}
	}
	if liveTool == nil || liveTool.DescriptorDigest != invocation.DescriptorDigest {
		m.dropSession(key, pooled)
		return Result{}, ErrCatalogStale
	}

	err = validateToolArguments(invocation.Arguments, liveTool.InputSchema)
	if err != nil {
		return Result{}, err
	}
	if verifyErr := m.verify(callCtx, invocation.Activation); verifyErr != nil {
		m.dropSession(key, pooled)
		return Result{}, verifyErr
	}
	response, err := live.client.CallTool(callCtx, &sdk.CallToolParams{
		Name:      invocation.RemoteTool,
		Arguments: invocation.Arguments,
	})
	if err != nil {
		m.dropSession(key, pooled)
		return Result{}, safeOperationError("tool call", err, live.sensitiveValues())
	}
	if response != nil && response.StructuredContent != nil {
		structured, err := json.Marshal(response.StructuredContent)
		if err != nil {
			m.dropSession(key, pooled)
			return Result{}, safeOperationError("result schema", errors.New("structured content cannot be encoded"), live.sensitiveValues())
		}
		if err := validateStructuredContent(structured, liveTool.OutputSchema); err != nil {
			m.dropSession(key, pooled)
			return Result{}, safeOperationError("result schema", err, live.sensitiveValues())
		}
	}
	result := boundResult(invocation.Activation.ServerID, invocation.RemoteTool, response, invocation.Activation.Limits.MaxResultBytes, live.sensitiveValues())
	if invocation.Activation.Limits.IdleTimeout == 0 {
		m.dropSession(key, pooled)
	} else {
		pooled.armIdle(invocation.Activation.Limits.IdleTimeout, func() { m.dropSession(key, pooled) })
	}
	return result, nil
}

func (m *Manager) verify(ctx context.Context, activation Activation) error {
	if m.verifier == nil {
		return nil
	}
	if err := m.verifier(ctx, activation); err != nil {
		return err
	}
	return nil
}

func (m *Manager) serverGate(serverID string) *sync.RWMutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	gate := m.serverLocks[serverID]
	if gate == nil {
		gate = new(sync.RWMutex)
		m.serverLocks[serverID] = gate
	}
	return gate
}

func (m *Manager) beginInvoke() (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return 0, ErrClosed
	}
	if m.resetting {
		return 0, ErrActivationChanged
	}
	return m.resetEpoch, nil
}

func (m *Manager) checkInvokeEpoch(epoch uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	if m.resetting || m.resetEpoch != epoch {
		return ErrActivationChanged
	}
	return nil
}

func (m *Manager) serverGateSnapshotLocked() []*sync.RWMutex {
	serverIDs := make([]string, 0, len(m.serverLocks))
	for serverID := range m.serverLocks {
		serverIDs = append(serverIDs, serverID)
	}
	sort.Strings(serverIDs)
	gates := make([]*sync.RWMutex, 0, len(serverIDs))
	for _, serverID := range serverIDs {
		gates = append(gates, m.serverLocks[serverID])
	}
	return gates
}

func lockServerGates(gates []*sync.RWMutex) func() {
	for _, gate := range gates {
		gate.Lock()
	}
	return func() {
		for i := len(gates) - 1; i >= 0; i-- {
			gates[i].Unlock()
		}
	}
}

func (m *Manager) discoveryLifecycle(serverID string) *discoveryLifecycle {
	m.mu.Lock()
	defer m.mu.Unlock()
	lifecycle := m.discoveryLocks[serverID]
	if lifecycle == nil {
		lifecycle = newDiscoveryLifecycle()
		m.discoveryLocks[serverID] = lifecycle
	}
	return lifecycle
}

func (m *Manager) beginDiscovery(ctx context.Context, serverID string) (context.Context, func(), error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, nil, ErrClosed
	}
	if m.resetting {
		m.mu.Unlock()
		return nil, nil, ErrActivationChanged
	}
	lifecycle := m.discoveryLocks[serverID]
	if lifecycle == nil {
		lifecycle = newDiscoveryLifecycle()
		m.discoveryLocks[serverID] = lifecycle
	}
	discoveryCtx, finish, err := lifecycle.begin(ctx)
	m.mu.Unlock()
	return discoveryCtx, finish, err
}

func validateInvocation(invocation Invocation) error {
	if err := invocation.Activation.validate(true); err != nil {
		return err
	}
	if invocation.RunID == "" || invocation.RemoteTool == "" || invocation.DescriptorDigest == "" {
		return fmt.Errorf("%w: invocation identity is incomplete", ErrInvalidActivation)
	}
	if len(invocation.Arguments) == 0 {
		invocation.Arguments = json.RawMessage(`{}`)
	}
	if len(invocation.Arguments) > maxArgumentsBytes {
		return errors.New("MCP tool arguments exceed 64 KiB")
	}
	var object map[string]any
	if err := json.Unmarshal(invocation.Arguments, &object); err != nil || object == nil {
		return errors.New("MCP tool arguments must be a JSON object")
	}
	copyTools := append([]ToolDescriptor(nil), invocation.Activation.Tools...)
	canonical, err := makeCatalog("", "", "", copyTools)
	if err != nil {
		return ErrCatalogStale
	}
	found := false
	for _, tool := range canonical.Tools {
		if tool.Name == invocation.RemoteTool {
			found = true
			if tool.DescriptorDigest != invocation.DescriptorDigest {
				return ErrCatalogStale
			}
			break
		}
	}
	if !found || canonical.Digest != invocation.Activation.CatalogDigest {
		return ErrCatalogStale
	}
	return nil
}

func (m *Manager) sessionFor(key sessionKey, activation Activation) (*pooledSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	if existing := m.sessions[key]; existing != nil {
		if existing.identity != activation.identity() {
			delete(m.sessions, key)
			go existing.close()
			return nil, ErrActivationChanged
		}
		return existing, nil
	}
	pooled := &pooledSession{identity: activation.identity(), activation: activation}
	m.sessions[key] = pooled
	return pooled, nil
}

func (p *pooledSession) ensure(ctx context.Context, connect func(context.Context) (*liveSession, error)) (*liveSession, error) {
	p.stateMu.Lock()
	if p.closed {
		p.stateMu.Unlock()
		return nil, ErrClosed
	}
	if p.live != nil {
		live := p.live
		p.stateMu.Unlock()
		return live, nil
	}
	if p.connecting {
		ready := p.ready
		p.stateMu.Unlock()
		select {
		case <-ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		p.stateMu.Lock()
		defer p.stateMu.Unlock()
		if p.live != nil {
			return p.live, nil
		}
		if p.connectErr != nil {
			return nil, p.connectErr
		}
		return nil, ErrClosed
	}
	p.connecting = true
	p.ready = make(chan struct{})
	ready := p.ready
	connectCtx, cancel := context.WithCancel(ctx)
	p.connectCancel = cancel
	p.stateMu.Unlock()

	live, err := connect(connectCtx)
	p.stateMu.Lock()
	p.connectCancel = nil
	if p.closed && live != nil {
		live.close()
		live = nil
		if err == nil {
			err = ErrClosed
		}
	}
	p.live = live
	p.connectErr = err
	p.connecting = false
	close(ready)
	p.stateMu.Unlock()
	return live, err
}

func (p *pooledSession) armIdle(after time.Duration, close func()) {
	p.stateMu.Lock()
	if !p.closed {
		if p.idle != nil {
			p.idle.Stop()
		}
		p.idle = time.AfterFunc(after, close)
	}
	p.stateMu.Unlock()
}

func (p *pooledSession) close() {
	p.stateMu.Lock()
	if p.closed {
		p.stateMu.Unlock()
		return
	}
	p.closed = true
	if p.idle != nil {
		p.idle.Stop()
		p.idle = nil
	}
	cancelConnect := p.connectCancel
	ready := p.ready
	connecting := p.connecting
	live := p.live
	p.live = nil
	p.stateMu.Unlock()
	if cancelConnect != nil {
		cancelConnect()
	}
	if connecting && ready != nil {
		<-ready
		return
	}
	if live != nil {
		live.closeTransport()
		if p.opMu.TryLock() {
			live.clearSensitive()
			p.opMu.Unlock()
		}
	}
}

func (p *pooledSession) isLive(live *liveSession) bool {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	return !p.closed && p.live == live
}

func (p *pooledSession) clearSensitiveIfClosed(live *liveSession) {
	if live == nil || !p.opMu.TryLock() {
		return
	}
	defer p.opMu.Unlock()
	p.stateMu.Lock()
	closed := p.closed || p.live != live
	p.stateMu.Unlock()
	if closed {
		live.clearSensitive()
	}
}

func (m *Manager) dropSession(key sessionKey, expected *pooledSession) {
	m.mu.Lock()
	if m.sessions[key] == expected {
		delete(m.sessions, key)
	}
	m.mu.Unlock()
	expected.close()
}

func (m *Manager) connect(ctx context.Context, activation Activation, toolsChanged func()) (*liveSession, error) {
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return nil, ErrClosed
	}
	lifetime, cancelLifetime := context.WithCancel(ctx)
	var (
		transport     sdk.Transport
		processCancel context.CancelFunc
		cleanup       func()
		sensitive     []string
		sensitiveRef  *[]string
		stderr        *boundedStderr
	)
	switch activation.Transport {
	case TransportStdio:
		processLifetime, cancelProcess := context.WithCancel(context.Background())
		config, err := buildStdioTransport(ctx, processLifetime, activation, m.resolver)
		if err != nil {
			cancelProcess()
			cancelLifetime()
			return nil, safeOperationError("stdio activation", err, nil)
		}
		processCancel = cancelProcess
		transport, sensitive, stderr = config.transport, config.sensitive, config.stderr
	case TransportStreamableHTTP:
		config, err := buildHTTPTransport(lifetime, activation, m.resolver, m.oauthCoordinator)
		if err != nil {
			cancelLifetime()
			return nil, safeOperationError("HTTP activation", err, nil)
		}
		transport, cleanup, sensitive, sensitiveRef = config.transport, config.cleanup, config.sensitive, config.sensitiveRef
	default:
		cancelLifetime()
		return nil, ErrInvalidActivation
	}
	options := &sdk.ClientOptions{
		Capabilities:   &sdk.ClientCapabilities{},
		MultiRoundTrip: &sdk.MultiRoundTripOptions{Disabled: true},
	}
	if toolsChanged != nil {
		options.ToolListChangedHandler = func(context.Context, *sdk.ToolListChangedRequest) { toolsChanged() }
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "nocx", Version: "1"}, options)
	startupCtx, cancelStartup := context.WithTimeout(lifetime, activation.Limits.StartupTimeout)
	defer cancelStartup()
	session, err := client.Connect(startupCtx, transport, nil)
	if err != nil {
		if processCancel != nil {
			processCancel()
		}
		cancelLifetime()
		if cleanup != nil {
			cleanup()
		}
		return nil, safeOperationError("server activation", err, sensitive, stderr)
	}
	return &liveSession{
		client:        session,
		cancel:        cancelLifetime,
		processCancel: processCancel,
		cleanup:       cleanup,
		sensitive:     sensitive,
		sensitiveRef:  sensitiveRef,
		stderr:        stderr,
	}, nil
}

func discoverCatalog(ctx context.Context, session *sdk.ClientSession) (Catalog, error) {
	initialize := session.InitializeResult()
	if initialize == nil {
		return Catalog{}, errors.New("MCP server did not initialize")
	}
	serverName, serverVersion := "", ""
	if initialize.ServerInfo != nil {
		serverName = initialize.ServerInfo.Name
		serverVersion = initialize.ServerInfo.Version
	}
	tools := make([]ToolDescriptor, 0)
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return Catalog{}, err
		}
		input, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return Catalog{}, errors.New("MCP tool input schema cannot be encoded")
		}
		var output json.RawMessage
		if tool.OutputSchema != nil {
			output, err = json.Marshal(tool.OutputSchema)
			if err != nil {
				return Catalog{}, errors.New("MCP tool output schema cannot be encoded")
			}
		}
		tools = append(tools, ToolDescriptor{
			Name:         tool.Name,
			Description:  tool.Description,
			InputSchema:  input,
			OutputSchema: output,
		})
		if len(tools) > 256 {
			return Catalog{}, errors.New("MCP tool count exceeds its bound")
		}
	}
	return makeCatalog(serverName, serverVersion, initialize.ProtocolVersion, tools)
}

func safeOperationError(operation string, err error, sensitive []string, diagnostics ...*boundedStderr) error {
	if err == nil {
		return nil
	}
	for _, sentinel := range []error{
		context.Canceled, context.DeadlineExceeded, ErrClosed, ErrCatalogStale,
		ErrDestinationRefused, ErrSecretUnavailable, ErrOAuthReconnectRequired,
		ErrResponseTooLarge, ErrFrameTooLarge,
	} {
		if errors.Is(err, sentinel) {
			return fmt.Errorf("MCP %s: %w", operation, sentinel)
		}
	}
	message := fmt.Sprintf("MCP %s failed", operation)
	if len(diagnostics) > 0 && diagnostics[0] != nil {
		if tail := diagnostics[0].safeTail(sensitive); tail != "" {
			message += ": " + tail
		}
	}
	return errors.New(message)
}

func (m *Manager) CloseRun(runID string) {
	m.closeMatching(func(key sessionKey) bool { return key.runID == runID })
}

func (m *Manager) CloseServer(serverID string) {
	finish := m.discoveryLifecycle(serverID).beginMutation()
	defer finish()
	gate := m.serverGate(serverID)
	gate.Lock()
	defer gate.Unlock()
	m.closeMatching(func(key sessionKey) bool { return key.serverID == serverID })
}

func (m *Manager) RunServerMutation(serverID string, mutation func() error) error {
	finish := m.discoveryLifecycle(serverID).beginMutation()
	defer finish()
	gate := m.serverGate(serverID)
	gate.Lock()
	defer gate.Unlock()
	m.closeMatching(func(key sessionKey) bool { return key.serverID == serverID })
	if err := mutation(); err != nil {
		return err
	}
	return nil
}

// CloseServers closes every live MCP session and drains active discovery and
// tool calls without permanently stopping the process-lifetime manager.
func (m *Manager) CloseServers() {
	m.resetMu.Lock()
	defer m.resetMu.Unlock()
	m.mu.Lock()
	m.resetting = true
	m.resetEpoch++
	lifecycles := m.discoverySnapshotLocked()
	gates := m.serverGateSnapshotLocked()
	m.mu.Unlock()

	finish := beginDiscoveryMutations(lifecycles)
	unlockGates := lockServerGates(gates)
	m.closeMatching(func(sessionKey) bool { return true })
	unlockGates()
	finish()
	m.mu.Lock()
	m.resetting = false
	m.mu.Unlock()
}

func (m *Manager) discoverySnapshotLocked() []*discoveryLifecycle {
	lifecycles := make([]*discoveryLifecycle, 0, len(m.discoveryLocks))
	for _, lifecycle := range m.discoveryLocks {
		lifecycles = append(lifecycles, lifecycle)
	}
	return lifecycles
}

func beginDiscoveryMutations(lifecycles []*discoveryLifecycle) func() {
	if len(lifecycles) == 0 {
		return func() {}
	}
	finished := make(chan func(), len(lifecycles))
	for _, lifecycle := range lifecycles {
		go func(lifecycle *discoveryLifecycle) {
			finished <- lifecycle.beginMutation()
		}(lifecycle)
	}
	finishers := make([]func(), 0, len(lifecycles))
	for range lifecycles {
		finishers = append(finishers, <-finished)
	}
	return func() {
		for i := len(finishers) - 1; i >= 0; i-- {
			finishers[i]()
		}
	}
}

func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		done := m.closeDone
		m.mu.Unlock()
		if done != nil {
			<-done
		}
		return nil
	}
	m.closed = true
	m.resetEpoch++
	if m.closeDone == nil {
		m.closeDone = make(chan struct{})
	}
	done := m.closeDone
	m.mu.Unlock()

	m.resetMu.Lock()
	m.mu.Lock()
	lifecycles := m.discoverySnapshotLocked()
	gates := m.serverGateSnapshotLocked()
	m.mu.Unlock()
	finish := beginDiscoveryMutations(lifecycles)
	unlockGates := lockServerGates(gates)
	m.closeMatching(func(sessionKey) bool { return true })
	unlockGates()
	finish()
	m.resetMu.Unlock()
	close(done)
	return nil
}

func (m *Manager) closeMatching(matches func(sessionKey) bool) {
	m.mu.Lock()
	closing := make([]*pooledSession, 0)
	for key, session := range m.sessions {
		if matches(key) {
			delete(m.sessions, key)
			closing = append(closing, session)
		}
	}
	m.mu.Unlock()
	for _, session := range closing {
		session.close()
	}
}

var _ Runtime = (*Manager)(nil)
