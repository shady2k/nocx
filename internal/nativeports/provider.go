package nativeports

// Provider samples the machine the app runs on and projects the result
// into portsprobe's five states: a successful read is available
// (or available-limited when every row's process evidence is degraded),
// ErrUnsupported and ErrToolMissing are unavailable (terminal, known
// degrades), and any other read failure is failed-transiently, paced by
// the scheduler's cadence.
//
// No self/ppid or system-port filtering: the kernel table is the truth.
// Orca's relay scan excludes its own listener because that scan rides the
// very connection it filters; the local panel is not riding a connection,
// and this provider's acceptance test requires a port the test itself
// opened to be visible. (Local sshd on port 22 is a real service, not
// noise, for the same reason.)
import (
	"context"
	"errors"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/portsprobe"
)

// Provider is the portsprobe.Provider implementation for LocalTargetID.
type Provider struct {
	read func(ctx context.Context) ([]portsprobe.Listener, error)
}

// NewProvider builds the local sampling provider. The logger is accepted
// for the composition-root factory signature; transient failures are
// surfaced through the sample's state and classification, which is what the
// panel renders.
func NewProvider(_ log.Logger) *Provider {
	return &Provider{read: Listeners}
}

// Sample runs one native read and maps it onto the domain's states.
func (p *Provider) Sample(ctx context.Context) portsprobe.Sample {
	listeners, err := p.read(ctx)
	switch {
	case errors.Is(err, ErrUnsupported):
		return portsprobe.Sample{
			State:          portsprobe.StateUnavailable,
			Classification: "local port discovery not supported on this platform",
		}
	case errors.Is(err, ErrToolMissing):
		return portsprobe.Sample{
			State:          portsprobe.StateUnavailable,
			Classification: err.Error(),
		}
	case err != nil:
		return portsprobe.Sample{
			State:          portsprobe.StateFailedTransiently,
			Classification: "local port discovery failed: " + err.Error(),
		}
	default:
		return portsprobe.Sample{
			State:       portsprobe.SampleState(listeners),
			Listeners:   listeners,
			Probe:       probeName,
			ProbesTried: []string{probeName},
		}
	}
}

// Retry is a no-op: the local provider has no refusal state, and a retry is
// simply another Sample.
func (p *Provider) Retry() {}

// Close is a no-op: a native read holds nothing between calls.
func (p *Provider) Close() error { return nil }
