package content

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"unicode"

	"golang.org/x/net/idna"
)

// ValidateGrantScope validates both the resource kind and its canonical id.
// It is the shared boundary for policy parsing and capability narrowing: a
// scope that cannot be represented canonically cannot be enforced safely.
func ValidateGrantScope(scope GrantScope) error {
	if !validResourceKind(scope.Kind) {
		return fmt.Errorf("resource scope: kind %q is not a resource kind", scope.Kind)
	}
	if scope.ID == "" {
		return fmt.Errorf("resource scope: %s has an empty id", scope.Kind)
	}
	// The marker is a destination's question and nothing else's. A path or a
	// workspace carrying it would be a scope whose stored form claims
	// something no predicate reads — silently wider on the page than on the
	// wire.
	if scope.IncludeSubdomains && scope.Kind != ResourceDestination {
		return fmt.Errorf("resource scope: %s has no subdomains to include", scope.Kind)
	}

	switch scope.Kind {
	case ResourcePath:
		if !isAbsolutePath(scope.ID) {
			return fmt.Errorf("resource scope: path %q is not absolute", scope.ID)
		}
	case ResourceContent:
		if err := validateContentID(scope.ID); err != nil {
			return fmt.Errorf("resource scope: content id %q: %w", scope.ID, err)
		}
	case ResourceWorkspace:
		if err := validateWorkspaceID(scope.ID); err != nil {
			return fmt.Errorf("resource scope: workspace id %q: %w", scope.ID, err)
		}
	case ResourceDestination:
		if scope.ID == star {
			if scope.IncludeSubdomains {
				return errors.New("resource scope: * already covers every address; it cannot also include subdomains")
			}
			return nil
		}
		endpoint, err := parseDestinationEndpoint(scope.ID)
		if err != nil {
			return fmt.Errorf("resource scope: destination %q: %w", scope.ID, err)
		}
		if scope.IncludeSubdomains && endpoint.isIP {
			return fmt.Errorf("resource scope: %q is an address, and an address has no subdomains", scope.ID)
		}
	}
	return nil
}

// Contains reports whether the child resource is inside the parent scope.
// Different ResourceKinds never contain one another. Content and workspace
// use their canonical sub-scope hierarchy; existing singleton resources use
// exact identity, while paths retain lexical, segment-aware containment.
//
// This is deliberately a policy-time predicate over recorded scope IDs. For
// ResourcePath it performs no provider canonicalization and is NEVER a
// filesystem authorization check: callers authorizing a filesystem read must
// use the provider-backed capability in internal/filesystem/scoped.go,
// specifically its contained check, which defeats symlink escapes.
func (scope GrantScope) Contains(child GrantScope) bool {
	if scope.Kind != child.Kind {
		return false
	}
	// A destination is the one kind whose SCOPE and whose RESOURCE are
	// written in different vocabularies: the scope is an endpoint and the
	// resource is a whole URL with a path on it. So the child is parsed as a
	// URL rather than validated as a scope, which ValidateGrantScope would
	// refuse for carrying that path.
	if scope.Kind == ResourceDestination {
		return destinationContains(scope, child)
	}
	if ValidateGrantScope(scope) != nil || ValidateGrantScope(child) != nil {
		return false
	}

	switch scope.Kind {
	case ResourcePath:
		return pathScopeContains(scope.ID, child.ID)
	case ResourceContent:
		parent, _ := parseContentID(scope.ID)
		candidate, _ := parseContentID(child.ID)
		if parent.kind == "content" {
			return true
		}
		if parent.kind != candidate.kind {
			return false
		}
		// Family roots revise the deliberate former hierarchy, which had no
		// "every note" scope. Positive note+snippet roots are required to
		// express a skills-off grant without the universal content root.
		return parent.id == "" || parent.id == candidate.id
	case ResourceWorkspace:
		parent, _ := parseWorkspaceID(scope.ID)
		candidate, _ := parseWorkspaceID(child.ID)
		if parent.workspace != candidate.workspace || parent.depth > candidate.depth {
			return false
		}
		if parent.depth == 1 {
			return true
		}
		if parent.tab != candidate.tab {
			return false
		}
		if parent.depth == 2 {
			return true
		}
		return parent.pane == candidate.pane
	default:
		return scope.ID == child.ID
	}
}

