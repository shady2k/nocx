package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/shady2k/nocx/internal/content"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sandbox"
	"github.com/shady2k/nocx/internal/session"
)

func newSandboxConfirmation() (string, string, error) {
	var raw [48]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", err
	}
	return hex.EncodeToString(raw[:16]), hex.EncodeToString(raw[16:]), nil
}

func (c *SandboxCoordinator) Preview(ctx context.Context, req SandboxPreviewRequest) (SandboxPreviewResult, error) {
	if err := c.storeReady(); err != nil {
		return SandboxPreviewResult{}, err
	}
	if req.Mode != content.LaunchOff && req.Mode != content.LaunchEnforce {
		return SandboxPreviewResult{}, sandboxRefusal("invalid_mode", nil)
	}
	if req.Mode == content.LaunchOff && (len(req.Delta.ReadOnlyDirs) != 0 || len(req.Delta.ReadWriteDirs) != 0) {
		return SandboxPreviewResult{}, sandboxRefusal("off_has_no_policy", nil)
	}
	if c.helper == nil {
		return SandboxPreviewResult{}, sandboxRefusal("helper_unavailable", nil)
	}
	if req.Mode == content.LaunchEnforce {
		if err := c.nativeReady(); err != nil {
			return SandboxPreviewResult{}, err
		}
	}
	if err := c.Recover(ctx); err != nil {
		return SandboxPreviewResult{}, sandboxRefusal("cleanup_unknown", err)
	}
	if err := c.cleanupAdmission(ctx, req.PaneID); err != nil {
		return SandboxPreviewResult{}, err
	}
	pane, workspaceID, err := c.pane(ctx, req.PaneID)
	if err != nil {
		return SandboxPreviewResult{}, err
	}
	if pane.Kind != content.PaneLocal {
		return SandboxPreviewResult{}, sandboxRefusal("local_only", nil)
	}
	profile, err := c.Profile(ctx, SandboxProfileRequest{WorkspaceID: workspaceID})
	if err != nil {
		return SandboxPreviewResult{}, err
	}
	if req.Mode == content.LaunchEnforce && !profile.Standard.Enabled {
		return SandboxPreviewResult{}, sandboxRefusal("feature_disabled", nil)
	}
	if revisionErr := c.profiles.WithStandardRevision(profile.Standard.Revision, func() error { return nil }); revisionErr != nil {
		return SandboxPreviewResult{}, sandboxProfileError(revisionErr)
	}
	cfg, source, err := c.previewSource(ctx, req, pane, workspaceID)
	if err != nil {
		return SandboxPreviewResult{}, err
	}
	operationID, confirmationID, err := newSandboxConfirmation()
	if err != nil {
		return SandboxPreviewResult{}, sandboxRefusal("preparation_failed", err)
	}
	preview := &sandboxPreview{request: req, operationID: operationID, confirmationID: confirmationID, workspaceID: workspaceID, cwd: pane.Cwd, source: source, cfg: cfg, standard: profile.Standard, workspace: *profile.Workspace, expires: c.now().Add(sandboxPreviewTTL)}
	c.mu.Lock()
	if c.root.Err() != nil {
		c.mu.Unlock()
		return SandboxPreviewResult{}, sandboxRefusal("coordinator_stopped", c.root.Err())
	}
	if len(c.previews) >= sandboxPreviewLimit {
		c.mu.Unlock()
		return SandboxPreviewResult{}, sandboxRefusal("preparation_busy", nil)
	}
	// Reserve the bounded slot before helper IO. Only completed previews expire.
	c.previews[operationID] = preview
	c.mu.Unlock()
	params := proto.SandboxPrepareParams{OperationID: operationID, LaunchID: operationID, Mode: proto.SandboxMode(req.Mode), Workspace: proto.WorkspaceID(workspaceID), Cwd: pane.Cwd}
	if req.Mode == content.LaunchEnforce {
		provenance := sandbox.StandardRoot
		if profile.Workspace.Override != nil {
			provenance = sandbox.WorkspaceProfileRoot
		}
		params.Enforce = &proto.SandboxEnforceIntent{StandardRevision: profile.Standard.Revision, WorkspaceRevision: profile.Workspace.Revision, Profile: profile.Effective, ProfileProvenance: provenance, Delta: req.Delta}
	}
	prepared, err := c.helper.SandboxPrepare(ctx, params)
	if err == nil {
		if prepared.Generation == "" || prepared.Preparation.OperationID != operationID || prepared.Preparation.LaunchID != operationID || prepared.Preparation.Mode != string(req.Mode) || prepared.Preparation.Ticket == "" {
			err = sandboxRefusal("preparation_mismatch", nil)
		}
		if req.Mode == content.LaunchEnforce {
			policy := prepared.Preparation.Policy
			if policy == nil || policy.WorkspaceID != workspaceID || policy.StandardRevision != profile.Standard.Revision || policy.WorkspaceRevision != profile.Workspace.Revision || policy.Version != sandbox.PolicyVersion {
				err = sandboxRefusal("preparation_mismatch", nil)
			} else {
				_, digest, encodeErr := sandbox.EncodePolicy(*policy)
				if encodeErr != nil || digest != prepared.Preparation.Digest {
					err = sandboxRefusal("preparation_mismatch", encodeErr)
				}
			}
		} else if prepared.Preparation.Policy != nil || prepared.Preparation.Digest != "" {
			err = sandboxRefusal("preparation_mismatch", nil)
		}
	}
	if err != nil {
		c.mu.Lock()
		delete(c.previews, operationID)
		c.mu.Unlock()
		if prepared.Preparation.Ticket != "" {
			c.discardPreview(prepared)
		}
		return SandboxPreviewResult{}, sandboxRefusal("preparation_failed", err)
	}
	expires, expiryErr := time.Parse(time.RFC3339Nano, prepared.Preparation.ExpiresAt)
	if expiryErr != nil || !c.now().Before(expires) {
		c.mu.Lock()
		delete(c.previews, operationID)
		c.mu.Unlock()
		c.discardPreview(prepared)
		return SandboxPreviewResult{}, sandboxRefusal("preparation_expired", expiryErr)
	}
	c.mu.Lock()
	if c.root.Err() != nil {
		delete(c.previews, operationID)
		c.mu.Unlock()
		c.discardPreview(prepared)
		return SandboxPreviewResult{}, sandboxRefusal("coordinator_stopped", c.root.Err())
	}
	preview.prepared = prepared
	if expires.Before(preview.expires) {
		preview.expires = expires
	}
	c.mu.Unlock()
	version := 0
	if prepared.Preparation.Policy != nil {
		version = prepared.Preparation.Policy.Version
	}
	return SandboxPreviewResult{OperationID: operationID, ConfirmationID: confirmationID, ExpiresAt: preview.expires.UTC().Format(time.RFC3339Nano), WorkspaceID: workspaceID, Mode: req.Mode, Policy: prepared.Preparation.Policy, PolicyDigest: prepared.Preparation.Digest, PolicyVersion: version}, nil
}

