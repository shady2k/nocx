package app

// The composition root's half of internal/farhelper: the remote seams it
// declares (probe, install, dial), the connection source it reads, and the
// D25 closer that has to run beside the far helper's own clients.
//
// # Why the seams take a RESOLVED destination
//
// Every other adapter in this file starts from host + []ssh.ConnectOption and
// resolves the destination itself. A connection, however, is known to the
// coordinator by its id and by the destination the helper already resolved and
// dialled; resolving it again would be a second answer to "which address is
// this" and, on a route with a jump host, a second walk of the same hops.
// So farhelper's seams carry proto.SSHDestination, and the adapters here send
// it to the local helper unchanged — the same destination the pane's own
// channel uses, which is what makes the helper's pool hand them one
// authenticated connection.
//
// # What is deliberately absent
//
// No ssh client, no dial and no resolution happens here: the local helper owns
// the dial (ADR-0057). This file only shapes what farhelper asked for into the
// helper ops that already exist, and classifies what comes back into
// farhelper's typed reasons.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"sync"

	"github.com/shady2k/nocx/internal/farhelper"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/deploy"
	helperartifacts "github.com/shady2k/nocx/internal/helper/deploy/artifacts"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/ssh"
)

// farHelperProbe asks the far host what it is over a probe lease on the
// destination the connection already names.
type farHelperProbe struct {
	local  *localHelperOpener
	source deploy.ArtifactSource
}

func (p *farHelperProbe) Probe(ctx context.Context, target farhelper.Target) (farhelper.Platform, error) {
	helper, err := p.local.probeHelper(ctx)
	if err != nil {
		return farhelper.Platform{}, err
	}
	lease, err := helper.AcquireProbeLease(ctx, proto.LeaseParams{
		HostKeyFingerprint: target.HostKeyFingerprint,
		Destination:        target.Destination,
		AcceptOnTrust:      false,
	})
	if err != nil {
		return farhelper.Platform{}, classifyFarHelperRefusal(err)
	}
	defer func() { _ = lease.Close() }()
	platform, err := deploy.Probe(ctx, probeExec{&platformLease{
		helperProbeLease: &helperProbeLease{lease: lease},
		host:             target.Destination.Host,
	}})
	if err != nil {
		return farhelper.Platform{}, classifyFarHelperRefusal(err)
	}
	if p.source != nil {
		if _, _, aerr := p.source.Artifact(platform); aerr != nil {
			return farhelper.Platform{}, fmt.Errorf("%w: %v", farhelper.ErrUnsupportedPlatform, aerr)
		}
	}
	return farhelper.Platform{GOOS: platform.GOOS, GOARCH: platform.GOARCH}, nil
}

// farHelperInstall writes the artifact over an install channel on the same
// destination and reports the directory and the build hash.
type farHelperInstall struct {
	local  *localHelperOpener
	over   *sshOverHelper
	source deploy.ArtifactSource
}

func (i *farHelperInstall) Install(ctx context.Context, target farhelper.Target, platform farhelper.Platform) (farhelper.Installed, error) {
	stream, err := i.over.openChannelFor(ctx, proto.OpenChannelParams{
		HostKeyFingerprint: target.HostKeyFingerprint,
		Destination:        target.Destination,
		Kind:               proto.ChannelSFTP,
		AcceptOnTrust:      false,
	})
	if err != nil {
		return farhelper.Installed{}, classifyFarHelperRefusal(err)
	}
	conn, err := ssh.NewHelperInstallConn(ctx, stream)
	if err != nil {
		_ = stream.Close()
		return farhelper.Installed{}, fmt.Errorf("install channel: %w", classifyFarHelperRefusal(err))
	}
	defer func() { _ = conn.Close() }()
	home, err := conn.Home()
	if err != nil {
		return farhelper.Installed{}, fmt.Errorf("remote home: %w", err)
	}
	var opts []deploy.Option
	if i.inflate(ctx, target, "") == nil {
		opts = append(opts, deploy.WithInflate(func(ctx context.Context, file string) error {
			return i.inflate(ctx, target, file)
		}))
	}
	command, hash, err := deploy.Ensure(ctx, installFS{conn}, i.source, home, deploy.Platform{
		GOOS: platform.GOOS, GOARCH: platform.GOARCH,
	}, nil, opts...)
	if err != nil {
		return farhelper.Installed{}, classifyFarHelperRefusal(err)
	}
	if err := deploy.Prune(ctx, installFS{conn}, home, path.Base(path.Dir(command))); err != nil {
		return farhelper.Installed{}, fmt.Errorf("prune after install: %w", err)
	}
	return farhelper.Installed{Dir: path.Dir(command), Generation: hash}, nil
}

