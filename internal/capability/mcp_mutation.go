package capability

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/profile"
)

// MCPMutationTarget is backend-only. Exactly the field named by Kind is set.
// Snapshotting never resolves secret material or enters the runtime barrier.
type MCPMutationKind string

const (
	MCPMutationServer             MCPMutationKind = "server"
	MCPMutationEndpointCredential MCPMutationKind = "endpoint-credential" // #nosec G101 — mutation-kind discriminator, not credential material.
	MCPMutationSecretRow          MCPMutationKind = "secret-row"
	MCPMutationReset              MCPMutationKind = "reset"
)

type MCPMutationTarget struct {
	Kind       MCPMutationKind
	ServerID   string
	EndpointID string
	SecretRow  string
}

type MCPServerIdentity struct {
	ID       string
	Revision uint64
}

// MCPMutationSnapshot exposes only copies of the barrier's required identities.
// Its private capture is authoritative; alteration of exported fields is refused
// rather than allowing a caller to drain a smaller set than the commit checks.
// This value must never be serialized as a wire response.
type MCPMutationSnapshot struct {
	Servers []MCPServerIdentity
	// ReferenceHolders excludes credential-producing authorizations that must
	// be drained by reset but have not attached any secret metadata yet.
	ReferenceHolders []MCPServerIdentity
	SecretIDs        []credential.SecretID
	capture          *mcpMutationCapture
}

type mcpMutationCapture struct {
	target           MCPMutationTarget
	identity         string
	servers          []MCPServerIdentity
	referenceHolders []MCPServerIdentity
	secretIDs        []credential.SecretID
}

type mcpMutationSource struct {
	repo     MCPConfigRepository
	endpoint func(string) (profile.Endpoint, error)
	row      func(string) (string, error)
}

func (s *configService) SnapshotMCPMutation(target MCPMutationTarget) (MCPMutationSnapshot, error) {
	if err := s.mcpReady(false); err != nil {
		return MCPMutationSnapshot{}, err
	}
	return (mcpMutationSource{repo: s.mcp.Repository, endpoint: s.GetEndpoint, row: s.mcpRowToRef}).snapshot(target)
}

func (s *configService) VerifyMCPMutation(snapshot MCPMutationSnapshot) error {
	if err := s.mcpReady(false); err != nil {
		return err
	}
	return (mcpMutationSource{repo: s.mcp.Repository, endpoint: s.GetEndpoint, row: s.mcpRowToRef}).verify(snapshot)
}

func (s *secretService) VerifyMCPMutation(snapshot MCPMutationSnapshot) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	if snapshot.capture == nil || snapshot.capture.target.Kind != MCPMutationSecretRow {
		return profile.ErrMCPServerConflict
	}
	return (mcpMutationSource{repo: s.mcp.Repository, row: func(row string) (string, error) {
		return resolveMCPRow(row, s.vault, s.profiles, s.groups)
	}}).verify(snapshot)
}

func (s *configService) mcpRowToRef(row string) (string, error) {
	return resolveMCPRow(row, s.rows, s.profiles, s.groups)
}

// Use the ordinary secret service's current inventory projection on both
// sides of the drain interval, including inherited group bindings.
func resolveMCPRow(row string, rows RowResolver, profiles profile.ProfileRepository, groups profile.GroupRepository) (string, error) {
	if rows == nil || profiles == nil || groups == nil {
		return "", ErrMCPUnavailable
	}
	ps, err := profiles.LoadProfiles()
	if err != nil {
		return "", err
	}
	gs, err := groups.LoadGroups()
	if err != nil {
		return "", err
	}
	id, ok := rows.ResolveRow(row, inventoryInputs(ps, gs))
	if !ok {
		return "", errors.New("unknown vault secret row")
	}
	return string(id), nil
}

func (s *vaultResetService) VerifyMCPMutation(snapshot MCPMutationSnapshot) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	if snapshot.capture == nil || snapshot.capture.target.Kind != MCPMutationReset {
		return profile.ErrMCPServerConflict
	}
	return (mcpMutationSource{repo: s.mcp.Repository}).verify(snapshot)
}

func (source mcpMutationSource) verify(snapshot MCPMutationSnapshot) error {
	if source.repo == nil {
		return ErrMCPUnavailable
	}
	captured := snapshot.capture
	if captured == nil || !reflect.DeepEqual(snapshot.Servers, captured.servers) || !reflect.DeepEqual(snapshot.ReferenceHolders, captured.referenceHolders) || !reflect.DeepEqual(snapshot.SecretIDs, captured.secretIDs) {
		return profile.ErrMCPServerConflict
	}
	current, err := source.snapshot(captured.target)
	if err != nil {
		// Deletion or loss of a formerly selectable row is a visible conflict,
		// never permission to commit against a different or missing target.
		return errors.Join(profile.ErrMCPServerConflict, err)
	}
	if !reflect.DeepEqual(current.capture, captured) {
		return profile.ErrMCPServerConflict
	}
	return nil
}

