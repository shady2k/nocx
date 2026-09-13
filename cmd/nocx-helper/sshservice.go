package main

import (
	"github.com/shady2k/nocx/internal/helper/host"
)

// sshSeam is what this BUILD's ssh capability is to the daemon: how to put its
// service on one connection, and how to let it go at shutdown.
//
// It is one value in two fields rather than two returned values for the reason
// the pair travels together: a build either has an ssh service or it does not,
// and a caller that got a registration without a release would leak the client
// it was built over, while one that got a release without a registration would
// have nothing to register and no way to notice.
//
// A build's answer is decided by nocx_local_ssh — see sshclient_local.go and
// sshclient_absent.go — and the difference is not a detail of this file but the
// whole of plan §1: the artifact written to a host nobody here controls links
// no ssh client, so its seam registers nothing and every ssh op is answered
// `unknown_service`.
type sshSeam struct {
	// register puts this build's ssh service on one connection's host. It is
	// called once per accepted connection, as sessions.Bind is, because a
	// helper daemon serves several coordinators at once and the service is
	// registered on the protocol engine of one connection, not on the daemon.
	//
	// A build with no ssh service registers nothing and that is the answer,
	// not a no-op standing in for one.
	register func(*host.Host)
	// release lets go of what register serves: the ssh client, when this build
	// has one. Called once, at shutdown, whether or not any connection ever
	// arrived.
	release func()
}
