package assistant

// What an install proposal RESOLVED to (nocx-ojfuc.2).
//
// skills.install's arguments are one address, and an address is not something
// anybody can decide about. The model was asked to install a skill from a
// page; what it resolved that page to is the thing the person is actually
// deciding, and a question that named only the ask would let somebody approve
// a page they read and receive a repository they never saw. So the question
// carries the RESOLUTION: the address that was fetched, the name and the
// description the document gives itself, the digest the install is bound to,
// and every file that will land — with its bytes.
//
// THE BYTES TRAVEL WITH THE QUESTION, and that was the one real decision
// here. A support file has not been written yet and must not be until the
// person answers, so it is not on disk and skills.file cannot read it. Three
// shapes were possible.
//
//   - A REQUEST for one file of the pending preview, shaped like skills.file.
//     Rejected. It would put a SECOND reader on the store's one-slot
//     remembered preview, which Settings already shares with this tool
//     (nocx-aesm2), and — decisively — the bytes a person read would then be
//     a second read of mutable server state rather than the bytes that came
//     with the question. "Approving installs exactly what was shown" is only
//     a property if what was shown is what travelled.
//   - A FLAG on skills.file meaning "the pending preview rather than a root".
//     Rejected: a mode that changes what a path means is two owners of one
//     input, and the loser goes on advertising what it can no longer deliver.
//   - THE BYTES, IN THE QUESTION. Taken. It is the shape a command approval
//     already has for the script a command names (script.go): the whole of
//     every file, read at the moment the question was asked, carried beside
//     the proposal. The bound is not ours to invent either — internal/skill
//     refuses a bundle of more than 32 files or more than 512 KiB of support
//     text plus a 64 KiB document BEFORE any question can exist, so the
//     ceiling is enforced upstream of this struct and there is no paging
//     problem to solve.
//
// AND SKILL.MD IS ONE OF THE FILES, not a special case beside them. It is the
// first entry of the same manifest, with the whole served document as its
// text — frontmatter included, because a finding names a file and counts its
// lines from that file's first byte. A question that showed the body alone
// would put every SKILL.md line number half a frontmatter out.
//
// NOTHING HERE IS A SECOND SCAN. The findings are the preview's own, grouped
// by the file they matched in so the surface can mark each one on the line it
// sits on rather than quoting it underneath. The scan ran once, before the
// question, over the same bytes this carries.

import "github.com/shady2k/nocx/internal/skill"

// ApprovalInstall is the skill an install proposal resolved to, as the person
// reads it before answering.
type ApprovalInstall struct {
	// URL is the address that was FETCHED — the one the digest, the manifest
	// and every byte below came from. It is stated here rather than left to
	// the arguments blob because it is the resolution the question is about;
	// the surface never re-derives it by parsing the model's arguments.
	URL string `json:"url"`
	// Name is the skill's name as its own frontmatter gives it. A URL cannot
	// name a skill (internal/skill/preview.go).
	Name string `json:"name"`
	// Description is the frontmatter description, and it is the field this
	// question is most obliged to show: it is the one part of a skill that
	// lives in the assistant's system prompt afterwards, so it is what
	// decides when these instructions get reached for (design §5).
	Description string `json:"description"`
	// Digest is the sha256 over the whole bundle, which Install compares its
	// second fetch against — the value that makes "what was approved is what
	// is written" a property rather than a claim. It is CHANGE DETECTION AND
	// NEVER PROVENANCE: bytes a stranger served hash to this, and nobody has
	// vouched for them. The surface says so.
	Digest string `json:"digest"`
	// Files is every file that will land, SKILL.md first — the same manifest
	// skills.preview names, never a shorter one, with the bytes of each.
	Files []ApprovalInstallFile `json:"files"`
}

// ApprovalInstallFile is one file that will land, with what the static scan
// had to say about it.
type ApprovalInstallFile struct {
	// Path is relative to the skill's own directory, slash-separated — the
	// path the file will have on disk and the one the manifest names.
	Path string `json:"path"`
	// Text is the file verbatim. There is no refusal vocabulary beside it,
	// and that is a fact about this path rather than an omission: every file
	// here was already fetched whole, as UTF-8, under the per-file ceiling,
	// before the question could exist — a file that could not be got is a
	// refusal of the whole preview and no question is asked at all.
	Text string `json:"text"`
	// Findings are the preview's static-scan matches IN THIS FILE, so a
	// surface can mark each on the line it matched instead of quoting it
	// somewhere else. Never nil: no matches is [], and an empty array is not
	// an all-clear — the scan is a fixed set of known phrasings.
	Findings []skill.Finding `json:"findings"`
}

// InstallFactsFor turns the resolution into the question's own shape. Nil in,
// nil out: a proposal that is not an install carries no install block, and an
// absent block is how the wire says "this question is not about a skill".
//
// It is EXPORTED because the transport's over-the-wire contract test builds
// its notification from the product's own derivation rather than from a
// payload the test wrote — the same reason ScriptReadingsFor is.
func InstallFactsFor(preview *skill.PreviewResult) *ApprovalInstall {
	if preview == nil {
		return nil
	}
	// The findings are grouped ONCE, here, rather than by the surface: the
	// preview's list is flat and each entry names its file, and a renderer
	// that re-derived the grouping would be a second answer to "which file
	// is this finding about".
	byPath := make(map[string][]skill.Finding, len(preview.Bundle))
	for _, finding := range preview.Findings {
		byPath[finding.Path] = append(byPath[finding.Path], finding)
	}
	files := make([]ApprovalInstallFile, 0, len(preview.Bundle))
	for _, file := range preview.Bundle {
		findings := byPath[file.Path]
		if findings == nil {
			findings = []skill.Finding{}
		}
		files = append(files, ApprovalInstallFile{
			Path:     file.Path,
			Text:     file.Text,
			Findings: findings,
		})
	}
	return &ApprovalInstall{
		URL:         preview.URL,
		Name:        preview.Name,
		Description: preview.Description,
		Digest:      preview.Digest,
		Files:       files,
	}
}
