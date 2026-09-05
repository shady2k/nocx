package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/mcp"
	"github.com/shady2k/nocx/internal/profile"
)

// MCPServerRefresher is the management-only discovery seam. Implementations
// must create a one-shot discovery session and close it before returning.
type MCPServerRefresher interface {
	Refresh(context.Context, mcp.Activation) (mcp.Catalog, error)
}

type mcpServerHandlers struct {
	op        capability.ConfigOperation
	repo      profile.MCPServerRepository
	secrets   credential.SecretStore
	rows      capability.RowResolver
	refresher MCPServerRefresher
	runtime   mcp.Runtime
	oauth     mcp.OAuthService
	presenter func() mcp.URLPresenter
	notify    func(mcpServersChangedParams)
	r         Responder
}

func (h mcpServerHandlers) runServerMutation(serverID string, mutation func() error) error {
	if runner, ok := h.runtime.(mcp.MutationRunner); ok {
		return runner.RunServerMutation(serverID, mutation)
	}
	err := mutation()
	if err == nil && h.runtime != nil {
		h.runtime.CloseServer(serverID)
	}
	return err
}

type mcpURLPresenter struct{ opener UrlOpener }

func (p mcpURLPresenter) PresentURL(ctx context.Context, target string) error {
	if p.opener == nil {
		return ErrNoURLHost
	}
	return p.opener.OpenURL(ctx, target)
}

func (s *WSServer) mcpOAuthPresenter() mcp.URLPresenter {
	s.urlMu.RLock()
	opener := s.urlOpener
	s.urlMu.RUnlock()
	return mcpURLPresenter{opener: opener}
}

type mcpServersListResult struct {
	Servers []mcpServerSummary `json:"servers"`
}

