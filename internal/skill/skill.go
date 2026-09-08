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

// inertOnArrival answers whether a skill from this root is OFF the moment it
// lands, waiting for the person to turn it on. Only `installed` is: those
// bytes came from outside the boundary, and design §8 buys the right to carry
// them whole — scripts included — with a look the person takes on the page
// before the skill can act. Managed and authored are both inside the
// boundary: the assistant wrote one because the person asked and the person
// wrote the other, so making them confirm what they just did is ceremony, and
// ceremony is what teaches people to click past the one prompt that matters.
//
// It is provenance, so it is the ROOT and never a field a file could carry;
// a skill somebody drops into the installed root with `mv` arrives inert for
// the same reason one fetched from a URL does, because the question this
// answers is which directory the bytes sit in and not who put them there.
func (p Provenance) inertOnArrival() bool {
	return p == ProvenanceInstalled
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

// Status describes whether the bytes still match the ones recorded for this
// skill — the snapshot taken when it was INSTALLED, or when Approve last
// adopted the file as it then stood. That is the vocabulary everything says
// it in (nocx-hzsxl): a digest detects a difference from a recorded snapshot,
// and calling that "changed since approval" claimed the snapshot certified
// the bytes when all it ever did was admit them.
type Status string

const (
	// StatusApproved means the discovered bytes are the recorded bytes. The
	// value keeps its name on the wire because it names the state a person
	// put the skill in — they adopted these bytes — while the FACT the row
	// states is the other one, and that fact is stated in one vocabulary
	// everywhere it appears.
	StatusApproved Status = "approved"
	// StatusChanged means a byte under the skill differs from what was
	// recorded when it was installed.
	StatusChanged Status = "changed"
)

func statusFor(provenance Provenance, changed bool) Status {
	if provenance.digested() && changed {
		return StatusChanged
	}
	return StatusApproved
}

// switches is what the document remembers about the person's switches: the
// names they turned OFF among the roots that arrive on, and the names they
// turned ON among the roots that arrive off (Provenance.inertOnArrival).
//
// Two sets rather than one, because they are two different DEPARTURES from
// two different defaults, and which of them applies to a skill is settled by
// its root before either is consulted — so they cannot both speak about the
// same skill and cannot disagree. One set of "enabled" names would have had
// to be rewritten for every skill on the machine the first time anything
// changed a default, and one set of "disabled" names cannot express "this
// installed skill is on" at all.
type switches struct {
	off map[string]struct{}
	on  map[string]struct{}
}

// personSwitches is supplied by a Store's document owner. Discovery owns the
// filtering decision; roots only provide the persisted state to that one
// decision point.
type personSwitches func() (switches, error)

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
	switches   personSwitches
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
	// Enabled is the person's switch, and only the person moves it. Its
	// DEFAULT is the root's (see inertOnArrival), so an installed skill is
	// off until they turn it on and everything else is on until they turn it
	// off; the document records only the departures from that default.
	Enabled bool
	Status  Status
}

// Offered is whether this skill is put in front of the assistant: the person
// has it on AND the bytes are still the ones recorded for it.
//
// It is a method rather than a stored field because it is the conjunction of
// two facts that ARE stored, and a third value beside them is a value that
// can disagree with them — the failure this repo names most often, and the
// one AGENTS.md settles for a bead's `blocked` in the same words: computed,
// never stored. Writing `Enabled = false` on noticing a change would also
// make listing the skills a WRITE, and would race the person who has just
// turned one on.
//
// It follows that restoring a file byte-for-byte puts the skill back with no
// further act, because the digest matches again. That is right rather than
// merely convenient: the same bytes carry the same review, and asking the
// person to re-approve what they already approved teaches them the prompt
// does not mean anything.
func (s Skill) Offered() bool {
	return s.Enabled && s.Status != StatusChanged
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
