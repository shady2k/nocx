package app

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/transport"
)

type sandboxRecoveryRepo struct {
	content.LaunchRepository
	launch      content.Launch
	launchErr   error
	head        content.Launch
	headErr     error
	preparing   []content.Launch
	retirements []content.SessionRetirement
	retirement  content.SessionRetirement
	retireErr   error
}

func (r *sandboxRecoveryRepo) ByHelper(context.Context, content.HelperIdentity) (content.Launch, error) {
	return r.launch, r.launchErr
}

func (r *sandboxRecoveryRepo) Retirement(context.Context, content.HelperIdentity) (content.SessionRetirement, error) {
	return r.retirement, r.retireErr
}

func (r *sandboxRecoveryRepo) Head(context.Context, string) (content.Launch, error) {
	return r.head, r.headErr
}

func (r *sandboxRecoveryRepo) Preparing(context.Context) ([]content.Launch, error) {
	return r.preparing, nil
}

func (r *sandboxRecoveryRepo) PendingRetirements(context.Context, int) ([]content.SessionRetirement, error) {
	return r.retirements, nil
}

type sandboxRecoveryHelper struct {
	transport.SandboxHelper
	binding helperclient.SandboxSession
	entries []helperclient.SessionEntry
	calls   int
}

func (h *sandboxRecoveryHelper) SandboxBinding(context.Context, content.HelperIdentity) (helperclient.SandboxSession, error) {
	h.calls++
	return h.binding, nil
}

func (h *sandboxRecoveryHelper) SandboxInventory(context.Context, string) ([]helperclient.SessionEntry, error) {
	h.calls++
	return h.entries, nil
}

func TestProtectedReadoptExcludesPreparingCandidateAndRetiredIdentity(t *testing.T) {
	identity := content.HelperIdentity{Generation: "gen", SessionID: "candidate"}
	candidate := content.Launch{ID: "launch", State: content.LaunchPreparing, Mode: content.LaunchEnforce, Helper: &identity}
	rp := &readoptPass{launches: &sandboxRecoveryRepo{launch: candidate, headErr: content.ErrNotFound, retireErr: content.ErrNotFound}}
	if _, err := rp.protectedLaunch(context.Background(), content.PendingSession{SessionID: identity.SessionID, Generation: identity.Generation, PaneID: "pane"}); !errors.Is(err, errProtectedExcluded) {
		t.Fatalf("preparing candidate classification error=%v", err)
	}

	repo := &sandboxRecoveryRepo{
		launchErr:  content.ErrNotFound,
		retirement: content.SessionRetirement{Identity: identity, ClosePending: false},
	}
	rp.launches = repo
	if _, err := rp.protectedLaunch(context.Background(), content.PendingSession{SessionID: identity.SessionID, Generation: identity.Generation, PaneID: "pane"}); !errors.Is(err, errProtectedExcluded) {
		t.Fatalf("completed-retirement classification error=%v", err)
	}
}

func TestPreparingCandidateCorrelationDoesNotExcludeItsSource(t *testing.T) {
	source := content.HelperIdentity{Generation: "gen", SessionID: "source"}
	preparing := content.Launch{ID: "next-launch", TargetGeneration: source.Generation, State: content.LaunchPreparing, Mode: content.LaunchEnforce}
	pending := content.PendingSession{SessionID: source.SessionID, Generation: source.Generation, PaneID: "pane"}
	repo := &sandboxRecoveryRepo{launchErr: content.ErrNotFound, headErr: content.ErrNotFound, retireErr: content.ErrNotFound, preparing: []content.Launch{preparing}}
	helper := &sandboxRecoveryHelper{binding: helperclient.SandboxSession{
		HostSessionID: helperclient.HostSessionID{Generation: source.Generation, Session: source.SessionID},
		LaunchID:      "current-launch", OperationID: "current-operation", Mode: "enforce",
	}}
	rp := &readoptPass{launches: repo, sandbox: helper}
	if _, err := rp.protectedLaunch(context.Background(), pending); err != nil {
		t.Fatalf("source was excluded while its replacement is preparing: %v", err)
	}
	if helper.calls != 1 {
		t.Fatalf("source classification called helper %d times, want one exact sandbox-get", helper.calls)
	}

	repo = &sandboxRecoveryRepo{launchErr: content.ErrNotFound, headErr: content.ErrNotFound, retireErr: content.ErrNotFound, preparing: []content.Launch{preparing}}
	helper = &sandboxRecoveryHelper{binding: helperclient.SandboxSession{
		HostSessionID: helperclient.HostSessionID{Generation: source.Generation, Session: "candidate"},
		LaunchID:      preparing.ID, OperationID: preparing.ID, Mode: "enforce",
	}}
	rp = &readoptPass{launches: repo, sandbox: helper}
	pending.SessionID = "candidate"
	if _, err := rp.protectedLaunch(context.Background(), pending); !errors.Is(err, errProtectedExcluded) {
		t.Fatalf("preparing helper candidate classification error=%v", err)
	}
}