type mcpServerSummary struct {
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

type mcpServerResult struct {
	Server mcpServerDTO `json:"server"`
}

type mcpServerDeleteResult struct {
	Deleted bool `json:"deleted"`
}

type mcpServersChangedParams struct {
	ID       string `json:"id"`
	Revision uint64 `json:"revision"`
	Change   string `json:"change"`
}

type mcpErrorData struct {
	Reason string `json:"reason"`
}

type mcpServerDTO struct {
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

type mcpCatalogDTO struct {
	State           profile.MCPCatalogState `json:"state"`
	ServerName      string                  `json:"serverName"`
	ServerVersion   string                  `json:"serverVersion"`
	ProtocolVersion string                  `json:"protocolVersion"`
	RefreshedAt     any                     `json:"refreshedAt"`
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

type mcpServerConfigInput struct {
	Name      string                   `json:"name"`
	Enabled   bool                     `json:"enabled"`
	Transport profile.MCPTransportKind `json:"transport"`
	Stdio     *mcpStdioInput           `json:"stdio"`
	HTTP      *mcpHTTPInput            `json:"http"`
	Limits    profile.MCPLimits        `json:"limits"`
}

type mcpServerCreateParams struct {
	Name      string                   `json:"name"`
	Enabled   bool                     `json:"enabled"`
	Transport profile.MCPTransportKind `json:"transport"`
	Stdio     *mcpStdioInput           `json:"stdio"`
	HTTP      *mcpHTTPInput            `json:"http"`
	Limits    profile.MCPLimits        `json:"limits"`
}

func (p mcpServerCreateParams) config() mcpServerConfigInput {
	return mcpServerConfigInput(p)
}

type mcpServerUpdateParams struct {
	ID        string                   `json:"id"`
	Revision  uint64                   `json:"revision"`
	Name      string                   `json:"name"`
	Enabled   bool                     `json:"enabled"`
	Transport profile.MCPTransportKind `json:"transport"`
	Stdio     *mcpStdioInput           `json:"stdio"`
	HTTP      *mcpHTTPInput            `json:"http"`
	Limits    profile.MCPLimits        `json:"limits"`
}

func (p mcpServerUpdateParams) config() mcpServerConfigInput {
	return mcpServerConfigInput{Name: p.Name, Enabled: p.Enabled, Transport: p.Transport, Stdio: p.Stdio, HTTP: p.HTTP, Limits: p.Limits}
}

type mcpStdioInput struct {
	Command string               `json:"command"`
	Argv    []string             `json:"argv"`
	Cwd     string               `json:"cwd"`
	Env     []mcpEnvBindingInput `json:"env"`
}

type mcpEnvBindingInput struct {
	Name  string               `json:"name"`
	Value mcpValueBindingInput `json:"value"`
}

type mcpHeaderBindingInput struct {
	Name  string               `json:"name"`
	Value mcpValueBindingInput `json:"value"`
}

type mcpValueBindingInput struct {
	Kind        profile.MCPBindingKind `json:"kind"`
	Literal     *string                `json:"literal"`
	Secret      *string                `json:"secret"`
	SecretValue *string                `json:"secretValue"`
	Keep        bool                   `json:"keep"`
}

type mcpSecretBindingInput struct {
	Secret      *string `json:"secret"`
	SecretValue *string `json:"secretValue"`
	Keep        bool    `json:"keep"`
}

type mcpOAuthInput struct {
	Registration profile.MCPOAuthRegistrationKind `json:"registration"`
	ClientID     string                           `json:"clientId"`
	ClientSecret *mcpSecretBindingInput           `json:"clientSecret"`
	Scopes       []string                         `json:"scopes"`
}

type mcpHTTPInput struct {
	Endpoint string                  `json:"endpoint"`
	Auth     profile.MCPHTTPAuthKind `json:"auth"`
	Headers  []mcpHeaderBindingInput `json:"headers"`
	Bearer   *mcpSecretBindingInput  `json:"bearer"`
	OAuth    *mcpOAuthInput          `json:"oauth"`
}

type mcpIDRevisionParams struct {
	ID       string `json:"id"`
	Revision uint64 `json:"revision"`
}

type mcpGetParams struct {
	ID string `json:"id"`
}

type mcpSetToolsEnabledParams struct {
	ID       string   `json:"id"`
	Revision uint64   `json:"revision"`
	Tools    []string `json:"tools"`
}

func (s *WSServer) mcpServerSpecs(configOp capability.ConfigOperation) []methodSpec {
	wired := s.mcpServers != nil
	sub := s.operationQueue("mcp-config")
	build := func(r Responder) handlerFunc {
		h := mcpServerHandlers{
			op: configOp, repo: s.mcpServers, secrets: s.credentials,
			rows: s.vaultRowResolver(), refresher: s.mcpRefresher, runtime: s.mcpRuntime,
			oauth: s.mcpOAuth, presenter: s.mcpOAuthPresenter,
			notify: s.broadcastMCPServersChanged, r: r,
		}
		return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
	}
	available := func() bool { return wired }
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
	specs := make([]methodSpec, 0, len(methods))
	for _, method := range methods {
		specs = append(specs, whenAvailable(regResponder(sub, method.name, method.validate, build), available, "MCP servers not available"))
	}
	return specs
}

func (h mcpServerHandlers) handleMethod(ctx context.Context, req jsonrpcRequest) {
	if req.Method == "mcpServers.refresh" || req.Method == "mcpServers.oauthAuthorize" {
		if req.Method == "mcpServers.refresh" {
			h.handleRefresh(ctx, req)
		} else {
			h.handleOAuthAuthorize(ctx, req)
		}
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, _ capability.ConfigService) error {
		switch req.Method {
		case "mcpServers.list":
			h.handleList(req)
		case "mcpServers.get":
			h.handleGet(req)
		case "mcpServers.create":
			h.handleCreate(ctx, req)
		case "mcpServers.update":
			h.handleUpdate(ctx, req)
		case "mcpServers.delete":
			h.handleDelete(ctx, req)
		case "mcpServers.setToolsEnabled":
			h.handleSetTools(req)
		case "mcpServers.oauthForget":
			h.handleOAuthForget(ctx, req)
		}
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

func (h mcpServerHandlers) handleList(req jsonrpcRequest) {
	servers, err := h.repo.ListMCPServers()
	if err != nil {
		h.answerError(req, err)
		return
	}
	out := make([]mcpServerSummary, len(servers))
	for i, server := range servers {
		out[i] = summarizeMCPServer(server)
	}
	_ = h.r.TryResult(req.ID, mustMarshal(mcpServersListResult{Servers: out}))
}

func (h mcpServerHandlers) handleGet(req jsonrpcRequest) {
	var p mcpGetParams
	_ = json.Unmarshal(req.Params, &p)
	server, err := h.repo.GetMCPServer(p.ID)
	if err != nil {
		h.answerError(req, err)
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(mcpServerResult{Server: wireMCPServer(server)}))
}

func (h mcpServerHandlers) handleCreate(ctx context.Context, req jsonrpcRequest) {
	var p mcpServerCreateParams
	_ = json.Unmarshal(req.Params, &p)
	server, minted, err := h.materialize(ctx, p.config(), nil, false)
	if err != nil {
		h.rollbackSecrets(ctx, minted)
		h.answerInvalid(req, err)
		return
	}
	created, err := h.repo.CreateMCPServer(server)
	if err != nil {
		h.rollbackSecrets(ctx, minted)
		h.answerError(req, err)
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(mcpServerResult{Server: wireMCPServer(created)}))
	h.changed(created.ID, created.Revision, "created")
}

func (h mcpServerHandlers) handleUpdate(ctx context.Context, req jsonrpcRequest) {
	var p mcpServerUpdateParams
	_ = json.Unmarshal(req.Params, &p)
	current, err := h.repo.GetMCPServer(p.ID)
	if err != nil {
		h.answerError(req, err)
		return
	}
	if current.Revision != p.Revision {
		h.answerConflict(req)
		return
	}
	updated, minted, err := h.materialize(ctx, p.config(), &current, true)
	if err != nil {
		h.rollbackSecrets(ctx, minted)
		h.answerInvalid(req, err)
		return
	}
	updated.ID, updated.Revision, updated.Catalog = current.ID, current.Revision, current.Catalog
	var stored profile.MCPServer
	err = h.runServerMutation(current.ID, func() error {
		var updateErr error
		stored, updateErr = h.repo.UpdateMCPServer(updated)
		return updateErr
	})
	if err != nil {
		h.rollbackSecrets(ctx, minted)
		h.answerError(req, err)
		return
	}
	h.deleteDisplacedSecrets(ctx, current, stored)
	_ = h.r.TryResult(req.ID, mustMarshal(mcpServerResult{Server: wireMCPServer(stored)}))
	h.changed(stored.ID, stored.Revision, "updated")
}

func (h mcpServerHandlers) handleDelete(ctx context.Context, req jsonrpcRequest) {
	var p mcpIDRevisionParams
	_ = json.Unmarshal(req.Params, &p)
	var deleted profile.MCPDeleteResult
	err := h.runServerMutation(p.ID, func() error {
		var deleteErr error
		deleted, deleteErr = h.repo.DeleteMCPServer(p.ID, p.Revision)
		return deleteErr
	})
	if err != nil {
		h.answerError(req, err)
		return
	}
	for _, ref := range deleted.OwnedSecretRefs {
		if h.secrets != nil {
			_ = h.secrets.Delete(ctx, credential.SecretID(ref))
		}
	}
	_ = h.r.TryResult(req.ID, mustMarshal(mcpServerDeleteResult{Deleted: true}))
	h.changed(p.ID, p.Revision, "deleted")
}

func (h mcpServerHandlers) handleSetTools(req jsonrpcRequest) {
	var p mcpSetToolsEnabledParams
	_ = json.Unmarshal(req.Params, &p)
	var server profile.MCPServer
	err := h.runServerMutation(p.ID, func() error {
		var setErr error
		server, setErr = h.repo.SetMCPToolsEnabled(p.ID, p.Revision, p.Tools)
		return setErr
	})
	if err != nil {
		h.answerError(req, err)
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(mcpServerResult{Server: wireMCPServer(server)}))
	h.changed(server.ID, server.Revision, "tools")
}

func (h mcpServerHandlers) handleRefresh(ctx context.Context, req jsonrpcRequest) {
	var p mcpIDRevisionParams
	_ = json.Unmarshal(req.Params, &p)
	var current profile.MCPServer
	var ok bool
	if err := h.op.Run(ctx, func(context.Context, capability.ConfigService) error {
		current, ok = h.currentRevision(req, p)
		return nil
	}); err != nil {
		answerOperationRefusal(h.r, req, err)
		return
	}
	if !ok {
		return
	}
	if h.refresher == nil {
		h.answerRuntimeUnavailable(req)
		return
	}
	activation, err := mcp.ActivationFromServer(current)
	if err != nil {
		h.answerInvalid(req, err)
		return
	}
	discovered, err := h.refresher.Refresh(ctx, activation)
	if err != nil {
		h.answerError(req, err)
		return
	}
	catalog, err := discovered.ProfileCatalog()
	if err != nil {
		h.answerError(req, err)
		return
	}
	var stored profile.MCPServer
	var committed bool
	operationErr := h.op.Run(ctx, func(context.Context, capability.ConfigService) error {
		mutationErr := h.runServerMutation(current.ID, func() error {
			var updateErr error
			stored, updateErr = h.repo.RefreshMCPServerCatalog(current.ID, current.Revision, catalog)
			return updateErr
		})
		if mutationErr != nil {
			h.answerError(req, mutationErr)
			return nil
		}
		committed = true
		return nil
	})
	if operationErr != nil {
		answerOperationRefusal(h.r, req, operationErr)
		return
	}
	if !committed {
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(mcpServerResult{Server: wireMCPServer(stored)}))
	h.changed(stored.ID, stored.Revision, "catalog")
}

func (h mcpServerHandlers) handleOAuthAuthorize(ctx context.Context, req jsonrpcRequest) {
	var p mcpIDRevisionParams
	_ = json.Unmarshal(req.Params, &p)
	current, err := h.snapshotMCPRevision(ctx, p)
	if err != nil {
		h.answerMCPConfigError(req, err)
		return
	}
	if current.HTTP == nil || current.HTTP.Auth != profile.MCPHTTPAuthOAuth || current.HTTP.OAuth == nil {
		h.answerInvalid(req, errors.New("server is not configured for OAuth"))
		return
	}
	if h.oauth == nil || h.presenter == nil {
		h.answerRuntimeUnavailable(req)
		return
	}
	activation, err := mcp.ActivationFromServer(current)
	if err != nil {
		h.answerInvalid(req, err)
		return
	}
	status, err := h.oauth.Authorize(ctx, activation, h.presenter())
	if err != nil {
		h.answerError(req, err)
		return
	}
	if status.SessionRef == "" {
		h.answerError(req, errors.New("MCP OAuth did not return a session reference"))
		return
	}
	updated := current
	oauth := updated.HTTP.OAuth
	oauth.SessionRef = &profile.MCPSecretBinding{SecretRef: status.SessionRef, Owned: true}
	oauth.Status = profile.MCPOAuthConnected
	oauth.Issuer = status.Issuer
	oauth.GrantedScopes = append([]string(nil), status.Scopes...)
	oauth.AccessTokenExpires = status.ExpiresAt
	var stored profile.MCPServer
	err = h.op.Run(ctx, func(_ context.Context, _ capability.ConfigService) error {
		return h.runServerMutation(p.ID, func() error {
			var updateErr error
			stored, updateErr = h.repo.UpdateMCPServer(updated)
			return updateErr
		})
	})
	if err != nil {
		if h.secrets != nil {
			_ = h.secrets.Delete(ctx, credential.SecretID(status.SessionRef))
		}
		if discarder, ok := h.oauth.(mcp.OAuthSessionDiscarder); ok {
			_ = discarder.DiscardOAuthSession(ctx, p.ID, status.SessionRef)
		}
		h.answerMCPConfigError(req, err)
		return
	}
	if old := current.HTTP.OAuth.SessionRef; old != nil && old.Owned && h.secrets != nil {
		_ = h.secrets.Delete(ctx, credential.SecretID(old.SecretRef))
	}
	_ = h.r.TryResult(req.ID, mustMarshal(mcpServerResult{Server: wireMCPServer(stored)}))
	h.changed(stored.ID, stored.Revision, "oauth")
}

func (h mcpServerHandlers) handleOAuthForget(ctx context.Context, req jsonrpcRequest) {
	var p mcpIDRevisionParams
	_ = json.Unmarshal(req.Params, &p)
	current, err := h.snapshotMCPRevision(ctx, p)
	if err != nil {
		h.answerMCPConfigError(req, err)
		return
	}
	if current.HTTP == nil || current.HTTP.Auth != profile.MCPHTTPAuthOAuth || current.HTTP.OAuth == nil {
		h.answerInvalid(req, errors.New("server is not configured for OAuth"))
		return
	}
	oldSession := current.HTTP.OAuth.SessionRef
	current.HTTP.OAuth.SessionRef = nil
	current.HTTP.OAuth.Status = profile.MCPOAuthMissing
	current.HTTP.OAuth.Issuer = ""
	current.HTTP.OAuth.GrantedScopes = []string{}
	current.HTTP.OAuth.AccessTokenExpires = nil
	var stored profile.MCPServer
	err = h.op.Run(ctx, func(_ context.Context, _ capability.ConfigService) error {
		return h.runServerMutation(p.ID, func() error {
			var updateErr error
			stored, updateErr = h.repo.UpdateMCPServer(current)
			return updateErr
		})
	})
	if err != nil {
		h.answerMCPConfigError(req, err)
		return
	}
	if oldSession != nil && oldSession.Owned && h.secrets != nil {
		_ = h.secrets.Delete(ctx, credential.SecretID(oldSession.SecretRef))
	}
	if h.oauth != nil {
		_ = h.oauth.Forget(ctx, stored.ID)
	}
	_ = h.r.TryResult(req.ID, mustMarshal(mcpServerResult{Server: wireMCPServer(stored)}))
	h.changed(stored.ID, stored.Revision, "oauth")
}

func (h mcpServerHandlers) currentRevision(req jsonrpcRequest, p mcpIDRevisionParams) (profile.MCPServer, bool) {
	current, err := h.repo.GetMCPServer(p.ID)
	if err != nil {
		h.answerError(req, err)
		return profile.MCPServer{}, false
	}
	if current.Revision != p.Revision {
		h.answerConflict(req)
		return profile.MCPServer{}, false
	}
	return current, true
}

func (h mcpServerHandlers) changed(id string, revision uint64, change string) {
	if h.notify != nil {
		h.notify(mcpServersChangedParams{ID: id, Revision: revision, Change: change})
	}
}

func (h mcpServerHandlers) snapshotMCPRevision(ctx context.Context, p mcpIDRevisionParams) (profile.MCPServer, error) {
	var current profile.MCPServer
	err := h.op.Run(ctx, func(_ context.Context, _ capability.ConfigService) error {
		var err error
		current, err = h.repo.GetMCPServer(p.ID)
		if err != nil {
			return err
		}
		if current.Revision != p.Revision {
			return profile.ErrMCPServerConflict
		}
		return nil
	})
	return current, err
}

func (h mcpServerHandlers) answerMCPConfigError(req jsonrpcRequest, err error) {
	switch {
	case errors.Is(err, profile.ErrMCPServerConflict),
		errors.Is(err, profile.ErrMCPServerNotFound),
		errors.Is(err, profile.ErrMCPToolNotFound):
		h.answerError(req, err)
	default:
		answerOperationRefusal(h.r, req, err)
	}
}

func (h mcpServerHandlers) answerRuntimeUnavailable(req jsonrpcRequest) {
	_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "MCP runtime unavailable", Data: mcpErrorData{Reason: "runtime-unavailable"}})
}

func (h mcpServerHandlers) answerConflict(req jsonrpcRequest) {
	_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: profile.ErrMCPServerConflict.Error(), Data: mcpErrorData{Reason: "conflict"}})
}

func (h mcpServerHandlers) answerInvalid(req jsonrpcRequest, err error) {
	_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: " + err.Error()})
}

func (h mcpServerHandlers) answerError(req jsonrpcRequest, err error) {
	switch {
	case errors.Is(err, profile.ErrMCPServerConflict):
		h.answerConflict(req)
	case errors.Is(err, profile.ErrMCPServerNotFound):
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: err.Error(), Data: mcpErrorData{Reason: "not-found"}})
	case errors.Is(err, profile.ErrMCPToolNotFound):
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: err.Error(), Data: mcpErrorData{Reason: "tool-not-found"}})
	default:
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
	}
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
	catalog := mcpCatalogDTO{State: server.Catalog.State, ServerName: server.Catalog.ServerName, ServerVersion: server.Catalog.ServerVersion, ProtocolVersion: server.Catalog.ProtocolVersion, Digest: server.Catalog.Digest, Tools: tools}
	if server.Catalog.RefreshedAt != nil {
		catalog.RefreshedAt = server.Catalog.RefreshedAt
	}
	return mcpServerDTO{ID: sanitized.ID, Revision: sanitized.Revision, Name: sanitized.Name, Enabled: sanitized.Enabled, Transport: sanitized.Transport, Stdio: sanitized.Stdio, HTTP: sanitized.HTTP, Limits: sanitized.Limits, Catalog: catalog}
}

