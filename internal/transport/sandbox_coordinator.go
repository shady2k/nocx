package transport

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/sandbox"
	"github.com/shady2k/nocx/internal/session"
)

const (
	sandboxPreviewLimit = 32
	sandboxPreviewTTL   = time.Minute
)

// SandboxCoordinatorOptions supplies the existing authorities; the coordinator
// creates neither another session registry nor another shell launcher.
type SandboxCoordinatorOptions struct {
	Context         context.Context
	Profiles        *sandbox.ProfileRepository
	Launches        content.LaunchRepository
	Layout          content.LayoutRepository
	Registry        *session.Reg
	Helper          SandboxHelper
	RetireSource    func(session.ID)
	NativeAvailable func() error
	Now             func() time.Time
}

type sandboxPreview struct {
	request        SandboxPreviewRequest
	operationID    string
	confirmationID string
	workspaceID    string
	cwd            string
	source         content.HelperIdentity
	cfg            session.Config
	standard       sandbox.StandardDocument
	workspace      sandbox.WorkspaceProfile
	prepared       SandboxPrepared
	expires        time.Time
}

type sandboxConfirmedOperation struct {
	preview   *sandboxPreview
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	mu        sync.Mutex
	committed bool
	result    SandboxOperation
	err       error
	finished  time.Time
}

// SandboxCoordinator serializes only its short admissions. Native preparation,
// candidate startup, rollback and exact-helper close never hold mu/config gates.
type SandboxCoordinator struct {
	root             context.Context
	profiles         *sandbox.ProfileRepository
	launches         content.LaunchRepository
	layout           content.LayoutRepository
	registry         *session.Reg
	helper           SandboxHelper
	retireSource     func(session.ID)
	now              func() time.Time
	available        func() error
	availabilityOnce sync.Once
	availabilityErr  error
	mu               sync.Mutex
	previews         map[string]*sandboxPreview
	operations       map[string]*sandboxConfirmedOperation
	publisher        func(context.Context, OpenedSession) error
	recoveryMu       sync.Mutex
	sweepDone        chan struct{}
}

var _ SandboxControl = (*SandboxCoordinator)(nil)