func (c *SandboxCoordinator) previewSource(ctx context.Context, req SandboxPreviewRequest, pane content.Pane, workspaceID string) (session.Config, content.HelperIdentity, error) {
	cfg := session.Config{Kind: session.KindLocal, Cwd: pane.Cwd, PaneID: pane.ID, Enhanced: true}
	head, err := c.launches.Head(ctx, pane.ID)
	if err != nil && !errors.Is(err, content.ErrNotFound) {
		return cfg, content.HelperIdentity{}, sandboxRefusal("head_unknown", err)
	}
	if errors.Is(err, content.ErrNotFound) {
		if req.ExpectedHeadID != "" || req.Source == nil {
			return cfg, content.HelperIdentity{}, sandboxRefusal("source_changed", nil)
		}
	} else if head.ID != req.ExpectedHeadID || head.Helper == nil {
		return cfg, content.HelperIdentity{}, sandboxRefusal("source_changed", nil)
	} else if head.State == content.LaunchEnded {
		if req.Source != nil {
			return cfg, content.HelperIdentity{}, sandboxRefusal("source_changed", nil)
		}
		size := session.DefaultSize()
		cfg.Cols, cfg.Rows, cfg.XPixel, cfg.YPixel = size.Cols, size.Rows, size.XPixel, size.YPixel
		return cfg, *head.Helper, nil
	} else if head.State != content.LaunchActive || req.Source == nil {
		return cfg, content.HelperIdentity{}, sandboxRefusal("source_unknown", nil)
	}
	current, err := c.registry.Get(session.ID(req.Source.SessionID))
	if err != nil || current.Identity() != req.Source.ref().Identity || current.PaneID() != pane.ID || current.Kind() != session.KindLocal || current.Liveness().Liveness != session.LivenessAlive || !c.registry.InputAllowed(current.ID()) {
		return cfg, content.HelperIdentity{}, sandboxRefusal("source_changed", err)
	}
	source, err := c.launches.SourceBinding(ctx, pane.ID, string(current.ID()))
	if err != nil || source.Host != "" || source.Account != "" {
		return cfg, content.HelperIdentity{}, sandboxRefusal("source_unknown", err)
	}
	if head.ID != "" {
		if *head.Helper != source {
			return cfg, source, sandboxRefusal("source_changed", nil)
		}
		binding, bindingErr := c.helper.SandboxBinding(ctx, source)
		if bindingErr != nil || !sandboxBindingMatches(head, source, binding) || binding.Enforcement == "ended" {
			return cfg, source, sandboxRefusal("source_unknown", bindingErr)
		}
	}
	size := current.EffectiveSize()
	cfg.Cols, cfg.Rows, cfg.XPixel, cfg.YPixel = size.Cols, size.Rows, size.XPixel, size.YPixel
	cfg.Parent, _ = current.Parent()
	return cfg, source, nil
}

