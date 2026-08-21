package agenttools

import "github.com/shady2k/nocx/internal/content"

// The resource kinds a tool touches are the ledger's closed set — the same
// six the grant_resources.resource_kind CHECK constraint allows
// (internal/content/sqlite.go) and content.ResourceKind names
// (internal/content/ledger.go). AD-8: the ledger owns the vocabulary; this
// package consumes it, never duplicates it. A kind added to the ledger is a
// kind this package sees the moment it compiles.
//
// What this package does own is the list of members it handles. supported
// ResourceKind's switch is the tripwire: a new kind that a declaration uses
// but nobody has handled here fails assembly, which is how "forgot to
// classify it" stops compiling in the ledger and stops assembling here.
var allResourceKinds = []content.ResourceKind{
	content.ResourceEnvironment,
	content.ResourceSession,
	content.ResourcePath,
	content.ResourceCredential,
	content.ResourceDestination,
	content.ResourceTool,
}

// supportedResourceKind reports whether k is a member this package knows how
// to classify. Exhaustive on purpose: the default is the failure a member
// added to the ledger but not here hits.
func supportedResourceKind(k content.ResourceKind) bool {
	switch k {
	case content.ResourceEnvironment, content.ResourceSession, content.ResourcePath,
		content.ResourceCredential, content.ResourceDestination, content.ResourceTool:
		return true
	default:
		return false
	}
}