// inflate is the install's one optimisation seam: the host's own gzip over the
// artifact. Every failure is reported to the caller, whose answer is the plain
// upload the install already has.
func (i *farHelperInstall) inflate(ctx context.Context, target farhelper.Target, file string) error {
	helper, err := i.local.probeHelper(ctx)
	if err != nil {
		return err
	}
	lease, err := helper.AcquireProbeLease(ctx, proto.LeaseParams{
		HostKeyFingerprint: target.HostKeyFingerprint,
		Destination:        target.Destination,
		AcceptOnTrust:      false,
	})
	if err != nil {
		return err
	}
	defer func() { _ = lease.Close() }()
	result, err := lease.Inflate(ctx, file)
	if err != nil {
		return err
	}
	if result.ExitStatus != 0 {
		return fmt.Errorf("gzip exited %d", result.ExitStatus)
	}
	return nil
}

// farHelperDial opens a lane to the installed helper on the connection's own
// destination and completes the hello.
type farHelperDial struct {
	over *sshOverHelper
	log  *slog.Logger
}

func (d *farHelperDial) Dial(ctx context.Context, target farhelper.Target, installed farhelper.Installed) (*helperclient.Client, error) {
	lane, err := d.over.openLaneFor(ctx, proto.LaneParams{
		HostKeyFingerprint: target.HostKeyFingerprint,
		Destination:        target.Destination,
		Machine:            proto.Machine{Dir: installed.Dir},
		Generation:         proto.GenerationID(installed.Generation),
	})
	if err != nil {
		return nil, classifyFarHelperRefusal(err)
	}
	peer, err := helperclient.Dial(ctx, helperclient.Config{
		Exec:       lane,
		ExpectHash: installed.Generation,
		Log:        d.log,
	})
	if err != nil {
		_ = lane.Close()
		return nil, classifyFarHelperDial(err)
	}
	return peer, nil
}

// classifyFarHelperRefusal maps the refusals the helper already speaks onto
// farhelper's own closed set. Everything it does not know keeps its own error,
// and farhelper's install-failed is the honest bucket for it.
func classifyFarHelperRefusal(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, deploy.ErrUnsupportedPlatform),
		errors.Is(err, helperartifacts.ErrArtifactsNotBuilt):
		return fmt.Errorf("%w: %v", farhelper.ErrUnsupportedPlatform, err)
	case errors.Is(err, helperclient.ErrExecForbidden):
		return fmt.Errorf("%w: %v", farhelper.ErrExecForbidden, err)
	case errors.Is(err, helperclient.ErrVersionMismatch), errors.Is(err, helperclient.ErrHashMismatch):
		return fmt.Errorf("%w: %v", farhelper.ErrVersionMismatch, err)
	}
	return err
}

// classifyFarHelperDial is classifyFarHelperRefusal plus the handshake states
// only a dial can answer: a peer that is not our helper and one that never
// answered are both "the host refused to run this", which is what a person
// acts on.
func classifyFarHelperDial(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, helperclient.ErrNotOurHelper),
		errors.Is(err, helperclient.ErrSentinelTimeout),
		errors.Is(err, helperclient.ErrHelperNotServing):
		return fmt.Errorf("%w: %v", farhelper.ErrExecForbidden, err)
	}
	return classifyFarHelperRefusal(err)
}

// openChannelFor opens one channel on a resolved destination. It is openChannel
// without the resolution: the caller already holds the destination the
// connection was dialled with.
func (h *sshOverHelper) openChannelFor(ctx context.Context, params proto.OpenChannelParams) (*helperclient.ChannelStream, error) {
	local, _, err := h.local.connect(ctx)
	if err != nil {
		return nil, err
	}
	stream, err := local.OpenChannel(ctx, params)
	if err != nil {
		return nil, h.translate(params.Destination.Host, err)
	}
	return stream, nil
}

// openLaneFor opens the exec lane a remote helper's bridge rides, on a
// destination the caller already resolved.
func (h *sshOverHelper) openLaneFor(ctx context.Context, params proto.LaneParams) (helperclient.HelperConn, error) {
	local, _, err := h.local.connect(ctx)
	if err != nil {
		return nil, err
	}
	lane, err := local.OpenLane(ctx, params)
	if err != nil {
		return nil, h.translate(params.Destination.Host, err)
	}
	return lane, nil
}

// farHelperConnections is the ConnectionSource farhelper reads, fed by the two
// places the coordinator already learns about a connection: the pane's bind
// (which names the resolved destination) and the helper's own connection-state
// notifications (which name the transport's state).
//
// # Generation until stage A
//
// The helper does not yet carry its transport generation on the wire, so this
// adapter mints one locally: a connection that reaches `connected` after being
// in any other state is a new link, and the counter moves. It is the same fence
// with a coarser source; stage A's Facts.Generation replaces the counter and
// this comment goes with it. The Shells count is the event's PlainShells: a
// helper-hosted shell implies an install already happened, so the only
// connection C9 must gate is the one whose own plain shell is what proves the
// host usable.
type farHelperConnections struct {
	mu       sync.Mutex
	conns    map[string]farhelper.Connection
	watchers map[uint64]func(farhelper.Connection)
	next     uint64
}

