package assistant

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
import (
	"net/url"
	"strings"

	"github.com/shady2k/nocx/internal/skill"
)

// ApprovalInstall is the route and skill set an install proposal carries as
// the person reads it before answering.
type ApprovalInstall struct {
	// Source is the address that started a resolved route. It is absent for a
	// direct install, where there is no repository route to explain.
	Source string `json:"source,omitempty"`
	// Destination is the canonical repository reached by a resolved route.
	Destination string `json:"destination,omitempty"`
	// OriginsDiffer states whether the source and destination hosts differ.
	// A pointer distinguishes an absent direct-install route from false.
	OriginsDiffer *bool `json:"originsDiffer,omitempty"`
	// Ref and Commit are the repository version facts covered by approval.
	Ref    string `json:"ref,omitempty"`
	Commit string `json:"commit,omitempty"`
	// Skills is always a non-empty set. A direct URL install is a set of one.
	Skills []ApprovalInstallSkill `json:"skills"`
}

// ApprovalInstallSkill is one skill covered by an install approval.
type ApprovalInstallSkill struct {
	// Path is the candidate's path inside the resolved repository. It is
	// absent for a direct URL install, which has no repository path.
	Path        string                `json:"path,omitempty"`
	Name        string                `json:"name"`
	Description string                `json:"description"`
	URL         string                `json:"url"`
	Digest      string                `json:"digest"`
	Files       []ApprovalInstallFile `json:"files"`
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
	item, ok := installSkillFacts(preview)
	if !ok {
		return nil
	}
	return &ApprovalInstall{Skills: []ApprovalInstallSkill{item}}
}

func installSkillFacts(preview *skill.PreviewResult) (ApprovalInstallSkill, bool) {
	if preview == nil {
		return ApprovalInstallSkill{}, false
	}
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
	return ApprovalInstallSkill{
		URL:         preview.URL,
		Name:        preview.Name,
		Description: preview.Description,
		Digest:      preview.Digest,
		Files:       files,
	}, true
}

func InstallFactsForResolved(resolution *skill.Resolution, source string, paths []string, previews []skill.PreviewResult) *ApprovalInstall {
	if resolution == nil || len(paths) != len(previews) || len(paths) == 0 {
		return nil
	}
	skills := make([]ApprovalInstallSkill, 0, len(previews))
	for i := range previews {
		item, ok := installSkillFacts(&previews[i])
		if !ok {
			return nil
		}
		item.Path = paths[i]
		skills = append(skills, item)
	}
	destination := resolution.Repository
	destinationURL := destination
	if !strings.HasPrefix(destinationURL, "http://") && !strings.HasPrefix(destinationURL, "https://") {
		destinationURL = "https://" + destinationURL
	}
	originsDiffer := routeOriginsDiffer(source, destinationURL)
	return &ApprovalInstall{
		Source:        source,
		Destination:   destination,
		OriginsDiffer: &originsDiffer,
		Ref:           resolution.Ref,
		Commit:        resolution.Commit,
		Skills:        skills,
	}
}

func routeOriginsDiffer(source, destination string) bool {
	sourceURL, err := url.Parse(source)
	if err != nil || sourceURL.Host == "" {
		return true
	}
	destinationURL, err := url.Parse(destination)
	if err != nil || destinationURL.Host == "" {
		return true
	}
	return !strings.EqualFold(sourceURL.Host, destinationURL.Host)
}
