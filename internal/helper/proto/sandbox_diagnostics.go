package proto

import "github.com/shady2k/nocx/internal/sandbox"

// All diagnostics operations name the helper-owned session. Paths arrive only
// from its collector; callers cannot supply a directory or workspace to allow.
type SandboxAccessListParams struct {
	Session HostSessionID `json:"session"`
	Cursor  uint16        `json:"cursor"`
	Limit   uint16        `json:"limit"`
}

type SandboxAccessListResult struct {
	Session  HostSessionID          `json:"session"`
	LaunchID string                 `json:"launchId"`
	Inbox    sandbox.DiagnosticPage `json:"inbox"`
}

type SandboxAccessReserveParams struct {
	Session  HostSessionID              `json:"session"`
	EventID  string                     `json:"eventId"`
	Revision uint64                     `json:"revision"`
	Decision sandbox.DiagnosticDecision `json:"decision"`
}

type SandboxAccessReserveResult struct {
	Session     HostSessionID            `json:"session"`
	LaunchID    string                   `json:"launchId"`
	Reservation string                   `json:"reservation"`
	Record      sandbox.DiagnosticRecord `json:"record"`
}

type SandboxAccessFinishParams struct {
	Session         HostSessionID `json:"session"`
	EventID         string        `json:"eventId"`
	Reservation     string        `json:"reservation"`
	Committed       bool          `json:"committed"`
	Uncertain       bool          `json:"uncertain"`
	ProfileRevision uint64        `json:"profileRevision"`
}

type SandboxAccessFinishResult struct {
	Session  HostSessionID            `json:"session"`
	LaunchID string                   `json:"launchId"`
	Record   sandbox.DiagnosticRecord `json:"record"`
}

// SandboxAccessChanged is coalesced metadata, never an unsolicited path stream.
type SandboxAccessChanged struct {
	Session  HostSessionID          `json:"session"`
	LaunchID string                 `json:"launchId"`
	Revision uint64                 `json:"revision"`
	Dropped  uint64                 `json:"dropped"`
	Observer sandbox.ObserverStatus `json:"observer"`
	Total    uint16                 `json:"total"`
}
