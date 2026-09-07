package skill

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
