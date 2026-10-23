package capability

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/vault"
)

// MCPConfigRepository is the aggregate metadata owner, including every kind of
// secret holder. Cleanup must not infer orphanhood from MCP records alone.
type MCPConfigRepository interface {
	profile.MCPServerRepository
	profile.SecretReferenceQuery
	profile.MCPSecretReferenceQuery
}

// MCPConfigSecrets retains the Vault's durable create/metadata journal. It has
// no material-read or lifecycle capability. *vault.Vault implements this seam.
type MCPConfigSecrets interface {
	CreateNamed(context.Context, credential.Secret, vault.SecretMeta) (credential.SecretID, error)
	PrepareDelete(context.Context, credential.SecretID) error
	PendingDeleteCandidates(context.Context) ([]credential.SecretID, error)
	ScheduleDelete(context.Context, credential.SecretID) error
	AttachTarget(context.Context, credential.SecretID, string) error
	CommitMetadata(context.Context, credential.SecretID) error
}

type MCPConfigDependencies struct {
	Repository MCPConfigRepository
	Secrets    MCPConfigSecrets
}

var ErrMCPUnavailable = errors.New("MCP configuration unavailable")

// These inputs are one-way: Secret is a selectable Vault row, SecretValue is
// fresh material, and Keep preserves the binding at the same configuration key.
// None is a read DTO, and none accepts a stored SecretID or ownership bit.
type MCPServerConfigInput struct {
	Name      string                   `json:"name"`
	Enabled   bool                     `json:"enabled"`
	Transport profile.MCPTransportKind `json:"transport"`
	Stdio     *MCPStdioInput           `json:"stdio"`
	HTTP      *MCPHTTPInput            `json:"http"`
	Limits    profile.MCPLimits        `json:"limits"`
}

type MCPStdioInput struct {
	Command string               `json:"command"`
	Argv    []string             `json:"argv"`
	Cwd     string               `json:"cwd"`
	Env     []MCPEnvBindingInput `json:"env"`
}

type MCPEnvBindingInput struct {
	Name  string               `json:"name"`
	Value MCPValueBindingInput `json:"value"`
}

type MCPHeaderBindingInput struct {
	Name  string               `json:"name"`
	Value MCPValueBindingInput `json:"value"`
}

type MCPValueBindingInput struct {
	Kind        profile.MCPBindingKind `json:"kind"`
	Literal     *string                `json:"literal"`
	Secret      *string                `json:"secret"`
	SecretValue *string                `json:"secretValue"`
	Keep        bool                   `json:"keep"`
}

type MCPSecretBindingInput struct {
	Secret      *string `json:"secret"`
	SecretValue *string `json:"secretValue"`
	Keep        bool    `json:"keep"`
}

type MCPOAuthInput struct {
	Registration profile.MCPOAuthRegistrationKind `json:"registration"`
	ClientID     string                           `json:"clientId"`
	ClientSecret *MCPSecretBindingInput           `json:"clientSecret"`
	Scopes       []string                         `json:"scopes"`
}

type MCPHTTPInput struct {
	Endpoint string                  `json:"endpoint"`
	Auth     profile.MCPHTTPAuthKind `json:"auth"`
	Headers  []MCPHeaderBindingInput `json:"headers"`
	Bearer   *MCPSecretBindingInput  `json:"bearer"`
	OAuth    *MCPOAuthInput          `json:"oauth"`
}

func (s *configService) mcpReady(write bool) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	if s.mcp.Repository == nil || (write && s.mcp.Secrets == nil) {
		return ErrMCPUnavailable
	}
	return nil
}

func (s *configService) ListMCPServers() ([]profile.MCPServer, error) {
	if err := s.mcpReady(false); err != nil {
		return nil, err
	}
	return s.mcp.Repository.ListMCPServers()
}

func (s *configService) GetMCPServer(id string) (profile.MCPServer, error) {
	if err := s.mcpReady(false); err != nil {
		return profile.MCPServer{}, err
	}
	return s.mcp.Repository.GetMCPServer(id)
}

