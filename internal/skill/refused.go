package skill

import "fmt"

// A SKILL-SHAPED DIRECTORY THAT DISCOVERY REFUSED IS A THIRD THING A ROW CAN
// BE (nocx-j0lei). It is not a skill — it is never offered to the assistant,
// it cannot be switched on, and Discover does not return it — and it is not
// absent either, because a person who put a SKILL.md on disk and cannot find
// it in the product has nothing to read but a log line they will never see.
//
// AGENTS.md: "A soft degrade must be visible in the product, not only in a
// log. A silent degrade the UI contradicts is how a feature that does not
// exist survives a release." The Settings list was contradicting one: it
// showed every skill nocx had, and a directory it had refused looked exactly
// like a directory nobody had created.
//
// WHAT COUNTS AS SKILL-SHAPED, and therefore what earns a row here: a
// directory in a root that HAS a SKILL.md. A directory without one is not a
// refusal, it is a directory — the roots hold ordinary folders and reporting
// each of them as a broken skill would be noise that teaches people to ignore
// the list. The same for an entry that is not a directory at all.

// RefusalReason is the closed vocabulary a surface keys on. It is closed for
// the reason every other vocabulary on this wire is: an unrecognised value
// must be a failure rather than a row rendered with a blank explanation.
type RefusalReason string

const (
	// RefusedSymlink — the directory, or its SKILL.md, is a symlink. Skills
	// are read by path and a link is how a root reaches outside itself.
	RefusedSymlink RefusalReason = "symlink"
	// RefusedUnreadable — the SKILL.md is there and could not be read.
	RefusedUnreadable RefusalReason = "unreadable"
	// RefusedFrontmatter — no complete YAML frontmatter block.
	RefusedFrontmatter RefusalReason = "frontmatter"
	// RefusedName — the frontmatter names something that is not a usable name.
	RefusedName RefusalReason = "name"
	// RefusedNoDescription — no description, and a skill without one is never
	// offered to the assistant, so there would be nothing to offer.
	RefusedNoDescription RefusalReason = "noDescription"
	// RefusedDescriptionTooLong — over the cap. Refused rather than clamped
	// (discover.go says why at length), which is exactly why it needs a row:
	// the remedy is in the person's own file and they have to be told.
	RefusedDescriptionTooLong RefusalReason = "descriptionTooLong"
)

// Refusal is one skill-shaped directory nocx would not index, in the words a
// person can act on.
type Refusal struct {
	// Directory is the folder's name in its root — what the person sees in a
	// file manager. It is NOT the frontmatter's name: half these refusals are
	// refusals of that name, and the rest may have none to report.
	Directory string `json:"directory"`
	// Provenance is the root it was found in, so the row can sit with the
	// skills from the same place and a person knows where to go.
	Provenance Provenance `json:"provenance"`
	// Path is the SKILL.md that was refused — the file to open and fix.
	Path string `json:"path"`
	// Reason is the closed vocabulary above.
	Reason RefusalReason `json:"reason"`
	// Detail is the sentence a person reads, with the number in it where
	// there is one. nocx writes it, so it is the product's words rather than
	// an error string from a library: a refusal is not a stack trace.
	Detail string `json:"detail"`
}

func refusedSymlink(what string) string {
	return "this " + what + " is a symbolic link, and a skill is read by path — replace the link with the real files"
}

func refusedUnreadable(err error) string {
	return fmt.Sprintf("its SKILL.md could not be read: %v", err)
}

const refusedFrontmatterDetail = "its SKILL.md has no complete frontmatter: the file must open with a line of three dashes, " +
	"carry name and description, and close with three dashes again"

func refusedNameDetail(name string) string {
	return fmt.Sprintf("its frontmatter names the skill %q, which is not a usable name: a name is lower-case letters, "+
		"digits and hyphens, starts with a letter or digit, and is at most 64 characters", name)
}

const refusedNoDescriptionDetail = "its frontmatter carries no description, and a skill without one is never offered to the assistant"

func refusedTooLongDetail(length, limit int) string {
	return fmt.Sprintf("its description is %d characters and the limit is %d: shorten it in the file — "+
		"nocx refuses it rather than cutting it, because half a sentence in the assistant's prompt would "+
		"read as a claim its author never made", length, limit)
}
