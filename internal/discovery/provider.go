package discovery

// Provider — the one sampling interface the domain drives. The cadence
// (Scheduler) knows only this: produce a Sample on demand, clear a terminal
// refusal (Retry), release (Close). Two implementations sit behind it,
// chosen at the composition root (AD-8):
//   - remote — the exec-channel ladder (*Detector): an authenticated SSH
//     host probed through the exec seam. Unchanged in behaviour.
//   - local — the machine the app runs on, read from the kernel by
//     internal/nativeports, wired here through WithLocalProvider.
//
// The seam is deliberately shaped for a THIRD implementation later: the
// planned remote helper runs the same nativeports module
// cross-compiled on the far host, and a helper provider implements this same
// interface by querying that far side — dropped in beside local and
// remote-command, not a fork of either. The only part that would change is
// the scheduler's target→provider mapping (today a local-target special
// case; the helper lands as another factory wired at the composition root).
//
// The domain knows nothing about how the listeners were obtained: this
// interface is the whole boundary.
//
// LocalTargetID is the reserved target identity for "this machine", shared
// with the renderer (worker B consumes it): a local tab has no profile, so
// ports.* is keyed for it by this constant instead. Profile ids are always
// "type:custom:slug:uuid" (profile.NewProfileID), so the bare value can
// never collide with a stored profile.
import (
	"context"

	"github.com/shady2k/nocx/internal/log"
)

// LocalTargetID is the reserved ports.* target identity for the machine the
// app runs on. The renderer echoes it to ports.status/sample/pause/visible
// exactly like a profile id; the backend never treats it as a stored
// profile. Host in the status result is the machine's hostname; forwards is
// always [] — there is nothing to forward from the machine you are already
// on (exposing a local port on a remote host is -R and needs a chosen
// connection: a later bead).
const LocalTargetID = "local"

// Provider is the per-target sampling surface the cadence drives. The
// remote implementation is the exec-channel ladder (*Detector); the local
// implementation is internal/nativeports. A provider is created fresh per
// target lifecycle, exactly like a fresh Detector per connection.
type Provider interface {
	// Sample returns one discovery result. The implementation may return
	// its previous result inside a backoff window (the remote Detector
	// does); the scheduler keeps its own copy regardless.
	Sample(ctx context.Context) Sample
	// Retry clears a terminal refusal so the next Sample attempts again.
	// The local provider has no refusal state and treats Retry as a no-op.
	Retry()
	// Close releases whatever the provider holds and stops any sample still
	// in flight.
	Close() error
}

// WithLocalProvider supplies the factory that builds the local-machine
// sampling provider for LocalTargetID — one provider per target lifecycle,
// exactly like a fresh Detector per connection. Wired at the composition
// root (AD-8); when nil, the local target cannot sample and reports
// failed-transiently with a classification naming the wiring gap.
func WithLocalProvider(factory func(logger log.Logger) Provider) SchedulerOption {
	return func(s *Scheduler) { s.localProvider = factory }
}