func (source mcpMutationSource) snapshot(target MCPMutationTarget) (MCPMutationSnapshot, error) {
	if source.repo == nil {
		return MCPMutationSnapshot{}, ErrMCPUnavailable
	}
	var servers []profile.MCPServer
	var refs []string
	referenceHolders := make([]MCPServerIdentity, 0)
	identity := ""
	switch target.Kind {
	case MCPMutationServer:
		if target.ServerID == "" || target.EndpointID != "" || target.SecretRow != "" {
			return MCPMutationSnapshot{}, errors.New("invalid MCP server mutation target")
		}
		server, err := source.repo.GetMCPServer(target.ServerID)
		if err != nil {
			return MCPMutationSnapshot{}, err
		}
		servers = []profile.MCPServer{server}
		refs = profile.MCPSecretReferences(server, false)
		encoded, err := json.Marshal(server)
		if err != nil {
			return MCPMutationSnapshot{}, err
		}
		identity = string(encoded)
	case MCPMutationEndpointCredential:
		if target.EndpointID == "" || target.ServerID != "" || target.SecretRow != "" || source.endpoint == nil {
			return MCPMutationSnapshot{}, errors.New("invalid endpoint mutation target")
		}
		endpoint, err := source.endpoint(target.EndpointID)
		if errors.Is(err, profile.ErrEndpointNotFound) {
			// Endpoint deletion is idempotent. Absence is an explicit captured
			// identity, not a lookup failure or permission to delete a newly
			// created endpoint after the runtime drain interval.
			identity = "absent"
			break
		}
		if err != nil {
			return MCPMutationSnapshot{}, err
		}
		// Capture the current credential, not a proposed replacement's row.
		if endpoint.CredentialRef != "" {
			refs = []string{endpoint.CredentialRef}
		}
		encoded, err := json.Marshal(endpoint)
		if err != nil {
			return MCPMutationSnapshot{}, err
		}
		identity = string(encoded)
		servers, err = source.repo.MCPServersReferencingSecrets(refs)
		if err != nil {
			return MCPMutationSnapshot{}, err
		}
	case MCPMutationSecretRow:
		if target.SecretRow == "" || target.ServerID != "" || target.EndpointID != "" || source.row == nil {
			return MCPMutationSnapshot{}, errors.New("invalid secret mutation target")
		}
		ref, err := source.row(target.SecretRow)
		if err != nil {
			return MCPMutationSnapshot{}, err
		}
		if ref == "" {
			return MCPMutationSnapshot{}, errors.New("unknown vault secret row")
		}
		refs = []string{ref}
		identity = ref
		servers, err = source.repo.MCPServersReferencingSecrets(refs)
		if err != nil {
			return MCPMutationSnapshot{}, err
		}
	case MCPMutationReset:
		if target.ServerID != "" || target.EndpointID != "" || target.SecretRow != "" {
			return MCPMutationSnapshot{}, errors.New("invalid reset mutation target")
		}
		all, err := source.repo.ListMCPServers()
		if err != nil {
			return MCPMutationSnapshot{}, err
		}
		for _, server := range all {
			serverRefs := profile.MCPSecretReferences(server, false)
			canAuthorize := server.HTTP != nil && server.HTTP.Auth == profile.MCPHTTPAuthOAuth
			if len(serverRefs) == 0 && !canAuthorize {
				continue
			}
			servers = append(servers, server)
			refs = append(refs, serverRefs...)
			if len(serverRefs) != 0 {
				referenceHolders = append(referenceHolders, MCPServerIdentity{ID: server.ID, Revision: server.Revision})
			}
		}
		sort.Slice(referenceHolders, func(i, j int) bool { return referenceHolders[i].ID < referenceHolders[j].ID })
	default:
		return MCPMutationSnapshot{}, errors.New("unknown MCP mutation target")
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].ID < servers[j].ID })
	identities := make([]MCPServerIdentity, len(servers))
	for i, server := range servers {
		identities[i] = MCPServerIdentity{ID: server.ID, Revision: server.Revision}
	}
	if target.Kind != MCPMutationReset {
		referenceHolders = identities
	}
	sort.Strings(refs)
	secretIDs := make([]credential.SecretID, 0, len(refs))
	for i, ref := range refs {
		if ref != "" && (i == 0 || refs[i-1] != ref) {
			secretIDs = append(secretIDs, credential.SecretID(ref))
		}
	}
	return MCPMutationSnapshot{
		Servers:          append([]MCPServerIdentity{}, identities...),
		ReferenceHolders: append([]MCPServerIdentity{}, referenceHolders...),
		SecretIDs:        append([]credential.SecretID{}, secretIDs...),
		capture:          &mcpMutationCapture{target: target, identity: identity, servers: identities, referenceHolders: referenceHolders, secretIDs: secretIDs},
	}, nil
}
