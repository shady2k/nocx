package main

import (
	"github.com/shady2k/nocx/internal/helper/host"
)

// The services one connection answers, registered in ONE place.
//
// It is a function rather than three statements inside the accept loop because
// "which services does this build serve" has to be answerable — by a test, and
// by whoever reads this file next. A registration that lives only inside a
// callback that binds a socket and needs an installed binary cannot be checked
// at all, so the one line that decides whether an ssh op reaches a helper or is
// refused with `unknown_service` would be the one line nothing looks at.
//
// The build's answer comes from sshSeam (sshservice.go): a helper built with
// nocx_local_ssh has a client to serve the service with, and every artifact
// `make helpers` produces does not, so on those builds the register call
// registers nothing and an ssh op reaches the dispatcher instead.
//
// `fs` is registered by every build, and that is the whole point of it being a
// parameter rather than something the accept loop constructs: reading files on
// the host the helper runs on is what Files does once it stops opening SFTP,
// and an artifact that could not do it would fall back to the connection stack
// the deployed helper must not link (C30).
func registerHelperServices(h *host.Host, git host.Service, sessions host.Service, fs host.Service, sshCap sshSeam) {
	h.Register(git)
	h.Register(sessions)
	h.Register(fs)
	sshCap.register(h)
}
