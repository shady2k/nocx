//go:build unix

package endpoint

import "syscall"

// detachAttr puts the helper Ensure starts in a session of its own. Without
// it the helper stays in the process group of whatever started it — an ssh
// exec channel, or a coordinator being replaced — and a signal sent to that
// group, or the channel's teardown, would take the sessions down with it.
// Surviving its starter is the whole promise (D1).
func detachAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
