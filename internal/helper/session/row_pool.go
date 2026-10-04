package session

import "unsafe"

// rowBytePool is the single per-session history.helperBufferMB budget. Owners
// count simultaneous copies independently: a row held by both the FIFO and
// the resend window spends its bytes twice until one owner releases it.
// Callers serialize access with rowMu.
type rowBytePool struct {
	limit              int64
	markerReserve      int64
	droppedEndsReserve int64
	owners             map[string]int64
	total              int64
}

const (
	rowOwnerFIFO           = "wire-fifo"
	rowOwnerResend         = "resend-window"
	rowOwnerDroppedEnds    = "dropped-ends"
	rowOwnerOverflowMarker = "overflow-marker"
)

func (p *rowBytePool) configure(limit int64) {
	if p.owners == nil {
		p.owners = make(map[string]int64)
	}
	p.limit = limit
	marker := rowEmission{incomplete: true}
	p.droppedEndsReserve = int64(maxResendEnds) * int64(unsafe.Sizeof(droppedEnd{}))
	p.markerReserve = emissionBytes(marker) + p.droppedEndsReserve
}

// charge admits a second owner charge only while the ONE configured pool can
// cover it. Reserve one marker's space so exhaustion can always be stated.
func (p *rowBytePool) charge(owner string, n int64) bool {
	if n < 0 || p.limit <= 0 {
		return false
	}
	cap := p.limit - p.markerReserve
	switch owner {
	case rowOwnerOverflowMarker:
		cap = p.limit - p.droppedEndsReserve
	case rowOwnerDroppedEnds:
		cap = p.limit - (p.markerReserve - p.droppedEndsReserve)
	}
	if p.total+n > cap {
		return false
	}
	p.owners[owner] += n
	p.total += n
	return true
}

func (p *rowBytePool) release(owner string, n int64) {
	if n <= 0 {
		return
	}
	if n > p.owners[owner] {
		panic("row byte pool owner released more than it charged")
	}
	p.owners[owner] -= n
	p.total -= n
}

func (p *rowBytePool) ownerBytes(owner string) int64 { return p.owners[owner] }
