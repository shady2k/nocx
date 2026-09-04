package agenttools

import (
	"strings"

	"github.com/shady2k/nocx/internal/content"
)

// ContentScope is the authority half of a notes or snippets tool. It carries
// only ResourceContent scopes that survived grant intersection; the operation
// that performs the work is supplied separately by the assistant run seam.
// Keeping the scope here prevents an executor from receiving a raw domain
// service or inventing a second authorization rule.
type ContentScope struct {
	scopes []content.GrantScope
}

// NewContentScope builds the narrowed capability from the call resources that
// the grant permits. A root content scope intentionally contains every note
// and snippet; an item scope contains only that item.
func NewContentScope(resources []ResourceRef) *ContentScope {
	scopes := make([]content.GrantScope, 0, len(resources))
	for _, ref := range resources {
		if ref.Kind == content.ResourceContent && ref.ID != "" {
			scopes = append(scopes, content.GrantScope{Kind: ref.Kind, ID: ref.ID})
		}
	}
	return &ContentScope{scopes: scopes}
}

// Allows reports whether the exact canonical resource is inside this
// narrowed capability. It is the execution backstop for policy's decision:
// a sibling note or a snippet can never pass through a note-only scope.
func (s *ContentScope) Allows(id string) bool {
	if s == nil || id == "" {
		return false
	}
	child := content.GrantScope{Kind: content.ResourceContent, ID: id}
	for _, parent := range s.scopes {
		if parent.Contains(child) {
			return true
		}
	}
	return false
}

func narrowContent(grant content.Grant, resources []ResourceRef, _ RunContext) (Capability, error) {
	return NewContentScope(grantedResources(grant, resources)), nil
}

func contentFamilyResources(family string) ResolveResources {
	return func(_ map[string]any, _ RunContext) ([]ResourceRef, error) {
		return []ResourceRef{{Kind: content.ResourceContent, ID: family}}, nil
	}
}

func contentItemResource(arg, kind string) ResolveResources {
	return func(args map[string]any, _ RunContext) ([]ResourceRef, error) {
		id, ok := args[arg].(string)
		if !ok || id == "" {
			return nil, nil
		}
		return []ResourceRef{{Kind: content.ResourceContent, ID: kind + "/" + id}}, nil
	}
}

// skillResource resolves the skill named by the call into its content
// sub-scope. A skill is a ResourceContent sub-scope exactly as a note and a
// snippet are: the resource vocabulary is the ledger's closed set, and
// ResourceContent's hierarchy already expresses a grantable library.
func skillResource(arg string) ResolveResources {
	return func(args map[string]any, _ RunContext) ([]ResourceRef, error) {
		name, ok := args[arg].(string)
		if !ok || name == "" {
			return nil, nil
		}
		return []ResourceRef{{Kind: content.ResourceContent, ID: "skill/" + name}}, nil
	}
}

// skillInstallResources resolves the two resources one skills.install call
// names. It is TWO because the call is two acts: a read of an address the
// MODEL chose, and a write into the skill library.
//
// The destination is the address as given, exactly as fetch.url resolves it —
// one owner for "is this string a destination", and the URL parse that
// answers it lives in resourceURL.
//
// The content resource is the skill FAMILY and never one skill/<name>,
// because at the moment arguments are resolved there is no name: the name is
// carried in a document that has not been fetched yet, and a name derived
// from the address would be a name no skill ever claimed (preview.go says
// this in the same words — "a URL cannot name a skill, only a skill can").
// Authority over a skill nobody can name yet IS authority over the family, so
// that is what the call declares and what a grant must contain. The
// consequence is deliberate: a grant narrowed to one skill by name permits no
// install at all, because it cannot possibly have named the one that is
// coming.
func skillInstallResources(arg string) ResolveResources {
	destination := resourceURL(arg)
	return func(args map[string]any, runCtx RunContext) ([]ResourceRef, error) {
		refs, err := destination(args, runCtx)
		if err != nil {
			return nil, err
		}
		// The destination is FIRST because it is the singular wire
		// projection of the ask (kernel.matchedResource): the address is
		// what the person is being asked about.
		return append(refs, ResourceRef{Kind: content.ResourceContent, ID: "skill"}), nil
	}
}

// SkillWriteScope is the authority half of a skills write: the granted skill
// scopes and nothing else. The managed root is wiring held by the assistant's
// skill library, not by this capability.
type SkillWriteScope struct {
	scopes []content.GrantScope
}

// NewSkillWriteScope builds a capability from only the skill resources that
// survived grant intersection.
//
// Every ResourceContent scope is kept and content.GrantScope.Contains is left
// to decide, rather than a `skill/` prefix filter doing half the job here:
// the filter excluded the FAMILY root `skill`, which contains every
// skill/<name> and is what a run that may install an unnamed skill holds
// (skillInstallResources). Contains already refuses `note`, `note/x` and
// `snippet/x` for a skill child, so the prefix test was never the thing
// keeping them out — it was a second, narrower copy of the containment rule
// sitting in front of it.
func NewSkillWriteScope(resources []ResourceRef) *SkillWriteScope {
	scopes := make([]content.GrantScope, 0, len(resources))
	for _, ref := range resources {
		if ref.Kind == content.ResourceContent {
			scopes = append(scopes, content.GrantScope{Kind: ref.Kind, ID: ref.ID})
		}
	}
	return &SkillWriteScope{scopes: scopes}
}

