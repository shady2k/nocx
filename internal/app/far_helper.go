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
	"time"

	"github.com/shady2k/nocx/internal/farhelper"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/helper/deploy"
	helperartifacts "github.com/shady2k/nocx/internal/helper/deploy/artifacts"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/panedriver"
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
	local    *localHelperOpener
	over     farInstallChannels
	source   deploy.ArtifactSource
	installs *consent.InstallStore
}

// farInstallChannels is the resolved-destination install channel the adapter needs.
type farInstallChannels interface {
	openInstallFor(context.Context, proto.OpenChannelParams) (ssh.HelperInstallConn, error)
}

func (h *sshOverHelper) openInstallFor(ctx context.Context, params proto.OpenChannelParams) (ssh.HelperInstallConn, error) {
	stream, err := h.openChannelFor(ctx, params)
	if err != nil {
		return nil, err
	}
	conn, err := ssh.NewHelperInstallConn(ctx, stream)
	if err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("install channel: %w", err)
	}
	return conn, nil
}

func (i *farHelperInstall) Find(ctx context.Context, target farhelper.Target, platform farhelper.Platform) (farhelper.Installed, bool, error) {
	conn, err := i.over.openInstallFor(ctx, proto.OpenChannelParams{
		HostKeyFingerprint: target.HostKeyFingerprint,
		Destination:        target.Destination,
		Kind:               proto.ChannelSFTP,
		AcceptOnTrust:      false,
	})
	if err != nil {
		return farhelper.Installed{}, false, classifyFarHelperRefusal(err)
	}
	defer func() { _ = conn.Close() }()
	home, err := conn.Home()
	if err != nil {
		return farhelper.Installed{}, false, fmt.Errorf("remote home: %w", err)
	}
	binary, hash, found, err := deploy.Find(ctx, installFS{conn}, i.source, home, deploy.Platform{GOOS: platform.GOOS, GOARCH: platform.GOARCH})
	if err != nil {
		return farhelper.Installed{}, false, classifyFarHelperRefusal(err)
	}
	if !found {
		return farhelper.Installed{}, false, nil
	}
	installed := farhelper.Installed{Dir: path.Dir(binary), Generation: hash}
	if err := i.record(target, installed); err != nil {
		return farhelper.Installed{}, false, err
	}
	return installed, true, nil
}

