package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/mcp"
	"github.com/shady2k/nocx/internal/profile"
)

// MCPOAuthService owns explicit authorization and cleanup of its fresh orphan.
// Metadata and deletion of previously attached material belong to ConfigService.
type MCPOAuthService interface {
	mcp.OAuthService
	mcp.OAuthSessionDiscarder
}

type mcpServices struct {
	runtime   mcp.Runtime
	mutations mcp.MutationRunner
	oauth     MCPOAuthService
}

// WithMCPServices installs one complete runtime boundary. Repositories are bound
// after all options, so option order cannot silently omit the mutation fence.
func WithMCPServices(runtime mcp.Runtime, mutations mcp.MutationRunner, oauth MCPOAuthService) WSServerOption {
	if runtime == nil || mutations == nil || oauth == nil {
		panic("transport: MCP runtime, mutation runner and OAuth service are required together")
	}
	return func(s *WSServer) { s.mcp = &mcpServices{runtime, mutations, oauth} }
}

func (s *WSServer) mcpConfigDependencies() capability.MCPConfigDependencies {
	repo, _ := s.profiles.(capability.MCPConfigRepository)
	secrets, _ := s.vaultLifecycle.(capability.MCPConfigSecrets)
	return capability.MCPConfigDependencies{Repository: repo, Secrets: secrets}
}

// mcpMutations coordinates admissions, never repository or secret decisions.
// The snapshot admission is released before any cancellation/drain. The caller
// verifies the snapshot inside its committing capability admission.
type mcpMutations struct {
	op     capability.ConfigOperation
	runner mcp.MutationRunner
	notify func(mcpServersChangedParams)
}

func (m *mcpMutations) snapshot(ctx context.Context, target capability.MCPMutationTarget) (capability.MCPMutationSnapshot, error) {
	var snapshot capability.MCPMutationSnapshot
	err := m.op.Run(ctx, func(_ context.Context, svc capability.ConfigService) error {
		var err error
		snapshot, err = svc.SnapshotMCPMutation(target)
		return err
	})
	return snapshot, err
}

func (m *mcpMutations) commit(ctx context.Context, snapshot capability.MCPMutationSnapshot, commit func(context.Context) error) error {
	ids := make([]string, len(snapshot.Servers))
	for i, server := range snapshot.Servers {
		ids[i] = server.ID
	}
	return m.runner.RunServerMutations(ctx, ids, commit)
}

func (m *mcpMutations) run(ctx context.Context, target capability.MCPMutationTarget, commit func(context.Context, *capability.MCPMutationSnapshot) error) error {
	// No MCP runtime exists on transports built without WithMCPServices.
	if m == nil {
		return commit(ctx, nil)
	}
	snapshot, err := m.snapshot(ctx, target)
	if err != nil {
		return err
	}
	return m.commit(ctx, snapshot, func(ctx context.Context) error { return commit(ctx, &snapshot) })
}

// A verified aggregate reference sweep advances each affected server exactly
// once. Material-only replacement never calls this notification path.
func (m *mcpMutations) announceCleared(snapshot *capability.MCPMutationSnapshot) {
	if m == nil || snapshot == nil {
		return
	}
	for _, server := range snapshot.ReferenceHolders {
		m.notify(mcpServersChangedParams{ID: server.ID, Revision: server.Revision + 1, Change: "updated"})
	}
}

func answerMCPMutationError(r Responder, req jsonrpcRequest, err error, fallback int, prefix string) {
	if errors.Is(err, profile.ErrMCPServerConflict) {
		_ = r.TryError(req.ID, RPCError{Code: -32602, Message: err.Error(), Data: mcpErrorData{Reason: "conflict"}})
		return
	}
	var refused *capability.RefusedError
	if errors.As(err, &refused) {
		answerOperationRefusal(r, req, err)
		return
	}
	_ = r.TryError(req.ID, rpcErrorFor(fallback, prefix, err))
}

// ReconcileMCPSecrets repairs interrupted metadata-first cleanup under the same
// admission as configuration writes. It never activates MCP or requests unlock.
// The composition root calls it before accepting clients after a restart.
func (s *WSServer) ReconcileMCPSecrets(ctx context.Context) error {
	if s.mcpMutations == nil {
		return nil
	}
	return s.mcpMutations.op.Run(ctx, func(ctx context.Context, svc capability.ConfigService) error {
		return svc.ReconcileMCPSecrets(ctx)
	})
}

