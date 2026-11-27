package discovery

import (
	"context"
	"errors"
	"fmt"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/operation/catalog"
	"github.com/shady2k/nocx/internal/portsprobe"
)

// The lease belongs to the registry. Tie the call to its serving lifetime
// and return that lifetime so the scheduler can also check it when publishing.
func sampleFarHelper(ctx context.Context, reg Connector, id string) (portsprobe.Sample, context.Context) {
	scope := catalog.BeginInternal(ctx, catalog.OpInternalDiscoverySample)
	defer scope.Complete()
	ctx, cancel := context.WithDeadline(ctx, scope.Deadline())
	defer cancel()

	if axis, known := reg.Axis(id); !known || axis.State != farhelper.StateServing {
		return portsprobe.Sample{Canceled: true}, nil
	}
	lease, err := reg.Acquire(ctx, id)
	if err != nil {
		return remoteSampleError(err), nil
	}
	if lease.Ctx.Err() != nil {
		return portsprobe.Sample{Canceled: true}, lease.Ctx
	}
	stop := context.AfterFunc(lease.Ctx, cancel)
	defer stop()

	var result proto.PortsSampleResult
	if err := lease.Client().Call(ctx, proto.ServicePorts, proto.OpPortsSample, proto.PortsSampleParams{}, &result); err != nil {
		return remoteSampleError(err), lease.Ctx
	}
	return portsSample(result), lease.Ctx
}

func remoteSampleError(err error) portsprobe.Sample {
	var unavailable *farhelper.Unavailable
	switch {
	case errors.As(err, &unavailable), errors.Is(err, client.ErrLost), errors.Is(err, context.Canceled):
		return portsprobe.Sample{Canceled: true}
	case errors.Is(err, context.DeadlineExceeded):
		return portsprobe.Sample{State: portsprobe.StateFailedTransiently, Classification: "probe timed out"}
	default:
		return portsprobe.Sample{State: portsprobe.StateFailedTransiently, Classification: fmt.Sprint(err)}
	}
}

func portsSample(result proto.PortsSampleResult) portsprobe.Sample {
	sample := portsprobe.Sample{
		State:          portsprobe.State(result.State),
		Probe:          result.Probe,
		ProbesTried:    result.ProbesTried,
		Classification: result.Classification,
		Stderr:         result.Stderr,
		Listeners:      make([]portsprobe.Listener, 0, len(result.Listeners)),
	}
	for _, listener := range result.Listeners {
		sample.Listeners = append(sample.Listeners, portsprobe.Listener{
			Family:  portsprobe.AddressFamily(listener.Family),
			Address: listener.Address,
			Port:    listener.Port,
			Process: portsprobe.Process{
				Evidence: portsprobe.ProcessEvidence(listener.Process.Evidence),
				Name:     listener.Process.Name,
				PID:      listener.Process.PID,
			},
		})
	}
	return sample
}