// Allows reports whether this exact skill name is inside the narrowed
// capability. It accepts a name rather than a canonical resource id so an
// executor cannot accidentally broaden the check with a caller-supplied
// prefix.
func (s *SkillWriteScope) Allows(name string) bool {
	if s == nil || name == "" || strings.Contains(name, "/") {
		return false
	}
	child := content.GrantScope{Kind: content.ResourceContent, ID: "skill/" + name}
	for _, parent := range s.scopes {
		if parent.Contains(child) {
			return true
		}
	}
	return false
}

func narrowSkillsWrite(grant content.Grant, resources []ResourceRef, _ RunContext) (Capability, error) {
	return NewSkillWriteScope(grantedResources(grant, resources)), nil
}

// SkillInstallScope is the authority half of skills.install, and it holds two
// authorities because the call performs two acts: it reads an address off
// this machine and it writes a skill onto it.
//
// They are kept apart rather than collapsed into one because the person's
// policy keeps them apart. A run may be permitted to reach the network and
// forbidden to write a skill, or the reverse, and a single answer for the
// pair would silently pick one of those two settings to obey.
type SkillInstallScope struct {
	// source is the destination authority, and it is a *URLScope rather than
	// a second list of strings: "may this run fetch this address" already
	// has an owner (fetch.url's capability), and a second one would agree
	// with it until the day it did not.
	source *URLScope
	// family is the skill-library authority: the content scopes that permit
	// writing a skill whose name is not yet known.
	family []content.GrantScope
}

// NewSkillInstallScope builds the capability from the two already-narrowed
// resource sets — the destinations the read is permitted to reach, and the
// content scopes the write is permitted to touch.
func NewSkillInstallScope(sources, family []ResourceRef) *SkillInstallScope {
	urls := make([]string, 0, len(sources))
	for _, ref := range sources {
		if ref.Kind == content.ResourceDestination && ref.ID != "" {
			urls = append(urls, ref.ID)
		}
	}
	scopes := make([]content.GrantScope, 0, len(family))
	for _, ref := range family {
		if ref.Kind == content.ResourceContent && ref.ID != "" {
			scopes = append(scopes, content.GrantScope{Kind: ref.Kind, ID: ref.ID})
		}
	}
	return &SkillInstallScope{source: &URLScope{URLs: urls}, family: scopes}
}

// AllowsSource reports whether this address is one the run may fetch.
func (s *SkillInstallScope) AllowsSource(rawURL string) bool {
	if s == nil {
		return false
	}
	return s.source.Allows(rawURL)
}

// AllowsInstall reports whether the run may write a skill at all.
//
// It asks about the FAMILY and not about a name, and that is the whole of
// what can honestly be asked before the document is fetched — the name lives
// in bytes nobody has yet. A per-name check here would be a tautology
// dressed as a backstop: the resolver already demands family authority, so by
// the time this capability exists every valid name is inside it. What this
// question does catch is the case the policy gate cannot: a grant whose
// mutate-reversible row refuses, or does not cover skills, while its
// cross-boundary row (the class the decision was made on) does.
func (s *SkillInstallScope) AllowsInstall() bool {
	if s == nil {
		return false
	}
	root := content.GrantScope{Kind: content.ResourceContent, ID: "skill"}
	for _, scope := range s.family {
		if scope.Contains(root) {
			return true
		}
	}
	return false
}

// narrowSkillsInstall builds the install capability from BOTH effect rows the
// declaration names, each against the resources of its own kind.
//
// It is the one Narrow that reads per-row scopes rather than the grant's
// derived union, and the reason is in the declaration: skills.install's
// effect set is a conjunction, while the policy gate decides on the worst
// member alone. Reading the union here would inherit that: a run whose
// mutate-reversible row refuses skill writes would be handed a capability
// that installs, because the union still carries the scope some OTHER row
// granted. Narrowing per row is what makes the capability the enforcement
// ADR-0028 decision 4 says it is — the tool never holds authority the row
// that governs the act did not give.
func narrowSkillsInstall(grant content.Grant, resources []ResourceRef, _ RunContext) (Capability, error) {
	return NewSkillInstallScope(
		rowGrantedResources(grant, content.EffectCrossBoundary, resources),
		rowGrantedResources(grant, content.EffectMutateReversible, resources),
	), nil
}

// rowGrantedResources keeps the resources ONE effect row permits: nothing at
// all when that row refuses, and otherwise the resources its own scopes
// contain. It is grantedResources asked of a row instead of the union.
func rowGrantedResources(grant content.Grant, effect content.Effect, resources []ResourceRef) []ResourceRef {
	if grant.Policy.DecisionFor(effect) == content.DecisionRefuse {
		return nil
	}
	scopes := grant.Policy.RowScopes(effect)
	out := make([]ResourceRef, 0, len(resources))
	for _, ref := range resources {
		child := content.GrantScope{Kind: ref.Kind, ID: ref.ID}
		for _, scope := range scopes {
			if scope.Contains(child) {
				out = append(out, ref)
				break
			}
		}
	}
	return out
}
