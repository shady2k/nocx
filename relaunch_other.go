//go:build !linux

package main

// waitForProcessExitPidfd reports that this platform has no pidfd_open, so
// waitForProcessExit polls the pid instead.
//
// The counterpart exists because pidfd_open is in x/sys/unix's Linux half
// only, and the waiter's code has to compile where the handover never runs:
// macOS restarts through `open -n` and starts no waiter, but `go build -tags
// gtk3 ./...` compiles this file all the same.
func waitForProcessExitPidfd(int) (supported bool, err error) { return false, nil }
