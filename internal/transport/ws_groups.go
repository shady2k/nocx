package transport

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/shady2k/nocx/internal/profile"
)

// ---------------------------------------------------------------------------
// groups.impact — compute the effect of a proposed group change
// ---------------------------------------------------------------------------

// groupImpactParams is the request for groups.impact.
// For an update preview, set Group. For a delete preview, set DeleteGroupID.
type groupImpactParams struct {
	Group         *profile.ProfileGroup `json:"group,omitempty"`
	DeleteGroupID string                `json:"deleteGroupId,omitempty"`
}

func (p groupImpactParams) validate() error {
	if p.Group != nil && p.DeleteGroupID != "" {
		return errors.New("only one of group or deleteGroupId may be set")
	}
	if p.Group == nil && p.DeleteGroupID == "" {
		return errors.New("either group or deleteGroupId is required")
	}
	if p.Group != nil && p.Group.ID == "" {
		return errors.New("group.id is required")
	}
	return nil
}

// fieldDiff describes one effective-field change.
type fieldDiff struct {
	Field     string      `json:"field"`
	OldValue  interface{} `json:"oldValue,omitempty"`
	NewValue  interface{} `json:"newValue,omitempty"`
	Dangerous bool        `json:"dangerous"`
}

// profileImpact describes the effective-field diff for one profile.
type profileImpact struct {
	ProfileID   string      `json:"profileId"`
	ProfileName string      `json:"profileName"`
	Diffs       []fieldDiff `json:"diffs"`
}

// deleteImpact describes what happens to children on group deletion.
type deleteImpact struct {
	Action           string   `json:"action"`                     // "promote_to_root", "refuse"
	Reason           string   `json:"reason"`                     // human-readable explanation
	AffectedGroupIDs []string `json:"affectedGroupIds,omitempty"` // child groups that would be reparented
}

// groupImpactResponse is the response for groups.impact.
type groupImpactResponse struct {
	Dangerous        bool            `json:"dangerous"`
	AffectedProfiles []profileImpact `json:"affectedProfiles,omitempty"`
	DeleteImpact     *deleteImpact   `json:"deleteImpact,omitempty"`
}

// dangerousFields is the set of field names whose change is auth-affecting.
var dangerousFields = map[string]bool{
	"passwordSecret":      true,
	"keySecret":           true,
	"keyPassphraseSecret": true,
	"user":                true,
	"auth":                true,
	"jumpHost":            true,
	"port":                true,
}

func isDangerousField(field string) bool {
	return dangerousFields[field]
}

func (s *WSServer) handleGroupImpact(wconn *wsConn, req jsonrpcRequest) {
	if s.groups == nil || s.profiles == nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32601, "groups not available"))
		return
	}

	var params groupImpactParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params"))
		return
	}
	if err := params.validate(); err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, err.Error()))
		return
	}

	allProfiles, err := s.profiles.LoadProfiles()
	if err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, err.Error()))
		return
	}
	allGroups, err := s.groups.LoadGroups()
	if err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, err.Error()))
		return
	}

	if params.Group != nil {
		// The renderer proposes bindings by row handle: resolve them to
		// stored references before computing impact, or the resolution of
		// the proposed defaults would carry row handles into the diff.
		proposed, werr := s.groupFromWire(*params.Group)
		if werr != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, werr.Error()))
			return
		}
		resp := computeGroupUpdateImpact(proposed, allProfiles, allGroups)
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(resp)))
	} else {
		resp := computeGroupDeleteImpact(params.DeleteGroupID, allProfiles, allGroups)
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(resp)))
	}
}

