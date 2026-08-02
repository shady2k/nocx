package profile

// Secret references, in bulk.
//
// A vault reset destroys the key material for every stored secret at once, and
// after it the profiles must stop claiming to hold any — a reference to a
// secret that cannot exist is a connection telling the user a password is
// saved when nothing can produce it.
//
// This is deliberately not expressible as a loop over the CRUD methods: the
// sweep has to be one atomic write, or an interruption leaves half the store
// pointing at a vault that has gone.

// SecretReferenceImpact is what a reset costs, in the two quantities that
// mean different things to a user reading a confirmation.
//
// They are counted separately on purpose. "12 secrets" is what is destroyed,
// and "9 connections" is what behaves differently afterwards — collapsing
// them into one number would make the sentence shorter and wrong.
type SecretReferenceImpact struct {
	// SecretCount is DISTINCT secret references. A secret shared by two
	// profiles is one thing the user loses, not two.
	SecretCount int
	// ProfileCount is connection profiles holding at least one reference.
	// Profiles that store nothing — agent auth, a key read from a path —
	// are not affected and are not counted.
	ProfileCount int
}

func impactOf(d *storeData) SecretReferenceImpact {
	distinct := make(map[string]struct{})
	heldByProfiles := make(map[string]struct{})

	for i := range d.Profiles {
		o := &d.Profiles[i].Options
		holds := false
		for _, ref := range []string{o.PasswordSecret, o.KeySecret, o.KeyPassphraseSecret} {
			if ref != "" {
				distinct[ref] = struct{}{}
				holds = true
			}
		}
		if holds {
			heldByProfiles[d.Profiles[i].ID] = struct{}{}
		}
	}

	return SecretReferenceImpact{
		SecretCount:  len(distinct),
		ProfileCount: len(heldByProfiles),
	}
}

// CountSecretReferences reports what clearing every reference would cost,
// changing nothing. It is the preview a confirmation dialog is built from.
//
// It reads the profile records, not the vault: the vault is sealed when a
// reset is wanted — that is why a reset is wanted — and it holds no catalogue
// of what it stores in any case.
func (s *JSONStore) CountSecretReferences() (SecretReferenceImpact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.load()
	if err != nil {
		return SecretReferenceImpact{}, err
	}
	return impactOf(d), nil
}

// ClearAllSecretReferences removes every secret reference from every
// profile, in one write, and reports what it cleared.
//
// It clears references only. The records — profiles with their names,
// usernames and key paths — all survive: the user's connections keep working
// and simply stop believing a password is saved. Deleting the records would
// be a different and much larger destruction than the one the user agreed to.
//
// Idempotent — the reset is re-runnable after an interruption, and a second
// run reports zero rather than failing.
func (s *JSONStore) ClearAllSecretReferences() (SecretReferenceImpact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.load()
	if err != nil {
		return SecretReferenceImpact{}, err
	}

	impact := impactOf(d)
	if impact.SecretCount == 0 {
		// Nothing to clear. Return without a write so a re-run does not
		// rewrite the document for no reason.
		return impact, nil
	}

	for i := range d.Profiles {
		o := &d.Profiles[i].Options
		o.PasswordSecret = ""
		o.KeySecret = ""
		o.KeyPassphraseSecret = ""
	}

	if err := s.writeLocked(d); err != nil {
		return SecretReferenceImpact{}, err
	}
	return impact, nil
}

// ClearSecretRefs removes every reference to secretID from all profiles —
// the options' password, key and key-passphrase bindings, and the same
// bindings in group defaults — in ONE write. It is the metadata-first half
// of deleting a secret (ADR-0011 §4): nothing may keep pointing at a store
// entry that is about to be gone, and a loop of per-field setters could fail
// halfway, leaving a partial clear.
//
// Idempotent: a secret nothing references clears nothing and succeeds.
func (s *JSONStore) ClearSecretRefs(secretID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.load()
	if err != nil {
		return err
	}

	changed := false
	for i := range d.Profiles {
		o := &d.Profiles[i].Options
		if o.PasswordSecret == secretID {
			o.PasswordSecret = ""
			changed = true
		}
		if o.KeySecret == secretID {
			o.KeySecret = ""
			changed = true
		}
		if o.KeyPassphraseSecret == secretID {
			o.KeyPassphraseSecret = ""
			changed = true
		}
	}
	for i := range d.Groups {
		def := d.Groups[i].Defaults
		if def == nil {
			continue
		}
		sp := &def.SparseSSHOptions
		if sp.PasswordSecret != nil && *sp.PasswordSecret == secretID {
			sp.PasswordSecret = nil
			changed = true
		}
		if sp.KeySecret != nil && *sp.KeySecret == secretID {
			sp.KeySecret = nil
			changed = true
		}
		if sp.KeyPassphraseSecret != nil && *sp.KeyPassphraseSecret == secretID {
			sp.KeyPassphraseSecret = nil
			changed = true
		}
	}

	if !changed {
		return nil
	}
	return s.writeLocked(d)
}
