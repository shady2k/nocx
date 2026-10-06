package transport

import (
	"context"

	"github.com/shady2k/nocx/internal/content"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/session"
)

// SandboxPrepared pins the generation that owns a single-use preparation.
// Its ticket never crosses an agent channel or enters an unsolicited event.
type SandboxPrepared struct {
	Preparation helperclient.SandboxPreparation
	Generation  string
}

// SandboxCandidate stays outside registry selection and public projections
// until Publish, which is called only after the durable selection commit.
type SandboxCandidate struct {
	Identity content.HelperIdentity
	Entry    helperclient.SessionEntry
	Publish  func(context.Context) (HostedSessionOpen, error)
	Abort    func(context.Context) error
}

// SandboxHelper is the local helper's prepared-launch route, not another PTY
// launcher. Inventory and close always name the exact generation.
type SandboxHelper interface {
	SandboxPrepare(context.Context, proto.SandboxPrepareParams) (SandboxPrepared, error)
	SandboxDiscard(context.Context, SandboxPrepared) error
	SandboxOpenCandidate(context.Context, session.Config, SandboxPrepared, proto.SandboxLaunchParams) (SandboxCandidate, error)
	SandboxBinding(context.Context, content.HelperIdentity) (helperclient.SandboxSession, error)
	SandboxInventory(context.Context, string) ([]helperclient.SessionEntry, error)
	SandboxRollback(context.Context, string, string, string) (*helperclient.SessionEntry, error)
	SandboxClose(context.Context, content.HelperIdentity) error
	SandboxAccessList(context.Context, content.HelperIdentity, proto.SandboxAccessListParams) (proto.SandboxAccessListResult, error)
	SandboxAccessReserve(context.Context, content.HelperIdentity, proto.SandboxAccessReserveParams) (proto.SandboxAccessReserveResult, error)
	SandboxAccessFinish(context.Context, content.HelperIdentity, proto.SandboxAccessFinishParams) (proto.SandboxAccessFinishResult, error)
}