// computeGroupUpdateImpact computes the impact of updating a group's
// ParentGroupID or Defaults. Returns the impact response.
func computeGroupUpdateImpact(
	proposed profile.ProfileGroup,
	allProfiles []profile.SSHProfile,
	allGroups []profile.ProfileGroup,
) groupImpactResponse {
	// Find the current group position.
	curIdx := -1
	for i, g := range allGroups {
		if g.ID == proposed.ID {
			curIdx = i
			break
		}
	}
	if curIdx < 0 {
		return groupImpactResponse{
			Dangerous: false,
			DeleteImpact: &deleteImpact{
				Action: "refuse",
				Reason: fmt.Sprintf("group %q not found", proposed.ID),
			},
		}
	}

	// Build modified group list: replace the current group with the proposed one.
	modifiedGroups := make([]profile.ProfileGroup, len(allGroups))
	copy(modifiedGroups, allGroups)
	modifiedGroups[curIdx] = proposed

	// Validate the modified group tree.
	if err := profile.ValidateGroupTree(modifiedGroups); err != nil {
		return groupImpactResponse{
			Dangerous: false,
			DeleteImpact: &deleteImpact{
				Action: "refuse",
				Reason: err.Error(),
			},
		}
	}

	// Resolve every profile with current groups and with modified groups.
	// Collect only profiles whose effective options actually change.
	var impacts []profileImpact
	anyDangerous := false

	for _, p := range allProfiles {
		oldEff, oldErr := profile.ResolveEffectiveProfile(p, allGroups, profile.SparseSSHOptions{})
		newEff, newErr := profile.ResolveEffectiveProfile(p, modifiedGroups, profile.SparseSSHOptions{})

		// If both fail resolution the same way, nothing changed.
		if oldErr != nil && newErr != nil && oldErr.Error() == newErr.Error() {
			continue
		}

		// Identity lives inline on the profile (ADR-0017): no credential
		// layer is applied.

		// Compute diffs between old and new resolved options.
		diffs := diffResolvedOptions(oldEff, newEff, oldErr, newErr)
		if len(diffs) == 0 {
			continue
		}

		for _, d := range diffs {
			if d.Dangerous {
				anyDangerous = true
			}
		}

		impacts = append(impacts, profileImpact{
			ProfileID:   p.ID,
			ProfileName: p.Name,
			Diffs:       diffs,
		})
	}

	if len(impacts) == 0 {
		return groupImpactResponse{Dangerous: false}
	}

	sort.Slice(impacts, func(i, j int) bool {
		return impacts[i].ProfileID < impacts[j].ProfileID
	})

	return groupImpactResponse{
		Dangerous:        anyDangerous,
		AffectedProfiles: impacts,
	}
}

// computeGroupDeleteImpact computes the impact of deleting a group.
func computeGroupDeleteImpact(
	deleteGroupID string,
	allProfiles []profile.SSHProfile,
	allGroups []profile.ProfileGroup,
) groupImpactResponse {
	// Find the group to delete.
	found := false
	for _, g := range allGroups {
		if g.ID == deleteGroupID {
			found = true
			break
		}
	}
	if !found {
		return groupImpactResponse{
			DeleteImpact: &deleteImpact{
				Action: "refuse",
				Reason: fmt.Sprintf("group %q not found", deleteGroupID),
			},
		}
	}

	// Find children — groups with this group as parent.
	var childGroups []profile.ProfileGroup
	for _, g := range allGroups {
		if g.ParentGroupID == deleteGroupID {
			childGroups = append(childGroups, g)
		}
	}

	childIDs := make([]string, len(childGroups))
	for i, g := range childGroups {
		childIDs[i] = g.ID
	}

	// Build modified groups: remove the deleted group, promote children to root.
	modifiedGroups := make([]profile.ProfileGroup, 0, len(allGroups))
	for _, g := range allGroups {
		if g.ID == deleteGroupID {
			continue
		}
		if g.ParentGroupID == deleteGroupID {
			g.ParentGroupID = ""
		}
		modifiedGroups = append(modifiedGroups, g)
	}

	// Validate the modified tree.
	if err := profile.ValidateGroupTree(modifiedGroups); err != nil {
		return groupImpactResponse{
			DeleteImpact: &deleteImpact{
				Action: "refuse",
				Reason: err.Error(),
			},
		}
	}

	di := &deleteImpact{
		Action:           "promote_to_root",
		AffectedGroupIDs: childIDs,
	}
	if len(childGroups) == 0 {
		di.Reason = "group has no children"
	} else if len(childGroups) == 1 {
		di.Reason = fmt.Sprintf("1 child group (%s) will be promoted to root", childGroups[0].Name)
	} else {
		di.Reason = fmt.Sprintf("%d child groups will be promoted to root", len(childGroups))
	}

	// Compute profile impact.
	var impacts []profileImpact
	anyDangerous := false

	for _, p := range allProfiles {
		oldEff, oldErr := profile.ResolveEffectiveProfile(p, allGroups, profile.SparseSSHOptions{})
		newEff, newErr := profile.ResolveEffectiveProfile(p, modifiedGroups, profile.SparseSSHOptions{})

		if oldErr != nil && newErr != nil && oldErr.Error() == newErr.Error() {
			continue
		}
		// Identity lives inline on the profile (ADR-0017): no credential
		// layer is applied.

		diffs := diffResolvedOptions(oldEff, newEff, oldErr, newErr)
		if len(diffs) == 0 {
			continue
		}

		for _, d := range diffs {
			if d.Dangerous {
				anyDangerous = true
			}
		}

		impacts = append(impacts, profileImpact{
			ProfileID:   p.ID,
			ProfileName: p.Name,
			Diffs:       diffs,
		})
	}

	sort.Slice(impacts, func(i, j int) bool {
		return impacts[i].ProfileID < impacts[j].ProfileID
	})

	return groupImpactResponse{
		Dangerous:        anyDangerous,
		AffectedProfiles: impacts,
		DeleteImpact:     di,
	}
}

