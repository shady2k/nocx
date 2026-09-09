// Package nativeports reads a machine's listening TCP ports from its
// kernel — no shell commands, no tool dependencies. One build-tagged
// implementation per OS (the house pattern: internal/contentkey):
//
//   - linux: /proc/net/tcp and /proc/net/tcp6, owner via the socket inode
//     matched through /proc/*/fd. The permission-denied evidence falls out
//     of the walk itself: you can only read the fd lists of the processes
//     you own, exactly like non-root ss on the remote side, so it renders
//     identically.
//   - windows: GetExtendedTcpTable from iphlpapi.dll, bound through
//     golang.org/x/sys/windows (pure Go, no cgo), owner pid from the table,
//     name via QueryFullProcessImageName.
//   - darwin: /usr/sbin/lsof — a documented fallback, not a kernel read.
//     The native route is libproc (proc_listpids/proc_pidfdinfo), which
//     needs cgo; the decision and its evidence are in
//     .internal/reports/nocx-wzc4.8.md. lsof ships with every macOS base
//     system and its -nP -iTCP -sTCP:LISTEN dialect is stable.
//   - everything else: ErrUnsupported, the typed "not implemented on this
//     platform" that the discovery domain maps to its unavailable state —
//     never to an empty list.
//
// The module is deliberately transport-agnostic: it reads the kernel of
// whatever machine it runs on. The planned remote helper cross-compiles
// this same module to the far host and ships the results back; the local
// provider in this package is one consumer, a helper provider later is
// another, and neither forks the read.
package nativeports

import (
	"context"
	"errors"
	"sort"

	"github.com/shady2k/nocx/internal/discovery"
)

// ErrUnsupported is the typed "not implemented on this platform" fallback.
// The provider maps it to the discovery unavailable state — an unsupported
// OS degrades into a sentence the panel already knows how to render, never
// into a convincing empty list.
var ErrUnsupported = errors.New("nativeports: not implemented on this platform")

// ErrToolMissing reports that the platform's listener source is not
// installed (darwin's lsof). Terminal, like ErrUnsupported: retrying cannot
// conjure the tool, so the provider maps it to unavailable.
var ErrToolMissing = errors.New("nativeports: no listener tool available on this system")

// Listeners returns the machine's listening TCP ports, read from the
// kernel. The result is the kernel truth: no self/ppid filtering, no
// system-port suppression — the discovery provider (and its tests) decide
// policy, this function reports the table.
//
// The table is sorted by port (then address) before returning, so the
// visible set never depends on /proc or API enumeration order — the remote
// ladder's ss output is sorted the same way, and both panels show the same
// shape.
func Listeners(ctx context.Context) ([]discovery.Listener, error) {
	ls, err := listeners(ctx)
	sortListeners(ls)
	return ls, err
}

func sortListeners(ls []discovery.Listener) {
	sort.SliceStable(ls, func(i, j int) bool {
		if ls[i].Port != ls[j].Port {
			return ls[i].Port < ls[j].Port
		}
		return ls[i].Address < ls[j].Address
	})
}
