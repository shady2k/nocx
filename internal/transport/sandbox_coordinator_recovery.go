package transport

import (
	"context"
	"errors"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/session"
)

func (c *SandboxCoordinator) running(operationID string) bool {
	c.mu.Lock()
	op := c.operations[operationID]
	c.mu.Unlock()
	if op == nil {
		return false
	}
	select {
	case <-op.done:
		return false
	default:
		return true
	}
}

// Recover never resumes a launch. Correlation discard is a helper-owned barrier:
// an inventory snapshot alone cannot exclude an inflight, caller-detached fork.
func (c *SandboxCoordinator) Recover(ctx context.Context) error {
	if err := c.storeReady(); err != nil {
		return err
	}
	if c.helper == nil {
		return sandboxRefusal("helper_unavailable", nil)
	}
	c.recoveryMu.Lock()
	defer c.recoveryMu.Unlock()
	preparing, err := c.launches.Preparing(ctx)
	if err != nil {
		return err
	}
	var unresolved error
	for _, launch := range preparing {
		if c.running(launch.ID) {
			continue
		}
		if err = c.rollbackLaunch(ctx, launch); err != nil {
			unresolved = errors.Join(unresolved, err)
		}
	}
	retirements, err := c.launches.PendingRetirements(ctx, 200)
	if err != nil {
		return errors.Join(unresolved, err)
	}
	for _, retirement := range retirements {
		if c.running(retirement.OperationID) {
			continue
		}
		if err = c.closeRetirement(ctx, retirement); err != nil {
			unresolved = errors.Join(unresolved, err)
		}
	}
	return unresolved
}

func (c *SandboxCoordinator) rollbackLaunch(ctx context.Context, launch content.Launch) error {
	entry, err := c.helper.SandboxRollback(ctx, launch.TargetGeneration, launch.ID, launch.ID)
	if err != nil {
		return err
	}
	if entry == nil {
		return c.launches.Fail(ctx, launch.ID, nil)
	}
	identity := content.HelperIdentity{Generation: launch.TargetGeneration, SessionID: entry.HostSessionID.Session}
	if entry.HostSessionID.Generation != launch.TargetGeneration || identity.SessionID == "" || entry.Workspace != launch.WorkspaceID || entry.Launch == nil {
		return sandboxRefusal("rollback_identity_unknown", nil)
	}
	binding, err := c.helper.SandboxBinding(ctx, identity)
	if err != nil || !sandboxBindingMatches(launch, identity, binding) {
		return sandboxRefusal("rollback_identity_unknown", err)
	}
	// Persist the close obligation before touching the discovered process.
	if err = c.launches.Fail(ctx, launch.ID, &identity); err != nil {
		return err
	}
	return c.closeRetirement(ctx, content.SessionRetirement{Identity: identity, OperationID: launch.ID, Cause: content.RetirementFailedCandidate, ClosePending: true})
}

func (c *SandboxCoordinator) closeRetirement(ctx context.Context, retirement content.SessionRetirement) error {
	if c.helper == nil {
		return sandboxRefusal("helper_unavailable", nil)
	}
	closeErr := c.helper.SandboxClose(ctx, retirement.Identity)
	resolution := content.RetirementClosed
	if closeErr != nil {
		inventory, err := c.helper.SandboxInventory(ctx, retirement.Identity.Generation)
		if err != nil {
			return errors.Join(closeErr, err)
		}
		for _, entry := range inventory {
			if entry.HostSessionID.Generation != retirement.Identity.Generation {
				return sandboxRefusal("retirement_identity_unknown", nil)
			}
			if entry.HostSessionID.Session == retirement.Identity.SessionID {
				return closeErr
			}
		}
		resolution = content.RetirementAbsent
	}
	if err := c.launches.CompleteRetirement(ctx, content.RetirementConfirmation{Identity: retirement.Identity, Resolution: resolution}); err != nil {
		return err
	}
	// EndSession removes the retired local projection too. The exact native
	// close is already confirmed, so its attachment may now release its claims.
	if current, err := c.registry.Get(session.ID(retirement.Identity.SessionID)); err == nil {
		if binding, lookupErr := c.launches.SourceBinding(ctx, current.PaneID(), string(current.ID())); lookupErr == nil && binding == retirement.Identity {
			_ = c.registry.EndSession(current.ID())
		}
	}
	return nil
}

func (c *SandboxCoordinator) cleanupAdmission(ctx context.Context, paneID string) error {
	preparing, err := c.launches.Preparing(ctx)
	if err != nil {
		return sandboxRefusal("cleanup_unknown", err)
	}
	for _, launch := range preparing {
		if launch.PaneID == paneID {
			return sandboxRefusal("operation_in_progress", nil)
		}
	}
	retirements, err := c.launches.PendingRetirements(ctx, 200)
	if err != nil {
		return sandboxRefusal("cleanup_unknown", err)
	}
	if len(retirements) == 200 {
		return sandboxRefusal("cleanup_unknown", nil)
	}
	for _, retirement := range retirements {
		launch, lookupErr := c.launches.GetLaunch(ctx, retirement.OperationID)
		if lookupErr != nil {
			return sandboxRefusal("cleanup_unknown", lookupErr)
		}
		if launch.PaneID == paneID || retirement.Cause == content.RetirementFailedCandidate {
			return sandboxRefusal("cleanup_unknown", nil)
		}
	}
	return nil
}

