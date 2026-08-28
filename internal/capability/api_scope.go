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
// wire vocabulary: request, folder or environment. Secret is separate from
// Scope because a secret is a value any layer can hold, not another layer
// under the chain; Scope continues to answer WHERE while Secret answers WHAT
// KIND OF ANSWER. Keeping them separate lets the Variables tab show which
// level won when two rows have the same name.
type ScopeVariable struct {
	Name       string
	Value      string
	Scope      string
	From       string
	Overridden bool
	Refused    string
	Secret     bool
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
	if _, err := s.pathFor(h); err != nil {
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
	var look apicoll.Lookup
	if envRelPath != "" {
		env, err = s.svc.ReadEnvironment(h, envRelPath)
		if err != nil {
			return RequestScopeResult{}, err
		}
		look = env.Lookup()
	}
	// Keep this call even though the scope projection does not need the
	// resolved values: it proves the displayed chain has the same precedence
	// and shadowing rules as Snapshot. Secret references are ordinary text and
	// no longer form a separate lookup layer.
	if _, err := requestLookup(req, env, look); err != nil {
		return RequestScopeResult{}, err
	}
	_ = ctx

	rows := make([]ScopeVariable, 0)
	seen := make(map[string]struct{})
	appendRow := func(name, value, scopeName, from string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		_, overridden := seen[name]
		seen[name] = struct{}{}
		secret := resolveLineRefRE.MatchString(value)
		if secret {
			value = ""
		}
		rows = append(rows, ScopeVariable{
			Name: name, Value: value, Scope: scopeName, From: from,
			Overridden: overridden, Secret: secret,
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
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			value, _ := env.Value(name)
			appendRow(name, value, "environment", envRelPath)
		}
	}
	return RequestScopeResult{Variables: rows}, nil
}

// requestLookup is the one owner of the send order. Snapshot and the scope
// explanation both call it, so a new scope cannot silently drift from the
// request that actually goes out.
func requestLookup(req apicoll.Request, env apicoll.Environment, look apicoll.Lookup) (apicoll.Lookup, error) {
	own, err := apicoll.RequestLookup(req, env)
	if err != nil {
		return nil, err
	}
	return apicoll.Chain(own, look), nil
}
