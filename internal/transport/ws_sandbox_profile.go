package transport

// sandbox.profile.get / sandbox.profile.set / sandbox.grant.get — the
// workspace-profile and grant-provenance surface (design 2026-08-23 §4).
//
// Profiles are PrivateMetadata (paths appear only in these explicit results);
// they are durable defaults, never authority. The renderer never chooses the
// workspace, the profile source, or the effective roots: it names a pane or
// sends a revision-bounded delta, and the backend resolves pane → workspace
// and composes the profile.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/sandbox"
	"github.com/shady2k/nocx/internal/settings"
)

// profileLayout returns the profile repository seam, or nil when the content
// store is not wired. The profile writes run through the content writer
// goroutine inside SetWorkspaceSandboxProfile, so no capability gate is
// acquired here (design 2026-08-23 §8).
func (s *WSServer) profileLayout() sandboxProfileLayout {
	if s.contentDB == nil {
		return nil
	}
	return s.contentDB.Layout()
}

// sandboxProfileLayout is the narrow layout seam the profile handlers need.
// It is the content.LayoutRepository read/write surface for profiles and the
// grant query; never the whole server.
type sandboxProfileLayout interface {
	WorkspaceForPane(ctx context.Context, paneID string) (string, error)
	WorkspaceSandboxProfile(ctx context.Context, workspaceID string) (*content.WorkspaceSandboxProfile, error)
	SetWorkspaceSandboxProfile(ctx context.Context, workspaceID string, expectedRevision int64, profile content.WorkspaceSandboxProfile) (int64, error)
	DeleteWorkspaceSandboxProfile(ctx context.Context, workspaceID string, expectedRevision int64) error
	SandboxGrantForPane(ctx context.Context, paneID string) (*content.SandboxGrant, error)
}

// workspaceProfileReader is the read half shared with the open handler.
type workspaceProfileReader interface {
	WorkspaceSandboxProfile(ctx context.Context, workspaceID string) (*content.WorkspaceSandboxProfile, error)
}

type sandboxProfileHandlers struct {
	layout   sandboxProfileLayout
	settings capability.SettingsService
	r        Responder
}

// effectiveSandboxProfile is the resolved profile for a pane's workspace:
// an explicit workspace profile replaces the standard profile; a missing one
// falls back whole to the standard path lists (design 2026-08-23 §4.2).
type effectiveSandboxProfile struct {
	WorkspaceID   string
	Source        string
	Revision      int64
	WritablePaths []string
	ReadOnlyPaths []string
}

func cloneSandboxPaths(paths []string) []string {
	return append(make([]string, 0, len(paths)), paths...)
}

// resolveEffectiveSandboxProfile resolves the effective profile for a
// workspace from an already-read settings snapshot. A workspace profile
// REPLACES the standard profile rather than merging with it.
func resolveEffectiveSandboxProfile(ctx context.Context, layout workspaceProfileReader, snap settings.SettingsSnapshot, workspaceID string) (effectiveSandboxProfile, error) {
	var eff effectiveSandboxProfile
	if layout != nil {
		profile, err := layout.WorkspaceSandboxProfile(ctx, workspaceID)
		if err != nil {
			return eff, err
		}
		if profile != nil {
			return effectiveSandboxProfile{
				WorkspaceID:   workspaceID,
				Source:        sandbox.ProfileSourceWorkspace,
				Revision:      profile.Revision,
				WritablePaths: cloneSandboxPaths(profile.WritablePaths),
				ReadOnlyPaths: cloneSandboxPaths(profile.ReadOnlyPaths),
			}, nil
		}
	}
	writable, readOnly, err := sandboxBaselines(snap)
	if err != nil {
		return eff, err
	}
	return effectiveSandboxProfile{
		WorkspaceID:   workspaceID,
		Source:        sandbox.ProfileSourceStandard,
		Revision:      int64(snap.Revision),
		WritablePaths: cloneSandboxPaths(writable),
		ReadOnlyPaths: cloneSandboxPaths(readOnly),
	}, nil
}

// checkSandboxProfileRevision enforces the open-param revision gate (design
// 2026-08-23 §4.3): a standard-source launch carries profileRevision null; a
// workspace-source launch carries the exact per-workspace revision.
func checkSandboxProfileRevision(profileRevision *int64, eff effectiveSandboxProfile) error {
	switch eff.Source {
	case sandbox.ProfileSourceStandard:
		if profileRevision != nil {
			return errors.New("sandbox profile source changed before launch")
		}
		return nil
	case sandbox.ProfileSourceWorkspace:
		if profileRevision == nil {
			return errors.New("sandbox profile source changed before launch")
		}
		if *profileRevision != eff.Revision {
			return errors.New("sandbox profile revision changed before launch")
		}
		return nil
	default:
		return errors.New("unknown profile source")
	}
}