func TestProtectedReadoptRejectsMismatchedHelperAuthority(t *testing.T) {
	identity := content.HelperIdentity{Generation: "gen", SessionID: "protected"}
	grantID := int64(41)
	launch := content.Launch{
		ID: "launch", PaneID: "pane", Mode: content.LaunchEnforce, State: content.LaunchActive,
		Helper: &identity, GrantID: &grantID, PolicyDigest: "digest", PolicyVersion: 2,
	}
	cases := map[string]func(*helperclient.SandboxSession){
		"launch":      func(binding *helperclient.SandboxSession) { binding.LaunchID = "other-launch" },
		"identity":    func(binding *helperclient.SandboxSession) { binding.HostSessionID.Session = "other-session" },
		"grant":       func(binding *helperclient.SandboxSession) { binding.Grant.ID++ },
		"digest":      func(binding *helperclient.SandboxSession) { binding.Grant.Digest = "other-digest" },
		"version":     func(binding *helperclient.SandboxSession) { binding.Grant.Version++ },
		"enforcement": func(binding *helperclient.SandboxSession) { binding.Enforcement = "unknown" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			binding := helperclient.SandboxSession{
				HostSessionID: helperclient.HostSessionID{Generation: identity.Generation, Session: identity.SessionID},
				LaunchID:      launch.ID, Mode: "enforce",
				Grant: &helperclient.SandboxGrant{ID: grantID, Digest: "digest", Version: 2}, Enforcement: "enforced",
			}
			mutate(&binding)
			helper := &sandboxRecoveryHelper{binding: binding}
			rp := &readoptPass{launches: &sandboxRecoveryRepo{launch: launch, head: launch, retireErr: content.ErrNotFound}, sandbox: helper}
			_, err := rp.protectedLaunch(context.Background(), content.PendingSession{SessionID: identity.SessionID, Generation: identity.Generation, PaneID: "pane"})
			if err == nil || helper.calls != 1 {
				t.Fatalf("mismatched helper binding error=%v helper calls=%d", err, helper.calls)
			}
		})
	}
}

func TestProtectedReadoptAcceptsOnlyMatchingEndedInventoryForDrain(t *testing.T) {
	identity := content.HelperIdentity{Generation: "gen", SessionID: "exited"}
	grantID := int64(52)
	launch := content.Launch{
		ID: "launch-ended", PaneID: "pane", Mode: content.LaunchEnforce, State: content.LaunchEnded,
		Helper: &identity, GrantID: &grantID, PolicyDigest: "digest", PolicyVersion: 3,
	}
	exit := &helperclient.ExitStatus{Code: 0}
	repo := &sandboxRecoveryRepo{launch: launch, head: launch, retireErr: content.ErrNotFound}
	helper := &sandboxRecoveryHelper{
		binding: helperclient.SandboxSession{
			HostSessionID: helperclient.HostSessionID{Generation: identity.Generation, Session: identity.SessionID},
			LaunchID:      launch.ID, Mode: "enforce",
			Grant: &helperclient.SandboxGrant{ID: grantID, Digest: "digest", Version: 3}, Enforcement: "ended",
		},
		entries: []helperclient.SessionEntry{{HostSessionID: helperclient.HostSessionID{Generation: identity.Generation, Session: identity.SessionID}, Exit: exit}},
	}
	rp := &readoptPass{launches: repo, sandbox: helper}
	_, err := rp.protectedLaunch(context.Background(), content.PendingSession{SessionID: identity.SessionID, Generation: identity.Generation, PaneID: "pane"})
	if err == nil || err.Error() != "protected session adoption is unavailable" || helper.calls != 2 {
		t.Fatalf("ended protected verification error=%v helper calls=%d", err, helper.calls)
	}
}