func (c *SandboxCoordinator) failReplacement(op *sandboxConfirmedOperation, launch content.Launch, candidate *SandboxCandidate, cause error) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(c.root), 30*time.Second)
	defer cancel()
	var cleanupErr error
	if candidate == nil {
		cleanupErr = c.rollbackLaunch(cleanupCtx, launch)
	} else {
		// A candidate attachment is private; the failed launch and retirement
		// record must survive a crash before Abort closes its native process.
		cleanupErr = c.launches.Fail(cleanupCtx, launch.ID, &candidate.Identity)
		if cleanupErr == nil {
			cleanupErr = candidate.Abort(cleanupCtx)
			if cleanupErr == nil {
				cleanupErr = c.launches.CompleteRetirement(cleanupCtx, content.RetirementConfirmation{Identity: candidate.Identity, Resolution: content.RetirementClosed})
			}
		}
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	if cleanupErr != nil {
		op.err = sandboxRefusal("cleanup_unknown", errors.Join(cause, cleanupErr))
		op.result.Reason = "cleanup_unknown"
	} else {
		op.err = cause
	}
}

func (c *SandboxCoordinator) Operation(ctx context.Context, req SandboxOperationRequest) (SandboxOperation, error) {
	if err := c.storeReady(); err != nil {
		return SandboxOperation{}, err
	}
	if req.OperationID == "" {
		return SandboxOperation{}, sandboxRefusal("operation_required", nil)
	}
	launch, err := c.launches.GetLaunch(ctx, req.OperationID)
	if err != nil {
		return SandboxOperation{}, sandboxRefusal("operation_unknown", err)
	}
	out := SandboxOperation{Launch: launch}
	if launch.State != content.LaunchActive || launch.Helper == nil {
		return out, nil
	}
	if c.helper == nil {
		out.Reason = "helper_unknown"
		return out, nil
	}
	head, err := c.launches.Head(ctx, launch.PaneID)
	if err != nil || head.ID != launch.ID {
		out.Reason = "head_unknown"
		return out, nil
	}
	binding, err := c.helper.SandboxBinding(ctx, *launch.Helper)
	if err != nil || !sandboxBindingMatches(launch, *launch.Helper, binding) {
		out.Reason = "helper_unknown"
		return out, nil
	}
	if binding.Enforcement == "ended" {
		if err = c.launches.End(ctx, *launch.Helper); err != nil {
			return SandboxOperation{}, sandboxRefusal("head_unknown", err)
		}
		out.Launch.State = content.LaunchEnded
		return out, nil
	}
	current, err := c.registry.Get(session.ID(launch.Helper.SessionID))
	if err != nil || current.PaneID() != launch.PaneID || !c.registry.InputAllowed(current.ID()) {
		out.Reason = "publication_pending"
		return out, nil
	}
	metadata, ok := c.registry.LaunchBinding(current.ID())
	if !ok || metadata.Mode != string(launch.Mode) || metadata.LaunchID != launch.ID || metadata.Digest != launch.PolicyDigest || metadata.Version != launch.PolicyVersion || launch.GrantID != nil && metadata.GrantID != *launch.GrantID || launch.GrantID == nil && metadata.GrantID != 0 {
		out.Reason = "publication_pending"
		return out, nil
	}
	c.mu.Lock()
	live := c.operations[req.OperationID]
	c.mu.Unlock()
	if live != nil {
		live.mu.Lock()
		if live.result.Opened != nil {
			opened := *live.result.Opened
			out.Opened = &opened
		}
		out.Reason = live.result.Reason
		live.mu.Unlock()
	}
	if out.Opened == nil {
		size := current.EffectiveSize()
		out.Opened = &OpenedSession{Session: current, Config: session.Config{Kind: session.KindLocal, Cwd: current.Cwd(), PaneID: launch.PaneID, Cols: size.Cols, Rows: size.Rows, XPixel: size.XPixel, YPixel: size.YPixel, LaunchBinding: metadata}, WorkspaceID: launch.WorkspaceID}
	}
	return out, nil
}

func (c *SandboxCoordinator) discardPreview(prepared SandboxPrepared) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.root), 10*time.Second)
	defer cancel()
	_ = c.helper.SandboxDiscard(ctx, prepared)
}

func (c *SandboxCoordinator) sweepPreviews() {
	defer close(c.sweepDone)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
		case <-c.root.Done():
		}
		closing := c.root.Err() != nil
		var expired []SandboxPrepared
		c.mu.Lock()
		now := c.now()
		for id, preview := range c.previews {
			if preview.prepared.Preparation.Ticket != "" && (closing || !now.Before(preview.expires)) {
				expired = append(expired, preview.prepared)
				delete(c.previews, id)
			}
		}
		for id, op := range c.operations {
			select {
			case <-op.done:
				op.mu.Lock()
				finished := op.finished
				op.mu.Unlock()
				if !now.Before(finished.Add(10 * time.Minute)) {
					delete(c.operations, id)
				}
			default:
			}
		}
		c.mu.Unlock()
		for _, prepared := range expired {
			c.discardPreview(prepared)
		}
		if closing {
			return
		}
	}
}