func validateMCPGetRaw(raw json.RawMessage) string {
	var p mcpGetParams
	if msg := decodeObject(raw, &p, "id"); msg != "" {
		return msg
	}
	if strings.TrimSpace(p.ID) == "" {
		return "id is required"
	}
	if len(p.ID) > 128 {
		return "id exceeds 128 bytes"
	}
	return ""
}

func validateMCPIDRevisionRaw(raw json.RawMessage) string {
	var p mcpIDRevisionParams
	if msg := decodeObject(raw, &p, "id", "revision"); msg != "" {
		return msg
	}
	if strings.TrimSpace(p.ID) == "" {
		return "id is required"
	}
	if len(p.ID) > 128 {
		return "id exceeds 128 bytes"
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
	if msg := validateMCPIDRevision(p.ID, p.Revision); msg != "" {
		return msg
	}
	if p.Tools == nil {
		return "tools is required"
	}
	if len(p.Tools) > profile.MaxMCPToolsPerServer {
		return fmt.Sprintf("tools exceeds %d entries", profile.MaxMCPToolsPerServer)
	}
	seen := make(map[string]struct{}, len(p.Tools))
	for _, name := range p.Tools {
		if name == "" || len(name) > 512 {
			return "tool name must be between 1 and 512 bytes"
		}
		if _, duplicate := seen[name]; duplicate {
			return "tool names must be unique"
		}
		seen[name] = struct{}{}
	}
	return ""
}

func validateMCPIDRevision(id string, revision uint64) string {
	if strings.TrimSpace(id) == "" || len(id) > 128 {
		return "id is required and must not exceed 128 bytes"
	}
	if revision == 0 {
		return "revision must be at least 1"
	}
	return ""
}

func validateMCPCreateRaw(raw json.RawMessage) string {
	var p mcpServerCreateParams
	if msg := decodeObject(raw, &p, "name", "enabled", "transport", "limits"); msg != "" {
		return msg
	}
	if msg := validateMCPInputPresence(raw, false); msg != "" {
		return msg
	}
	return validateMCPConfigInput(p.config(), false)
}

func validateMCPUpdateRaw(raw json.RawMessage) string {
	var p mcpServerUpdateParams
	if msg := decodeObject(raw, &p, "id", "revision", "name", "enabled", "transport", "limits"); msg != "" {
		return msg
	}
	if msg := validateMCPInputPresence(raw, true); msg != "" {
		return msg
	}
	if msg := validateMCPIDRevision(p.ID, p.Revision); msg != "" {
		return msg
	}
	return validateMCPConfigInput(p.config(), true)
}

func validateMCPConfigInput(in mcpServerConfigInput, allowKeep bool) string {
	placeholder := profile.MCPServer{Name: in.Name, Enabled: in.Enabled, Transport: in.Transport, Limits: in.Limits, Catalog: profile.MCPCatalog{State: profile.MCPCatalogMissing, Tools: []profile.MCPTool{}}}
	if in.Stdio != nil {
		stdio := profile.MCPStdioConfig{Command: in.Stdio.Command, Argv: in.Stdio.Argv, Cwd: in.Stdio.Cwd, Env: make([]profile.MCPEnvBinding, len(in.Stdio.Env))}
		for i, row := range in.Stdio.Env {
			value, msg := validateMCPValueInput(row.Value, allowKeep)
			if msg != "" {
				return fmt.Sprintf("env[%d]: %s", i, msg)
			}
			stdio.Env[i] = profile.MCPEnvBinding{Name: row.Name, Value: value}
		}
		placeholder.Stdio = &stdio
	}
	if in.HTTP != nil {
		http := profile.MCPHTTPConfig{Endpoint: in.HTTP.Endpoint, Auth: in.HTTP.Auth, Headers: make([]profile.MCPHeaderBinding, len(in.HTTP.Headers))}
		for i, row := range in.HTTP.Headers {
			value, msg := validateMCPValueInput(row.Value, allowKeep)
			if msg != "" {
				return fmt.Sprintf("headers[%d]: %s", i, msg)
			}
			http.Headers[i] = profile.MCPHeaderBinding{Name: row.Name, Value: value}
		}
		if in.HTTP.Bearer != nil {
			binding, msg := validateMCPSecretInput(*in.HTTP.Bearer, allowKeep)
			if msg != "" {
				return "bearer: " + msg
			}
			http.Bearer = &binding
		}
		if in.HTTP.OAuth != nil {
			oauth := profile.MCPOAuthConfig{Registration: in.HTTP.OAuth.Registration, ClientID: in.HTTP.OAuth.ClientID, Scopes: in.HTTP.OAuth.Scopes, GrantedScopes: []string{}, Status: profile.MCPOAuthMissing}
			if in.HTTP.OAuth.ClientSecret != nil {
				binding, msg := validateMCPSecretInput(*in.HTTP.OAuth.ClientSecret, allowKeep)
				if msg != "" {
					return "OAuth client secret: " + msg
				}
				oauth.ClientSecret = &binding
			}
			http.OAuth = &oauth
		}
		placeholder.HTTP = &http
	}
	if err := profile.ValidateMCPServer(placeholder); err != nil {
		return err.Error()
	}
	return ""
}

func validateMCPValueInput(in mcpValueBindingInput, allowKeep bool) (profile.MCPValueBinding, string) {
	if in.Kind == profile.MCPBindingLiteral {
		if in.Literal == nil || in.Secret != nil || in.SecretValue != nil || in.Keep {
			return profile.MCPValueBinding{}, "literal binding must carry only literal"
		}
		return profile.MCPValueBinding{Kind: in.Kind, Literal: in.Literal}, ""
	}
	if in.Kind != profile.MCPBindingSecret {
		return profile.MCPValueBinding{}, "binding kind must be literal or secret"
	}
	if in.Literal != nil {
		return profile.MCPValueBinding{}, "secret binding must not carry literal"
	}
	binding, msg := validateMCPSecretInput(mcpSecretBindingInput{Secret: in.Secret, SecretValue: in.SecretValue, Keep: in.Keep}, allowKeep)
	if msg != "" {
		return profile.MCPValueBinding{}, msg
	}
	return profile.MCPValueBinding{Kind: in.Kind, SecretRef: binding.SecretRef, Owned: binding.Owned}, ""
}

func validateMCPSecretInput(in mcpSecretBindingInput, allowKeep bool) (profile.MCPSecretBinding, string) {
	sources := 0
	if in.Secret != nil {
		sources++
	}
	if in.SecretValue != nil {
		sources++
	}
	if in.Keep {
		sources++
	}
	if sources != 1 {
		return profile.MCPSecretBinding{}, "secret binding must select exactly one source"
	}
	if in.Keep && !allowKeep {
		return profile.MCPSecretBinding{}, "keep is not valid while creating a server"
	}
	if in.Secret != nil && (*in.Secret == "" || len(*in.Secret) > 512 || !strings.HasPrefix(*in.Secret, "secrow:")) {
		return profile.MCPSecretBinding{}, "secret must be a bounded secrow handle"
	}
	if in.SecretValue != nil && len(*in.SecretValue) > profile.MaxMCPBindingLiteralBytes {
		return profile.MCPSecretBinding{}, "secretValue exceeds 8192 bytes"
	}
	return profile.MCPSecretBinding{SecretRef: "pending-secret", Owned: in.SecretValue != nil || in.Keep}, ""
}

func validateMCPInputPresence(raw json.RawMessage, update bool) string {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return "params must be a JSON object"
	}
	required := []string{"name", "enabled", "transport", "stdio", "http", "limits"}
	if update {
		required = append([]string{"id", "revision"}, required...)
	}
	if msg := requireMCPFields(top, required...); msg != "" {
		return msg
	}
	return ""
}

