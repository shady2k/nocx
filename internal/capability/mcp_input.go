package capability

import (
	"errors"
	"fmt"
	"strings"

	"github.com/shady2k/nocx/internal/profile"
)

// ValidateMCPServerConfigInput validates one-way input without repositories,
// secret material resolution, minting, or activation. Keep is syntactically
// permitted only for updates; the guarded service additionally verifies that
// the named binding actually exists. Wire presence/unknown-field checks remain
// the transport's responsibility. Profile remains the canonical configuration
// validation owner, reached through the same projection used by materialization.
func ValidateMCPServerConfigInput(in MCPServerConfigInput, allowKeep bool) error {
	_, err := projectMCPConfig(in, nil, func(input MCPSecretBindingInput, _ *profile.MCPSecretBinding) (profile.MCPSecretBinding, error) {
		if err := validateMCPSecretInput(input, allowKeep); err != nil {
			return profile.MCPSecretBinding{}, err
		}
		// Only this transient validation projection sees this marker. The
		// materializing projection always substitutes a real resolved/minted ID.
		return profile.MCPSecretBinding{SecretRef: "validation-only", Owned: input.SecretValue != nil || input.Keep}, nil
	})
	return err
}

func validateMCPSecretInput(in MCPSecretBindingInput, allowKeep bool) error {
	sources := 0
	if in.Secret != nil {
		sources++
	}
	if in.SecretValue != nil {
		sources++
	}
	if in.Keep {
		sources++
	}
	if sources != 1 {
		return errors.New("secret binding requires exactly one source")
	}
	if in.Keep && !allowKeep {
		return errors.New("keep is not allowed when creating an MCP server")
	}
	if in.Secret != nil && (len(*in.Secret) > 512 || !strings.HasPrefix(*in.Secret, "secrow:") || len(*in.Secret) == len("secrow:") || profile.HasControlChars(*in.Secret)) {
		return errors.New("secret must be a bounded selectable Vault row")
	}
	if in.SecretValue != nil && (len(*in.SecretValue) == 0 || len(*in.SecretValue) > profile.MaxMCPBindingLiteralBytes) {
		return errors.New("secret value must be nonempty and bounded")
	}
	return nil
}

type mcpBindingProjector func(MCPSecretBindingInput, *profile.MCPSecretBinding) (profile.MCPSecretBinding, error)

func projectMCPValue(in MCPValueBindingInput, old profile.MCPValueBinding, secret mcpBindingProjector) (profile.MCPValueBinding, error) {
	if in.Kind == profile.MCPBindingLiteral {
		if in.Literal == nil || in.Secret != nil || in.SecretValue != nil || in.Keep {
			return profile.MCPValueBinding{}, errors.New("literal binding requires only a literal")
		}
		return profile.MCPValueBinding{Kind: in.Kind, Literal: in.Literal}, nil
	}
	if in.Kind != profile.MCPBindingSecret || in.Literal != nil {
		return profile.MCPValueBinding{}, errors.New("invalid secret binding kind or literal")
	}
	var current *profile.MCPSecretBinding
	if old.Kind == profile.MCPBindingSecret {
		current = &profile.MCPSecretBinding{SecretRef: old.SecretRef, Owned: old.Owned}
	}
	binding, err := secret(MCPSecretBindingInput{Secret: in.Secret, SecretValue: in.SecretValue, Keep: in.Keep}, current)
	return profile.MCPValueBinding{Kind: profile.MCPBindingSecret, SecretRef: binding.SecretRef, Owned: binding.Owned}, err
}

