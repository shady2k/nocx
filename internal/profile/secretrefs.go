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
	// EndpointCount is AI endpoints holding at least one reference
	// (ADR-0030). Endpoints that store no credential are not affected and
	// are not counted. Counted separately from ProfileCount on purpose:
	// "9 connections" and "2 endpoints" answer different questions
	// (ADR-0031).
	EndpointCount int
	// MCPServerCount is MCP server records holding at least one secret reference.
	MCPServerCount int
}

func impactOf(d *storeData) SecretReferenceImpact {
	distinct := make(map[string]struct{})
	heldByProfiles := make(map[string]struct{})
	heldByEndpoints := make(map[string]struct{})
	heldByMCPServers := make(map[string]struct{})

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
	for i := range d.Endpoints {
		ep := &d.Endpoints[i]
		holds := ep.CredentialRef != ""
		if !holds {
			for j := range ep.Headers {
				if ep.Headers[j].ValueRef != "" {
					holds = true
					break
				}
			}
		}
		if holds {
			heldByEndpoints[ep.ID] = struct{}{}
		}
		if ep.CredentialRef != "" {
			distinct[ep.CredentialRef] = struct{}{}
		}
		for j := range ep.Headers {
			if ref := ep.Headers[j].ValueRef; ref != "" {
				distinct[ref] = struct{}{}
			}
		}
	}
	for i := range d.MCPServers {
		holds := false
		visitMCPSecretRefs(&d.MCPServers[i], func(ref string, _ bool) {
			if ref != "" {
				distinct[ref] = struct{}{}
				holds = true
			}
		})
		if holds {
			heldByMCPServers[d.MCPServers[i].ID] = struct{}{}
		}
	}

	return SecretReferenceImpact{
		SecretCount:    len(distinct),
		ProfileCount:   len(heldByProfiles),
		EndpointCount:  len(heldByEndpoints),
		MCPServerCount: len(heldByMCPServers),
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

func clearAllMCPSecretRefs(server *MCPServer) bool {
	hasRefs := false
	visitMCPSecretRefs(server, func(ref string, _ bool) {
		if ref != "" {
			hasRefs = true
		}
	})
	if !hasRefs {
		return false
	}
	if server.Stdio != nil {
		kept := server.Stdio.Env[:0]
		for _, row := range server.Stdio.Env {
			if row.Value.Kind == MCPBindingSecret {
				continue
			}
			kept = append(kept, row)
		}
		server.Stdio.Env = kept
	}
	if server.HTTP != nil {
		kept := server.HTTP.Headers[:0]
		for _, row := range server.HTTP.Headers {
			if row.Value.Kind == MCPBindingSecret {
				continue
			}
			kept = append(kept, row)
		}
		server.HTTP.Headers = kept
		server.HTTP.Bearer = nil
		if server.HTTP.Auth == MCPHTTPAuthBearer {
			server.HTTP.Auth = MCPHTTPAuthNone
		}
		if oauth := server.HTTP.OAuth; oauth != nil {
			oauth.ClientSecret = nil
			oauth.SessionRef = nil
			oauth.Status = MCPOAuthMissing
			oauth.Issuer = ""
			oauth.GrantedScopes = []string{}
			oauth.AccessTokenExpires = nil
		}
	}
	markMCPUnavailable(server)
	server.Revision++
	return true
}

func markMCPUnavailable(server *MCPServer) {
	server.Enabled = false
	server.Catalog.State = MCPCatalogStale
	for i := range server.Catalog.Tools {
		server.Catalog.Tools[i].Enabled = false
	}
}

func clearMCPSecretRef(server *MCPServer, secretID string) bool {
	changed := false
	if server.Stdio != nil {
		kept := server.Stdio.Env[:0]
		for _, row := range server.Stdio.Env {
			if row.Value.Kind == MCPBindingSecret && row.Value.SecretRef == secretID {
				changed = true
				continue
			}
			kept = append(kept, row)
		}
		server.Stdio.Env = kept
	}
	if server.HTTP == nil {
		if changed {
			markMCPUnavailable(server)
		}
		return changed
	}
	kept := server.HTTP.Headers[:0]
	for _, row := range server.HTTP.Headers {
		if row.Value.Kind == MCPBindingSecret && row.Value.SecretRef == secretID {
			changed = true
			continue
		}
		kept = append(kept, row)
	}
	server.HTTP.Headers = kept
	if server.HTTP.Bearer != nil && server.HTTP.Bearer.SecretRef == secretID {
		server.HTTP.Bearer = nil
		if server.HTTP.Auth == MCPHTTPAuthBearer {
			server.HTTP.Auth = MCPHTTPAuthNone
		}
		changed = true
	}
	if oauth := server.HTTP.OAuth; oauth != nil {
		if oauth.ClientSecret != nil && oauth.ClientSecret.SecretRef == secretID {
			oauth.ClientSecret = nil
			changed = true
		}
		if oauth.SessionRef != nil && oauth.SessionRef.SecretRef == secretID {
			oauth.SessionRef = nil
			oauth.Status = MCPOAuthMissing
			oauth.Issuer = ""
			oauth.GrantedScopes = []string{}
			oauth.AccessTokenExpires = nil
			changed = true
		}
	}
	if changed {
		markMCPUnavailable(server)
		server.Revision++
	}
	return changed
}
