//go:build !nocx_local_ssh

package main

import (
	"log/slog"

	"github.com/shady2k/nocx/internal/helper/host"
)

// holdSSHClient answers a seam that registers nothing and releases nothing, and
// that is the point of the build: this binary links no ssh client at all,
// because nocx_local_ssh — what puts one in the helper installed on this
// machine — was not passed to the build. That is the ordinary state of every
// artifact `make helpers` produces, which are the bytes written to hosts nobody
// here controls.
//
// It is a BUILD fact rather than a configured one: there is no package to reach
// for, no client to leave open and no service to register, so a helper that got
// an ssh service here would be a daemon that could dial when it must not be
// able to. What a coordinator gets instead is `unknown_service` from the
// dispatcher, which is a sentence about this build rather than a failure it has
// to guess at (cmd/nocx-helper/sshservice.go).
func holdSSHClient(*slog.Logger) (sshSeam, error) {
	return sshSeam{
		register: func(*host.Host) {},
		release:  func() {},
	}, nil
}