func (s *configService) currentMCP(id string, revision uint64) (profile.MCPServer, error) {
	server, err := s.mcp.Repository.GetMCPServer(id)
	if err != nil {
		return profile.MCPServer{}, err
	}
	if server.Revision != revision {
		return profile.MCPServer{}, profile.ErrMCPServerConflict
	}
	return server, nil
}

func (s *configService) CreateMCPServer(ctx context.Context, in MCPServerConfigInput) (profile.MCPServer, error) {
	if err := s.mcpReady(true); err != nil {
		return profile.MCPServer{}, err
	}
	server, minted, err := s.materializeMCP(ctx, in, nil)
	if err != nil {
		return profile.MCPServer{}, errors.Join(err, s.rollbackMCP(ctx, minted))
	}
	// The repository owns ID allocation. Mark the intended aggregate before
	// writing, then replace this recovery target with its allocated identity.
	if attachErr := s.attachMCP(ctx, "mcp:new-server", minted); attachErr != nil {
		return profile.MCPServer{}, errors.Join(attachErr, s.rollbackMCP(ctx, minted))
	}
	created, err := s.mcp.Repository.CreateMCPServer(server)
	if err != nil {
		return profile.MCPServer{}, errors.Join(err, s.rollbackMCP(ctx, minted))
	}
	// The metadata is committed. Finalization failure leaves the create journal
	// intact for reconciliation; never roll back a now-referenced secret.
	if s.attachMCP(ctx, created.ID, minted) == nil {
		s.finalizeMCP(ctx, minted)
	}
	return created, s.ReconcileMCPSecrets(ctx)
}

func (s *configService) UpdateMCPServer(ctx context.Context, id string, revision uint64, in MCPServerConfigInput) (profile.MCPServer, error) {
	if err := s.mcpReady(true); err != nil {
		return profile.MCPServer{}, err
	}
	old, err := s.currentMCP(id, revision)
	if err != nil {
		return profile.MCPServer{}, err
	}
	server, minted, err := s.materializeMCP(ctx, in, &old)
	if err != nil {
		return profile.MCPServer{}, errors.Join(err, s.rollbackMCP(ctx, minted))
	}
	server.ID, server.Revision = old.ID, old.Revision
	if attachErr := s.attachMCP(ctx, id, minted); attachErr != nil {
		return profile.MCPServer{}, errors.Join(attachErr, s.rollbackMCP(ctx, minted))
	}
	if prepareErr := s.prepareMCPRetirement(ctx, profile.MCPSecretReferences(old, true), server); prepareErr != nil {
		return profile.MCPServer{}, errors.Join(prepareErr, s.rollbackMCP(ctx, minted))
	}
	updated, err := s.mcp.Repository.UpdateMCPServer(server)
	if err != nil {
		return profile.MCPServer{}, errors.Join(err, s.rollbackMCP(ctx, minted))
	}
	s.finalizeMCP(ctx, minted)
	return updated, s.ReconcileMCPSecrets(ctx)
}

func (s *configService) DeleteMCPServer(ctx context.Context, id string, revision uint64) error {
	if err := s.mcpReady(true); err != nil {
		return err
	}
	old, err := s.currentMCP(id, revision)
	if err != nil {
		return err
	}
	if err := s.prepareMCPRetirement(ctx, profile.MCPSecretReferences(old, true), profile.MCPServer{}); err != nil {
		return err
	}
	if _, err := s.mcp.Repository.DeleteMCPServer(id, revision); err != nil {
		return errors.Join(err, s.ReconcileMCPSecrets(ctx))
	}
	return s.ReconcileMCPSecrets(ctx)
}

func (s *configService) SetMCPToolsEnabled(id string, revision uint64, names []string) (profile.MCPServer, error) {
	if err := s.mcpReady(false); err != nil {
		return profile.MCPServer{}, err
	}
	return s.mcp.Repository.SetMCPToolsEnabled(id, revision, names)
}

func (s *configService) RefreshMCPServerCatalog(id string, revision uint64, catalog profile.MCPCatalog) (profile.MCPServer, error) {
	if err := s.mcpReady(false); err != nil {
		return profile.MCPServer{}, err
	}
	return s.mcp.Repository.RefreshMCPServerCatalog(id, revision, catalog)
}

