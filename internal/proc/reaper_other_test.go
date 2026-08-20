//go:build !linux

package proc_test

// becomeOrphanReaper has nothing to ask for here: PR_SET_CHILD_SUBREAPER is a
// Linux facility, and on the BSD-derived platforms this repository also builds
// for the orphans go to PID 1, which is launchd — a process whose whole
// contract is to reap them. waitGone's reap attempt then answers ECHILD and it
// falls back to observing the release, which is what it did everywhere before
// the container with a Go program for a PID 1 turned up.
func becomeOrphanReaper() error { return nil }