// VerifyMCPActivation is the runtime's narrow current-state reader. The manager
// invokes it only after registering cancellation, without config/Vault gates.
func (s *WSServer) VerifyMCPActivation(ctx context.Context, activation mcp.Activation) error {
	if s.mcpMutations == nil {
		return mcp.ErrClosed
	}
	return s.mcpMutations.op.Run(ctx, func(_ context.Context, svc capability.ConfigService) error {
		current, err := svc.GetMCPServer(activation.ServerID)
		if err != nil {
			return err
		}
		if current.Revision != activation.ServerRevision {
			return mcp.ErrActivationChanged
		}
		now, err := mcp.ActivationFromServer(current)
		if err != nil {
			return err
		}
		if !now.SameIdentity(activation) {
			return mcp.ErrActivationChanged
		}
		return nil
	})
}

type mcpServerHandlers struct {
	op        capability.ConfigOperation
	mutations *mcpMutations
	runtime   mcp.Runtime
	oauth     MCPOAuthService
	presenter func() mcp.URLPresenter
	notify    func(mcpServersChangedParams)
	r         Responder
}

type (
	mcpGetParams struct {
		ID string `json:"id"`
	}
	mcpIDRevisionParams struct {
		ID       string `json:"id"`
		Revision uint64 `json:"revision"`
	}
)

type mcpServerUpdateParams struct {
	mcpIDRevisionParams
	capability.MCPServerConfigInput
}
type mcpSetToolsEnabledParams struct {
	mcpIDRevisionParams
	Tools []string `json:"tools"`
}

func (s *WSServer) mcpServerSpecs(op capability.ConfigOperation) []methodSpec {
	wired := s.mcp != nil
	if wired {
		deps := s.mcpConfigDependencies()
		if deps.Repository == nil || deps.Secrets == nil {
			panic("transport: MCP metadata and journaled secrets are required")
		}
		s.mcpMutations = &mcpMutations{op: op, runner: s.mcp.mutations, notify: s.broadcastMCPServersChanged}
	}
	sub := s.operationQueue("mcp-config")
	build := func(r Responder) handlerFunc {
		h := mcpServerHandlers{op: op, mutations: s.mcpMutations, presenter: s.mcpOAuthPresenter, notify: s.broadcastMCPServersChanged, r: r}
		if wired {
			h.runtime, h.oauth = s.mcp.runtime, s.mcp.oauth
		}
		return h.handleMethod
	}
	methods := []struct {
		name     string
		validate paramsValidator
	}{
		{"mcpServers.list", noParams()},
		{"mcpServers.get", params(validateMCPGetRaw)},
		{"mcpServers.create", params(validateMCPCreateRaw)},
		{"mcpServers.update", params(validateMCPUpdateRaw)},
		{"mcpServers.delete", params(validateMCPIDRevisionRaw)},
		{"mcpServers.refresh", params(validateMCPIDRevisionRaw)},
		{"mcpServers.setToolsEnabled", params(validateMCPSetToolsEnabledRaw)},
		{"mcpServers.oauthAuthorize", params(validateMCPIDRevisionRaw)},
		{"mcpServers.oauthForget", params(validateMCPIDRevisionRaw)},
	}
	out := make([]methodSpec, 0, len(methods))
	for _, method := range methods {
		out = append(out, whenAvailable(regResponder(sub, method.name, method.validate, build), func() bool { return wired }, "MCP servers not available"))
	}
	return out
}

func (h mcpServerHandlers) handleMethod(ctx context.Context, req jsonrpcRequest) {
	var result any
	var changed *mcpServersChangedParams
	var operationErr error
	if req.Method == "mcpServers.list" || req.Method == "mcpServers.get" || req.Method == "mcpServers.create" {
		operationErr = h.op.Run(ctx, func(ctx context.Context, svc capability.ConfigService) error {
			switch req.Method {
			case "mcpServers.list":
				servers, err := svc.ListMCPServers()
				if err != nil {
					return err
				}
				rows := make([]mcpServerSummary, len(servers))
				for i, server := range servers {
					rows[i] = summarizeMCPServer(server)
				}
				result = mcpServersListResult{Servers: rows}
			case "mcpServers.get":
				var p mcpGetParams
				_ = json.Unmarshal(req.Params, &p)
				server, err := svc.GetMCPServer(p.ID)
				if err != nil {
					return err
				}
				result = mcpServerResult{Server: wireMCPServer(server)}
			case "mcpServers.create":
				var p capability.MCPServerConfigInput
				_ = json.Unmarshal(req.Params, &p)
				server, err := svc.CreateMCPServer(ctx, p)
				if err != nil {
					return err
				}
				result = mcpServerResult{Server: wireMCPServer(server)}
				changed = &mcpServersChangedParams{ID: server.ID, Revision: server.Revision, Change: "created"}
			}
			return nil
		})
	} else {
		result, changed, operationErr = h.mutate(ctx, req)
	}
	if operationErr != nil {
		h.answerError(req, operationErr)
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(result))
	if changed != nil {
		h.notify(*changed)
	}
}