func newFarHelperConnections() *farHelperConnections {
	return &farHelperConnections{
		conns:    make(map[string]farhelper.Connection),
		watchers: make(map[uint64]func(farhelper.Connection)),
	}
}

func (f *farHelperConnections) List() []farhelper.Connection {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]farhelper.Connection, 0, len(f.conns))
	for _, c := range f.conns {
		out = append(out, c)
	}
	return out
}

func (f *farHelperConnections) Watch(fn func(farhelper.Connection)) (stop func()) {
	f.mu.Lock()
	f.next++
	id := f.next
	f.watchers[id] = fn
	f.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			delete(f.watchers, id)
			f.mu.Unlock()
		})
	}
}

// Observe records what a bind knows about a connection. A bind names the
// destination and the machine; it carries no ordered state, so an observation
// that is already known keeps its state and shell count.
func (f *farHelperConnections) Observe(c farhelper.Connection) {
	if f == nil || c.ID == "" {
		return
	}
	f.mu.Lock()
	previous, known := f.conns[c.ID]
	if known && c.Seq == 0 {
		if c.Destination.Host != "" {
			previous.Destination = c.Destination
		}
		if c.HostKeyFingerprint != "" {
			previous.HostKeyFingerprint = c.HostKeyFingerprint
		}
		c = previous
	}
	if !known && c.State == hostconn.Opening && c.Generation == 0 {
		// A bind before any event: the connection exists and is opening.
	}
	f.conns[c.ID] = c
	watchers := f.snapshotLocked()
	f.mu.Unlock()
	for _, fn := range watchers {
		fn(c)
	}
}

// Apply folds one connection-state notification into the cache. An event for a
// connection no bind named a destination for is ignored until stage A's
// `connections` op can answer it: a decision needs a destination, and guessing
// one from an id would be a second derivation of it.
func (f *farHelperConnections) Apply(_ string, ev proto.ConnectionStateEvent) {
	if f == nil || ev.ConnectionID == "" {
		return
	}
	f.mu.Lock()
	c, known := f.conns[ev.ConnectionID]
	if !known {
		f.mu.Unlock()
		return
	}
	if ev.Seq != 0 && c.Seq != 0 && ev.Seq <= c.Seq {
		f.mu.Unlock()
		return
	}
	previous := c.State
	c.Seq = ev.Seq
	c.State = farHelperKind(ev.State)
	c.Slow = ev.Slow
	c.Shells = ev.PlainShells
	if c.State == hostconn.Connected && previous != hostconn.Connected {
		c.Generation++
	}
	f.conns[ev.ConnectionID] = c
	watchers := f.snapshotLocked()
	f.mu.Unlock()
	for _, fn := range watchers {
		fn(c)
	}
}

func (f *farHelperConnections) snapshotLocked() []func(farhelper.Connection) {
	if len(f.watchers) == 0 {
		return nil
	}
	out := make([]func(farhelper.Connection), 0, len(f.watchers))
	for _, fn := range f.watchers {
		out = append(out, fn)
	}
	return out
}

// farHelperKind translates the wire's state name into hostconn's own kind. An
// unknown name is opening, the state every connection starts in; a name this
// build does not know cannot be allowed to look like a usable link.
func farHelperKind(state string) hostconn.Kind {
	switch state {
	case proto.ConnectionWaitingPerson:
		return hostconn.WaitingPerson
	case proto.ConnectionConnected:
		return hostconn.Connected
	case proto.ConnectionStalled:
		return hostconn.Stalled
	case proto.ConnectionReconnecting:
		return hostconn.Reconnecting
	case proto.ConnectionFailed:
		return hostconn.Failed
	case proto.ConnectionEnded:
		return hostconn.Ended
	default:
		return hostconn.Opening
	}
}

// farHelperCloser is D25's close-before-remove, with the far helper's clients
// in it. The transport already asks one value to close a machine's helper
// channels; this composite answers that question for both owners, so the order
// (farhelper's clients, then the git/session channels) is stated once.
type farHelperCloser struct {
	far *farhelper.Registry
	reg *helperRegistry
}

func (c farHelperCloser) CloseHelpersFor(ctx context.Context, fingerprint string) error {
	c.far.CloseFor(fingerprint)
	return c.reg.CloseHelpersFor(ctx, fingerprint)
}

func (c farHelperCloser) FinishHelpersFor(fingerprint string) {
	c.reg.FinishHelpersFor(fingerprint)
	c.far.FinishFor(fingerprint)
}