type contentResourceID struct {
	kind string
	id   string
}

func parseContentID(id string) (contentResourceID, bool) {
	switch id {
	case "content":
		return contentResourceID{kind: "content"}, true
	case "note", "snippet", "skill":
		return contentResourceID{kind: id}, true
	}
	parts := strings.Split(id, "/")
	if len(parts) != 2 || (parts[0] != "note" && parts[0] != "snippet" && parts[0] != "skill") || !validResourceAtom(parts[1]) {
		return contentResourceID{}, false
	}
	return contentResourceID{kind: parts[0], id: parts[1]}, true
}

func validateContentID(id string) error {
	if _, ok := parseContentID(id); !ok {
		return fmt.Errorf("want content, note, snippet, skill, note/<id>, snippet/<id> or skill/<name>")
	}
	return nil
}

type workspaceResourceID struct {
	workspace string
	tab       string
	pane      string
	depth     int
}

func parseWorkspaceID(id string) (workspaceResourceID, bool) {
	parts := strings.Split(id, "/")
	if len(parts) != 2 && len(parts) != 4 && len(parts) != 6 {
		return workspaceResourceID{}, false
	}
	if parts[0] != "workspace" || !validResourceAtom(parts[1]) {
		return workspaceResourceID{}, false
	}
	out := workspaceResourceID{workspace: parts[1], depth: len(parts) / 2}
	if len(parts) == 2 {
		return out, true
	}
	if parts[2] != "tab" || !validResourceAtom(parts[3]) {
		return workspaceResourceID{}, false
	}
	out.tab = parts[3]
	if len(parts) == 4 {
		return out, true
	}
	if parts[4] != "pane" || !validResourceAtom(parts[5]) {
		return workspaceResourceID{}, false
	}
	out.pane = parts[5]
	return out, true
}

func validateWorkspaceID(id string) error {
	if _, ok := parseWorkspaceID(id); !ok {
		return fmt.Errorf("want workspace/<id>, workspace/<id>/tab/<id> or workspace/<id>/tab/<id>/pane/<id>")
	}
	return nil
}

func validResourceAtom(s string) bool {
	if s == "" || s == "." || s == ".." || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == '/' {
			return false
		}
	}
	return true
}

// pathScopeContains is intentionally the policy-time lexical check, not the
// execution-time filesystem check. It has no provider or context, so it
// cannot canonicalize symlinks. The enforcement check is
// internal/filesystem/scoped.go's provider-backed contained function; this
// predicate MUST NOT authorize a filesystem read.
func pathScopeContains(parent, child string) bool {
	if parent == "/" {
		return strings.HasPrefix(child, "/")
	}
	if child == parent {
		return true
	}
	for _, sep := range pathScopeSeparators() {
		if strings.HasPrefix(child, parent+string(sep)) {
			return true
		}
	}
	return false
}

func pathScopeSeparators() []byte {
	if os.PathSeparator == '/' {
		return []byte{'/'}
	}
	return []byte{byte(os.PathSeparator), '/'}
}

// ── the destination endpoint (design §5.4; bead nocx-67byy) ──────────────

// star is the universal destination scope: every address, and the only
// destination id that is not an endpoint. It stays a separate value rather
// than a wildcard host because "everything" and "everything under this name"
// are different grants and the page has to be able to tell them apart.
const star = "*"