func NewSandboxCoordinator(opts SandboxCoordinatorOptions) *SandboxCoordinator {
	if opts.Context == nil {
		opts.Context = context.Background()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.NativeAvailable == nil {
		opts.NativeAvailable = sandbox.NativeAvailable
	}
	c := &SandboxCoordinator{root: opts.Context, profiles: opts.Profiles, launches: opts.Launches, layout: opts.Layout, registry: opts.Registry, helper: opts.Helper, retireSource: opts.RetireSource, now: opts.Now, available: opts.NativeAvailable, previews: make(map[string]*sandboxPreview), operations: make(map[string]*sandboxConfirmedOperation), sweepDone: make(chan struct{})}
	go c.sweepPreviews()
	return c
}

// Drain waits for root-owned work after the composition root cancels its context.
// No worker can be admitted after that cancellation; committed processes survive.
func (c *SandboxCoordinator) Drain(ctx context.Context) error {
	c.mu.Lock()
	pending := make([]<-chan struct{}, 0, len(c.operations)+1)
	pending = append(pending, c.sweepDone)
	for _, op := range c.operations {
		pending = append(pending, op.done)
	}
	c.mu.Unlock()
	for _, done := range pending {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (c *SandboxCoordinator) BindPublisher(publish func(context.Context, OpenedSession) error) {
	c.mu.Lock()
	c.publisher = publish
	c.mu.Unlock()
}

func (c *SandboxCoordinator) storeReady() error {
	if c.profiles == nil || c.launches == nil || c.layout == nil || c.registry == nil {
		return sandboxRefusal("store_unavailable", nil)
	}
	return nil
}

func (c *SandboxCoordinator) nativeReady() error {
	if c.helper == nil {
		return sandboxRefusal("helper_unavailable", nil)
	}
	c.availabilityOnce.Do(func() { c.availabilityErr = c.available() })
	if c.availabilityErr != nil {
		return sandboxRefusal("native_unsupported", c.availabilityErr)
	}
	return nil
}

func (c *SandboxCoordinator) Profile(ctx context.Context, req SandboxProfileRequest) (SandboxProfileResult, error) {
	if c.profiles == nil {
		return SandboxProfileResult{}, sandboxRefusal("store_unavailable", nil)
	}
	standard, err := c.profiles.GetStandard()
	if err != nil {
		return SandboxProfileResult{}, sandboxRefusal("profile_unavailable", err)
	}
	out := SandboxProfileResult{Standard: standard, Effective: sandbox.EffectiveWorkspaceRoots(sandbox.WorkspaceProfile{}, standard), ProfileSource: "standard"}
	if req.WorkspaceID == "" {
		return out, nil
	}
	workspace, err := c.profiles.GetWorkspaceProfile(ctx, req.WorkspaceID)
	if err != nil {
		return SandboxProfileResult{}, sandboxRefusal("profile_unavailable", err)
	}
	out.Workspace = &workspace
	out.Effective = sandbox.EffectiveWorkspaceRoots(workspace, standard)
	if workspace.Override != nil {
		out.ProfileSource = "workspace"
	}
	return out, nil
}

func (c *SandboxCoordinator) UpdateProfile(ctx context.Context, req SandboxProfileUpdateRequest) (SandboxProfileResult, error) {
	if c.profiles == nil {
		return SandboxProfileResult{}, sandboxRefusal("store_unavailable", nil)
	}
	var err error
	if req.WorkspaceID == "" {
		if req.Enabled == nil {
			return SandboxProfileResult{}, sandboxRefusal("invalid_profile_update", nil)
		}
		_, err = c.profiles.UpdateStandard(req.ExpectedRevision, *req.Enabled, req.Roots)
	} else {
		if req.Enabled != nil {
			return SandboxProfileResult{}, sandboxRefusal("invalid_profile_update", nil)
		}
		_, err = c.profiles.UpdateWorkspaceProfile(ctx, req.WorkspaceID, req.ExpectedRevision, &req.Roots)
	}
	if err != nil {
		return SandboxProfileResult{}, sandboxProfileError(err)
	}
	return c.Profile(ctx, SandboxProfileRequest{WorkspaceID: req.WorkspaceID})
}

func (c *SandboxCoordinator) ResetProfile(ctx context.Context, req SandboxProfileResetRequest) (SandboxProfileResult, error) {
	if c.profiles == nil {
		return SandboxProfileResult{}, sandboxRefusal("store_unavailable", nil)
	}
	if req.WorkspaceID == "" || req.WorkspaceID == sandbox.DefaultWorkspaceID {
		return SandboxProfileResult{}, sandboxRefusal("workspace_profile_unsupported", nil)
	}
	if _, err := c.profiles.UpdateWorkspaceProfile(ctx, req.WorkspaceID, req.ExpectedRevision, nil); err != nil {
		return SandboxProfileResult{}, sandboxProfileError(err)
	}
	return c.Profile(ctx, SandboxProfileRequest{WorkspaceID: req.WorkspaceID})
}

func sandboxProfileError(err error) error {
	switch {
	case errors.Is(err, sandbox.ErrProfileConflict), errors.Is(err, content.ErrLaunchConflict):
		return sandboxRefusal("profile_conflict", err)
	case errors.Is(err, sandbox.ErrProfileRestoreBusy):
		return sandboxRefusal("restore_busy", err)
	case errors.Is(err, sandbox.ErrWorkspaceProfileUnsupported):
		return sandboxRefusal("workspace_profile_unsupported", err)
	default:
		return sandboxRefusal("profile_update_failed", err)
	}
}

func (c *SandboxCoordinator) pane(ctx context.Context, paneID string) (content.Pane, string, error) {
	if paneID == "" {
		return content.Pane{}, "", sandboxRefusal("pane_required", nil)
	}
	workspaceID, err := c.layout.WorkspaceForPane(ctx, paneID)
	if err != nil {
		return content.Pane{}, "", sandboxRefusal("pane_unavailable", err)
	}
	tabID, err := c.layout.TabForPane(ctx, paneID)
	if err != nil {
		return content.Pane{}, "", sandboxRefusal("pane_unavailable", err)
	}
	panes, err := c.layout.Panes(ctx, tabID)
	if err != nil {
		return content.Pane{}, "", sandboxRefusal("pane_unavailable", err)
	}
	for _, pane := range panes {
		if pane.ID == paneID {
			return pane, workspaceID, nil
		}
	}
	return content.Pane{}, "", sandboxRefusal("pane_unavailable", content.ErrNotFound)
}

func (c *SandboxCoordinator) Status(ctx context.Context, req SandboxStatusRequest) (SandboxStatusResult, error) {
	out := SandboxStatusResult{PaneID: req.PaneID, Availability: "unavailable", ProfileSource: "standard"}
	if err := c.storeReady(); err != nil {
		out.Reason = "store_unavailable"
		return out, nil
	}
	pane, workspaceID, err := c.pane(ctx, req.PaneID)
	if err != nil {
		return out, err
	}
	out.WorkspaceID = workspaceID
	profile, err := c.Profile(ctx, SandboxProfileRequest{WorkspaceID: workspaceID})
	if err != nil {
		out.Reason = "profile_unavailable"
		return out, nil
	}
	out.Enabled, out.StandardRevision = profile.Standard.Enabled, profile.Standard.Revision
	out.WorkspaceRevision, out.ProfileSource = profile.Workspace.Revision, profile.ProfileSource
	if pane.Kind != content.PaneLocal {
		out.Reason = "local_only"
	} else if err = c.nativeReady(); err != nil {
		out.Reason = "native_unsupported"
	} else {
		out.Availability = "available"
	}
	head, headErr := c.launches.Head(ctx, pane.ID)
	if headErr != nil && !errors.Is(headErr, content.ErrNotFound) {
		out.Availability, out.Reason = "unknown", "head_unavailable"
		return out, nil
	}
	if headErr == nil {
		out.Head = &SandboxHeadSummary{LaunchID: head.ID, Mode: head.Mode, State: head.State, GrantID: head.GrantID, PolicyDigest: head.PolicyDigest, PolicyVersion: head.PolicyVersion, Enforcement: "unknown", Observer: "unavailable"}
		if head.State == content.LaunchEnded {
			out.Head.Enforcement = "ended"
		}
		if head.Helper != nil && c.helper != nil {
			binding, bindingErr := c.helper.SandboxBinding(ctx, *head.Helper)
			if bindingErr == nil && sandboxBindingMatches(head, *head.Helper, binding) {
				out.Head.Enforcement, out.Head.Observer = binding.Enforcement, binding.Observer
				if binding.Enforcement == "ended" && head.State == content.LaunchActive {
					if endErr := c.launches.End(ctx, *head.Helper); endErr != nil {
						out.Availability, out.Reason = "unknown", "head_unavailable"
						return out, nil
					}
					head.State, out.Head.State = content.LaunchEnded, content.LaunchEnded
				}
			}
			if head.State == content.LaunchActive && out.Head.Enforcement != "unknown" {
				if current, lookupErr := c.registry.Get(session.ID(head.Helper.SessionID)); lookupErr == nil && current.PaneID() == pane.ID && current.Liveness().Liveness == session.LivenessAlive && c.registry.InputAllowed(current.ID()) {
					out.Source = sandboxSourceOf(current)
				}
			}
		}
	} else {
		for _, current := range c.registry.List() {
			if current.PaneID() == pane.ID && current.Kind() == session.KindLocal && c.registry.InputAllowed(current.ID()) {
				binding, ok := c.registry.LaunchBinding(current.ID())
				if ok && binding.LaunchID == "" {
					out.Source = sandboxSourceOf(current)
					break
				}
			}
		}
	}
	preparing, prepareErr := c.launches.Preparing(ctx)
	if prepareErr != nil {
		out.Availability, out.Reason = "unknown", "cleanup_unknown"
		return out, nil
	}
	for _, pending := range preparing {
		if pending.PaneID == pane.ID {
			out.PreparingOperationID = pending.ID
			break
		}
	}
	return out, nil
}

func (c *SandboxCoordinator) Grant(ctx context.Context, req SandboxGrantRequest) (SandboxGrantResult, error) {
	if err := c.storeReady(); err != nil {
		return SandboxGrantResult{}, err
	}
	launch, err := c.launches.GetLaunch(ctx, req.LaunchID)
	if err != nil {
		return SandboxGrantResult{}, sandboxRefusal("grant_unavailable", err)
	}
	return SandboxGrantResult{LaunchID: launch.ID, GrantID: launch.GrantID, Mode: launch.Mode, Policy: launch.Policy, PolicyDigest: launch.PolicyDigest, PolicyVersion: launch.PolicyVersion}, nil
}

func (c *SandboxCoordinator) PermitOrdinaryOpen(ctx context.Context, paneID string) error {
	if paneID == "" {
		return nil
	}
	if err := c.storeReady(); err != nil {
		return err
	}
	if err := c.cleanupAdmission(ctx, paneID); err != nil {
		return err
	}
	head, err := c.launches.Head(ctx, paneID)
	if errors.Is(err, content.ErrNotFound) {
		return nil
	}
	if err != nil {
		return sandboxRefusal("head_unknown", err)
	}
	if head.ID != "" {
		return sandboxRefusal("sandbox_relaunch_required", nil)
	}
	return nil
}
