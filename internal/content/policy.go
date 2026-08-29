package content

import "sync"

// Policy is the live, mutable set of History decisions the user made in
// Settings (design §5.4, brief 2026-08-01). The store consults it per
// operation; the composition root updates it from the settings registry's
// change notifier, so a Settings toggle takes effect without a restart.
//
// The budget (retention size + disk ceiling) is NOT here: it is open-time
// state (auto_vacuum is decided at creation) and lives in Config.Budget.
type Policy struct {
	mu            sync.RWMutex
	enabled       bool
	retentionDays int
	outputEnabled bool
	outputCap     int
}

// NewPolicy returns the default policy: history kept, no age limit, output
// retained, and the per-command cap at its setting's default. The cap is
// stated here as well as in settings because a store opened without a
// registry — a test, a migration tool — must still bound what one command
// can spend.
func NewPolicy() *Policy {
	return &Policy{enabled: true, outputEnabled: true, outputCap: DefaultOutputCapBytes}
}

// DefaultOutputCapBytes is what one command's body may be worth until the
// user says otherwise: 256 KiB of head and tail together, which is the
// default of settings.HistoryOutputCapKB expressed in the unit the code
// works in.
const DefaultOutputCapBytes = 256 << 10

// SetEnabled flips "keep history at all". When off, Add records nothing.
func (p *Policy) SetEnabled(v bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.enabled = v
}

// Enabled reports whether history is being recorded.
func (p *Policy) Enabled() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.enabled
}

// SetRetentionDays sets the age limit in days; 0 means unbounded by age.
func (p *Policy) SetRetentionDays(d int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.retentionDays = d
}

// RetentionDays returns the age limit in days; 0 means unbounded by age.
func (p *Policy) RetentionDays() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.retentionDays
}

// SetOutputEnabled flips whether command output is retained. It is the gate
// CaptureOutput consults: off means the command keeps its row and keeps no
// body, and the ack says so rather than failing.
func (p *Policy) SetOutputEnabled(v bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.outputEnabled = v
}

// OutputEnabled reports whether command output is retained.
func (p *Policy) OutputEnabled() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.outputEnabled
}

// SetOutputCapBytes sets how much of ONE command's output is kept. Zero or
// less means the default: a cap of nothing would be output retention off
// wearing another switch's clothes, and there is already a switch for that.
func (p *Policy) SetOutputCapBytes(v int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if v <= 0 {
		v = DefaultOutputCapBytes
	}
	p.outputCap = v
}

// OutputCapBytes reports the per-command cap. TWO surfaces apply it, and they
// cut the same way on purpose: the RENDERER caps a frozen block's body, which
// it can do on a character boundary because it holds the rows (capBody in
// capture-client.ts), and the STORE caps a session's live recording, which
// cannot wait for the end of something that has no end
// (session_output_sqlite.go). Both keep the head and the tail and drop the
// middle; both take the number from here.
//
// The store's own ceiling (MaxArtifactBytes) is a different number for a
// different question and is not this.
func (p *Policy) OutputCapBytes() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.outputCap <= 0 {
		return DefaultOutputCapBytes
	}
	return p.outputCap
}
