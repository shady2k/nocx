package transport

import (
	"encoding/json"
	"fmt"

	"github.com/shady2k/nocx/internal/profile"
)

// ---------------------------------------------------------------------------
// profiles.effective — batched effective profile resolution with provenance
// ---------------------------------------------------------------------------

type effectiveParams struct {
	IDs []string `json:"ids"`
}

// profileErrorEntry is a typed per-profile error in the batch response.
type profileErrorEntry struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

type effectiveResponse struct {
	Profiles []profile.EffectiveProfileDTO `json:"profiles"`
	Errors   []profileErrorEntry           `json:"errors,omitempty"`
}

func (s *WSServer) handleEffective(wconn *wsConn, req jsonrpcRequest) {
	var params effectiveParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params"))
		return
	}

	if len(params.IDs) == 0 {
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(effectiveResponse{})))
		return
	}

	allProfiles, err := s.profiles.LoadProfiles()
	if err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, fmt.Sprintf("load profiles: %v", err)))
		return
	}
	allGroups, err := s.groups.LoadGroups()
	if err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, fmt.Sprintf("load groups: %v", err)))
		return
	}
	// Build lookups first.
	profByID := make(map[string]profile.SSHProfile, len(allProfiles))
	for _, p := range allProfiles {
		profByID[p.ID] = p
	}
	groupByID := make(map[string]profile.ProfileGroup, len(allGroups))
	for _, g := range allGroups {
		groupByID[g.ID] = g
	}

	var dtos []profile.EffectiveProfileDTO
	var errs []profileErrorEntry

	for _, id := range params.IDs {
		p, ok := profByID[id]
		if !ok {
			errs = append(errs, profileErrorEntry{ID: id, Error: "profile not found"})
			continue
		}

		// Identity lives inline on the profile (ADR-0017): the effective
		// options are the resolved options.
		eff, err := profile.ResolveEffectiveProfile(p, allGroups, profile.SparseSSHOptions{})
		if err != nil {
			errs = append(errs, profileErrorEntry{ID: id, Error: err.Error()})
			continue
		}

		// Secret references stay backend-owned: hand the renderer row handles.
		dto := profile.ToEffectiveDTO(eff, groupByID)
		wireEffectiveSecretFields(&dto)
		dtos = append(dtos, dto)
	}

	_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(effectiveResponse{
		Profiles: dtos,
		Errors:   errs,
	})))
}

// ---------------------------------------------------------------------------
// profiles.patch — explicit set and unset of specific fields
// ---------------------------------------------------------------------------

type patchParams struct {
	ID    string         `json:"id"`
	Set   map[string]any `json:"set,omitempty"`
	Unset []string       `json:"unset,omitempty"`
}

func validatePatch(p patchParams) error {
	if p.ID == "" {
		return fmt.Errorf("id required")
	}
	for path := range p.Set {
		if !profile.PatchPathAllowed(path) {
			return fmt.Errorf("unknown set path: %s", path)
		}
	}
	for _, path := range p.Unset {
		if !profile.PatchPathAllowed(path) {
			return fmt.Errorf("unknown unset path: %s", path)
		}
	}
	// Disjoint: no path in both set and unset.
	for path := range p.Set {
		for _, upath := range p.Unset {
			if path == upath {
				return fmt.Errorf("path %q is in both set and unset", path)
			}
		}
	}
	return nil
}

func (s *WSServer) handlePatch(wconn *wsConn, req jsonrpcRequest) {
	var params patchParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params"))
		return
	}

	if err := validatePatch(params); err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, err.Error()))
		return
	}

	allProfiles, err := s.profiles.LoadProfiles()
	if err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, fmt.Sprintf("load profiles: %v", err)))
		return
	}
	allGroups, err := s.groups.LoadGroups()
	if err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, fmt.Sprintf("load groups: %v", err)))
		return
	}
	// No credential layer exists (ADR-0017): identity lives inline on the
	// profile, and the effective options are the resolved options.

	// Build lookups first for later use.
	groupByID := make(map[string]profile.ProfileGroup, len(allGroups))
	for _, g := range allGroups {
		groupByID[g.ID] = g
	}

	var target *profile.SSHProfile
	for i := range allProfiles {
		if allProfiles[i].ID == params.ID {
			target = &allProfiles[i]
			break
		}
	}
	if target == nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, fmt.Sprintf("profile %q not found", params.ID)))
		return
	}

	// Apply set/unset operations directly on the stored (presence-aware)
	// options. The renderer names secrets by row handle: resolve the three
	// secret paths' values to references before they are stored.
	opts := &target.Options
	for path, value := range params.Set {
		switch path {
		case "options.passwordSecret", "options.keySecret", "options.keyPassphraseSecret":
			row, isStr := value.(string)
			if !isStr {
				_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, path+" must be a string"))
				return
			}
			resolved, resolveErr := s.rowToSecretRef(row)
			if resolveErr != nil {
				_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, resolveErr.Error()))
				return
			}
			params.Set[path] = resolved
		}
		profile.ApplyPatchSet(opts, path, params.Set[path])
	}
	for _, path := range params.Unset {
		profile.ApplyPatchUnset(opts, path)
	}

	// Validate: host is required and cannot be unset-made-empty.
	if opts.Host == "" {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "host is required and cannot be unset"))
		return
	}

	// Persist — UpdateProfile writes the presence-aware options directly.
	if updateErr := s.profiles.UpdateProfile(*target); updateErr != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, profileMethodErrorCode(updateErr), updateErr.Error()))
		return
	}

	// Resolve effective profile from the patched stored options directly.
	// ResolveEffectiveProfile reads the presence-aware StoredSSHProfileOptions
	// and produces dense resolved values with provenance.
	eff, err := profile.ResolveEffectiveProfile(*target, allGroups, profile.SparseSSHOptions{})
	if err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, fmt.Sprintf("resolve after patch: %v", err)))
		return
	}

	dto := profile.ToEffectiveDTO(eff, groupByID)
	wireEffectiveSecretFields(&dto)
	_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(dto)))
}