func (h mcpServerHandlers) mutate(ctx context.Context, req jsonrpcRequest) (any, *mcpServersChangedParams, error) {
	var p mcpIDRevisionParams
	_ = json.Unmarshal(req.Params, &p)
	var current profile.MCPServer
	var snapshot capability.MCPMutationSnapshot
	operationErr := h.op.Run(ctx, func(_ context.Context, svc capability.ConfigService) error {
		var err error
		current, err = svc.GetMCPServer(p.ID)
		if err != nil {
			return err
		}
		if current.Revision != p.Revision {
			return profile.ErrMCPServerConflict
		}
		snapshot, err = svc.SnapshotMCPMutation(capability.MCPMutationTarget{Kind: capability.MCPMutationServer, ServerID: p.ID})
		return err
	})
	if operationErr != nil {
		return nil, nil, operationErr
	}
	var catalog profile.MCPCatalog
	var authorization mcp.OAuthStatus
	if req.Method == "mcpServers.refresh" || req.Method == "mcpServers.oauthAuthorize" {
		activation, activationErr := mcp.ActivationFromServer(current)
		if activationErr != nil {
			return nil, nil, activationErr
		}
		if req.Method == "mcpServers.refresh" {
			discovered, err := h.runtime.Refresh(ctx, activation)
			if err != nil {
				return nil, nil, err
			}
			catalog, err = discovered.ProfileCatalog()
			if err != nil {
				return nil, nil, err
			}
		} else {
			authorization, operationErr = h.oauth.Authorize(ctx, activation, h.presenter())
			if operationErr != nil {
				return nil, nil, operationErr
			}
		}
	}
	var updated profile.MCPServer
	oauthCommitAttempted := false
	operationErr = h.mutations.commit(ctx, snapshot, func(ctx context.Context) error {
		return h.op.Run(ctx, func(ctx context.Context, svc capability.ConfigService) error {
			if err := svc.VerifyMCPMutation(snapshot); err != nil {
				return err
			}
			var err error
			switch req.Method {
			case "mcpServers.update":
				var input mcpServerUpdateParams
				_ = json.Unmarshal(req.Params, &input)
				updated, err = svc.UpdateMCPServer(ctx, p.ID, p.Revision, input.MCPServerConfigInput)
			case "mcpServers.delete":
				err = svc.DeleteMCPServer(ctx, p.ID, p.Revision)
			case "mcpServers.setToolsEnabled":
				var input mcpSetToolsEnabledParams
				_ = json.Unmarshal(req.Params, &input)
				updated, err = svc.SetMCPToolsEnabled(p.ID, p.Revision, input.Tools)
			case "mcpServers.refresh":
				updated, err = svc.RefreshMCPServerCatalog(p.ID, p.Revision, catalog)
			case "mcpServers.oauthAuthorize":
				oauthCommitAttempted = true
				updated, err = svc.CommitMCPOAuth(ctx, p.ID, p.Revision, profile.MCPOAuthConfig{
					Status: profile.MCPOAuthConnected, Issuer: authorization.Issuer,
					GrantedScopes: authorization.Scopes, AccessTokenExpires: authorization.ExpiresAt,
					SessionRef: &profile.MCPSecretBinding{SecretRef: authorization.SessionRef, Owned: true},
				})
			case "mcpServers.oauthForget":
				updated, err = svc.ForgetMCPOAuth(ctx, p.ID, p.Revision)
			}
			return err
		})
	})
	if operationErr != nil {
		if authorization.SessionRef != "" && !oauthCommitAttempted {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = h.oauth.DiscardOAuthSession(cleanup, authorization.SessionRef)
		}
		return nil, nil, operationErr
	}
	if req.Method == "mcpServers.delete" {
		return mcpServerDeleteResult{Deleted: true}, &mcpServersChangedParams{ID: p.ID, Revision: p.Revision + 1, Change: "deleted"}, nil
	}
	change := "updated"
	switch req.Method {
	case "mcpServers.refresh":
		change = "catalog"
	case "mcpServers.setToolsEnabled":
		change = "tools"
	case "mcpServers.oauthAuthorize", "mcpServers.oauthForget":
		change = "oauth"
	}
	return mcpServerResult{Server: wireMCPServer(updated)}, &mcpServersChangedParams{ID: updated.ID, Revision: updated.Revision, Change: change}, nil
}