// ── wire shapes ───────────────────────────────────────────────────────────

type sandboxProfileGetResult struct {
	WorkspaceID   string   `json:"workspaceId"`
	Source        string   `json:"source"`
	Revision      int64    `json:"revision"`
	Inherited     bool     `json:"inherited"`
	WritablePaths []string `json:"writablePaths"`
	ReadOnlyPaths []string `json:"readOnlyPaths"`
}

type sandboxProfileSetResult struct {
	WorkspaceID   string   `json:"workspaceId"`
	Revision      int64    `json:"revision"`
	WritablePaths []string `json:"writablePaths"`
	ReadOnlyPaths []string `json:"readOnlyPaths"`
}

type sandboxProfileDeleteResult struct {
	WorkspaceID string `json:"workspaceId"`
}

type sandboxGrantGetResult struct {
	IssuedAt   int64                   `json:"issuedAt"`
	Realized   *sandbox.SessionInfo    `json:"realized"`
	Provenance sandbox.GrantProvenance `json:"provenance"`
}

// ── params and validators ─────────────────────────────────────────────────

type sandboxProfileGetParams struct {
	PaneID string `json:"paneId"`
}

type sandboxProfileSetParams struct {
	WorkspaceID      string   `json:"workspaceId"`
	ExpectedRevision int64    `json:"expectedRevision"`
	WritablePaths    []string `json:"writablePaths"`
	ReadOnlyPaths    []string `json:"readOnlyPaths"`
}

type sandboxProfileDeleteParams struct {
	WorkspaceID      string `json:"workspaceId"`
	ExpectedRevision int64  `json:"expectedRevision"`
}

type sandboxGrantGetParams struct {
	PaneID string `json:"paneId"`
}

func validatePaneID(value string) string {
	if value == "" {
		return "paneId is required"
	}
	return validateStringBound("paneId", value, maxIDRunes)
}

func validateSandboxProfileGetRaw(raw json.RawMessage) string {
	var params sandboxProfileGetParams
	if msg := decodeSandboxAccessObject(raw, &params); msg != "" {
		return msg
	}
	return validatePaneID(params.PaneID)
}

func validateSandboxProfileSetRaw(raw json.RawMessage) string {
	var params sandboxProfileSetParams
	if msg := decodeSandboxAccessObject(raw, &params); msg != "" {
		return msg
	}
	if params.WorkspaceID == "" {
		return "workspaceId is required"
	}
	if msg := validateStringBound("workspaceId", params.WorkspaceID, maxIDRunes); msg != "" {
		return msg
	}
	if params.ExpectedRevision < 0 {
		return "expectedRevision must be a non-negative integer"
	}
	for _, field := range []struct {
		name  string
		value []string
	}{{"writablePaths", params.WritablePaths}, {"readOnlyPaths", params.ReadOnlyPaths}} {
		if field.value == nil {
			return field.name + " is required"
		}
		if len(field.value) > maxSandboxPaths {
			return fmt.Sprintf("%s must have at most %d entries", field.name, maxSandboxPaths)
		}
		for _, p := range field.value {
			if p == "" {
				return field.name + " entries must be non-empty"
			}
			if msg := validateStringBound(field.name, p, maxCwdRunes); msg != "" {
				return msg
			}
		}
	}
	return ""
}

func validateSandboxProfileDeleteRaw(raw json.RawMessage) string {
	var params sandboxProfileDeleteParams
	if msg := decodeSandboxAccessObject(raw, &params); msg != "" {
		return msg
	}
	if params.WorkspaceID == "" {
		return "workspaceId is required"
	}
	if msg := validateStringBound("workspaceId", params.WorkspaceID, maxIDRunes); msg != "" {
		return msg
	}
	if params.ExpectedRevision < 0 {
		return "expectedRevision must be a non-negative integer"
	}
	return ""
}

func validateSandboxGrantGetRaw(raw json.RawMessage) string {
	var params sandboxGrantGetParams
	if msg := decodeSandboxAccessObject(raw, &params); msg != "" {
		return msg
	}
	return validatePaneID(params.PaneID)
}

// ── handlers ──────────────────────────────────────────────────────────────