// CommitMCPOAuth accepts only backend-produced session/status metadata. Public
// registration configuration always remains the persisted configuration.
func (s *configService) CommitMCPOAuth(ctx context.Context, id string, revision uint64, oauth profile.MCPOAuthConfig) (result profile.MCPServer, resultErr error) {
	if err := s.mcpReady(true); err != nil {
		return profile.MCPServer{}, err
	}
	committed := false
	defer func() {
		// A failed save may have an uncertain durable outcome. Reconcile old
		// candidates and any new token together through the aggregate owner.
		if !committed {
			var refs []string
			if oauth.SessionRef != nil {
				refs = []string{oauth.SessionRef.SecretRef}
			}
			resultErr = errors.Join(resultErr, s.cleanupMCP(ctx, refs))
		}
	}()
	server, err := s.currentMCP(id, revision)
	if err != nil {
		return profile.MCPServer{}, err
	}
	if server.HTTP == nil || server.HTTP.Auth != profile.MCPHTTPAuthOAuth || server.HTTP.OAuth == nil {
		return profile.MCPServer{}, errors.New("MCP server does not use OAuth")
	}
	oldRefs := profile.MCPSecretReferences(server, true)
	oldSession := server.HTTP.OAuth.SessionRef
	updatedOAuth := *server.HTTP.OAuth
	updatedOAuth.SessionRef = oauth.SessionRef
	updatedOAuth.Status, updatedOAuth.Issuer = oauth.Status, oauth.Issuer
	updatedOAuth.GrantedScopes = append([]string{}, oauth.GrantedScopes...)
	updatedOAuth.AccessTokenExpires = oauth.AccessTokenExpires
	server.HTTP.OAuth = &updatedOAuth
	if validationErr := profile.ValidateMCPServer(server); validationErr != nil {
		return profile.MCPServer{}, validationErr
	}
	var minted []credential.SecretID
	if oauth.SessionRef != nil && (oldSession == nil || oldSession.SecretRef != oauth.SessionRef.SecretRef) {
		minted = []credential.SecretID{credential.SecretID(oauth.SessionRef.SecretRef)}
		if attachErr := s.attachMCP(ctx, id, minted); attachErr != nil {
			return profile.MCPServer{}, attachErr
		}
	}
	if prepareErr := s.prepareMCPRetirement(ctx, oldRefs, server); prepareErr != nil {
		return profile.MCPServer{}, prepareErr
	}
	updated, err := s.mcp.Repository.UpdateMCPServer(server)
	// Cleanup on failure checks every surviving holder before deleting material.
	if err != nil {
		return profile.MCPServer{}, err
	}
	committed = true
	s.finalizeMCP(ctx, minted)
	return updated, s.ReconcileMCPSecrets(ctx)
}

func (s *configService) ForgetMCPOAuth(ctx context.Context, id string, revision uint64) (profile.MCPServer, error) {
	return s.CommitMCPOAuth(ctx, id, revision, profile.MCPOAuthConfig{Status: profile.MCPOAuthMissing, GrantedScopes: []string{}})
}

func (s *configService) attachMCP(ctx context.Context, target string, ids []credential.SecretID) error {
	for _, id := range ids {
		if err := s.mcp.Secrets.AttachTarget(ctx, id, target); err != nil {
			return err
		}
	}
	return nil
}

func (s *configService) finalizeMCP(ctx context.Context, ids []credential.SecretID) {
	for _, id := range ids {
		_ = s.mcp.Secrets.CommitMetadata(ctx, id)
	}
}

func (s *configService) rollbackMCP(ctx context.Context, ids []credential.SecretID) error {
	refs := make([]string, len(ids))
	for i, id := range ids {
		refs[i] = string(id)
	}
	// Even a failed metadata save can have an uncertain durable outcome. Ask
	// the aggregate owner; if it cannot answer, retain the Vault journal.
	return s.cleanupMCP(ctx, refs)
}

func (s *configService) cleanupMCP(ctx context.Context, refs []string) error {
	// Fresh rollback must retain identity too: a failed promotion must remain
	// recoverable without relying on this callback's in-memory list.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.prepareMCPRetirement(cleanupCtx, refs, profile.MCPServer{}); err != nil {
		return err
	}
	return s.ReconcileMCPSecrets(cleanupCtx)
}