func (c *SandboxCoordinator) Replace(ctx context.Context, req SandboxReplaceRequest) (SandboxOperation, error) {
	if req.OperationID == "" || req.ConfirmationID == "" {
		return SandboxOperation{}, sandboxRefusal("confirmation_required", nil)
	}
	c.mu.Lock()
	if c.root.Err() != nil {
		c.mu.Unlock()
		return SandboxOperation{}, sandboxRefusal("coordinator_stopped", c.root.Err())
	}
	if prior := c.operations[req.OperationID]; prior != nil {
		c.mu.Unlock()
		if prior.preview.confirmationID != req.ConfirmationID {
			return SandboxOperation{}, sandboxRefusal("confirmation_mismatch", nil)
		}
		return c.awaitOperation(ctx, req.OperationID, prior)
	}
	preview := c.previews[req.OperationID]
	if preview == nil || preview.confirmationID != req.ConfirmationID {
		c.mu.Unlock()
		return SandboxOperation{}, sandboxRefusal("confirmation_unknown", nil)
	}
	if preview.prepared.Preparation.Ticket == "" {
		c.mu.Unlock()
		return SandboxOperation{}, sandboxRefusal("preparation_pending", nil)
	}
	if len(c.operations) >= 1024 {
		for id, prior := range c.operations {
			select {
			case <-prior.done:
				delete(c.operations, id)
			default:
			}
			if len(c.operations) < 1024 {
				break
			}
		}
		if len(c.operations) >= 1024 {
			c.mu.Unlock()
			return SandboxOperation{}, sandboxRefusal("operation_busy", nil)
		}
	}
	delete(c.previews, req.OperationID)
	if !c.now().Before(preview.expires) {
		c.mu.Unlock()
		c.discardPreview(preview.prepared)
		return SandboxOperation{}, sandboxRefusal("preparation_expired", nil)
	}
	opCtx, cancel := context.WithCancel(c.root)
	op := &sandboxConfirmedOperation{preview: preview, ctx: opCtx, cancel: cancel, done: make(chan struct{})}
	c.operations[req.OperationID] = op
	c.mu.Unlock()
	go c.runReplacement(op)
	return c.awaitOperation(ctx, req.OperationID, op)
}

func (c *SandboxCoordinator) awaitOperation(ctx context.Context, operationID string, op *sandboxConfirmedOperation) (SandboxOperation, error) {
	select {
	case <-ctx.Done():
		return SandboxOperation{}, ctx.Err()
	case <-op.done:
	}
	op.mu.Lock()
	operationErr := op.err
	op.mu.Unlock()
	if operationErr != nil {
		return SandboxOperation{}, operationErr
	}
	return c.Operation(ctx, SandboxOperationRequest{OperationID: operationID})
}