func projectMCPConfig(in MCPServerConfigInput, current *profile.MCPServer, secret mcpBindingProjector) (profile.MCPServer, error) {
	if in.Stdio != nil && (len(in.Stdio.Argv) > profile.MaxMCPBindingRows || len(in.Stdio.Env) > profile.MaxMCPBindingRows) {
		return profile.MCPServer{}, errors.New("MCP stdio binding limit exceeded")
	}
	if in.HTTP != nil && len(in.HTTP.Headers) > profile.MaxMCPBindingRows {
		return profile.MCPServer{}, errors.New("MCP HTTP header limit exceeded")
	}
	server := profile.MCPServer{Name: in.Name, Enabled: in.Enabled, Transport: in.Transport, Limits: in.Limits, Catalog: profile.MCPCatalog{State: profile.MCPCatalogMissing, Tools: []profile.MCPTool{}}}
	if in.Stdio != nil {
		stdio := &profile.MCPStdioConfig{Command: in.Stdio.Command, Argv: append([]string{}, in.Stdio.Argv...), Cwd: in.Stdio.Cwd, Env: make([]profile.MCPEnvBinding, len(in.Stdio.Env))}
		old := map[string]profile.MCPValueBinding{}
		if current != nil && current.Stdio != nil {
			for _, row := range current.Stdio.Env {
				old[row.Name] = row.Value
			}
		}
		for i, row := range in.Stdio.Env {
			value, err := projectMCPValue(row.Value, old[row.Name], secret)
			if err != nil {
				return profile.MCPServer{}, fmt.Errorf("env[%d]: %w", i, err)
			}
			stdio.Env[i] = profile.MCPEnvBinding{Name: row.Name, Value: value}
		}
		server.Stdio = stdio
	}
	if in.HTTP != nil {
		http := &profile.MCPHTTPConfig{Endpoint: in.HTTP.Endpoint, Auth: in.HTTP.Auth, Headers: make([]profile.MCPHeaderBinding, len(in.HTTP.Headers))}
		old := map[string]profile.MCPValueBinding{}
		if current != nil && current.HTTP != nil {
			for _, row := range current.HTTP.Headers {
				old[strings.ToLower(row.Name)] = row.Value
			}
		}
		for i, row := range in.HTTP.Headers {
			value, err := projectMCPValue(row.Value, old[strings.ToLower(row.Name)], secret)
			if err != nil {
				return profile.MCPServer{}, fmt.Errorf("headers[%d]: %w", i, err)
			}
			http.Headers[i] = profile.MCPHeaderBinding{Name: row.Name, Value: value}
		}
		if in.HTTP.Bearer != nil {
			var oldBearer *profile.MCPSecretBinding
			if current != nil && current.HTTP != nil {
				oldBearer = current.HTTP.Bearer
			}
			binding, err := secret(*in.HTTP.Bearer, oldBearer)
			if err != nil {
				return profile.MCPServer{}, fmt.Errorf("bearer: %w", err)
			}
			http.Bearer = &binding
		}
		if in.HTTP.OAuth != nil {
			oauth := &profile.MCPOAuthConfig{Registration: in.HTTP.OAuth.Registration, ClientID: in.HTTP.OAuth.ClientID, Scopes: append([]string{}, in.HTTP.OAuth.Scopes...), GrantedScopes: []string{}, Status: profile.MCPOAuthMissing}
			var oldOAuth *profile.MCPOAuthConfig
			if current != nil && current.HTTP != nil {
				oldOAuth = current.HTTP.OAuth
			}
			if in.HTTP.OAuth.ClientSecret != nil {
				var oldSecret *profile.MCPSecretBinding
				if oldOAuth != nil {
					oldSecret = oldOAuth.ClientSecret
				}
				binding, err := secret(*in.HTTP.OAuth.ClientSecret, oldSecret)
				if err != nil {
					return profile.MCPServer{}, fmt.Errorf("OAuth client secret: %w", err)
				}
				oauth.ClientSecret = &binding
			}
			if oldOAuth != nil && sameMCPOAuthIdentity(current.HTTP.Endpoint, http.Endpoint, oldOAuth, oauth) {
				oauth.SessionRef, oauth.Status, oauth.Issuer = oldOAuth.SessionRef, oldOAuth.Status, oldOAuth.Issuer
				oauth.GrantedScopes = append([]string{}, oldOAuth.GrantedScopes...)
				oauth.AccessTokenExpires = oldOAuth.AccessTokenExpires
			}
			http.OAuth = oauth
		}
		server.HTTP = http
	}
	if err := profile.ValidateMCPServer(server); err != nil {
		return profile.MCPServer{}, err
	}
	return server, nil
}
