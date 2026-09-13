//go:build !nocx_local_ssh

package main

import "log/slog"

// holdSSHClient answers a release that releases nothing, and that is the point
// of the build: this binary links no ssh client at all, because
// nocx_local_ssh — what puts one in the helper installed on this machine — was
// not passed to the build. That is the ordinary state of every artifact
// `make helpers` produces, which are the bytes written to hosts nobody here
// controls.
//
// It is a BUILD fact rather than a configured one: there is no package to reach
// for and no client to leave open, so a caller that got a client here would be
// a daemon that could dial when it must not be able to.
func holdSSHClient(*slog.Logger) (release func(), err error) {
	return func() {}, nil
}