func (s *configService) prepareMCPRetirement(ctx context.Context, owned []string, next profile.MCPServer) error {
	kept := make(map[string]struct{})
	for _, ref := range profile.MCPSecretReferences(next, false) {
		kept[ref] = struct{}{}
	}
	for _, ref := range owned {
		if _, remains := kept[ref]; remains {
			continue
		}
		// A candidate is nonexecuting and keeps its Vault record. Prepare it
		// before metadata can forget its identity, even when another holder
		// currently shares the secret.
		if err := s.mcp.Secrets.PrepareDelete(ctx, credential.SecretID(ref)); err != nil {
			return err
		}
	}
	return nil
}

// ReconcileMCPSecrets is the only candidate-to-deletion promotion boundary.
// The caller holds config and Vault admission for this entire reference check
// and promotion. Neither startup Vault recovery nor unseal may infer orphanhood.
func (s *configService) ReconcileMCPSecrets(ctx context.Context) error {
	if err := s.mcpReady(true); err != nil {
		return err
	}
	ids, err := s.mcp.Secrets.PendingDeleteCandidates(ctx)
	// No initialized Vault means no durable deletion candidates. Literal-only
	// MCP configuration and application startup do not require Vault setup.
	if errors.Is(err, vault.ErrVaultUninitialized) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	refs := make([]string, len(ids))
	for i, id := range ids {
		refs[i] = string(id)
	}
	unused, err := s.mcp.Repository.UnreferencedSecretRefs(refs)
	if err != nil {
		return err
	}
	var cleanupErr error
	for _, ref := range unused {
		cleanupErr = errors.Join(cleanupErr, s.mcp.Secrets.ScheduleDelete(ctx, credential.SecretID(ref)))
	}
	return cleanupErr
}

func sameMCPOAuthIdentity(oldEndpoint, endpoint string, old, next *profile.MCPOAuthConfig) bool {
	return oldEndpoint == endpoint && old.Registration == next.Registration && old.ClientID == next.ClientID &&
		reflect.DeepEqual(old.ClientSecret, next.ClientSecret) && reflect.DeepEqual(old.Scopes, next.Scopes)
}

func (s *configService) materializeMCP(ctx context.Context, in MCPServerConfigInput, current *profile.MCPServer) (profile.MCPServer, []credential.SecretID, error) {
	if err := ValidateMCPServerConfigInput(in, current != nil); err != nil {
		return profile.MCPServer{}, nil, err
	}
	var minted []credential.SecretID
	server, err := projectMCPConfig(in, current, func(input MCPSecretBindingInput, old *profile.MCPSecretBinding) (profile.MCPSecretBinding, error) {
		binding, made, err := s.materializeMCPSecret(ctx, input, old)
		minted = append(minted, made...)
		return binding, err
	})
	return server, minted, err
}

func (s *configService) materializeMCPSecret(ctx context.Context, in MCPSecretBindingInput, old *profile.MCPSecretBinding) (profile.MCPSecretBinding, []credential.SecretID, error) {
	// All source/config semantics were validated before the first mint. Only
	// repository-dependent binding availability remains to be resolved here.
	if in.Keep {
		if old == nil || old.SecretRef == "" {
			return profile.MCPSecretBinding{}, nil, errors.New("no existing secret is available to keep")
		}
		return *old, nil, nil
	}
	if in.Secret != nil {
		id, err := s.mcpRowToRef(*in.Secret)
		if err != nil {
			return profile.MCPSecretBinding{}, nil, errors.New("cannot resolve vault secret row")
		}
		return profile.MCPSecretBinding{SecretRef: id}, nil, nil
	}
	id, err := s.mcp.Secrets.CreateNamed(ctx, credential.NewSecret(*in.SecretValue), vault.SecretMeta{Kind: vault.KindAPIToken})
	if err != nil {
		return profile.MCPSecretBinding{}, nil, err
	}
	return profile.MCPSecretBinding{SecretRef: string(id), Owned: true}, []credential.SecretID{id}, nil
}
