//go:build darwin

package skill

import "syscall"

// statNlink reads the hard-link count out of a Darwin stat, whose Nlink is
// uint16 — see the linux file for why the two are separate.
func statNlink(stat *syscall.Stat_t) uint64 { return uint64(stat.Nlink) }