// destinationEndpoint is a destination scope parsed: the four things that
// decide whether two addresses are the same place. There is no path here on
// purpose — a grant is over a place on the network, and a path is a document
// at that place.
type destinationEndpoint struct {
	scheme string // "http" or "https", lower case
	host   string // ASCII, lower case, no trailing dot, no brackets
	port   string // the EFFECTIVE port: the scheme's default when omitted
	isIP   bool
}

// parseDestinationEndpoint reads a destination SCOPE id. It is the strict
// end: a scope may not carry a path, a query, a fragment or userinfo,
// because none of those narrow a place and userinfo is a credential wearing
// an address.
func parseDestinationEndpoint(id string) (destinationEndpoint, error) {
	u, err := url.Parse(id)
	if err != nil {
		return destinationEndpoint{}, fmt.Errorf("not a URL: %w", err)
	}
	if u.Path != "" && u.Path != "/" {
		return destinationEndpoint{}, errors.New("an endpoint names a place, not a document: drop the path")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return destinationEndpoint{}, errors.New("an endpoint carries no query and no fragment")
	}
	return destinationEndpointOf(u)
}

// parseDestinationURL reads a RESOURCE id — the whole URL a call resolved to.
// A path, a query and a fragment are ordinary here; userinfo is refused on
// both ends, so a credential in an address can never be the thing that makes
// a grant match.
func parseDestinationURL(raw string) (destinationEndpoint, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return destinationEndpoint{}, fmt.Errorf("not a URL: %w", err)
	}
	return destinationEndpointOf(u)
}

func destinationEndpointOf(u *url.URL) (destinationEndpoint, error) {
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return destinationEndpoint{}, fmt.Errorf("scheme %q is neither http nor https", u.Scheme)
	}
	if u.User != nil {
		return destinationEndpoint{}, errors.New("an address carrying userinfo names a credential, not a place")
	}
	host := strings.TrimSuffix(u.Hostname(), ".")
	if host == "" {
		return destinationEndpoint{}, errors.New("no host")
	}
	port := u.Port()
	if port == "" {
		port = defaultPortFor(scheme)
	}
	// An address is normalized as an address: net.IP.String collapses the
	// many spellings of one IPv6 host, which idna would refuse outright.
	if ip := net.ParseIP(host); ip != nil {
		return destinationEndpoint{scheme: scheme, host: ip.String(), port: port, isIP: true}, nil
	}
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil {
		return destinationEndpoint{}, fmt.Errorf("host %q: %w", host, err)
	}
	return destinationEndpoint{scheme: scheme, host: strings.ToLower(ascii), port: port}, nil
}

func defaultPortFor(scheme string) string {
	if scheme == "http" {
		return "80"
	}
	return "443"
}

// destinationContains is THE containment predicate for network grants, and
// the only one: content.GrantScope.Contains and agenttools.URLScope.Allows
// both reach it, so what the settings page shows and what the dialler
// enforces cannot drift apart (AGENTS.md, one owner per behaviour).
//
// Matching is LABEL-WISE and never a string suffix. "notgithub.com" ends in
// "github.com" and "github.com.evil.example" contains it; neither is inside
// a grant over github.com, and the leading dot on the suffix is the whole of
// why.
func destinationContains(scope, child GrantScope) bool {
	if ValidateGrantScope(scope) != nil {
		return false
	}
	if scope.ID == star {
		return true
	}
	if child.ID == star {
		// The child claims every address; one endpoint does not hold it.
		return false
	}
	// A child that itself claims subdomains is wider than its bare host, so
	// only a parent that also claims them can hold it.
	if child.IncludeSubdomains && !scope.IncludeSubdomains {
		return false
	}
	parent, err := parseDestinationEndpoint(scope.ID)
	if err != nil {
		return false
	}
	candidate, err := parseDestinationURL(child.ID)
	if err != nil {
		return false
	}
	if parent.scheme != candidate.scheme || parent.port != candidate.port {
		return false
	}
	if parent.host == candidate.host {
		return true
	}
	return scope.IncludeSubdomains && strings.HasSuffix(candidate.host, "."+parent.host)
}
