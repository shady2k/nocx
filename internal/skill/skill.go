// Package skill owns discovery and reading of SKILL.md libraries.
//
// A skill is a directory holding a SKILL.md: YAML frontmatter with a name
// and description, and a markdown body. The layout is the agentskills.io
// one — one level under a root, no recursion — so a skill written for
// another agent is a skill here.
//
// PROVENANCE IS THE ROOT, never a field in the file (spec §6 layer 1).
// Content cannot forge which directory it sits in; a `provenance:` key in
// frontmatter could be written by anything able to write the file, so it is
// deliberately not read.
package skill

import "io/fs"

// Provenance is where a skill came from, which is what its trust is built
// on. The set is closed and the values are the four roots.
type Provenance string

const (
	// ProvenanceAuthored is what the person wrote or placed by hand.
	ProvenanceAuthored Provenance = "authored"
	// ProvenanceBuiltin is our own bytes, shipped in the binary.
	ProvenanceBuiltin Provenance = "builtin"
	// ProvenanceManaged is what the assistant drafted and the person
	// approved. It is the ONLY root any tool writes to.
	ProvenanceManaged Provenance = "managed"
	// ProvenanceInstalled is what the person downloaded from a URL and
	// approved. Its root is searched LAST, so nothing downloaded can shadow
	// what the person wrote or what we ship.
	ProvenanceInstalled Provenance = "installed"
)

// digested answers whether a provenance's bytes are recorded when the person
// approves them and compared against that record at discovery. Managed and
// installed both are, because they share the property every branch asking
// this question actually cares about: the person did not write the bytes, and
// what they approved was one specific version of them. Authored bytes are the
// person's own and builtin bytes are ours, so neither has an approval to
// diverge from.
//
// One owner for the question, rather than the same disjunction repeated in
// statusFor, discovery, Approve and removeDiscovered — four copies that agree
// until one of them does not.
func (p Provenance) digested() bool {
	return p == ProvenanceManaged || p == ProvenanceInstalled
}

// holderPhrase names, in the person's terms, whose skill already holds a name,
// and includes the provenance verbatim so a refusal says which root to look
// in rather than only that something is in the way.
func holderPhrase(p Provenance) string {
	switch p {
	case ProvenanceAuthored:
		return "a skill you wrote (authored)"
	case ProvenanceBuiltin:
		return "a skill nocx ships (builtin)"
	case ProvenanceInstalled:
		return "a skill you installed (installed)"
	case ProvenanceManaged:
		return "a skill the assistant wrote (managed)"
	}
	return "a skill with provenance " + string(p)
}

// Status describes whether the bytes still match the person's approval.
type Status string

const (
	// StatusApproved means the discovered bytes are the approved bytes.
	StatusApproved Status = "approved"
	// StatusChanged means the bytes differ from the approved managed skill.
	StatusChanged Status = "changed"
)

func statusFor(provenance Provenance, changed bool) Status {
	if provenance.digested() && changed {
		return StatusChanged
	}
	return StatusApproved
}

// disabledNames is supplied by a Store's document owner. Discovery owns the
// filtering decision; roots only provide the persisted state to that one
// decision point.
type disabledNames func() (map[string]struct{}, error)

// approvedDigests is supplied by a Store's document owner. It contains the
// digest recorded when the person approved each managed skill.
type approvedDigests func() (map[string]string, error)

// Root is one searched location. FS is set for the builtin root, whose bytes
// live in an embed.FS; Dir is set for the on-disk roots. Exactly one of them
// is populated.
type Root struct {
	Dir        string
	FS         fs.FS
	Provenance Provenance
	disabled   disabledNames
	digests    approvedDigests
}

// Skill is one discovered skill. The body is deliberately absent: discovery
// reads frontmatter only, and the body is fetched by Read when a tool asks
// for it.
type Skill struct {
	Name        string
	Description string
	Provenance  Provenance
	BaseDir     string
	Enabled     bool
	Status      Status
}

// FilesystemRoots returns the on-disk directories among roots, preserving
// their order and omitting roots backed by an embedded filesystem.
func FilesystemRoots(roots []Root) []string {
	dirs := make([]string, 0, len(roots))
	for _, root := range roots {
		if root.Dir != "" {
			dirs = append(dirs, root.Dir)
		}
	}
	return dirs
}

// Content is the bytes returned by Read and the root that supplied them.
// Provenance travels with the bytes so callers do not perform a second lookup
// across a TOCTOU window.
type Content struct {
	Bytes      []byte
	Provenance Provenance
	Path       string
	Changed    bool
}

// The bounds. Each is a cost paid on every ask, so each is capped and the
// cut is logged rather than silently applied.
const (
	// MaxEntriesPerRoot is how many directory entries are READ per root.
	// The enumeration stops here rather than after filtering, so a root
	// with 100 000 entries costs 256 reads.
	MaxEntriesPerRoot = 256
	// MaxFrontmatterBytes bounds the head of each SKILL.md that is parsed.
	// It is DERIVED from the description cap (maxDescriptionRunes, write.go)
	// rather than chosen, because the two bounds have to agree in one
	// direction: every description the write cap admits must still be
	// PARSEABLE here, or a skill would be written, accepted, and then dropped
	// by discovery as malformed — refused for the wrong reason, and silently.
	// The factor is four because the cap counts runes and UTF-8 spends at
	// most four bytes on one; the slack covers the name line, the delimiters
	// and the escaping strconv.Quote adds. Nothing is spent by the widening:
	// the read stops at the file's end, so it costs only files that are
	// actually that large, which is exactly the case that must not be
	// truncated.
	MaxFrontmatterBytes = 4*maxDescriptionRunes + 512
	// MaxIndexed is how many skills reach the system prompt. Every
	// description is paid for in tokens on every ask.
	MaxIndexed = 64
	// MaxReadBytes bounds content returned from a skill file.
	MaxReadBytes = 64 << 10
)
