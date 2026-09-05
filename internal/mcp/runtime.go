package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	sessions         map[sessionKey]*pooledSession
	serverLocks      map[string]*sync.RWMutex
	oauthCoordinator *oauthRefreshCoordinator
	closed           bool
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
	if s.processCancel != nil {
		_ = s.client.Close()
		s.processCancel()
		s.cancel()
	} else {
		s.cancel()
		_ = s.client.Close()
	}
	if s.cleanup != nil {
		s.cleanup()
	}
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
		oauthCoordinator: newOAuthRefreshCoordinator(),
		resolver:         resolver,
	}
	for _, option := range options {
		if option != nil {
			option(manager)
		}
	}
	return manager
}

func NewRuntime(resolver SecretResolver) Runtime { return NewManager(resolver) }

func (m *Manager) Refresh(ctx context.Context, activation Activation) (Catalog, error) {
	activation = activation.clone()
	if err := activation.validate(false); err != nil {
		return Catalog{}, err
	}
	if err := m.verify(ctx, activation); err != nil {
		return Catalog{}, err
	}
	live, err := m.connect(ctx, activation, nil)
	if err != nil {
		return Catalog{}, err
	}
	if verifyErr := m.verify(ctx, activation); verifyErr != nil {
		live.close()
		return Catalog{}, verifyErr
	}
	defer live.close()
	listCtx, cancel := context.WithTimeout(ctx, activation.Limits.CallTimeout)
	defer cancel()
	catalog, err := discoverCatalog(listCtx, live.client)
	if err != nil {
		return Catalog{}, safeOperationError("tool discovery", err, live.sensitiveValues())
	}
	return catalog, nil
}

func (m *Manager) Invoke(ctx context.Context, invocation Invocation) (Result, error) {
	invocation.Activation = invocation.Activation.clone()
	invocation.Arguments = append(json.RawMessage(nil), invocation.Arguments...)
	if err := validateInvocation(invocation); err != nil {
		return Result{}, err
	}
	if err := m.verify(ctx, invocation.Activation); err != nil {
		return Result{}, err
	}
	serverGate := m.serverGate(invocation.Activation.ServerID)
	serverGate.RLock()
	defer serverGate.RUnlock()
	if err := m.verify(ctx, invocation.Activation); err != nil {
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
	defer pooled.opMu.Unlock()
	pooled.stopIdle()
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
	found := false
	for _, tool := range invocation.Activation.Tools {
		if tool.Name == invocation.RemoteTool {
			found = true
			if tool.DescriptorDigest != invocation.DescriptorDigest || descriptorDigest(tool) != invocation.DescriptorDigest {
				return ErrCatalogStale
			}
			break
		}
	}
	if !found {
		return ErrCatalogStale
	}
	if expectedCatalogDigest(invocation.Activation.Tools) != invocation.Activation.CatalogDigest {
		return ErrCatalogStale
	}
	return nil
}

func expectedCatalogDigest(tools []ToolDescriptor) string {
	copyTools := append([]ToolDescriptor(nil), tools...)
	catalog, err := makeCatalog("", "", "", copyTools)
	if err != nil {
		return ""
	}
	return catalog.Digest
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

func (p *pooledSession) stopIdle() {
	p.stateMu.Lock()
	if p.idle != nil {
		p.idle.Stop()
		p.idle = nil
	}
	p.stateMu.Unlock()
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
		live.close()
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
		transport, sensitive = config.transport, config.sensitive
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
		return nil, safeOperationError("server activation", err, sensitive)
	}
	return &liveSession{
		client:        session,
		cancel:        cancelLifetime,
		processCancel: processCancel,
		cleanup:       cleanup,
		sensitive:     sensitive,
		sensitiveRef:  sensitiveRef,
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

func safeOperationError(operation string, err error, sensitive []string) error {
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
	// SDK, subprocess, and remote protocol errors may echo request headers,
	// arguments, or stderr. They are intentionally collapsed at this boundary.
	_ = sensitive
	return fmt.Errorf("MCP %s failed", operation)
}

func (m *Manager) CloseRun(runID string) {
	m.closeMatching(func(key sessionKey) bool { return key.runID == runID })
}

func (m *Manager) CloseServer(serverID string) {
	gate := m.serverGate(serverID)
	gate.Lock()
	defer gate.Unlock()
	m.closeMatching(func(key sessionKey) bool { return key.serverID == serverID })
}

func (m *Manager) RunServerMutation(serverID string, mutation func() error) error {
	gate := m.serverGate(serverID)
	gate.Lock()
	defer gate.Unlock()
	if err := mutation(); err != nil {
		return err
	}
	m.closeMatching(func(key sessionKey) bool { return key.serverID == serverID })
	return nil
}

// CloseServers closes every live MCP session without permanently stopping the
// process-lifetime manager. Vault reset uses this after removing credentials.
func (m *Manager) CloseServers() {
	m.closeMatching(func(sessionKey) bool { return true })
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

func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	closing := make([]*pooledSession, 0, len(m.sessions))
	for key, session := range m.sessions {
		delete(m.sessions, key)
		closing = append(closing, session)
	}
	m.mu.Unlock()
	for _, session := range closing {
		session.close()
	}
	return nil
}

var _ Runtime = (*Manager)(nil)
