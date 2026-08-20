//go:build !unix

package mux

import (
	"errors"
	"net"
	"os"
)

// A multiplex master is an OpenSSH concept carried over a unix-domain socket
// with descriptor passing. On a platform without SCM_RIGHTS there is no way
// to open an auxiliary channel on somebody else's connection, so the adapter
// refuses rather than degrading — degrading is the second authentication this
// package exists to prevent.
var errNoDescriptorPassing = errors.New("mux: this platform cannot pass descriptors over a control socket")

func socketPair() (theirs, ours *os.File, err error) { return nil, nil, errNoDescriptorPassing }

func sendFD(_ *net.UnixConn, _ *os.File) error { return errNoDescriptorPassing }
