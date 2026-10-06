package content

import (
	"context"
	"errors"
	"time"

	"github.com/shady2k/nocx/internal/sandbox"
)

var (
	ErrLaunchConflict          = errors.New("content: sandbox launch state changed")
	ErrLaunchIntentConflict    = errors.New("content: launch id already belongs to a different intent")
	ErrLaunchUnsupportedPolicy = errors.New("content: unsupported sandbox policy version")
	ErrInvalidLaunch           = errors.New("content: invalid sandbox launch")
)

type LaunchMode string

const (
	LaunchOff     LaunchMode = "off"
	LaunchEnforce LaunchMode = "enforce"
)

type LaunchState string

const (
	LaunchPreparing LaunchState = "preparing"
	LaunchActive    LaunchState = "active"
	LaunchEnded     LaunchState = "ended"
	LaunchFailed    LaunchState = "failed"
)

// HelperIdentity qualifies helper-minted session IDs; all fields participate
// in equality so an answer from another helper generation cannot satisfy a CAS.
type HelperIdentity struct {
	Host       string
	Account    string
	Generation string
	SessionID  string
}

func (h HelperIdentity) valid() bool {
	return h.Host != "" && h.Account != "" && h.Generation != "" && h.SessionID != ""
}

type Launch struct {
	ID                string
	PaneID            string
	WorkspaceID       string
	StandardRevision  uint64
	WorkspaceRevision uint64
	Source            HelperIdentity
	SourceHeadID      string
	Mode              LaunchMode
	State             LaunchState
	Helper            *HelperIdentity
	Policy            *sandbox.Policy
	PolicyDigest      string
	PolicyVersion     int
	GrantID           *int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type LaunchPrepare struct {
	ID                string
	PaneID            string
	WorkspaceID       string
	Source            HelperIdentity
	ExpectedHeadID    string
	StandardRevision  uint64
	WorkspaceRevision uint64
	Mode              LaunchMode
	Policy            *sandbox.Policy
	PolicyDigest      string
	PolicyVersion     int
}

type LaunchCommit struct {
	LaunchID       string
	ExpectedSource HelperIdentity
	ExpectedHeadID string
	Candidate      HelperIdentity
}

type RetirementCause string

const (
	RetirementReplacement     RetirementCause = "replacement"
	RetirementFailedCandidate RetirementCause = "failed-candidate"
)

type RetirementResolution string

const (
	RetirementClosed RetirementResolution = "closed"
	RetirementAbsent RetirementResolution = "exact-absent"
)

type RetirementConfirmation struct {
	Identity   HelperIdentity
	Resolution RetirementResolution
}

type SessionRetirement struct {
	Identity     HelperIdentity
	OperationID  string
	Cause        RetirementCause
	ClosePending bool
	CreatedAt    time.Time
}

type LaunchRepository interface {
	Prepare(context.Context, LaunchPrepare) (Launch, error)
	Commit(context.Context, LaunchCommit) (Launch, error)
	End(context.Context, HelperIdentity) error
	Fail(context.Context, string, *HelperIdentity) error
	RecordRetirement(context.Context, SessionRetirement) error
	CompleteRetirement(context.Context, RetirementConfirmation) error
	GetLaunch(context.Context, string) (Launch, error)
	Head(context.Context, string) (Launch, error)
	Preparing(context.Context) ([]Launch, error)
	PendingRetirements(context.Context, int) ([]SessionRetirement, error)
}