func (c *SandboxCoordinator) Cancel(ctx context.Context, req SandboxReplaceRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	preview, op := c.previews[req.OperationID], c.operations[req.OperationID]
	if preview != nil && preview.confirmationID == req.ConfirmationID {
		if preview.prepared.Preparation.Ticket == "" {
			c.mu.Unlock()
			return sandboxRefusal("preparation_pending", nil)
		}
		delete(c.previews, req.OperationID)
		prepared := preview.prepared
		c.mu.Unlock()
		return c.helper.SandboxDiscard(ctx, prepared)
	}
	c.mu.Unlock()
	if op == nil || op.preview.confirmationID != req.ConfirmationID {
		return sandboxRefusal("confirmation_unknown", nil)
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	if op.committed {
		return sandboxRefusal("already_committed", nil)
	}
	op.cancel()
	return nil
}

func (c *SandboxCoordinator) recheckPreview(ctx context.Context, p *sandboxPreview) error {
	pane, workspaceID, err := c.pane(ctx, p.request.PaneID)
	if err != nil {
		return err
	}
	if workspaceID != p.workspaceID || pane.Cwd != p.cwd || pane.Kind != content.PaneLocal {
		return sandboxRefusal("source_changed", nil)
	}
	_, source, err := c.previewSource(ctx, p.request, pane, workspaceID)
	if err != nil {
		return err
	}
	if source != p.source {
		return sandboxRefusal("source_changed", nil)
	}
	return nil
}

func (c *SandboxCoordinator) runReplacement(op *sandboxConfirmedOperation) {
	defer func() {
		op.mu.Lock()
		op.finished = c.now()
		op.mu.Unlock()
		op.cancel()
		close(op.done)
	}()
	p := op.preview
	if err := c.recheckPreview(op.ctx, p); err != nil {
		c.discardPreview(p.prepared)
		op.mu.Lock()
		op.err = err
		op.mu.Unlock()
		return
	}
	intent := content.LaunchPrepare{ID: p.operationID, PaneID: p.request.PaneID, WorkspaceID: p.workspaceID, Source: p.source, TargetGeneration: p.prepared.Generation, ExpectedHeadID: p.request.ExpectedHeadID, StandardRevision: p.standard.Revision, WorkspaceRevision: p.workspace.Revision, Mode: p.request.Mode, Policy: p.prepared.Preparation.Policy, PolicyDigest: p.prepared.Preparation.Digest}
	if intent.Policy != nil {
		intent.PolicyVersion = intent.Policy.Version
	}
	var launch content.Launch
	err := c.profiles.WithStandardRevision(p.standard.Revision, func() error {
		var prepareErr error
		launch, prepareErr = c.launches.Prepare(op.ctx, intent)
		return prepareErr
	})
	if err != nil {
		c.discardPreview(p.prepared)
		op.mu.Lock()
		op.err = sandboxProfileError(err)
		op.mu.Unlock()
		return
	}
	op.mu.Lock()
	op.result.Launch = launch
	op.mu.Unlock()
	cfg := p.cfg
	cfg.LaunchBinding = session.LaunchBinding{Mode: string(launch.Mode), LaunchID: launch.ID, Digest: launch.PolicyDigest, Version: launch.PolicyVersion}
	params := proto.SandboxLaunchParams{Ticket: p.prepared.Preparation.Ticket, OperationID: p.operationID, LaunchID: launch.ID, Mode: proto.SandboxMode(launch.Mode), Shape: proto.SandboxLaunchShape{Cols: cfg.Cols, Rows: cfg.Rows, XPixel: cfg.XPixel, YPixel: cfg.YPixel}}
	if launch.GrantID != nil {
		cfg.LaunchBinding.GrantID = *launch.GrantID
		params.Grant = &proto.SandboxGrantBinding{ID: *launch.GrantID, Digest: launch.PolicyDigest, Version: launch.PolicyVersion}
	}
	candidate, err := c.helper.SandboxOpenCandidate(op.ctx, cfg, p.prepared, params)
	if err != nil {
		c.failReplacement(op, launch, nil, sandboxRefusal("candidate_start_failed", err))
		return
	}
	if candidate.Identity.Generation != p.prepared.Generation || candidate.Identity.Host != "" || candidate.Identity.Account != "" || candidate.Identity.SessionID != candidate.Entry.HostSessionID.Session || candidate.Entry.HostSessionID.Generation != candidate.Identity.Generation || candidate.Entry.Workspace != p.workspaceID || candidate.Entry.Launch == nil || candidate.Entry.Exit != nil || candidate.Publish == nil || candidate.Abort == nil {
		c.failReplacement(op, launch, &candidate, sandboxRefusal("candidate_mismatch", nil))
		return
	}
	binding, err := c.helper.SandboxBinding(op.ctx, candidate.Identity)
	if err != nil || !sandboxBindingMatches(launch, candidate.Identity, binding) || binding.Enforcement == "ended" {
		c.failReplacement(op, launch, &candidate, sandboxRefusal("candidate_mismatch", err))
		return
	}
	if err = c.recheckPreview(op.ctx, p); err != nil {
		c.failReplacement(op, launch, &candidate, err)
		return
	}
	commit := func() error {
		op.mu.Lock()
		defer op.mu.Unlock()
		if cancelErr := op.ctx.Err(); cancelErr != nil {
			return cancelErr
		}
		commitErr := c.profiles.WithStandardRevision(p.standard.Revision, func() error {
			var dbErr error
			launch, dbErr = c.launches.Commit(op.ctx, content.LaunchCommit{LaunchID: launch.ID, ExpectedSource: p.source, ExpectedHeadID: p.request.ExpectedHeadID, SourceCwd: p.cwd, Candidate: candidate.Identity, Binding: content.Session{ID: candidate.Identity.SessionID, WorkspaceID: p.workspaceID, PaneID: p.request.PaneID, Generation: candidate.Identity.Generation, LifecycleApplied: new(uint64)}})
			return dbErr
		})
		if commitErr == nil {
			op.committed = true
			op.result.Launch = launch
		}
		return commitErr
	}
	retire := func() {
		if c.retireSource != nil {
			c.retireSource(session.ID(p.source.SessionID))
		}
	}
	if p.request.Source != nil {
		err = c.registry.FenceInput(op.ctx, p.request.Source.ref(), commit, retire)
	} else {
		err = commit()
		if err == nil {
			retire()
		}
	}
	if err != nil {
		c.failReplacement(op, launch, &candidate, sandboxRefusal("selection_conflict", err))
		return
	}
	// Once selected, UI cancellation or shutdown must not turn publication into
	// rollback. Recovery owns a committed process if local publication fails.
	publishCtx := context.WithoutCancel(op.ctx)
	hosted, err := candidate.Publish(publishCtx)
	if err == nil {
		opened := OpenedSession{Session: hosted.Session, Config: cfg, Hosted: &hosted, WorkspaceID: p.workspaceID, OwnedProcessPID: candidate.Entry.Launch.Pid}
		c.mu.Lock()
		publisher := c.publisher
		c.mu.Unlock()
		if publisher == nil {
			err = errors.New("sandbox publisher is not bound")
		} else {
			err = publisher(publishCtx, opened)
		}
		if err == nil {
			op.mu.Lock()
			op.result.Opened = &opened
			op.mu.Unlock()
		}
	}
	if err != nil {
		op.mu.Lock()
		op.result.Reason = "publication_pending"
		op.mu.Unlock()
	}
	// Retirement cannot undo the new foreground. An unresolved exact close is
	// retained independently of the pane and retried through recovery.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(c.root), 30*time.Second)
	defer cancel()
	_ = c.closeRetirement(cleanupCtx, content.SessionRetirement{Identity: p.source, OperationID: launch.ID, Cause: content.RetirementReplacement, ClosePending: true})
}

func sandboxBindingMatches(launch content.Launch, identity content.HelperIdentity, binding helperclient.SandboxSession) bool {
	if binding.HostSessionID.Generation != identity.Generation || binding.HostSessionID.Session != identity.SessionID || binding.OperationID != launch.ID || binding.LaunchID != launch.ID || binding.Mode != string(launch.Mode) {
		return false
	}
	if launch.Mode == content.LaunchOff {
		return binding.Grant == nil && (binding.Enforcement == "off" || binding.Enforcement == "ended")
	}
	return launch.Mode == content.LaunchEnforce && launch.GrantID != nil && binding.Grant != nil && binding.Grant.ID == *launch.GrantID && binding.Grant.Digest == launch.PolicyDigest && binding.Grant.Version == launch.PolicyVersion && (binding.Enforcement == "enforced" || binding.Enforcement == "ended")
}
