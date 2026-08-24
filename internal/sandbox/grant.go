package sandbox

import (
	"encoding/json"
	"errors"
)

// ProfileSource values are frozen wire values naming which profile sourced a
// pane grant's authority (design 2026-08-23 §3.3). `legacy` is never written:
// it is the provenance reported for a grant whose payload predates the
// envelope and therefore records no source.
const (
	ProfileSourceStandard  = "standard"
	ProfileSourceWorkspace = "workspace"
	ProfileSourceLegacy    = "legacy"
)

// GrantProvenance records which profile sourced a pane grant. It is stored
// inside sandbox_grants.payload and surfaced by sandbox.grant.get, never
// invented by the renderer.
type GrantProvenance struct {
	// WorkspaceID is the layout workspace the pane belonged to when the grant
	// was minted (backend-owned provenance, never the renderer's claim).
	WorkspaceID string `json:"workspaceId"`
	// ProfileSource is standard, workspace, or legacy (a grant that predates
	// the envelope).
	ProfileSource string `json:"profileSource"`
	// ProfileRevision is the effective profile revision the grant realized:
	// settings revision for standard, per-workspace revision for workspace,
	// nil only for a legacy grant.
	ProfileRevision *int64 `json:"profileRevision"`
}

// GrantPayload is the stored sandbox_grants.payload envelope: the realized
// enforcement metadata plus the provenance that sourced it.
type GrantPayload struct {
	Realized   *SessionInfo     `json:"realized"`
	Provenance *GrantProvenance `json:"provenance"`
}

// DecodeGrantPayload decodes a stored grant payload. When the payload carries
// a top-level `realized` member it is decoded as the envelope; otherwise the
// whole object is decoded as a legacy SessionInfo and the provenance is
// reported as {profileSource: "legacy", profileRevision: null} — the legacy
// result exposes realized roots but offers no profile-staleness decision
// (design 2026-08-23 §3.3).
func DecodeGrantPayload(raw []byte) (*SessionInfo, GrantProvenance, error) {
	if len(raw) == 0 {
		return nil, GrantProvenance{}, errors.New("sandbox: grant payload is empty")
	}
	var probe struct {
		Realized json.RawMessage `json:"realized"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, GrantProvenance{}, err
	}
	if len(probe.Realized) != 0 {
		var envelope GrantPayload
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, GrantProvenance{}, err
		}
		if envelope.Realized == nil {
			return nil, GrantProvenance{}, errors.New("sandbox: grant payload realized member is null")
		}
		if envelope.Provenance == nil {
			return nil, GrantProvenance{}, errors.New("sandbox: grant payload provenance member is missing")
		}
		provenance := *envelope.Provenance
		switch provenance.ProfileSource {
		case ProfileSourceStandard:
			if provenance.WorkspaceID == "" || provenance.ProfileRevision == nil || *provenance.ProfileRevision < 0 {
				return nil, GrantProvenance{}, errors.New("sandbox: standard grant provenance is invalid")
			}
		case ProfileSourceWorkspace:
			if provenance.WorkspaceID == "" || provenance.ProfileRevision == nil || *provenance.ProfileRevision < 1 {
				return nil, GrantProvenance{}, errors.New("sandbox: workspace grant provenance is invalid")
			}
		default:
			return nil, GrantProvenance{}, errors.New("sandbox: grant profile source is invalid")
		}
		return envelope.Realized, provenance, nil
	}
	// Legacy: the whole object is a SessionInfo.
	var info SessionInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, GrantProvenance{}, err
	}
	return &info, GrantProvenance{ProfileSource: ProfileSourceLegacy}, nil
}
