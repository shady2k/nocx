package skill

import "time"

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

// usageFor answers what the document records about one skill. A skill with no
// row reads as a zero Usage rather than an error.
func (s *Store) usageFor(name string) (Usage, error) {
	s.docMu.Lock()
	defer s.docMu.Unlock()
	d, err := s.loadDocumentLocked()
	if err != nil {
		return Usage{}, err
	}
	return d.Usage[name], nil
}

// recordUsageForTest writes one row straight through, without the accumulator.
// It exists so the document shape can be tested apart from flushing behavior.
func (s *Store) recordUsageForTest(name string, u Usage) error {
	s.docMu.Lock()
	defer s.docMu.Unlock()
	d, err := s.loadDocumentLocked()
	if err != nil {
		return err
	}
	if d.Usage == nil {
		d.Usage = make(map[string]Usage, 1)
	}
	d.Usage[name] = u
	return s.writeDocumentLocked(d)
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

// stampFirstSeen records the moment discovery first saw each name, and never
// moves one already recorded.
func (s *Store) stampFirstSeen(names []string) error {
	if s == nil || len(names) == 0 {
		return nil
	}
	s.docMu.Lock()
	defer s.docMu.Unlock()
	d, err := s.loadDocumentLocked()
	if err != nil {
		return err
	}
	changed := false
	for _, name := range names {
		row := d.Usage[name]
		if row.FirstSeenAt != "" {
			continue
		}
		row.FirstSeenAt = s.now().UTC().Format(time.RFC3339)
		if d.Usage == nil {
			d.Usage = make(map[string]Usage, len(names))
		}
		d.Usage[name] = row
		changed = true
	}
	if !changed {
		return nil
	}
	return s.writeDocumentLocked(d)
}
