package profile

import "sort"

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
	// EndpointCount is AI endpoints holding at least one reference
	// (ADR-0030). Endpoints that store no credential are not affected and
	// are not counted. Counted separately from ProfileCount on purpose:
	// "9 connections" and "2 endpoints" answer different questions
	// (ADR-0031).
	EndpointCount int
	// MCPServerCount counts servers holding any env, header or OAuth reference.
	MCPServerCount int
}

// visitSecretReferences owns enumeration of every durable reference holder.
// Group defaults contribute distinct secrets, not connection-profile counts.
func visitSecretReferences(d *storeData, visit func(ref, kind, id string)) {
	emit := func(ref, kind, id string) {
		if ref != "" {
			visit(ref, kind, id)
		}
	}
	for _, p := range d.Profiles {
		for _, ref := range []string{p.Options.PasswordSecret, p.Options.KeySecret, p.Options.KeyPassphraseSecret} {
			emit(ref, "profile", p.ID)
		}
	}
	for _, g := range d.Groups {
		if g.Defaults == nil {
			continue
		}
		o := &g.Defaults.SparseSSHOptions
		for _, ref := range []*string{o.PasswordSecret, o.KeySecret, o.KeyPassphraseSecret} {
			if ref != nil {
				emit(*ref, "group", g.ID)
			}
		}
	}
	for _, ep := range d.Endpoints {
		emit(ep.CredentialRef, "endpoint", ep.ID)
		for _, h := range ep.Headers {
			emit(h.ValueRef, "endpoint", ep.ID)
		}
	}
	for i := range d.MCPServers {
		server := &d.MCPServers[i]
		visitMCPSecretRefs(server, func(ref string, _ bool) {
			emit(ref, "mcp", server.ID)
		})
	}
}

func impactOf(d *storeData) SecretReferenceImpact {
	distinct := make(map[string]struct{})
	profiles := make(map[string]struct{})
	endpoints := make(map[string]struct{})
	servers := make(map[string]struct{})
	visitSecretReferences(d, func(ref, kind, id string) {
		distinct[ref] = struct{}{}
		switch kind {
		case "profile":
			profiles[id] = struct{}{}
		case "endpoint":
			endpoints[id] = struct{}{}
		case "mcp":
			servers[id] = struct{}{}
		}
	})
	return SecretReferenceImpact{
		SecretCount: len(distinct), ProfileCount: len(profiles),
		EndpointCount: len(endpoints), MCPServerCount: len(servers),
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
	for i := range d.Groups {
		if d.Groups[i].Defaults != nil {
			o := &d.Groups[i].Defaults.SparseSSHOptions
			o.PasswordSecret = nil
			o.KeySecret = nil
			o.KeyPassphraseSecret = nil
		}
	}
	for i := range d.Endpoints {
		ep := &d.Endpoints[i]
		ep.CredentialRef = ""
		kept := ep.Headers[:0]
		for _, h := range ep.Headers {
			if h.ValueRef != "" {
				continue
			}
			kept = append(kept, h)
		}
		ep.Headers = kept
	}
	for i := range d.MCPServers {
		clearAllMCPSecretRefs(&d.MCPServers[i])
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

	if !clearSecretRefLocked(d, secretID) {
		return nil
	}
	return s.writeLocked(d)
}

// clearSecretRefLocked removes every reference to secretID from every
// record kind in d — profile options, group defaults and endpoint
// credentials — reporting whether anything changed. The caller holds s.mu
// and owns the subsequent write.
func clearSecretRefLocked(d *storeData, secretID string) bool {
	if secretID == "" {
		return false
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
	for i := range d.Endpoints {
		ep := &d.Endpoints[i]
		if ep.CredentialRef == secretID {
			ep.CredentialRef = ""
			changed = true
		}
		kept := ep.Headers[:0]
		for _, h := range ep.Headers {
			if h.ValueRef == secretID {
				// The row goes: a header whose value was the deleted secret
				// can no longer produce a value, must not be sent, and a
				// stored row with no source could not be validated anyway.
				// Inventing a literal here would store material the user
				// never typed.
				changed = true
				continue
			}
			kept = append(kept, h)
		}
		ep.Headers = kept
	}
	for i := range d.MCPServers {
		if clearMCPSecretRef(&d.MCPServers[i], secretID) {
			changed = true
		}
	}
	return changed
}

// SecretReferenceQuery is the aggregate query needed after metadata-first
// updates. Callers hold their configuration admission through cleanup so no
// new holder can appear between this query and deletion of owned material.
type SecretReferenceQuery interface {
	UnreferencedSecretRefs(refs []string) ([]string, error)
}

// MCPSecretReferenceQuery supplies immutable mutation-barrier snapshots.
// Empty input matches no servers; reset callers enumerate servers explicitly.
type MCPSecretReferenceQuery interface {
	MCPServersReferencingSecrets(refs []string) ([]MCPServer, error)
}

func unreferencedSecretRefs(d *storeData, refs []string) []string {
	candidates := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if ref != "" {
			candidates[ref] = struct{}{}
		}
	}
	visitSecretReferences(d, func(ref, _, _ string) { delete(candidates, ref) })
	out := make([]string, 0, len(candidates))
	for ref := range candidates {
		out = append(out, ref)
	}
	sort.Strings(out)
	return out
}

func (s *JSONStore) UnreferencedSecretRefs(refs []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return nil, err
	}
	return unreferencedSecretRefs(d, refs), nil
}

func (s *JSONStore) MCPServersReferencingSecrets(refs []string) ([]MCPServer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return nil, err
	}
	wanted := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if ref != "" {
			wanted[ref] = struct{}{}
		}
	}
	affected := make(map[string]struct{})
	visitSecretReferences(d, func(ref, kind, id string) {
		if _, ok := wanted[ref]; ok && kind == "mcp" {
			affected[id] = struct{}{}
		}
	})
	out := make([]MCPServer, 0, len(affected))
	for _, server := range d.MCPServers {
		if _, ok := affected[server.ID]; ok {
			out = append(out, server)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
