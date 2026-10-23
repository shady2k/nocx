package profile

import "sort"

// MCPSecretReferences returns sorted, distinct, nonempty references held by
// the server. ownedOnly restricts cleanup candidates to owned bindings.
func MCPSecretReferences(server MCPServer, ownedOnly bool) []string {
	seen := make(map[string]struct{})
	visitMCPSecretRefs(&server, func(ref string, owned bool) {
		if ref != "" && (!ownedOnly || owned) {
			seen[ref] = struct{}{}
		}
	})
	refs := make([]string, 0, len(seen))
	for ref := range seen {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs
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
			server.Revision++
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