func (h sandboxProfileHandlers) handleProfileGet(ctx context.Context, req jsonrpcRequest) {
	if h.layout == nil || h.settings == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "sandbox profiles not available"})
		return
	}
	var params sandboxProfileGetParams
	if msg := decodeSandboxAccessObject(req.Params, &params); msg != "" || params.PaneID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "invalid params"})
		return
	}
	workspaceID, err := h.layout.WorkspaceForPane(ctx, params.PaneID)
	if err != nil {
		_ = h.r.TryError(req.ID, sandboxProfileError(req, err))
		return
	}
	snap, err := h.settings.GetSnapshot()
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "settings unavailable"})
		return
	}
	eff, err := resolveEffectiveSandboxProfile(ctx, h.layout, snap, workspaceID)
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "sandbox profile unavailable"})
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(sandboxProfileGetResult{
		WorkspaceID:   eff.WorkspaceID,
		Source:        eff.Source,
		Revision:      eff.Revision,
		Inherited:     eff.Source == sandbox.ProfileSourceStandard,
		WritablePaths: eff.WritablePaths,
		ReadOnlyPaths: eff.ReadOnlyPaths,
	}))
}

func (h sandboxProfileHandlers) handleProfileSet(ctx context.Context, req jsonrpcRequest) {
	if h.layout == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "sandbox profiles not available"})
		return
	}
	var params sandboxProfileSetParams
	if msg := decodeSandboxAccessObject(req.Params, &params); msg != "" || params.WorkspaceID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "invalid params"})
		return
	}
	canonicalWritable, canonicalReadOnly, err := settings.CanonicalizeSandboxProfile(params.WritablePaths, params.ReadOnlyPaths)
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "invalid params"})
		return
	}
	revision, err := h.layout.SetWorkspaceSandboxProfile(ctx, params.WorkspaceID, params.ExpectedRevision, content.WorkspaceSandboxProfile{
		SchemaVersion: 1,
		WritablePaths: canonicalWritable,
		ReadOnlyPaths: canonicalReadOnly,
	})
	if err != nil {
		_ = h.r.TryError(req.ID, sandboxProfileError(req, err))
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(sandboxProfileSetResult{
		WorkspaceID:   params.WorkspaceID,
		Revision:      revision,
		WritablePaths: cloneSandboxPaths(canonicalWritable),
		ReadOnlyPaths: cloneSandboxPaths(canonicalReadOnly),
	}))
}

func (h sandboxProfileHandlers) handleProfileDelete(ctx context.Context, req jsonrpcRequest) {
	if h.layout == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "sandbox profiles not available"})
		return
	}
	var params sandboxProfileDeleteParams
	if msg := decodeSandboxAccessObject(req.Params, &params); msg != "" || params.WorkspaceID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "invalid params"})
		return
	}
	if err := h.layout.DeleteWorkspaceSandboxProfile(ctx, params.WorkspaceID, params.ExpectedRevision); err != nil {
		_ = h.r.TryError(req.ID, sandboxProfileError(req, err))
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(sandboxProfileDeleteResult{WorkspaceID: params.WorkspaceID}))
}

func (h sandboxProfileHandlers) handleGrantGet(ctx context.Context, req jsonrpcRequest) {
	if h.layout == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "sandbox grants not available"})
		return
	}
	var params sandboxGrantGetParams
	if msg := decodeSandboxAccessObject(req.Params, &params); msg != "" || params.PaneID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "invalid params"})
		return
	}
	grant, err := h.layout.SandboxGrantForPane(ctx, params.PaneID)
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "sandbox grant lookup failed"})
		return
	}
	if grant == nil {
		_ = h.r.TryResult(req.ID, json.RawMessage(`null`))
		return
	}
	realized, provenance, err := sandbox.DecodeGrantPayload([]byte(grant.Payload))
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "sandbox grant is malformed"})
		return
	}
	if provenance.ProfileSource == sandbox.ProfileSourceLegacy {
		workspaceID, workspaceErr := h.layout.WorkspaceForPane(ctx, params.PaneID)
		if workspaceErr != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "sandbox grant lookup failed"})
			return
		}
		provenance.WorkspaceID = workspaceID
	}
	_ = h.r.TryResult(req.ID, mustMarshal(sandboxGrantGetResult{
		IssuedAt:   grant.IssuedAt,
		Realized:   realized,
		Provenance: provenance,
	}))
}

// sandboxProfileError maps repository refusals to their wire codes. A stale
// revision, an unknown workspace, and the default-workspace refusal are all
// the renderer's error (-32602); anything else is internal.
func sandboxProfileError(req jsonrpcRequest, err error) RPCError {
	switch {
	case errors.Is(err, content.ErrSandboxProfileRevision),
		errors.Is(err, content.ErrSandboxProfileAbsent),
		errors.Is(err, content.ErrNoSuchWorkspace),
		errors.Is(err, content.ErrNoSuchPane),
		errors.Is(err, content.ErrDefaultWorkspace):
		return RPCError{Code: -32602, Message: "invalid params"}
	default:
		return RPCError{Code: -32603, Message: "internal error"}
	}
}