func (i *farHelperInstall) Install(ctx context.Context, target farhelper.Target, platform farhelper.Platform) (farhelper.Installed, error) {
	conn, err := i.over.openInstallFor(ctx, proto.OpenChannelParams{
		HostKeyFingerprint: target.HostKeyFingerprint,
		Destination:        target.Destination,
		Kind:               proto.ChannelSFTP,
		AcceptOnTrust:      false,
	})
	if err != nil {
		return farhelper.Installed{}, classifyFarHelperRefusal(err)
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
	installed := farhelper.Installed{Dir: path.Dir(command), Generation: hash}
	if err := i.record(target, installed); err != nil {
		return farhelper.Installed{}, err
	}
	return installed, nil
}

// record keeps the footprint surface's durable observation beside the remote
// success. A failed observation is an error, not an untracked success.
func (i *farHelperInstall) record(target farhelper.Target, installed farhelper.Installed) error {
	destination := target.Destination
	if err := i.installs.Record(consent.Install{
		Fingerprint: target.HostKeyFingerprint,
		Identity:    fmt.Sprintf("%s@%s:%d", destination.User, destination.Host, destination.Port),
		Path:        installed.Dir,
		Hash:        installed.Generation,
		InstalledAt: time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("record helper footprint: %w", err)
	}
	return nil
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

// farHelperConnections is the ConnectionSource farhelper reads. Its entries are
// the local helper's connection table: the `connections` answer names every
// connection the helper's supervisors hold — a tunnel, a probe lease or the far
// helper's own lane included — with the destination and the host key its link
// was verified against, and the connection-state stream keeps those entries
// current. What of that table is worth deciding about is this adapter's half;
// the helper's answer is unfiltered (see farHelperDecidable).
//
// # Why the table is the source, and what a pane's bind still adds
//
// A coordinator that restarts knows nothing, and the connections its panes
// never bound are exactly the ones a per-pane subscription could never show
// it (design §5.10, scenario 7). So the table is read from the helper (Sync)
// rather than rebuilt from what this process saw. A pane's bind still runs
// first for a connection that has not dialled: it names the destination and
// the pin the open was built with, which the helper's table cannot have until
// something acquires the connection.
//
// # Generation and Shells are the helper's, not this adapter's
//
// The helper reports which LINK an observation is about (hostconn's Facts
// carries the same generation, on the wire since protocol 30), so a decision's
// fence is the transport's own number rather than a local count of
// transitions: a reconnect under a stall and a replaced link are no longer the
// same event here either. PlainShells is the same record's count of shells
// that would end, for every connection including one no pane rides.
//
// # A helper that restarted is a new epoch
//
// The sequence and generation numbers belong to one helper PROCESS (the
// hello's instance id), and a replacement daemon starts them over. Observations
// from an instance this cache has not seen are therefore applied with a fresh
// fence, and the entries of the replaced instance are announced as ended: the
// helper that held them is gone, and a decision that kept riding its client
// would serve a dead transport.
type farHelperConnections struct {
	mu       sync.Mutex
	conns    map[string]farhelper.Connection
	pending  map[string]proto.ConnectionStateEvent
	watchers map[uint64]func(farhelper.Connection)
	next     uint64
	// instance is the helper process the cached sequences were observed from,
	// as the hello named it. Empty until the first observation.
	instance string
}

// farHelperTable is the one question this adapter asks the local helper: every
// connection it holds, and the subscription that keeps answering. The helper
// client is the implementation; the pair is an interface so the adapter is
// testable without a daemon.
type farHelperTable interface {
	Connections(ctx context.Context) ([]proto.ConnectionSummary, error)
	InstanceID() string
}

// Sync adopts the helper's whole connection table.
//
// IT IS THE TABLE and not a delta: the answer replaces what the cache holds
// for a helper this coordinator has not seen before, so a restart builds
// entries for connections no pane of this process ever bound. The instance is
// adopted BEFORE the request is made, so an observation the new helper sends
// while this read is in flight is folded into the new epoch rather than
// discarded with the old one.
//
// WHAT IT DOES NOT UNFOLD is the table's dead half: a record in `ended` or
// `failed`, and one that never had a link (generation zero), is not a
// connection the far helper can act on, and the helper keeps its supervisors
// for the life of the daemon. See farHelperDecidable.
func (f *farHelperConnections) Sync(ctx context.Context, table farHelperTable) error {
	if f == nil || table == nil {
		return nil
	}
	f.adoptInstance(table.InstanceID())
	summaries, err := table.Connections(ctx)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.acceptInstance(table.InstanceID()) {
		return nil
	}
	for _, summary := range summaries {
		f.foldLocked(summary.Connection, summary.Destination, summary.HostKeyFingerprint)
	}
	return nil
}

// adoptInstance makes instance the helper process this cache reads, and ends
// the entries of the one it replaces.
//
// THE ENTRIES END BECAUSE NOTHING THEY SAID IS ABOUT THE NEW PROCESS: its
// supervisors and their sequence numbers begin again, so a fence built on the
// old numbers would refuse every observation for ever, and a client a decision
// is still riding rides a transport that died with the helper. An entry that
// the new helper also holds comes back from Sync with a current observation,
// and a fresh decision.
//
// It is called before the table is read, not after, so an observation the new
// helper sends while the read is in flight is folded into the epoch it belongs
// to instead of being discarded with the old one.
func (f *farHelperConnections) adoptInstance(instance string) {
	if f == nil || instance == "" {
		return
	}
	f.mu.Lock()
	if f.instance == instance {
		f.mu.Unlock()
		return
	}
	first := f.instance == ""
	f.instance = instance
	if first {
		f.mu.Unlock()
		return
	}
	dropped := f.conns
	f.conns = make(map[string]farhelper.Connection)
	f.pending = make(map[string]proto.ConnectionStateEvent)
	watchers := f.snapshotLocked()
	defer f.mu.Unlock()
	for _, c := range dropped {
		c.State = hostconn.Ended
		for _, fn := range watchers {
			fn(c)
		}
	}
}

// acceptInstance reports whether an observation from instance may be applied:
// the first instance is adopted, one this cache has already replaced is
// refused. The epoch is opened explicitly — by adoptInstance, on the client the
// opener handed out — rather than by whichever frame arrives next, so a
// replaced helper's last queued event cannot take the cache back.
// The caller holds mu.
func (f *farHelperConnections) acceptInstance(instance string) bool {
	if instance == "" {
		return true
	}
	if f.instance == "" {
		f.instance = instance
		return true
	}
	return f.instance == instance
}

func newFarHelperConnections() *farHelperConnections {
	return &farHelperConnections{
		conns:    make(map[string]farhelper.Connection),
		pending:  make(map[string]proto.ConnectionStateEvent),
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

// Watch callbacks run under mu and may run on the helper reader goroutine.
// They may only enqueue O(1) work and cancel contexts, never wait or re-enter.
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
// destination and the machine, and it carries no ordered state, so an
// observation of a connection the table already named keeps that connection's
// state, generation and counts: the bind is newer information about WHERE, and
// nothing at all about WHAT the transport is doing.
func (f *farHelperConnections) Observe(c farhelper.Connection) {
	if f == nil || c.ID == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if previous, known := f.conns[c.ID]; known {
		if c.Destination.Host != "" {
			previous.Destination = c.Destination
		}
		if c.HostKeyFingerprint != "" {
			previous.HostKeyFingerprint = c.HostKeyFingerprint
		}
		c = previous
	}
	f.conns[c.ID] = c
	if pending, ok := f.pending[c.ID]; ok {
		delete(f.pending, c.ID)
		if pending.Seq > c.Seq || c.Seq == 0 {
			f.foldLocked(pending, nil, "")
			return
		}
	}
	watchers := f.snapshotLocked()
	for _, fn := range watchers {
		fn(c)
	}
}

// Apply folds one connection-state notification into the cache.
//
// A notification carries no destination, so an unknown connection is retained
// only as a pending observation until the table or a pane's bind names where
// it dials. No decision is published without that destination.
func (f *farHelperConnections) Apply(source string, ev proto.ConnectionStateEvent) {
	if f == nil || ev.ConnectionID == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.acceptInstance(source) {
		return
	}
	f.foldLocked(ev, nil, "")
}

// foldLocked merges one observation — a table entry or a notification — into the
// cache. Both arrive in the same shape because the helper answers the table
// with the event it publishes (proto.ConnectionSummary), which is what lets
// one function own the fence, the generation and the shell count.
// It also delivers callbacks under mu, preserving observation order.
// Subscribers only enqueue work; they must never wait or re-enter this source.
func (f *farHelperConnections) foldLocked(ev proto.ConnectionStateEvent, destination *proto.SSHDestination, fingerprint string) {
	c, known := f.conns[ev.ConnectionID]
	if !known {
		if destination == nil {
			// Keep the sequence fence until a table or bind supplies the
			// destination. A subscription can precede its table response.
			if previous, ok := f.pending[ev.ConnectionID]; !ok || ev.Seq > previous.Seq {
				f.pending[ev.ConnectionID] = ev
			}
			return
		}
		if pending, ok := f.pending[ev.ConnectionID]; ok {
			if pending.Seq > ev.Seq {
				ev = pending
			}
		}
		if !farHelperDecidable(ev) {
			f.pending[ev.ConnectionID] = ev
			// The helper's table is every supervisor it has ever created and is
			// never pruned, so it carries connections that ended, ones whose
			// last link failed, and ones that never dialled. None of them is
			// unfolded: a dead host is not something to probe, and a connection
			// with no link cannot be decided about at all. A later table or
			// bind supplies the destination before a newer pending observation
			// can bring such a connection back.
			return
		}
		delete(f.pending, ev.ConnectionID)
		c = farhelper.Connection{ID: ev.ConnectionID}
	}
	if ev.Seq != 0 && c.Seq != 0 && ev.Seq <= c.Seq {
		return
	}
	if destination != nil {
		c.Destination = *destination
	}
	if fingerprint != "" {
		c.HostKeyFingerprint = fingerprint
	}
	c.Seq = ev.Seq
	c.Generation = ev.Generation
	c.State = farHelperKind(ev.State)
	c.Slow = ev.Slow
	c.Shells = ev.PlainShells
	f.conns[c.ID] = c
	watchers := f.snapshotLocked()
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

// farHelperDecidable reports whether a connection the table reports is one the
// far helper may ever act on.
//
// TWO THINGS DISQUALIFY IT, and both are facts the table cannot hide by
// omission. A connection in `ended` or `failed` is a host not to probe: the
// helper keeps its supervisors for the life of the daemon, so a record of a
// link that is gone outlives it. Generation zero means no link has ever existed
// for the connection, which is what a supervisor created by a `connection` or a
// `disconnect` op looks like before anything dials — there is nothing to decide
// and nothing to write to. Anything else — opening, waiting for a person,
// connected, stalled, reconnecting — has a link to reason about.
func farHelperDecidable(ev proto.ConnectionStateEvent) bool {
	if ev.Generation == 0 {
		return false
	}
	switch farHelperKind(ev.State) {
	case hostconn.Ended, hostconn.Failed:
		return false
	}
	return true
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

// adoptFarTable reads the local helper's connection table into the far
// helper's source, once per client.
//
// ONE READ PER HELPER CONNECTION, and the client is the identity that matters:
// the answer is the daemon's table and the subscription it installs is per
// coordinator connection, so a second ask would answer the same list and add a
// second subscription. A new client — a restarted daemon, a generation that
// replaced it — is a new table and is read again.
//
// A REFUSAL IS NOT A STARTUP FAILURE, and it is not retried when the helper
// simply does not have the op: an older generation answers unknown_op, and a
// helper built without the ssh service answers unknown_service, and neither
// will grow the op on the next open. The entries a pane binds still arrive
// through Observe, which is the behaviour every helper had before this op
// existed. Any other failure — a write that lost the transport, an answer that
// did not arrive — is left for the next opener that reaches for the helper.
func (o *localHelperOpener) adoptFarTable(ctx context.Context, c *helperclient.Client) {
	if o == nil || c == nil {
		return
	}
	o.mu.Lock()
	if o.farConnections == nil || o.farTableAsked == c {
		o.mu.Unlock()
		return
	}
	o.farTableAsked = c
	conns := o.farConnections
	o.mu.Unlock()
	// BEFORE THE READ, so a bind another opener performs while this request is
	// in flight already addresses this helper's epoch: the cache ends the
	// replaced helper's entries here, and Sync folds the table into the same
	// one.
	conns.adoptInstance(c.InstanceID())
	if err := conns.Sync(ctx, c); err != nil {
		o.mu.Lock()
		if o.farTableAsked == c && !helperAnswersNoSuchOp(err) {
			o.farTableAsked = nil
		}
		o.mu.Unlock()
		if o.log != nil {
			o.log.Debug("ssh: the helper's connection table was not read", "error", err)
		}
	}
}

// helperAnswersNoSuchOp reports whether a refusal is the helper saying it does
// not have the op, which is how a generation older than this app answers one
// it never knew.
func helperAnswersNoSuchOp(err error) bool {
	var refusal *helperclient.RefusalError
	return errors.As(err, &refusal) &&
		(refusal.Code == proto.ErrCodeUnknownOp || refusal.Code == proto.ErrCodeUnknownService)
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
	if err := c.reg.CloseHelpersFor(ctx, fingerprint); err != nil {
		c.far.FinishFor(fingerprint)
		return err
	}
	return nil
}

func (c farHelperCloser) FinishHelpersFor(fingerprint string) {
	c.reg.FinishHelpersFor(fingerprint)
	c.far.FinishFor(fingerprint)
}

// paneHelperAxis adapts internal/farhelper's per-connection axis to the source
// panedriver declares. The direction is deliberate: panedriver must not import
// the party that decides the axis (design §5.10, "panedriver reads the axis
// through an interface it declares"), and the composition root is where the
// two neutral shapes meet (AD-8).
type paneHelperAxis struct {
	reg *farhelper.Registry
}

func (a paneHelperAxis) Axis(connectionID string) (panedriver.Helper, bool) {
	if a.reg == nil {
		return panedriver.Helper{}, false
	}
	status, ok := a.reg.Axis(connectionID)
	if !ok {
		return panedriver.Helper{}, false
	}
	return paneHelperOf(status), true
}

func (a paneHelperAxis) Watch(fn func(connectionID string, helper panedriver.Helper)) func() {
	if a.reg == nil {
		return func() {}
	}
	return a.reg.Watch(func(connectionID string, status farhelper.Status) {
		fn(connectionID, paneHelperOf(status))
	})
}

// Refresh is the one action the axis offers. The registry decides whether it
// applies — it redials only a helper it has installed — so this adapter adds
// no rule of its own.
func (a paneHelperAxis) Refresh(ctx context.Context, connectionID string) error {
	if a.reg == nil {
		return &farhelper.Unavailable{Reason: farhelper.ReasonConnection, Action: farhelper.ActionNone}
	}
	return a.reg.Refresh(ctx, connectionID)
}

func paneHelperOf(status farhelper.Status) panedriver.Helper {
	return panedriver.Helper{
		State:  panedriver.HelperState(status.State),
		Reason: string(status.Reason),
		Action: string(status.Action),
	}
}
