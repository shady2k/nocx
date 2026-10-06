package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/session"
)

var errProtectedExcluded = errors.New("protected launch is excluded from generic readopt")

// protectedLaunch classifies durable launch provenance before generic session
// recovery. errProtectedExcluded means an exact candidate/retirement must not
// flow into ordinary adoption, even when its cleanup remains uncertain.
func (rp *readoptPass) protectedLaunch(ctx context.Context, p content.PendingSession) (sessionInventory, error) {
	identity := content.HelperIdentity{Host: p.Host, Account: p.Account, Generation: p.Generation, SessionID: p.SessionID}
	head, headErr := rp.launches.Head(ctx, p.PaneID)
	if headErr != nil && !errors.Is(headErr, content.ErrNotFound) {
		return nil, fmt.Errorf("read sandbox head for pane %s: %w", p.PaneID, headErr)
	}
	preparing, err := rp.launches.Preparing(ctx)
	if err != nil {
		return nil, fmt.Errorf("classify preparing sandbox launches: %w", err)
	}
	// The startup sweep owns rollback; this check is a second, exact barrier
	// against a candidate accidentally reaching generic adoption.
	for _, candidate := range preparing {
		if candidate.TargetGeneration != identity.Generation {
			continue
		}
		if rp.sandbox == nil {
			return nil, errors.New("cannot classify a helper generation with a pending preparation")
		}
		binding, bindErr := rp.sandbox.SandboxBinding(ctx, identity)
		if bindErr != nil {
			return nil, fmt.Errorf("classify pending sandbox candidate: %w", bindErr)
		}
		if binding.LaunchID == candidate.ID || binding.OperationID == candidate.ID {
			return nil, errProtectedExcluded
		}
	}
	retirements, err := rp.launches.PendingRetirements(ctx, 32)
	if err != nil {
		return nil, fmt.Errorf("classify pending session retirements: %w", err)
	}
	for _, pending := range retirements {
		if pending.Identity == identity {
			return nil, errProtectedExcluded
		}
	}
	launch, launchErr := rp.launches.ByHelper(ctx, identity)
	if launchErr != nil && !errors.Is(launchErr, content.ErrNotFound) {
		return nil, fmt.Errorf("classify sandbox launch for session %s: %w", p.SessionID, launchErr)
	}
	_, retirementErr := rp.launches.Retirement(ctx, identity)
	if retirementErr != nil && !errors.Is(retirementErr, content.ErrNotFound) {
		return nil, fmt.Errorf("classify retirement for session %s: %w", p.SessionID, retirementErr)
	}
	if retirementErr == nil {
		return nil, errProtectedExcluded
	}
	if launchErr != nil {
		return nil, nil
	}
	if launch.State == content.LaunchPreparing || launch.State == content.LaunchFailed || launch.Helper == nil || !sameReadoptIdentity(*launch.Helper, identity, isLocalBinding(p)) {
		return nil, errProtectedExcluded
	}
	if launch.State != content.LaunchActive && launch.State != content.LaunchEnded {
		return nil, errProtectedExcluded
	}
	if launch.Mode != content.LaunchEnforce && launch.Mode != content.LaunchOff {
		return nil, errProtectedExcluded
	}
	if headErr != nil || head.ID != launch.ID || head.Helper == nil || !sameReadoptIdentity(*head.Helper, identity, isLocalBinding(p)) {
		return nil, errProtectedExcluded
	}
	if rp.sandbox == nil {
		return nil, errors.New("the protected helper route is unavailable; refusing ordinary readopt")
	}
	binding, err := rp.sandbox.SandboxBinding(ctx, *launch.Helper)
	if err != nil {
		return nil, fmt.Errorf("verify protected helper binding: %w", err)
	}
	if binding.HostSessionID.Generation != identity.Generation || binding.HostSessionID.Session != identity.SessionID ||
		binding.LaunchID != launch.ID || binding.Mode != string(launch.Mode) {
		return nil, errors.New("protected helper binding does not match the committed launch; refusing readopt")
	}
	if launch.Mode == content.LaunchEnforce {
		if binding.Grant == nil || launch.GrantID == nil || binding.Grant.ID != *launch.GrantID ||
			binding.Grant.Digest != launch.PolicyDigest || binding.Grant.Version != launch.PolicyVersion ||
			(binding.Enforcement != "enforced" && binding.Enforcement != "ended") {
			return nil, errors.New("protected helper grant does not match the committed launch; refusing readopt")
		}
	} else if binding.Grant != nil || launch.GrantID != nil ||
		(binding.Enforcement != "off" && binding.Enforcement != "ended") {
		return nil, errors.New("Off helper binding unexpectedly carries sandbox authority")
	}
	entries, err := rp.sandbox.SandboxInventory(ctx, identity.Generation)
	if err != nil {
		return nil, fmt.Errorf("verify protected helper inventory: %w", err)
	}
	var entry *client.SessionEntry
	for i := range entries {
		if entries[i].HostSessionID.Generation == identity.Generation && entries[i].HostSessionID.Session == identity.SessionID {
			entry = &entries[i]
			break
		}
	}
	if entry == nil {
		return nil, errors.New("protected helper inventory omitted the committed session; refusing ordinary readopt")
	}
	if entry.Exit == nil && binding.Enforcement == "ended" {
		return nil, errors.New("helper reports an ended launch without an exited inventory entry")
	}
	if entry.Exit != nil && binding.Enforcement != "ended" {
		return nil, errors.New("exited helper entry does not have ended sandbox state")
	}
	if entry.Exit != nil && launch.State == content.LaunchActive {
		// Generic exited-session recovery can release retained helper inventory.
		// Preserve its authenticated exit fact in launch authority first.
		if err := rp.launches.End(ctx, *launch.Helper); err != nil {
			return nil, fmt.Errorf("persist protected launch exit before inventory release: %w", err)
		}
	}
	if rp.adopter == nil || rp.registry == nil || rp.registry.registry == nil {
		return nil, errors.New("protected session adoption is unavailable")
	}
	carrier := rp.protectedCarrier(identity)
	if carrier == nil {
		return nil, errors.New("protected helper attachment route is unavailable; refusing ordinary readopt")
	}
	bindingMetadata := session.LaunchBinding{Mode: string(launch.Mode), LaunchID: launch.ID}
	if launch.Mode == content.LaunchEnforce {
		bindingMetadata.GrantID = *launch.GrantID
		bindingMetadata.Digest = launch.PolicyDigest
		bindingMetadata.Version = launch.PolicyVersion
	}
	cfg := session.Config{
		Kind: session.KindLocal, PaneID: p.PaneID, Cwd: launchCwd(entry),
		OpenedAt: pipeOpenedAt(entry, p.Since), LaunchBinding: bindingMetadata,
	}
	if p.Host != "" {
		if rp.routes == nil {
			return nil, errors.New("protected remote session route is unavailable")
		}
		host, resolved, err := rp.routes.Resolve(p.ProfileID)
		if err != nil {
			return nil, fmt.Errorf("resolve protected session route: %w", err)
		}
		if host != p.Host || resolved == nil || (p.Account != "" && resolved.User != p.Account) {
			return nil, errors.New("protected session route no longer matches its committed helper identity")
		}
		cfg.Kind, cfg.Host, cfg.Remote, cfg.ProfileID = session.KindRemote, host, resolved, p.ProfileID
	}
	inv := &readoptedInventory{generation: identity.Generation, host: p.Host, account: p.Account, live: map[string]struct{}{identity.SessionID: {}}}
	if err := rp.readopt(ctx, p, cfg, nil, carrier, *entry); err != nil {
		if isLocalIdentity(identity) {
			rp.local.Release(identity.SessionID)
		}
		return nil, fmt.Errorf("readopt protected session: %w", err)
	}
	if isLocalIdentity(identity) {
		rp.local.noteHeld(session.ID(identity.SessionID))
	}
	return inv, nil
}

func sameReadoptIdentity(a, b content.HelperIdentity, local bool) bool {
	if a.Generation != b.Generation || a.SessionID != b.SessionID {
		return false
	}
	if local && a.Host == "" && a.Account == "" {
		return true
	}
	return a.Host == b.Host && a.Account == b.Account
}

func (rp *readoptPass) protectedCarrier(identity content.HelperIdentity) hostedCarrier {
	if isLocalIdentity(identity) {
		return rp.local
	}
	return nil
}

func isLocalIdentity(identity content.HelperIdentity) bool {
	return identity.Host == "" && identity.Account == ""
}
