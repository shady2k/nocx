package skill

// TEST SEAMS FOR THE USAGE DOCUMENT.
//
// They live in a _test.go file because that is where a helper nothing in the
// product calls belongs: the dead-code ratchet runs deadcode WITHOUT -test on
// purpose (.githooks/check-deadcode.mjs), so a production file carrying a
// test-only function is a new unreachable function on every platform, and the
// baseline may only shrink.

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
