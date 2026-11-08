//go:build !linux

package procwatch

// waitForExitPidfd reports that this platform has no pidfd_open, so
// [WaitForExit] polls the pid instead.
//
// The counterpart exists because pidfd_open is in x/sys/unix's Linux half
// only. macOS runs the same waiter (relaunch.go) and takes this half: it polls
// the pid, which is the same fact at a coarser grain and has no reuse window
// for a pid this process started. The package is in the OS set that every job
// builds, so both halves must compile everywhere.
func waitForExitPidfd(int) (supported bool, err error) { return false, nil }