// diffResolvedOptions computes the field-by-field diff between two resolved
// effective profiles. Either or both sides may be resolving with errors.
func diffResolvedOptions(oldEff, newEff profile.EffectiveProfile, oldErr, newErr error) []fieldDiff {
	var diffs []fieldDiff

	// Handle resolution changes.
	if (oldErr == nil) != (newErr == nil) {
		if oldErr == nil && newErr != nil {
			diffs = append(diffs, fieldDiff{
				Field:     "_error",
				OldValue:  "resolvable",
				NewValue:  newErr.Error(),
				Dangerous: true,
			})
		} else {
			diffs = append(diffs, fieldDiff{
				Field:     "_error",
				OldValue:  oldErr.Error(),
				NewValue:  "resolvable",
				Dangerous: true,
			})
		}
		return diffs
	}

	// Both sides have errors — no meaningful diff.
	if oldErr != nil {
		return nil
	}

	// Both sides resolved: compare individual fields.
	oldOpts := oldEff.ResolvedOptions
	newOpts := newEff.ResolvedOptions

	addDiff := func(field string, oldVal, newVal interface{}) {
		// Only report actual changes.
		if oldVal == newVal {
			return
		}
		// For string/int zero values, compare more carefully.
		diffs = append(diffs, fieldDiff{
			Field:     field,
			OldValue:  oldVal,
			NewValue:  newVal,
			Dangerous: isDangerousField(field),
		})
	}

	addDiff("passwordSecret", secretRefToRow(oldOpts.PasswordSecret), secretRefToRow(newOpts.PasswordSecret))
	addDiff("keySecret", secretRefToRow(oldOpts.KeySecret), secretRefToRow(newOpts.KeySecret))
	addDiff("keyPassphraseSecret", secretRefToRow(oldOpts.KeyPassphraseSecret), secretRefToRow(newOpts.KeyPassphraseSecret))
	addDiff("port", oldOpts.Port, newOpts.Port)
	addDiff("user", oldOpts.User, newOpts.User)
	addDiff("auth", oldOpts.Auth, newOpts.Auth)
	addDiff("jumpHost", oldOpts.JumpHost, newOpts.JumpHost)
	addDiff("keepaliveInterval", oldOpts.KeepaliveInterval, newOpts.KeepaliveInterval)
	addDiff("keepaliveCountMax", oldOpts.KeepaliveCountMax, newOpts.KeepaliveCountMax)
	addDiff("readyTimeout", oldOpts.ReadyTimeout, newOpts.ReadyTimeout)
	addDiff("agentForward", oldOpts.AgentForward, newOpts.AgentForward)
	return diffs
}

// ---------------------------------------------------------------------------
// profiles.moveImpact — compute the effect of moving a profile to a new group
// ---------------------------------------------------------------------------

// profileMoveImpactParams is the request for profiles.moveImpact.
// TargetGroupID may be empty to indicate promotion to root.
type profileMoveImpactParams struct {
	ProfileIDs    []string `json:"profileIds"`
	TargetGroupID string   `json:"targetGroupId"`
}

func (p profileMoveImpactParams) validate() error {
	if len(p.ProfileIDs) == 0 {
		return errors.New("profileIds is required")
	}
	return nil
}

