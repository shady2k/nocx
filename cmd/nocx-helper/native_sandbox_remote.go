//go:build !nocx_local_ssh

package main

import "github.com/shady2k/nocx/internal/helper/session"

func nativeSandboxEnvironment(string) session.SandboxEnvironment {
	return session.SandboxEnvironment{}
}
