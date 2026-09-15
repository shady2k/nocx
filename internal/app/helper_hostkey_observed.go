package app

import "sync"

// hostKeyObserver is the coordinator's own record of the host key its own
// verifyHostKey reverse handler last saw for a destination, keyed by the same
// storage identity (KnownHostsAddr) the dial and the ask both use
// (nocx-y6fh7 items 5 and 6's shared foundation).
//
// It exists because a helper-hosted ssh session's wire carries no
// fingerprint back to the coordinator (proto.SSHLaunchRecord has none, and
// is not owed one for this): the coordinator already sees the fingerprint,
// synchronously, in-process, the moment its own verifyHostKey answers the
// helper's ask that gates every dial (internal/app/helper_reverse.go). This
// is a coordinator-internal cache of that answer, read back by openSSH right
// after the spawn that triggered it — nothing here crosses a process
// boundary that was not already crossing it, so it is not a wire field.
//
// Last-write-wins is deliberate: two panes to the same storage identity
// observe the same key, so a race between them can only ever agree.
type hostKeyObserver struct {
	mu  sync.Mutex
	obs map[string]string // storage address -> SHA256 fingerprint
}

func newHostKeyObserver() *hostKeyObserver {
	return &hostKeyObserver{obs: make(map[string]string)}
}

// record notes the fingerprint verifyHostKey judged for storageAddr. A blank
// address or fingerprint is ignored rather than stored as a false answer for
// an unnamed destination.
func (o *hostKeyObserver) record(storageAddr, fingerprint string) {
	if o == nil || storageAddr == "" || fingerprint == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.obs[storageAddr] = fingerprint
}

// lookup answers the last fingerprint recorded for storageAddr, if any.
func (o *hostKeyObserver) lookup(storageAddr string) (string, bool) {
	if o == nil || storageAddr == "" {
		return "", false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	fp, ok := o.obs[storageAddr]
	return fp, ok
}
