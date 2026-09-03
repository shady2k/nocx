//go:build !unix

package endpoint

import "syscall"

// detachAttr has no session to leave off unix. The helper's supported hosts
// are the unix ones; this exists so the package compiles everywhere the module
// does rather than to offer a weaker guarantee that looks like the same one.
func detachAttr() *syscall.SysProcAttr { return nil }
