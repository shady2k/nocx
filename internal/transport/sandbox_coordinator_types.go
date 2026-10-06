package transport

import (
	"context"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/sandbox"
	"github.com/shady2k/nocx/internal/session"
)

// SandboxSource is the same incarnation claim used by ordinary attach.
type SandboxSource struct {
	SessionID    string `json:"sessionId"`
	InstanceID   string `json:"instanceId"`
	SessionEpoch uint64 `json:"sessionEpoch"`
}

func sandboxSourceOf(s session.Session) *SandboxSource {
	identity := s.Identity()
	return &SandboxSource{SessionID: string(s.ID()), InstanceID: string(identity.InstanceID), SessionEpoch: identity.Epoch}
}

func (s SandboxSource) ref() session.Ref {
	return session.Ref{ID: session.ID(s.SessionID), Identity: session.Identity{InstanceID: session.InstanceID(s.InstanceID), Epoch: s.SessionEpoch}}
}

type SandboxStatusRequest struct {
	PaneID string `json:"paneId"`
}

type SandboxHeadSummary struct {
	LaunchID      string              `json:"launchId"`
	Mode          content.LaunchMode  `json:"mode"`
	State         content.LaunchState `json:"state"`
	GrantID       *int64              `json:"grantId"`
	PolicyDigest  string              `json:"policyDigest"`
	PolicyVersion int                 `json:"policyVersion"`
	Enforcement   string              `json:"enforcement"`
	Observer      string              `json:"observer"`
}

type SandboxStatusResult struct {
	PaneID               string              `json:"paneId"`
	WorkspaceID          string              `json:"workspaceId"`
	Enabled              bool                `json:"enabled"`
	StandardRevision     uint64              `json:"standardRevision"`
	WorkspaceRevision    uint64              `json:"workspaceRevision"`
	ProfileSource        string              `json:"profileSource"`
	Availability         string              `json:"availability"`
	Reason               string              `json:"reason"`
	Source               *SandboxSource      `json:"source"`
	Head                 *SandboxHeadSummary `json:"head"`
	PreparingOperationID string              `json:"preparingOperationId"`
}

type SandboxProfileRequest struct {
	WorkspaceID string `json:"workspaceId,omitempty"`
}

type SandboxProfileResult struct {
	Standard      sandbox.StandardDocument  `json:"standard"`
	Workspace     *sandbox.WorkspaceProfile `json:"workspace"`
	Effective     sandbox.ProfileRoots      `json:"effective"`
	ProfileSource string                    `json:"profileSource"`
}

type SandboxProfileUpdateRequest struct {
	WorkspaceID      string               `json:"workspaceId,omitempty"`
	ExpectedRevision uint64               `json:"expectedRevision"`
	Enabled          *bool                `json:"enabled,omitempty"`
	Roots            sandbox.ProfileRoots `json:"roots"`
}

type SandboxProfileResetRequest struct {
	WorkspaceID      string `json:"workspaceId"`
	ExpectedRevision uint64 `json:"expectedRevision"`
}

type SandboxPreviewRequest struct {
	PaneID         string               `json:"paneId"`
	Source         *SandboxSource       `json:"source"`
	ExpectedHeadID string               `json:"expectedHeadId"`
	Mode           content.LaunchMode   `json:"mode"`
	Delta          sandbox.ProfileRoots `json:"delta"`
}

type SandboxPreviewResult struct {
	OperationID    string             `json:"operationId"`
	ConfirmationID string             `json:"confirmationId"`
	ExpiresAt      string             `json:"expiresAt"`
	WorkspaceID    string             `json:"workspaceId"`
	Mode           content.LaunchMode `json:"mode"`
	Policy         *sandbox.Policy    `json:"policy"`
	PolicyDigest   string             `json:"policyDigest"`
	PolicyVersion  int                `json:"policyVersion"`
}

type SandboxReplaceRequest struct {
	OperationID    string `json:"operationId"`
	ConfirmationID string `json:"confirmationId"`
}

type SandboxOperationRequest struct {
	OperationID string `json:"operationId"`
}

type SandboxGrantRequest struct {
	LaunchID string `json:"launchId"`
}

type SandboxGrantResult struct {
	LaunchID      string             `json:"launchId"`
	GrantID       *int64             `json:"grantId"`
	Mode          content.LaunchMode `json:"mode"`
	Policy        *sandbox.Policy    `json:"policy"`
	PolicyDigest  string             `json:"policyDigest"`
	PolicyVersion int                `json:"policyVersion"`
}

// SandboxOperation carries a committed session to transport publication without
// invoking Open again. The wire projection deliberately omits the private DTO.
type SandboxOperation struct {
	Launch content.Launch
	Opened *OpenedSession
	Reason string
}

type SandboxOperationResult struct {
	OperationID string              `json:"operationId"`
	PaneID      string              `json:"paneId"`
	State       content.LaunchState `json:"state"`
	Mode        content.LaunchMode  `json:"mode"`
	Reason      string              `json:"reason"`
	Open        *openResult         `json:"open"`
}

// SandboxControl is trusted-UI control. It is never an agent tool capability.
type SandboxControl interface {
	Status(context.Context, SandboxStatusRequest) (SandboxStatusResult, error)
	Profile(context.Context, SandboxProfileRequest) (SandboxProfileResult, error)
	UpdateProfile(context.Context, SandboxProfileUpdateRequest) (SandboxProfileResult, error)
	ResetProfile(context.Context, SandboxProfileResetRequest) (SandboxProfileResult, error)
	Preview(context.Context, SandboxPreviewRequest) (SandboxPreviewResult, error)
	Replace(context.Context, SandboxReplaceRequest) (SandboxOperation, error)
	Cancel(context.Context, SandboxReplaceRequest) error
	Operation(context.Context, SandboxOperationRequest) (SandboxOperation, error)
	Grant(context.Context, SandboxGrantRequest) (SandboxGrantResult, error)
	PermitOrdinaryOpen(context.Context, string) error
	BindPublisher(func(context.Context, OpenedSession) error)
}

// SandboxError exposes a bounded state reason through the existing RPC envelope.
// Native errors remain wrapped for error identity, not interpolated into wire prose.
type SandboxError struct {
	Reason string
	cause  error
}

func (e *SandboxError) Error() string { return "sandbox: " + e.Reason }
func (e *SandboxError) Unwrap() error { return e.cause }

func sandboxRefusal(reason string, cause error) error {
	return &SandboxError{Reason: reason, cause: cause}
}
