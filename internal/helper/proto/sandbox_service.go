package proto

import (
	"github.com/shady2k/nocx/internal/sandbox"
)

// SandboxMode describes one launch, not mutable workspace defaults.
type SandboxMode string

const (
	SandboxOff     SandboxMode = "off"
	SandboxEnforce SandboxMode = "enforce"
)

// SandboxPrepareParams reserves one single-use launch. Shell/argv and native
// backend facts belong to the helper, never the requesting coordinator.
type SandboxPrepareParams struct {
	OperationID string                `json:"operationId"`
	LaunchID    string                `json:"launchId"`
	Mode        SandboxMode           `json:"mode"`
	Workspace   WorkspaceID           `json:"workspace"`
	Cwd         string                `json:"cwd"`
	Enforce     *SandboxEnforceIntent `json:"enforce,omitempty"`
}

type SandboxEnforceIntent struct {
	StandardRevision  uint64               `json:"standardRevision"`
	WorkspaceRevision uint64               `json:"workspaceRevision"`
	Profile           sandbox.ProfileRoots `json:"profile"`
	ProfileProvenance sandbox.Provenance   `json:"profileProvenance"`
	Delta             sandbox.ProfileRoots `json:"delta"`
}

type SandboxPrepareResult struct {
	Ticket      string                 `json:"ticket"`
	OperationID string                 `json:"operationId"`
	LaunchID    string                 `json:"launchId"`
	Mode        SandboxMode            `json:"mode"`
	ExpiresAt   string                 `json:"expiresAt"`
	Enforce     *SandboxPreparedPolicy `json:"enforce,omitempty"`
}

type SandboxPreparedPolicy struct {
	Policy sandbox.Policy `json:"policy"`
	Digest string         `json:"digest"`
}

// The native grant has already been durably minted when launch consumes its
// preparation. Off explicitly carries no filesystem grant.
type SandboxGrantBinding struct {
	ID      int64  `json:"id"`
	Digest  string `json:"digest"`
	Version int    `json:"version"`
}

type SandboxLaunchParams struct {
	Ticket      string               `json:"ticket"`
	OperationID string               `json:"operationId"`
	LaunchID    string               `json:"launchId"`
	Mode        SandboxMode          `json:"mode"`
	Grant       *SandboxGrantBinding `json:"grant,omitempty"`
	Shape       SandboxLaunchShape   `json:"shape"`
}

// Geometry and lifecycle retain the ordinary terminal replay contract, without
// letting launch replace the CWD, workspace or policy prepared earlier.
type SandboxLaunchShape struct {
	Cols            uint16            `json:"cols"`
	Rows            uint16            `json:"rows"`
	XPixel          uint16            `json:"xPixel"`
	YPixel          uint16            `json:"yPixel"`
	WindowBytes     int64             `json:"windowBytes"`
	RowBufferBytes  int64             `json:"rowBufferBytes"`
	ScrollbackLines *uint64           `json:"scrollbackLines,omitempty"`
	Lifecycle       *LifecycleLaunch  `json:"lifecycle,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
}

type SandboxGetParams struct {
	Session HostSessionID `json:"session"`
}

type SandboxGetResult struct {
	Session     HostSessionID        `json:"session"`
	OperationID string               `json:"operationId"`
	LaunchID    string               `json:"launchId"`
	Mode        SandboxMode          `json:"mode"`
	Grant       *SandboxGrantBinding `json:"grant,omitempty"`
	Enforcement string               `json:"enforcement"`
	Observer    string               `json:"observer"`
}

type SandboxDiscardParams struct {
	Ticket      string `json:"ticket,omitempty"`
	OperationID string `json:"operationId,omitempty"`
	LaunchID    string `json:"launchId,omitempty"`
}

// Correlation discard is the coordinator's rollback barrier after a crash.
// A terminal entry is returned without creating or closing another process.
type SandboxDiscardResult struct {
	Entry *SessionEntry `json:"entry,omitempty"`
}