func (h mcpServerHandlers) answerError(req jsonrpcRequest, err error) {
	switch {
	case errors.Is(err, profile.ErrMCPServerConflict):
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: err.Error(), Data: mcpErrorData{Reason: "conflict"}})
	case errors.Is(err, profile.ErrMCPServerNotFound):
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: err.Error(), Data: mcpErrorData{Reason: "not-found"}})
	case errors.Is(err, profile.ErrMCPToolNotFound):
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: err.Error(), Data: mcpErrorData{Reason: "tool-not-found"}})
	default:
		answerMCPMutationError(h.r, req, err, -32603, "")
	}
}

func (s *WSServer) broadcastMCPServersChanged(change mcpServersChangedParams) {
	payload := mustMarshal(change)
	s.connsMu.Lock()
	conns := make([]*wsConn, 0, len(s.conns))
	for conn := range s.conns {
		conns = append(conns, conn)
	}
	s.connsMu.Unlock()
	for _, conn := range conns {
		_ = conn.TryNotify("mcpServers.changed", payload)
	}
}

type (
	mcpServersListResult struct {
		Servers []mcpServerSummary `json:"servers"`
	}
	mcpServerSummary struct {
		ID               string                      `json:"id"`
		Revision         uint64                      `json:"revision"`
		Name             string                      `json:"name"`
		Enabled          bool                        `json:"enabled"`
		Transport        profile.MCPTransportKind    `json:"transport"`
		CatalogState     profile.MCPCatalogState     `json:"catalogState"`
		ToolCount        int                         `json:"toolCount"`
		EnabledToolCount int                         `json:"enabledToolCount"`
		OAuthStatus      *profile.MCPOAuthStatusKind `json:"oauthStatus"`
	}
)

type (
	mcpServerResult struct {
		Server mcpServerDTO `json:"server"`
	}
	mcpServerDeleteResult struct {
		Deleted bool `json:"deleted"`
	}
	mcpServersChangedParams struct {
		ID       string `json:"id"`
		Revision uint64 `json:"revision"`
		Change   string `json:"change"`
	}
)

type (
	mcpErrorData struct {
		Reason string `json:"reason"`
	}
	mcpServerDTO struct {
		ID        string                   `json:"id"`
		Revision  uint64                   `json:"revision"`
		Name      string                   `json:"name"`
		Enabled   bool                     `json:"enabled"`
		Transport profile.MCPTransportKind `json:"transport"`
		Stdio     *profile.MCPStdioDTO     `json:"stdio"`
		HTTP      *profile.MCPHTTPDTO      `json:"http"`
		Limits    profile.MCPLimits        `json:"limits"`
		Catalog   mcpCatalogDTO            `json:"catalog"`
	}
)

type mcpCatalogDTO struct {
	State           profile.MCPCatalogState `json:"state"`
	ServerName      string                  `json:"serverName"`
	ServerVersion   string                  `json:"serverVersion"`
	ProtocolVersion string                  `json:"protocolVersion"`
	RefreshedAt     *time.Time              `json:"refreshedAt"`
	Digest          string                  `json:"digest"`
	Tools           []mcpToolDTO            `json:"tools"`
}
type mcpToolDTO struct {
	Name             string                `json:"name"`
	Description      string                `json:"description"`
	InputSchema      json.RawMessage       `json:"inputSchema"`
	OutputSchema     json.RawMessage       `json:"outputSchema"`
	DescriptorDigest string                `json:"descriptorDigest"`
	Enabled          bool                  `json:"enabled"`
	Status           profile.MCPToolStatus `json:"status"`
}

