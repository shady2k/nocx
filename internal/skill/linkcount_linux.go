//go:build linux

package skill

import "syscall"

// statNlink reads the hard-link count out of a Linux stat, where Nlink is
// already the uint64 linkCount returns. Darwin's is uint16 and needs a
// conversion, which is why this is per-OS rather than one expression: a
// conversion written for both platforms is dead weight on this one, and
// unconvert says so.
func statNlink(stat *syscall.Stat_t) uint64 { return stat.Nlink }
