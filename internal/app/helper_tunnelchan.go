package app

// The coordinator's forwards, listeners and routed requests ride channels on
// THIS MACHINE'S HELPER (nocx-50w7p.8, plan §3).
//
// # Which tenants this is
//
// Every tenant of the lease `ssh.RealClient.TunnelConn` used to hand out, at
// once, because a transport cannot be moved tenant by tenant without leaving a
// tree where two processes dial one host:
//
//   - the forward model's three strategies (internal/tunnel: -L, -R, -D), for
//     which the connector below IS the tunnel.Connector seam;
//   - the remote lifecycle channel (ADR-0024), whose loopback listener on the
//     far side is a `forward` on this helper;
//   - an API request routed through a connection (apisend), which opens a
//     direct-tcpip channel per send.
//
// What did NOT move is every decision around the dial: the destination is
// resolved and authorized HERE, by the process that reads ~/.ssh/config and
// holds the credential binding (ssh.RealClient.ResolveTarget), and the file
// code, the forward model and the sender keep their own shapes — they consume
// the same lease interface they always did.
//
// # Why the two seams in this file exist
//
// sshTunnelLeaser is the narrow surface the two tenants IN THIS PACKAGE need
// (remoteLifecycleProvider, apiRouteLeaser), declared as its own interface
// rather than reusing *ssh.RealClient so that neither can dial from this
// process by accident: the helper-backed connector is the only thing wired
// into it, and a second implementation would have to be written deliberately.
//
// helperTunnelConnector is the composition root's answer to the one thing the
// connector cannot work out for itself: which connection to this machine's
// daemon to ride. It is the SAME connection every local pane uses
// (helper_local.go's "one connection, every pane"), so a forward, a routed
// request and a pane share one helper connection — and the helper's pool then
// shares one ssh connection per destination across all three (AD-4).

import (
	"context"
	"log/slog"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/tunnelchan"
	"github.com/shady2k/nocx/internal/ssh"
)

// sshTunnelLeaser is the transport a forward, a remote listener or a routed
// request rides. Its one implementation in production is
// internal/helper/tunnelchan's Connector: channels on this machine's helper,
// resolved and authorized here.
type sshTunnelLeaser interface {
	TunnelConn(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.TunnelConn, error)
}

// helperTunnelConnector builds the connector this composition root wires into
// every tenant above.
//
// The Local closure is a second read of localHelperOpener.connect, which is
// also what sshOverHelper does for the install lease: the opener owns the
// connection, its lifetime and its generation, and this function asks it for
// the client rather than holding one — a connection cached here would be a
// second answer to "which daemon serves this machine", and the opener is the
// one that can drop it when it is lost.
func helperTunnelConnector(local *localHelperOpener, resolve *ssh.RealClient, log *slog.Logger) *tunnelchan.Connector {
	return &tunnelchan.Connector{
		Local: func(ctx context.Context) (*helperclient.Client, error) {
			client, _, err := local.connect(ctx)
			return client, err
		},
		Resolve: resolve,
		Log:     log,
	}
}

// The leases the composition root hands the two in-package tenants satisfy the
// narrow surface they need, asserted where a drift would otherwise surface as
// a compile error in one wiring line and nothing else.
var _ sshTunnelLeaser = (*tunnelchan.Connector)(nil)