func (s *WSServer) handleProfileMoveImpact(wconn *wsConn, req jsonrpcRequest) {
	if s.groups == nil || s.profiles == nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32601, "profiles not available"))
		return
	}

	var params profileMoveImpactParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params"))
		return
	}
	if err := params.validate(); err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, err.Error()))
		return
	}

	allProfiles, err := s.profiles.LoadProfiles()
	if err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, err.Error()))
		return
	}
	allGroups, err := s.groups.LoadGroups()
	if err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, err.Error()))
		return
	}

	resp := computeProfileMoveImpact(params.ProfileIDs, params.TargetGroupID, allProfiles, allGroups)
	_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(resp)))
}

// computeProfileMoveImpact computes the impact of moving one or more profiles
// to a target group (or root, when targetGroupID is empty). Each profile is
// resolved twice — once with its current group, once with the proposed group —
// and the effective-field diff is returned. The response shape matches
// groups.impact so the renderer needs only one diff display.
func computeProfileMoveImpact(
	profileIDs []string,
	targetGroupID string,
	allProfiles []profile.SSHProfile,
	allGroups []profile.ProfileGroup,
) groupImpactResponse {
	// Build profile lookup.
	profileByID := make(map[string]profile.SSHProfile, len(allProfiles))
	for _, p := range allProfiles {
		profileByID[p.ID] = p
	}

	var impacts []profileImpact
	anyDangerous := false

	for _, id := range profileIDs {
		prof, ok := profileByID[id]
		if !ok {
			continue
		}

		// Resolve with current group.
		oldEff, oldErr := profile.ResolveEffectiveProfile(prof, allGroups, profile.SparseSSHOptions{})

		// Build modified profile with the proposed group.
		modifiedProf := prof
		modifiedProf.Group = targetGroupID
		newEff, newErr := profile.ResolveEffectiveProfile(modifiedProf, allGroups, profile.SparseSSHOptions{})

		// If both fail resolution the same way, nothing changed.
		if oldErr != nil && newErr != nil && oldErr.Error() == newErr.Error() {
			continue
		}

		// Identity lives inline on the profile (ADR-0017): no credential
		// layer is applied.

		// Compute diffs.
		diffs := diffResolvedOptions(oldEff, newEff, oldErr, newErr)
		if len(diffs) == 0 {
			continue
		}

		for _, d := range diffs {
			if d.Dangerous {
				anyDangerous = true
			}
		}

		impacts = append(impacts, profileImpact{
			ProfileID:   prof.ID,
			ProfileName: prof.Name,
			Diffs:       diffs,
		})
	}

	if len(impacts) == 0 {
		return groupImpactResponse{Dangerous: false}
	}

	sort.Slice(impacts, func(i, j int) bool {
		return impacts[i].ProfileID < impacts[j].ProfileID
	})

	return groupImpactResponse{
		Dangerous:        anyDangerous,
		AffectedProfiles: impacts,
	}
}

// ---------------------------------------------------------------------------
// groups.apply —  apply one or more group changes atomically
// ---------------------------------------------------------------------------

// handleGroupApply applies one or more full group updates atomically. The
// renderer MUST have called groups.impact first and shown the result to the
// user. This is the write path for ParentGroupID and Defaults changes.
//
// Unlike the old handler which called LoadGroups() → validate → UpdateGroup(g)
// in three separate lock acquisitions, this handler delegates to the store's
// ApplyGroups which loads, validates, and writes under a single lock.
func (s *WSServer) handleGroupApply(wconn *wsConn, req jsonrpcRequest) {
	if s.groups == nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32601, "groups not available"))
		return
	}

	var groups []profile.ProfileGroup
	if err := json.Unmarshal(req.Params, &groups); err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params"))
		return
	}
	if len(groups) == 0 {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "groups required"))
		return
	}

	// The renderer names secret bindings by row handle (ADR-0011 §2):
	// resolve them to stored references so storage never holds a secrow.
	for i := range groups {
		wg, werr := s.groupFromWire(groups[i])
		if werr != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, werr.Error()))
			return
		}
		groups[i] = wg
	}

	ag, ok := s.groups.(interface {
		ApplyGroups([]profile.ProfileGroup) error
	})
	if !ok {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "group store does not support atomic apply"))
		return
	}
	if err := ag.ApplyGroups(groups); err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, profileMethodErrorCode(err), err.Error()))
		return
	}

	// The echo carries the row handles the renderer addressed, never the
	// stored references (ADR-0011 §2).
	for i := range groups {
		groups[i] = wireGroup(groups[i])
	}
	_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(groups)))
}