func summarizeMCPServer(server profile.MCPServer) mcpServerSummary {
	var oauth *profile.MCPOAuthStatusKind
	if server.HTTP != nil && server.HTTP.OAuth != nil {
		status := server.HTTP.OAuth.Status
		oauth = &status
	}
	enabled := 0
	for _, tool := range server.Catalog.Tools {
		if tool.Enabled {
			enabled++
		}
	}
	return mcpServerSummary{ID: server.ID, Revision: server.Revision, Name: server.Name, Enabled: server.Enabled, Transport: server.Transport, CatalogState: server.Catalog.State, ToolCount: len(server.Catalog.Tools), EnabledToolCount: enabled, OAuthStatus: oauth}
}

func wireMCPServer(server profile.MCPServer) mcpServerDTO {
	sanitized := server.SanitizedDTO()
	tools := make([]mcpToolDTO, len(server.Catalog.Tools))
	for i, tool := range server.Catalog.Tools {
		output := tool.OutputSchema
		if len(output) == 0 {
			output = json.RawMessage("null")
		}
		tools[i] = mcpToolDTO{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema, OutputSchema: output, DescriptorDigest: tool.DescriptorDigest, Enabled: tool.Enabled, Status: tool.Status}
	}
	catalog := mcpCatalogDTO{State: server.Catalog.State, ServerName: server.Catalog.ServerName, ServerVersion: server.Catalog.ServerVersion, ProtocolVersion: server.Catalog.ProtocolVersion, RefreshedAt: server.Catalog.RefreshedAt, Digest: server.Catalog.Digest, Tools: tools}
	return mcpServerDTO{ID: sanitized.ID, Revision: sanitized.Revision, Name: sanitized.Name, Enabled: sanitized.Enabled, Transport: sanitized.Transport, Stdio: sanitized.Stdio, HTTP: sanitized.HTTP, Limits: sanitized.Limits, Catalog: catalog}
}

type mcpURLPresenter struct{ opener UrlOpener }

func (p mcpURLPresenter) PresentURL(ctx context.Context, raw string) error {
	u, ok := browserURL(raw)
	if !ok || u.User != nil || u.Fragment != "" {
		return errors.New("invalid MCP OAuth URL")
	}
	if u.Scheme == "http" {
		host := u.Hostname()
		ip := net.ParseIP(host)
		if !(ip != nil && ip.IsLoopback()) && !strings.EqualFold(host, "localhost") {
			return errors.New("MCP OAuth URL must use HTTPS or loopback HTTP")
		}
	}
	if p.opener == nil {
		return ErrNoURLHost
	}
	return p.opener.OpenURL(ctx, u.String())
}

func (s *WSServer) mcpOAuthPresenter() mcp.URLPresenter {
	s.urlMu.RLock()
	opener := s.urlOpener
	s.urlMu.RUnlock()
	return mcpURLPresenter{opener: opener}
}

func validateMCPGetRaw(raw json.RawMessage) string {
	var p mcpGetParams
	if msg := decodeObject(raw, &p, "id"); msg != "" {
		return msg
	}
	if strings.TrimSpace(p.ID) == "" || len(p.ID) > 128 {
		return "id is required and must not exceed 128 bytes"
	}
	return ""
}

func validateMCPIDRevisionRaw(raw json.RawMessage) string {
	var p mcpIDRevisionParams
	if msg := decodeObject(raw, &p, "id", "revision"); msg != "" {
		return msg
	}
	return validateMCPIDRevision(p)
}

func validateMCPIDRevision(p mcpIDRevisionParams) string {
	if strings.TrimSpace(p.ID) == "" || len(p.ID) > 128 {
		return "id is required and must not exceed 128 bytes"
	}
	if p.Revision == 0 {
		return "revision must be at least 1"
	}
	return ""
}

func validateMCPSetToolsEnabledRaw(raw json.RawMessage) string {
	var p mcpSetToolsEnabledParams
	if msg := decodeObject(raw, &p, "id", "revision", "tools"); msg != "" {
		return msg
	}
	if msg := validateMCPIDRevision(p.mcpIDRevisionParams); msg != "" {
		return msg
	}
	if p.Tools == nil || len(p.Tools) > profile.MaxMCPToolsPerServer {
		return "tools must be a bounded array"
	}
	seen := make(map[string]struct{}, len(p.Tools))
	for _, name := range p.Tools {
		if name == "" || len(name) > 512 {
			return "tool name must be between 1 and 512 bytes"
		}
		if _, ok := seen[name]; ok {
			return "tool names must be unique"
		}
		seen[name] = struct{}{}
	}
	return ""
}

