package skill

import (
	"fmt"
	"time"
)

// Usage is what nocx knows about how a skill has been reached for. It lives
// in skills.json beside digests and sources rather than in the skill's own
// frontmatter, because frontmatter contributes to the digest used to detect
// changed skills.
type Usage struct {
	// Count is successful skills.read calls. Inspection is not use.
	Count int `json:"count"`
	// LastUsedAt is RFC3339, empty when the skill has never been read.
	LastUsedAt string `json:"lastUsedAt,omitempty"`
	// FirstSeenAt is when discovery first saw the skill.
	FirstSeenAt string `json:"firstSeenAt,omitempty"`
}

// AutoOff records that nocx switched a skill off for going unused.
type AutoOff struct {
	At          string `json:"at"`
	SilentSince string `json:"silentSince"`
	Days        int    `json:"days"`
}

type PinKind string

const (
	PinKeepEnabled   PinKind = "keepEnabled"
	PinKeepUnchanged PinKind = "keepUnchanged"
)

type Pins struct {
	KeepEnabled   bool `json:"keepEnabled,omitempty"`
	KeepUnchanged bool `json:"keepUnchanged,omitempty"`
}

type pendingUse struct {
	count int
	last  time.Time
}

// RecordUse notes that a skill was read. It returns nothing so a usage write
// can never make the successful read fail.
func (s *Store) RecordUse(name string) {
	if s == nil || name == "" {
		return
	}
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	if s.pendingUsage == nil {
		s.pendingUsage = make(map[string]pendingUse, 1)
	}
	held := s.pendingUsage[name]
	held.count++
	held.last = s.now().UTC()
	s.pendingUsage[name] = held
}

// FlushUsage folds what has accumulated into the document. A failed write
// restores pending counts so the next flush can retry them.
func (s *Store) FlushUsage() error {
	if s == nil {
		return nil
	}
	s.usageMu.Lock()
	pending := s.pendingUsage
	s.pendingUsage = nil
	s.usageMu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	s.docMu.Lock()
	defer s.docMu.Unlock()
	d, err := s.loadDocumentLocked()
	if err != nil {
		s.restorePending(pending)
		return err
	}
	if d.Usage == nil {
		d.Usage = make(map[string]Usage, len(pending))
	}
	for name, held := range pending {
		row := d.Usage[name]
		row.Count += held.count
		row.LastUsedAt = held.last.Format(time.RFC3339)
		d.Usage[name] = row
	}
	if writeErr := s.writeDocumentLocked(d); writeErr != nil {
		s.restorePending(pending)
		return writeErr
	}
	return nil
}

func (s *Store) restorePending(pending map[string]pendingUse) {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	if s.pendingUsage == nil {
		s.pendingUsage = pending
		return
	}
	for name, held := range pending {
		merged := s.pendingUsage[name]
		merged.count += held.count
		if held.last.After(merged.last) {
			merged.last = held.last
		}
		s.pendingUsage[name] = merged
	}
}

// stampFirstSeen records the moment discovery first saw each skill, and never
// moves one already recorded.
//
// It takes what discovery found rather than a list of names because a BUILTIN
// IS NOT STAMPED: the stamp answers "silent since when" and nothing else, and
// the only reader of that is the idle sweep, which exempts builtins by
// provenance (applyAutoOff, on the same slice). A row written for a builtin is
// therefore dead data that puts one of our own shipped names into the person's
// settings document — which a backup carries, and a backup is supposed to
// carry no builtin at all (internal/backup's round-trip test is where it
// surfaced).
func (s *Store) stampFirstSeen(found []discovered) error {
	if s == nil || len(found) == 0 {
		return nil
	}
	s.docMu.Lock()
	defer s.docMu.Unlock()
	d, err := s.loadDocumentLocked()
	if err != nil {
		return err
	}
	changed := false
	for _, candidate := range found {
		if candidate.Provenance == ProvenanceBuiltin {
			continue
		}
		row := d.Usage[candidate.Name]
		if row.FirstSeenAt != "" {
			continue
		}
		row.FirstSeenAt = s.now().UTC().Format(time.RFC3339)
		if d.Usage == nil {
			d.Usage = make(map[string]Usage, len(found))
		}
		d.Usage[candidate.Name] = row
		changed = true
	}
	if !changed {
		return nil
	}
	return s.writeDocumentLocked(d)
}

func silentSince(u Usage) string {
	if u.LastUsedAt != "" {
		return u.LastUsedAt
	}
	return u.FirstSeenAt
}

func idleBeyond(u Usage, days int, now time.Time) (string, bool) {
	if days <= 0 {
		return "", false
	}
	since := silentSince(u)
	if since == "" {
		return "", false
	}
	at, err := time.Parse(time.RFC3339, since)
	if err != nil || now.Sub(at) <= time.Duration(days)*24*time.Hour {
		return "", false
	}
	return since, true
}

func (s *Store) applyAutoOff(found []discovered) (map[string]AutoOff, error) {
	days := 0
	if s.idleDays != nil {
		days = s.idleDays()
	}
	s.docMu.Lock()
	defer s.docMu.Unlock()
	d, err := s.loadDocumentLocked()
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	changed := false
	for _, candidate := range found {
		if _, already := d.AutoOff[candidate.Name]; already {
			continue
		}
		if candidate.Provenance == ProvenanceBuiltin {
			continue
		}
		if d.Pins[candidate.Name].KeepEnabled {
			continue
		}
		since, idle := idleBeyond(d.Usage[candidate.Name], days, now)
		if !idle {
			continue
		}
		if d.AutoOff == nil {
			d.AutoOff = make(map[string]AutoOff, 1)
		}
		d.AutoOff[candidate.Name] = AutoOff{At: now.Format(time.RFC3339), SilentSince: since, Days: days}
		changed = true
	}
	if changed {
		if err := s.writeDocumentLocked(d); err != nil {
			return nil, err
		}
	}
	return d.AutoOff, nil
}

func (s *Store) usageAndAutoOff() (map[string]Usage, map[string]AutoOff, map[string]Pins, error) {
	s.docMu.Lock()
	defer s.docMu.Unlock()
	d, err := s.loadDocumentLocked()
	if err != nil {
		return nil, nil, nil, err
	}
	return d.Usage, d.AutoOff, d.Pins, nil
}

func (s *Store) SetPin(name string, pin PinKind, on bool) error {
	name, err := normalizeName(name)
	if err != nil {
		return err
	}
	if pin != PinKeepEnabled && pin != PinKeepUnchanged {
		return fmt.Errorf("skill %q: unknown pin %q", name, pin)
	}
	s.docMu.Lock()
	defer s.docMu.Unlock()
	d, err := s.loadDocumentLocked()
	if err != nil {
		return err
	}
	row := d.Pins[name]
	switch pin {
	case PinKeepEnabled:
		row.KeepEnabled = on
	case PinKeepUnchanged:
		row.KeepUnchanged = on
	}
	if row.KeepEnabled || row.KeepUnchanged {
		if d.Pins == nil {
			d.Pins = make(map[string]Pins, 1)
		}
		d.Pins[name] = row
	} else {
		delete(d.Pins, name)
	}
	return s.writeDocumentLocked(d)
}

func (s *Store) pinned(name string) (Pins, error) {
	s.docMu.Lock()
	defer s.docMu.Unlock()
	d, err := s.loadDocumentLocked()
	if err != nil {
		return Pins{}, err
	}
	return d.Pins[name], nil
}