func requireMCPFields(object map[string]json.RawMessage, fields ...string) string {
	for _, field := range fields {
		if _, ok := object[field]; !ok {
			return field + " is required"
		}
	}
	return ""
}

func sameOAuthIdentity(oldEndpoint, newEndpoint string, old, next *profile.MCPOAuthConfig) bool {
	if old == nil || next == nil || oldEndpoint != newEndpoint ||
		old.Registration != next.Registration || old.ClientID != next.ClientID ||
		!sameStringSlice(old.Scopes, next.Scopes) {
		return false
	}
	if old.ClientSecret == nil || next.ClientSecret == nil {
		return old.ClientSecret == nil && next.ClientSecret == nil
	}
	return old.ClientSecret.SecretRef == next.ClientSecret.SecretRef &&
		old.ClientSecret.Owned == next.ClientSecret.Owned
}

func sameStringSlice(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func (h mcpServerHandlers) materialize(ctx context.Context, in mcpServerConfigInput, current *profile.MCPServer, allowKeep bool) (profile.MCPServer, []credential.SecretID, error) {
	server := profile.MCPServer{Name: in.Name, Enabled: in.Enabled, Transport: in.Transport, Limits: in.Limits, Catalog: profile.MCPCatalog{State: profile.MCPCatalogMissing, Tools: []profile.MCPTool{}}}
	var minted []credential.SecretID
	if in.Stdio != nil {
		stdio := &profile.MCPStdioConfig{Command: in.Stdio.Command, Argv: append([]string{}, in.Stdio.Argv...), Cwd: in.Stdio.Cwd, Env: make([]profile.MCPEnvBinding, len(in.Stdio.Env))}
		currentEnv := map[string]profile.MCPValueBinding{}
		if current != nil && current.Stdio != nil {
			for _, row := range current.Stdio.Env {
				currentEnv[row.Name] = row.Value
			}
		}
		for i, row := range in.Stdio.Env {
			value, made, err := h.materializeValue(ctx, row.Value, currentEnv[row.Name], allowKeep)
			minted = append(minted, made...)
			if err != nil {
				return profile.MCPServer{}, minted, fmt.Errorf("env[%d]: %w", i, err)
			}
			stdio.Env[i] = profile.MCPEnvBinding{Name: row.Name, Value: value}
		}
		server.Stdio = stdio
	}
	if in.HTTP != nil {
		http := &profile.MCPHTTPConfig{Endpoint: in.HTTP.Endpoint, Auth: in.HTTP.Auth, Headers: make([]profile.MCPHeaderBinding, len(in.HTTP.Headers))}
		currentHeaders := map[string]profile.MCPValueBinding{}
		if current != nil && current.HTTP != nil {
			for _, row := range current.HTTP.Headers {
				currentHeaders[strings.ToLower(row.Name)] = row.Value
			}
		}
		for i, row := range in.HTTP.Headers {
			value, made, err := h.materializeValue(ctx, row.Value, currentHeaders[strings.ToLower(row.Name)], allowKeep)
			minted = append(minted, made...)
			if err != nil {
				return profile.MCPServer{}, minted, fmt.Errorf("headers[%d]: %w", i, err)
			}
			http.Headers[i] = profile.MCPHeaderBinding{Name: row.Name, Value: value}
		}
		if in.HTTP.Bearer != nil {
			var old *profile.MCPSecretBinding
			if current != nil && current.HTTP != nil {
				old = current.HTTP.Bearer
			}
			binding, made, err := h.materializeSecret(ctx, *in.HTTP.Bearer, old, allowKeep)
			minted = append(minted, made...)
			if err != nil {
				return profile.MCPServer{}, minted, fmt.Errorf("bearer: %w", err)
			}
			http.Bearer = &binding
		}
		if in.HTTP.OAuth != nil {
			oauth := &profile.MCPOAuthConfig{Registration: in.HTTP.OAuth.Registration, ClientID: in.HTTP.OAuth.ClientID, Scopes: append([]string{}, in.HTTP.OAuth.Scopes...), GrantedScopes: []string{}, Status: profile.MCPOAuthMissing}
			var oldOAuth *profile.MCPOAuthConfig
			if current != nil && current.HTTP != nil {
				oldOAuth = current.HTTP.OAuth
			}
			if in.HTTP.OAuth.ClientSecret != nil {
				var old *profile.MCPSecretBinding
				if oldOAuth != nil {
					old = oldOAuth.ClientSecret
				}
				binding, made, err := h.materializeSecret(ctx, *in.HTTP.OAuth.ClientSecret, old, allowKeep)
				minted = append(minted, made...)
				if err != nil {
					return profile.MCPServer{}, minted, fmt.Errorf("OAuth client secret: %w", err)
				}
				oauth.ClientSecret = &binding
			}
			if oldOAuth != nil && current.HTTP != nil &&
				sameOAuthIdentity(current.HTTP.Endpoint, http.Endpoint, oldOAuth, oauth) {
				oauth.SessionRef, oauth.Status, oauth.Issuer = oldOAuth.SessionRef, oldOAuth.Status, oldOAuth.Issuer
				oauth.GrantedScopes = append([]string{}, oldOAuth.GrantedScopes...)
				oauth.AccessTokenExpires = oldOAuth.AccessTokenExpires
			}
			http.OAuth = oauth
		}
		server.HTTP = http
	}
	if err := profile.ValidateMCPServer(server); err != nil {
		return profile.MCPServer{}, minted, err
	}
	return server, minted, nil
}

func (h mcpServerHandlers) materializeValue(ctx context.Context, in mcpValueBindingInput, current profile.MCPValueBinding, allowKeep bool) (profile.MCPValueBinding, []credential.SecretID, error) {
	if in.Kind == profile.MCPBindingLiteral {
		return profile.MCPValueBinding{Kind: in.Kind, Literal: in.Literal}, nil, nil
	}
	var old *profile.MCPSecretBinding
	if current.Kind == profile.MCPBindingSecret && current.SecretRef != "" {
		old = &profile.MCPSecretBinding{SecretRef: current.SecretRef, Owned: current.Owned}
	}
	binding, minted, err := h.materializeSecret(ctx, mcpSecretBindingInput{Secret: in.Secret, SecretValue: in.SecretValue, Keep: in.Keep}, old, allowKeep)
	return profile.MCPValueBinding{Kind: profile.MCPBindingSecret, SecretRef: binding.SecretRef, Owned: binding.Owned}, minted, err
}

func (h mcpServerHandlers) materializeSecret(ctx context.Context, in mcpSecretBindingInput, current *profile.MCPSecretBinding, allowKeep bool) (profile.MCPSecretBinding, []credential.SecretID, error) {
	if in.Keep {
		if !allowKeep || current == nil || current.SecretRef == "" {
			return profile.MCPSecretBinding{}, nil, errors.New("no existing secret is available to keep")
		}
		return *current, nil, nil
	}
	if in.Secret != nil {
		if h.rows == nil {
			return profile.MCPSecretBinding{}, nil, errors.New("vault row resolver unavailable")
		}
		id, ok := h.rows.ResolveRow(*in.Secret, nil)
		if !ok {
			return profile.MCPSecretBinding{}, nil, errors.New("unknown vault secret row")
		}
		return profile.MCPSecretBinding{SecretRef: string(id), Owned: false}, nil, nil
	}
	if in.SecretValue != nil {
		if h.secrets == nil {
			return profile.MCPSecretBinding{}, nil, errors.New("vault secret store unavailable")
		}
		id, err := h.secrets.Create(ctx, credential.NewSecret(*in.SecretValue))
		if err != nil {
			return profile.MCPSecretBinding{}, nil, err
		}
		return profile.MCPSecretBinding{SecretRef: string(id), Owned: true}, []credential.SecretID{id}, nil
	}
	return profile.MCPSecretBinding{}, nil, errors.New("secret binding has no source")
}

func (h mcpServerHandlers) rollbackSecrets(ctx context.Context, ids []credential.SecretID) {
	if h.secrets == nil {
		return
	}
	for _, id := range ids {
		_ = h.secrets.Delete(ctx, id)
	}
}

func (h mcpServerHandlers) deleteDisplacedSecrets(ctx context.Context, old, updated profile.MCPServer) {
	if h.secrets == nil || h.repo == nil {
		return
	}
	kept := mcpOwnedSecretRefs(updated)
	servers, err := h.repo.ListMCPServers()
	if err != nil {
		return
	}
	used := make(map[string]struct{})
	for _, server := range servers {
		for ref := range mcpSecretRefs(server) {
			used[ref] = struct{}{}
		}
	}
	for ref := range mcpOwnedSecretRefs(old) {
		if _, ok := kept[ref]; !ok {
			if _, referenced := used[ref]; referenced {
				continue
			}
			_ = h.secrets.Delete(ctx, credential.SecretID(ref))
		}
	}
}

func mcpSecretRefs(server profile.MCPServer) map[string]struct{} {
	refs := map[string]struct{}{}
	addValue := func(value profile.MCPValueBinding) {
		if value.Kind == profile.MCPBindingSecret && value.SecretRef != "" {
			refs[value.SecretRef] = struct{}{}
		}
	}
	addSecret := func(binding *profile.MCPSecretBinding) {
		if binding != nil && binding.SecretRef != "" {
			refs[binding.SecretRef] = struct{}{}
		}
	}
	if server.Stdio != nil {
		for _, row := range server.Stdio.Env {
			addValue(row.Value)
		}
	}
	if server.HTTP != nil {
		for _, row := range server.HTTP.Headers {
			addValue(row.Value)
		}
		addSecret(server.HTTP.Bearer)
		if server.HTTP.OAuth != nil {
			addSecret(server.HTTP.OAuth.ClientSecret)
			addSecret(server.HTTP.OAuth.SessionRef)
		}
	}
	return refs
}

func mcpOwnedSecretRefs(server profile.MCPServer) map[string]struct{} {
	refs := map[string]struct{}{}
	addValue := func(value profile.MCPValueBinding) {
		if value.Kind == profile.MCPBindingSecret && value.Owned && value.SecretRef != "" {
			refs[value.SecretRef] = struct{}{}
		}
	}
	addSecret := func(binding *profile.MCPSecretBinding) {
		if binding != nil && binding.Owned && binding.SecretRef != "" {
			refs[binding.SecretRef] = struct{}{}
		}
	}
	if server.Stdio != nil {
		for _, row := range server.Stdio.Env {
			addValue(row.Value)
		}
	}
	if server.HTTP != nil {
		for _, row := range server.HTTP.Headers {
			addValue(row.Value)
		}
		addSecret(server.HTTP.Bearer)
		if server.HTTP.OAuth != nil {
			addSecret(server.HTTP.OAuth.ClientSecret)
			addSecret(server.HTTP.OAuth.SessionRef)
		}
	}
	return refs
}

func (s *WSServer) broadcastMCPServersChanged(params mcpServersChangedParams) {
	s.connsMu.Lock()
	conns := make([]*wsConn, 0, len(s.conns))
	for conn := range s.conns {
		conns = append(conns, conn)
	}
	s.connsMu.Unlock()
	payload := mustMarshal(params)
	for _, conn := range conns {
		_ = conn.TryNotify("mcpServers.changed", payload)
	}
}