func validateMCPCreateRaw(raw json.RawMessage) string {
	var p capability.MCPServerConfigInput
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	if msg := validateMCPInputPresence(raw); msg != "" {
		return msg
	}
	if err := capability.ValidateMCPServerConfigInput(p, false); err != nil {
		return err.Error()
	}
	return ""
}

func validateMCPUpdateRaw(raw json.RawMessage) string {
	var p mcpServerUpdateParams
	if msg := decodeObject(raw, &p, "id", "revision"); msg != "" {
		return msg
	}
	if msg := validateMCPIDRevision(p.mcpIDRevisionParams); msg != "" {
		return msg
	}
	if msg := validateMCPInputPresence(raw); msg != "" {
		return msg
	}
	if err := capability.ValidateMCPServerConfigInput(p.MCPServerConfigInput, true); err != nil {
		return err.Error()
	}
	return ""
}

// These checks enforce required/null wire fields, not configuration semantics.
// The guarded service owns the latter, shared with these validators before mint.
func mcpRequiredObject(raw json.RawMessage, fields, nullable []string) (map[string]json.RawMessage, string) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, "expected an object"
	}
	for _, field := range fields {
		value, exists := object[field]
		if !exists {
			return nil, field + " is required"
		}
		if strings.TrimSpace(string(value)) != "null" {
			continue
		}
		allowed := false
		for _, name := range nullable {
			if name == field {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, field + " must not be null"
		}
	}
	return object, ""
}

func mcpSecretPresence(raw json.RawMessage, valueBinding bool) string {
	fields := []string{"secret", "secretValue", "keep"}
	nullable := []string{"secret", "secretValue"}
	if valueBinding {
		fields = append(fields, "kind", "literal")
		nullable = append(nullable, "literal")
	}
	_, msg := mcpRequiredObject(raw, fields, nullable)
	return msg
}

func mcpRowsPresence(raw json.RawMessage) string {
	var rows []json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil || rows == nil {
		return "bindings must be an array"
	}
	for _, row := range rows {
		object, msg := mcpRequiredObject(row, []string{"name", "value"}, nil)
		if msg != "" {
			return msg
		}
		if msg := mcpSecretPresence(object["value"], true); msg != "" {
			return msg
		}
	}
	return ""
}

func validateMCPInputPresence(raw json.RawMessage) string {
	top, msg := mcpRequiredObject(raw, []string{"name", "enabled", "transport", "stdio", "http", "limits"}, []string{"stdio", "http"})
	if msg != "" {
		return msg
	}
	if _, msg := mcpRequiredObject(top["limits"], []string{"startupTimeoutMs", "callTimeoutMs", "idleTimeoutMs", "maxResultBytes"}, nil); msg != "" {
		return msg
	}
	if strings.TrimSpace(string(top["stdio"])) != "null" {
		stdio, msg := mcpRequiredObject(top["stdio"], []string{"command", "argv", "cwd", "env"}, nil)
		if msg != "" {
			return msg
		}
		if msg := mcpRowsPresence(stdio["env"]); msg != "" {
			return msg
		}
	}
	if strings.TrimSpace(string(top["http"])) != "null" {
		http, msg := mcpRequiredObject(top["http"], []string{"endpoint", "auth", "headers", "bearer", "oauth"}, []string{"bearer", "oauth"})
		if msg != "" {
			return msg
		}
		if msg := mcpRowsPresence(http["headers"]); msg != "" {
			return msg
		}
		if strings.TrimSpace(string(http["bearer"])) != "null" {
			if msg := mcpSecretPresence(http["bearer"], false); msg != "" {
				return msg
			}
		}
		if strings.TrimSpace(string(http["oauth"])) != "null" {
			oauth, msg := mcpRequiredObject(http["oauth"], []string{"registration", "clientId", "clientSecret", "scopes"}, []string{"clientSecret"})
			if msg != "" {
				return msg
			}
			if strings.TrimSpace(string(oauth["clientSecret"])) != "null" {
				if msg := mcpSecretPresence(oauth["clientSecret"], false); msg != "" {
					return msg
				}
			}
		}
	}
	return ""
}
