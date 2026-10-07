package app

import (
	"fmt"

	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport"
)

func (o *localHelperOpener) hostedOpenResult(generation string, res hostedSpawnResult) (transport.HostedSessionOpen, error) {
	sid := res.Session.ID()
	if o.registry == nil {
		_ = res.Session.Close()
		return transport.HostedSessionOpen{}, fmt.Errorf("local helper opener has no session registry")
	}
	remote := res.Entry.RemoteLaunch
	shell := ""
	var status string
	var reason ssh.RefusalReason
	if remote == nil {
		launch := res.Entry.Launch
		if launch == nil {
			_ = res.Session.Close()
			return transport.HostedSessionOpen{}, fmt.Errorf("this machine's helper reported no launch record for session %s", sid)
		}
		shell = launch.Shell
		if err := o.registry.RecordOwnedProcessPID(sid, launch.Pid); err != nil {
			_ = res.Session.Close()
			return transport.HostedSessionOpen{}, fmt.Errorf("recording local helper launch pid: %w", err)
		}
		status, reason = localIntegrationStatus(shell, res.LifecycleLane)
		o.watchForReplacement(res.Session, launch.Pid, shell)
	}
	if res.LifecycleLane != "" && o.noteChildDomainParent != nil {
		o.noteChildDomainParent(res.LifecycleTransport, res.LifecycleLane, string(sid))
	}
	out := transport.HostedSessionOpen{
		Session: res.Session, Generation: generation,
		LifecycleLane: res.LifecycleLane, StartLifecycle: res.StartLifecycle,
		AbortLifecycle: res.AbortLifecycle, DetachLifecycle: res.DetachLifecycle,
		ObserveOutputHoles: res.ObserveOutputHoles,
		IntegrationShell:   shell, IntegrationStatus: status, IntegrationReason: reason,
	}
	if remote != nil {
		out.Host, out.Account = remote.Host, remote.User
	}
	o.noteHeld(sid)
	go func() {
		<-res.Session.Done()
		o.forgetHeld(sid)
	}()
	return out, nil
}
