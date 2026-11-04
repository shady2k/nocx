//go:build !linux

package procwatch

// waitForExitPidfd reports that this platform has no pidfd_open, so
// [WaitForExit] polls the pid instead.
//
// The counterpart exists because pidfd_open is in x/sys/unix's Linux half
// only, and this package has to compile where the waiter never runs: macOS's
// update restart goes through `open -n` and starts no waiter, and the
// package is in the OS set that every job builds.
func waitForExitPidfd(int) (supported bool, err error) { return false, nil }
