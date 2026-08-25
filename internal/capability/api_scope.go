package capability

import (
	"context"
	"sort"
	"strings"

	"github.com/shady2k/nocx/internal/apicoll"
)

// RequestScopeResult is the renderer-facing explanation of the same ordered
// lookup chain Snapshot uses to send a request.
type RequestScopeResult struct {
	Variables []ScopeVariable
}

// ScopeVariable is one effective-scope row. Scope is deliberately a closed
// wire vocabulary: request, folder, environment or vault.
type ScopeVariable struct {
	Name       string
	Value      string
	Scope      string
	From       string
	Overridden bool
	Refused    string
}

func (s *apiCollectionService) RequestScope(
	ctx context.Context,
	h apicoll.HandleID,
	relPath, envRelPath string,
	variables []apicoll.Param,
) (RequestScopeResult, error) {
	if err := s.guard.check(); err != nil {
		return RequestScopeResult{}, err
	}
	scope, err := s.pathFor(h)
	if err != nil {
		return RequestScopeResult{}, err
	}
	req, err := s.svc.ReadRequest(h, relPath)
	if err != nil {
		return RequestScopeResult{}, err
	}
	// Replace only the request layer on this local copy. The stored request
	// remains the source for folder metadata, while the draft owns exactly the
	// rows that resolution must answer for this call.
	req.Variables = append([]apicoll.Param(nil), variables...)

	var env apicoll.Environment
	var look, secrets apicoll.Lookup
	if envRelPath != "" {
		env, err = s.svc.ReadEnvironment(h, envRelPath)
		if err != nil {
			return RequestScopeResult{}, err
		}
		look = env.Lookup()
		if s.values != nil {
			secrets = s.values.Variables(ctx, scope, env.Name)
		}
	}

	refused := ""
	refusedName := ""
	_, lookupErr := requestLookup(req, env, look, secrets)
	if lookupErr != nil {
		if name, ok := apicoll.SecretShadowedName(lookupErr); ok {
			refused = lookupErr.Error()
			refusedName = name
		} else {
			return RequestScopeResult{}, lookupErr
		}
	}

	rows := make([]ScopeVariable, 0)
	seen := make(map[string]struct{})
	refusalAttached := false
	appendRow := func(name, value, scopeName, from string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		refusalForRow := name == refusedName && !refusalAttached &&
			(scopeName == "request" || scopeName == "folder")
		if refusalForRow {
			refusalAttached = true
		}
		_, overridden := seen[name]
		seen[name] = struct{}{}
		rowRefusal := ""
		if refusalForRow {
			rowRefusal = refused
		}
		rows = append(rows, ScopeVariable{
			Name: name, Value: value, Scope: scopeName, From: from,
			Overridden: overridden, Refused: rowRefusal,
		})
	}

	for _, variable := range apicoll.RequestVariableRows(req) {
		if !variable.Enabled {
			continue
		}
		scopeName := "request"
		if variable.Inherited {
			scopeName = "folder"
		}
		appendRow(variable.Name, variable.Value, scopeName, variable.From)
	}

	if envRelPath != "" {
		names := make([]string, 0, len(env.Values))
		for name := range env.Values {
			if _, ok := env.Value(name); ok {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			value, _ := env.Value(name)
			appendRow(name, value, "environment", envRelPath)
		}

		secretNames := append([]string(nil), env.SecretVars...)
		sort.Strings(secretNames)
		for _, name := range secretNames {
			name = strings.TrimSpace(name)
			if secrets != nil {
				if _, _, err := secrets(name); err != nil {
					return RequestScopeResult{}, err
				}
			}
			// Secret values are never sent to the renderer; the declared name
			// remains visible so the Variables tab explains the full scope.
			appendRow(name, "", "vault", "")
		}
	}
	return RequestScopeResult{Variables: rows}, nil
}

// requestLookup is the one owner of the send order. Snapshot and the scope
// explanation both call it, so a new scope cannot silently drift from the
// request that actually goes out.
func requestLookup(req apicoll.Request, env apicoll.Environment, look, secrets apicoll.Lookup) (apicoll.Lookup, error) {
	own, err := apicoll.RequestLookup(req, env)
	if err != nil {
		return nil, err
	}
	return apicoll.Chain(own, look, secrets), nil
}
