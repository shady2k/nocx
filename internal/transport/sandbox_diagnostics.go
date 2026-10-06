package transport

import (
	"context"
	"errors"

	"github.com/shady2k/nocx/internal/content"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sandbox"
)

func (c *SandboxCoordinator) diagnosticTarget(ctx context.Context, paneID, launchID string) (content.Launch, string, error) {
	if err := c.storeReady(); err != nil {
		return content.Launch{}, "", err
	}
	pane, workspaceID, err := c.pane(ctx, paneID)
	if err != nil {
		return content.Launch{}, "", err
	}
	if pane.Kind != content.PaneLocal {
		return content.Launch{}, "", sandboxRefusal("local_only", nil)
	}
	head, err := c.launches.Head(ctx, paneID)
	if err != nil {
		return content.Launch{}, "", sandboxRefusal("diagnostic_launch_unavailable", err)
	}
	if launchID == "" || head.ID != launchID || head.Mode != content.LaunchEnforce || head.Helper == nil || head.WorkspaceID != workspaceID || c.helper == nil {
		return content.Launch{}, "", sandboxRefusal("diagnostic_target_changed", nil)
	}
	if head.Helper.Host != "" || head.Helper.Account != "" {
		return content.Launch{}, "", sandboxRefusal("local_only", nil)
	}
	return head, workspaceID, nil
}

func (c *SandboxCoordinator) AccessList(ctx context.Context, req SandboxAccessListRequest) (SandboxAccessListResult, error) {
	head, workspaceID, err := c.diagnosticTarget(ctx, req.PaneID, req.LaunchID)
	if err != nil {
		return SandboxAccessListResult{}, err
	}
	profile, err := c.Profile(ctx, SandboxProfileRequest{WorkspaceID: workspaceID})
	if err != nil {
		return SandboxAccessListResult{}, err
	}
	result := SandboxAccessListResult{PaneID: req.PaneID, LaunchID: head.ID, WorkspaceID: workspaceID, StandardRevision: profile.Standard.Revision, WorkspaceRevision: profile.Workspace.Revision}
	page, err := c.helper.SandboxAccessList(ctx, *head.Helper, proto.SandboxAccessListParams{Cursor: req.Cursor, Limit: req.Limit})
	if err != nil {
		// The helper is the only history owner. Coordinator recovery cannot
		// reconstruct observations or pretend this empty page is complete.
		result.Reason = "diagnostic_history_unavailable"
		result.Inbox = sandbox.DiagnosticPage{Observer: sandbox.ObserverUnavailable, Discontinuity: true, Records: []sandbox.DiagnosticRecord{}}
		return result, nil
	}
	if page.LaunchID != head.ID || string(page.Session.Generation) != head.Helper.Generation || page.Session.Session != head.Helper.SessionID {
		return SandboxAccessListResult{}, sandboxRefusal("diagnostic_target_changed", nil)
	}
	result.Inbox = page.Inbox
	return result, nil
}

