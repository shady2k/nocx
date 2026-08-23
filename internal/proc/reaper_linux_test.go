//go:build linux

package proc_test

import "golang.org/x/sys/unix"

// becomeOrphanReaper makes this process the one that orphaned descendants of
// its children are reparented to, in place of PID 1.
//
// It is what turns "wait for the system to collect this zombie" into "collect
// it" — see TestMain for the failure that bought it. Nothing here changes the
// code under test: proc reaps the child it started and cannot reap a
// grandchild it was never the parent of, which is correct. What changes is
// who plays init for the fixture, and the test can only assert a state it can
// reach.
func becomeOrphanReaper() error {
	return unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)
}