func (c *SandboxCoordinator) AccessResolve(_ context.Context, req SandboxAccessResolveRequest) (SandboxAccessResolveResult, error) {
	// Once admitted, renderer cancellation is not rollback of a profile CAS.
	// The helper's reservation survives a coordinator connection disconnect.
	ctx := c.root
	if err := ctx.Err(); err != nil {
		return SandboxAccessResolveResult{}, err
	}
	head, workspaceID, err := c.diagnosticTarget(ctx, req.PaneID, req.LaunchID)
	if err != nil {
		return SandboxAccessResolveResult{}, err
	}
	reserved, err := c.helper.SandboxAccessReserve(ctx, *head.Helper, proto.SandboxAccessReserveParams{EventID: req.EventID, Revision: req.EventRevision, Decision: req.Decision})
	if err != nil {
		return SandboxAccessResolveResult{}, diagnosticHelperError(err)
	}
	if reserved.LaunchID != head.ID || string(reserved.Session.Generation) != head.Helper.Generation || reserved.Session.Session != head.Helper.SessionID {
		return SandboxAccessResolveResult{}, sandboxRefusal("diagnostic_target_changed", nil)
	}
	if reserved.Reservation == "" {
		profile, profileErr := c.Profile(ctx, SandboxProfileRequest{WorkspaceID: workspaceID})
		return SandboxAccessResolveResult{Record: reserved.Record, Profile: profile}, profileErr
	}
	abort := func() error {
		_, finishErr := c.helper.SandboxAccessFinish(ctx, *head.Helper, proto.SandboxAccessFinishParams{EventID: req.EventID, Reservation: reserved.Reservation})
		return finishErr
	}
	current, currentWorkspace, targetErr := c.diagnosticTarget(ctx, req.PaneID, req.LaunchID)
	if targetErr != nil || currentWorkspace != workspaceID || current.Helper == nil || *current.Helper != *head.Helper {
		if abortErr := abort(); abortErr != nil {
			return SandboxAccessResolveResult{}, sandboxRefusal("future_policy_unknown", abortErr)
		}
		return SandboxAccessResolveResult{}, sandboxRefusal("diagnostic_target_changed", targetErr)
	}
	if reserved.Record.Proposal == nil {
		if abortErr := abort(); abortErr != nil {
			return SandboxAccessResolveResult{}, sandboxRefusal("future_policy_unknown", abortErr)
		}
		return SandboxAccessResolveResult{}, sandboxRefusal("diagnostic_location_unknown", nil)
	}
	access := sandbox.ReadOnly
	if req.Decision == sandbox.DecisionAllowRW {
		access = sandbox.ReadWrite
	}
	standard, workspace, updateErr := c.profiles.PromoteDirectory(ctx, workspaceID, req.ExpectedStandardRevision, req.ExpectedWorkspaceRevision, access, reserved.Record.Proposal.Directory)
	if updateErr != nil {
		var profileErr *sandbox.ProfileError
		definite := errors.Is(updateErr, sandbox.ErrProfileConflict) || errors.Is(updateErr, sandbox.ErrProfileRestoreBusy) || errors.Is(updateErr, sandbox.ErrWorkspaceProfileUnsupported) || errors.Is(updateErr, sandbox.ErrProfileRevisionExhausted)
		if errors.As(updateErr, &profileErr) && profileErr.Code != "document_write_failed" {
			definite = true
		}
		if !definite {
			_, _ = c.helper.SandboxAccessFinish(ctx, *head.Helper, proto.SandboxAccessFinishParams{EventID: req.EventID, Reservation: reserved.Reservation, Uncertain: true})
			return SandboxAccessResolveResult{}, sandboxRefusal("future_policy_unknown", updateErr)
		}
		if abortErr := abort(); abortErr != nil {
			return SandboxAccessResolveResult{}, sandboxRefusal("future_policy_unknown", abortErr)
		}
		return SandboxAccessResolveResult{}, sandboxProfileError(updateErr)
	}
	profile := SandboxProfileResult{Standard: standard, Effective: sandbox.EffectiveWorkspaceRoots(workspace, standard), ProfileSource: "standard", Workspace: &workspace}
	profileRevision := workspace.Revision
	if workspaceID == sandbox.DefaultWorkspaceID {
		profileRevision = standard.Revision
	}
	if workspace.Override != nil {
		profile.ProfileSource = "workspace"
	}
	finished, err := c.helper.SandboxAccessFinish(ctx, *head.Helper, proto.SandboxAccessFinishParams{EventID: req.EventID, Reservation: reserved.Reservation, Committed: true, ProfileRevision: profileRevision})
	if err != nil {
		_, _ = c.helper.SandboxAccessFinish(ctx, *head.Helper, proto.SandboxAccessFinishParams{EventID: req.EventID, Reservation: reserved.Reservation, Uncertain: true})
		return SandboxAccessResolveResult{}, sandboxRefusal("future_policy_unknown", err)
	}
	if finished.LaunchID != head.ID || string(finished.Session.Generation) != head.Helper.Generation || finished.Session.Session != head.Helper.SessionID {
		return SandboxAccessResolveResult{}, sandboxRefusal("future_policy_unknown", nil)
	}
	return SandboxAccessResolveResult{Record: finished.Record, Profile: profile}, nil
}

func diagnosticHelperError(err error) error {
	var refusal *helperclient.RefusalError
	if errors.As(err, &refusal) {
		switch refusal.Code {
		case "diagnostic_conflict", "diagnostic_retarget", "diagnostic_location_unknown", "diagnostic_pending":
			return sandboxRefusal(refusal.Code, err)
		}
	}
	return sandboxRefusal("diagnostic_unavailable", err)
}
