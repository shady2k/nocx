package skill

// Installing a skill by its URL, read-only half (design §4, §5 steps 1-5).
//
// Preview fetches one document, parses it with the parser discovery already
// uses, refuses what it must, scans it, and answers with the whole body and
// every finding. IT WRITES NOTHING, and that is the reason it is a method of
// its own rather than a flag on the install: the person reads the exact bytes
// before deciding whether to adopt them, instead of approving a dialog that
// describes bytes it has not shown them (design §8).
//
// The fetch is internal/apifetch's — the person-initiated fetch
// api.import.postman already goes through — and therefore internal/httppolicy's
// address and credential rules, which that package was extracted to own "for
// every HTTP client in nocx". Nothing here constructs an http.Client: a second
// one would be a second answer to which addresses may be reached, agreeing
// with the first everywhere anybody looked.
//
// What is inherited rather than chosen here, so it is not mistaken for a
// decision of this file: https is unrestricted; http is permitted only where
// every resolved address is loopback or private, checked at connection time;
// redirects are bounded at ten and credentials are dropped on an origin
// change. The bounds this file adds are the 64 KiB ceiling (the one
// write.go already enforces on a skill file) and the refusals below.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/shady2k/nocx/internal/apifetch"
	"github.com/shady2k/nocx/internal/profile"
)

// PreviewResult is what the person is shown before they decide. The body is
// the WHOLE body — a skill is instructions, and an excerpt is not something
// anybody can adopt responsibly — and Findings carries every match rather
// than the first, because the 8 KiB bound that makes the assistant's write
// path attach one finding is a property of a tool result and not of a dialog
// (design §5 step 5).
type PreviewResult struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
	URL         string `json:"url"`
	// Findings are every static-scan match in the WHOLE BUNDLE — SKILL.md
	// and every support file that will land — each naming the file it
	// matched in. A bundled scripts/setup.sh is the file whose contents most
	// warrant a look and it used to get no findings anywhere, so the person
	// approved a manifest of names and a scan of one of them (nocx-872jc.4).
	//
	// SKILL.md's are counted over the whole fetched document, frontmatter
	// included, not over Body: the finding names a file, so its line number
	// has to count that file from its first byte or it points at nothing —
	// and the description in the frontmatter is what the assistant is offered
	// on every ask, which makes it exactly the wrong half to leave unscanned.
	Findings []Finding `json:"findings"`
	// Files is every path that will land, SKILL.md first (bundle.go). It is
	// the manifest design §5 asks the approval to name, and it is PATHS and
	// not contents: the person is reading the body here, and reading each
	// support file is the viewer's job — one capability in three places,
	// which is epic nocx-872jc and deliberately not three viewers.
	//
	// It is never nil: a skill that references nothing is ["SKILL.md"], and a
	// missing manifest and an empty one would be two ways to say the same
	// thing on the wire.
	Files []string `json:"files"`
	// Bundle is the same manifest WITH the bytes — every file that will
	// land, SKILL.md first, in the order Files names them. Both are
	// projections of one list (wholeBundle), so the paths a caller lists and
	// the paths a caller can read are the same paths by construction.
	//
	// IT IS NOT ON THE WIRE OF skills.preview, and that is deliberate rather
	// than an oversight. That result's contract says paths only, because the
	// dialog it serves shows the body and sends a person to skills.file for
	// anything else. This field exists for the caller that has NOWHERE to
	// send them: the assistant's install raises an approval NOTIFICATION,
	// and a question cannot answer a follow-up request about a bundle that
	// is not on disk yet and must not be (nocx-ojfuc.2). So the bytes travel
	// with the question, the way the whole of a named script already travels
	// with a command approval.
	Bundle []BundleFile `json:"-"`
	// Digest is the sha256 over the whole bundle — the exact value Install
	// compares its second fetch against. It is in-process for the same
	// reason Bundle is: the approval question names it so a person can see
	// that what they are reading is what the install is bound to, while
	// skills.preview's contract has no field for it and the dialog it serves
	// never needed one.
	//
	// It is CHANGE DETECTION AND NEVER PROVENANCE (design §5). Bytes a
	// stranger served hash to this; nobody has vouched for them.
	Digest string `json:"-"`
}

// previewedDocument is what a Preview showed a person, kept on the SERVER so
// Install has something to compare a second fetch against (install.go).
//
// IT USED TO BE ONE SLOT, and that was right while Settings was the only
// acquisition surface: design §9 holds one source at a time, visibly, and it
// can be taken back, so a single slot needed no eviction policy, no expiry and
// no bound, and there was no question about which preview an install referred
// to. skills.install (nocx-ojfuc.1) made the assistant a second caller, and
// removing the paste box (nocx-ojfuc.4) did not remove the class: two
// concurrent runs are still two callers. Two previews of different addresses
// clobbered each other and the loser's install refused with "nothing has been
// read from that address" — about an address the person had read (nocx-aesm2).
//
// SO IT IS A KEYED MAP, AND THE MAP OWES WHAT THE SLOT DID NOT.
//
//   - THE KEY is what the installing caller actually holds: a direct URL
//     install has only its address, a resolved install has only its handle.
//     They cannot collide because they are prefixed apart.
//   - THE BOUND is maxRememberedPreviews entries. Every entry is a digest and
//     a few strings, so the bound is about not growing without limit rather
//     than about bytes.
//   - THE EVICTION is oldest-first by preview order, never newest-first: the
//     preview a person is most likely to be answering right now is the last
//     one shown.
//   - AND AN EVICTION IS REMEMBERED, by key, for exactly as long as the ring
//     below holds it. That is not bookkeeping for its own sake — it is the
//     only way the refusal can say "you read this and nocx has since let it
//     go" instead of "you never read this", which is the defect this replaces.
//
// It holds the DIGEST and not the bytes: nothing here ever needs to serve the
// document a second time, only to answer whether the document that comes back
// is the one the person read.
type previewedDocument struct {
	key        string
	seq        uint64
	url        string
	digest     string
	resolution *githubResolutionPlan
	selected   []string
	activePath string
	digests    map[string]string
}

// maxRememberedPreviews bounds both the live previews and the ring of keys
// recently evicted from them. One preview belongs to one unanswered approval,
// and unanswered approvals are a handful at the very most; the number is
// generous rather than tuned, because being too small costs a person a
// re-read and being too large costs a few hundred bytes.
const maxRememberedPreviews = 16

func previewKeyForURL(rawURL string) string { return "url:" + rawURL }

func previewKeyForHandle(handle string) string { return "handle:" + handle }

// putPreview stores one entry under its key, evicting the oldest if the bound
// is reached. Replacing an existing key is not an eviction: the same address
// previewed twice is one preview, freshened.
func (s *Store) putPreview(entry *previewedDocument) {
	if s.previews == nil {
		s.previews = make(map[string]*previewedDocument, maxRememberedPreviews)
	}
	if _, replacing := s.previews[entry.key]; !replacing && len(s.previews) >= maxRememberedPreviews {
		oldest := ""
		for key, held := range s.previews {
			if oldest == "" || held.seq < s.previews[oldest].seq {
				oldest = key
			}
		}
		delete(s.previews, oldest)
		s.rememberEviction(oldest)
	}
	s.previewSeq++
	entry.seq = s.previewSeq
	s.previews[entry.key] = entry
}

func (s *Store) rememberEviction(key string) {
	s.evicted = append(s.evicted, key)
	if len(s.evicted) > maxRememberedPreviews {
		s.evicted = append(s.evicted[:0], s.evicted[len(s.evicted)-maxRememberedPreviews:]...)
	}
}

func (s *Store) wasEvicted(key string) bool {
	for _, held := range s.evicted {
		if held == key {
			return true
		}
	}
	return false
}

// previewFor finds the entry an install of rawURL refers to. A direct install
// is keyed by its address; a RESOLVED install reaches here too, because
// InstallResolved arms its chosen candidate's address on the resolution entry
// (armResolvedPath) and then walks the same write path. The scan is over a map
// bounded at maxRememberedPreviews.
func (s *Store) previewFor(rawURL string) *previewedDocument {
	if entry, ok := s.previews[previewKeyForURL(rawURL)]; ok && entry.url == rawURL {
		return entry
	}
	for _, entry := range s.previews {
		if entry.resolution != nil && entry.url == rawURL {
			return entry
		}
	}
	return nil
}

// ReadButDisplaced reports that an address WAS previewed and its record has
// since been evicted by newer ones. The refusal a person sees is built from
// it, so that "nothing has been read from that address" is never printed for
// an address that was read (nocx-aesm2).
func (s *Store) ReadButDisplaced(rawURL string) bool {
	if s == nil {
		return false
	}
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	return s.wasEvicted(previewKeyForURL(rawURL))
}

// ResolutionDisplaced is ReadButDisplaced for the resolved half: a handle
// whose plan has been evicted was resolved, it is not unknown.
func (s *Store) ResolutionDisplaced(handle string) bool {
	if s == nil {
		return false
	}
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	return s.wasEvicted(previewKeyForHandle(handle))
}

// displacedNextStep is what both refusals owe their reader once the cause is
// known. One constant, for the reason nextStepForAPage is one constant.
const displacedNextStep = " read it again, then install what you read"

// rememberPreview records what was just shown. Only a successful preview is
// remembered: a document that was refused was never shown, so there is
// nothing an install could refer back to.
//
// The digest is over the WHOLE BUNDLE and not over the document alone. An
// approval that covered only the file the person happened to be reading would
// let a support file be swapped between the read and the install without the
// comparison noticing, and a bundle whose reference files changed after they
// were shown is not the bundle that was approved.
func (s *Store) rememberPreview(rawURL, digest string) {
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	s.putPreview(&previewedDocument{key: previewKeyForURL(rawURL), url: rawURL, digest: digest})
}

// rememberResolution replaces the one approval slot with a resolution. The
// address and bytes stay in this server-side plan; the returned handle is the
// only value a caller may carry into InstallResolved.
func (s *Store) rememberResolution(plan *githubResolutionPlan, handle string) {
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	plan.Resolution.Handle = handle
	s.putPreview(&previewedDocument{key: previewKeyForHandle(handle), resolution: plan})
}

func (s *Store) approvedAcquisition(rawURL string) (acquisition, bool) {
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	entry := s.previewFor(rawURL)
	if entry == nil {
		return acquisition{}, false
	}
	from := acquisition{url: rawURL, digest: entry.digest}
	if entry.resolution != nil {
		from.entryURL = entry.resolution.address
		from.path = entry.activePath
		from.ref = entry.resolution.Ref
		from.commit = entry.resolution.Commit
	}
	return from, true
}

func (s *Store) approvedResolution(handle string, paths []string) (*githubResolutionPlan, string, bool) {
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	entry := s.previews[previewKeyForHandle(handle)]
	if entry == nil || entry.resolution == nil {
		return nil, "", false
	}
	if len(entry.selected) > 0 && !samePaths(entry.selected, paths) {
		return nil, "", false
	}
	return entry.resolution, entry.digest, true
}

func (s *Store) rememberResolvedDigests(handle string, paths []string, digests map[string]string) bool {
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	entry := s.previews[previewKeyForHandle(handle)]
	if entry == nil || entry.resolution == nil {
		return false
	}
	entry.selected = append([]string(nil), paths...)
	entry.activePath = ""
	entry.url = ""
	entry.digest = ""
	entry.digests = make(map[string]string, len(digests))
	for path, digest := range digests {
		entry.digests[path] = digest
	}
	return true
}

func (s *Store) approvedResolvedDigests(handle string, paths []string) (map[string]string, bool) {
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	entry := s.previews[previewKeyForHandle(handle)]
	if entry == nil || entry.resolution == nil || !samePaths(entry.selected, paths) {
		return nil, false
	}
	if len(entry.digests) != len(paths) {
		return nil, false
	}
	digests := make(map[string]string, len(paths))
	for _, path := range paths {
		digest := entry.digests[path]
		if digest == "" {
			return nil, false
		}
		digests[path] = digest
	}
	return digests, true
}

func (s *Store) armResolvedPath(handle, activePath, rawURL, digest string) bool {
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	entry := s.previews[previewKeyForHandle(handle)]
	if entry == nil || entry.resolution == nil {
		return false
	}
	entry.activePath = activePath
	entry.url = rawURL
	entry.digest = digest
	return true
}

// forgetPreview spends the approval. An approval is for one document on one
// occasion, so installing it consumes it rather than leaving a standing
// permission to write those bytes again.
func (s *Store) forgetPreview(rawURL string) {
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	entry := s.previewFor(rawURL)
	if entry == nil {
		return
	}
	if entry.resolution != nil && len(entry.selected) > 1 {
		remaining := entry.selected[:0]
		for _, path := range entry.selected {
			if path != entry.activePath {
				remaining = append(remaining, path)
			}
		}
		entry.selected = append([]string(nil), remaining...)
		entry.activePath = ""
		entry.url = ""
		entry.digest = ""
		return
	}
	delete(s.previews, entry.key)
}

func samePaths(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// Preview acquires the document at rawURL and answers with what a person needs
// to decide. Every refusal names the step that refused, in their words.
func (s *Store) Preview(ctx context.Context, rawURL string) (PreviewResult, error) {
	if s == nil {
		return PreviewResult{}, errUnavailable
	}
	if s.fetcher == nil {
		return PreviewResult{}, errors.New("installing a skill from a URL is unavailable: this backend has no fetch seam wired")
	}

	// "Is this string an address at all" has one owner in this repo, and it
	// is the same one the stored source URL is checked by (store_doc.go).
	// Its refusal of user:password@host matters here too: a credential in the
	// address would be sent to whatever answers, and a redirect could not
	// strip it.
	if err := profile.ValidateBaseURL(rawURL); err != nil {
		return PreviewResult{}, fmt.Errorf("that is not an address a skill can be fetched from: %w", err)
	}

	text, err := s.fetchDocument(ctx, rawURL)
	if err != nil {
		return PreviewResult{}, err
	}
	result, err := documentPreview(text, rawURL)
	if err != nil {
		return PreviewResult{}, err
	}

	// Whether this name may be written at all is asked HERE as well as at the
	// install, from the one function that owns the question (install.go). A
	// preview that showed a document the install would refuse would be
	// offering the person a button that can only fail, which is the shape
	// Remove already avoids for builtins.
	if _, planErr := s.planInstall(result.Name, rawURL); planErr != nil {
		return PreviewResult{}, planErr
	}

	// THE BUNDLE IS FETCHED HERE, IN THE PREVIEW, and not only at the
	// install. Two reasons, and the second is the binding one.
	//
	// A referenced file that 404s fails the install (bundle.go). Discovering
	// that only after the person has approved would offer them a button that
	// can only fail — the exact shape the name check and the description cap
	// above are asked here to avoid.
	//
	// And the digest that joins the two calls has to be over what will land.
	// If the preview digested the document alone, an install could write a
	// bundle whose support files nobody had ever compared against anything,
	// and "install writes what preview showed" would be a claim rather than a
	// property. So the preview acquires the whole bundle, digests the whole
	// bundle, and the install re-acquires and re-digests it.
	//
	// The cost is that a preview now spends one request per referenced file.
	// It is bounded by maxBundleFiles and by the aggregate ceiling, and it is
	// spent on a gesture a person is watching, which is where this product
	// already spends a fetch.
	files, err := s.fetchBundle(ctx, rawURL, result.Body)
	if err != nil {
		return PreviewResult{}, err
	}
	// ONE LIST, THREE PROJECTIONS. The manifest the person reads, the bytes
	// they can open and the digest the install is bound to are all taken
	// from the same wholeBundle here, so none of the three can name a file
	// the other two do not.
	result.Bundle = wholeBundle(text, files)
	result.Files = bundleManifest(result.Bundle)
	result.Digest = digestOfBundle(result.Bundle)
	// THE SUPPORT FILES ARE SCANNED HERE, where their bytes are, and not in
	// documentPreview, which is pure and reaches nothing (see its Files
	// note). This is the one place in the install path that holds a whole
	// bundle in memory before anything is written, so it is the only place
	// the person can be shown a finding in a file they are about to adopt.
	// The install re-fetches and re-digests the same bundle; it does not
	// re-scan, because nothing there reads a finding — the reading happened
	// before the approval, which is the point of a preview.
	result.Findings = append(result.Findings, scanBundleFiles(files)...)

	s.rememberPreview(rawURL, result.Digest)
	return result, nil
}

// nextStepForAPage is what every refusal a DOCUMENTATION PAGE can earn owes
// its reader, and it is one constant because three refusals that name the
// next step three different ways is the same defect wearing better clothes.
//
// A person pastes the page they were reading; the SKILL.md address is what
// they are asking the assistant to find. So a page is the ordinary first
// attempt at this tool, not a mistake, and the ceiling, the decoder and the
// parser are simply the three ways it comes back. Each said what was wrong
// and stopped there — and a model reading "too large" went to the page for
// instructions instead, found `npx skills add …` on it and proposed running
// that (nocx-e28cw). The redirect refusal in fetchDocument has always named
// its remedy; these three now do too.
//
// It names the FILE rather than an actor because both a person and a model
// read these, and a next step addressed to one of them is noise to the other.
const nextStepForAPage = "; what nocx needs is the address that answers with the SKILL.md file itself — a page about the skill is not it"

// fetchDocument acquires one skill document through the guarded seam. It is
// shared with Install rather than copied there, because the second fetch has
// to be the SAME fetch under the same bounds — a second acquisition path
// would be a second answer to what a skill document may be.
func (s *Store) fetchDocument(ctx context.Context, rawURL string) (string, error) {
	// The ceiling is applied by the fetch, so an over-long document is
	// refused BEFORE it is parsed rather than truncated: a truncated skill is
	// a skill whose instructions end in the middle of a sentence.
	// SAME ORIGIN, EVERY HOP, for the document as well as for its support
	// files (bundle.go). Three things follow from the document's origin and
	// each of them would be wrong if a redirect could move it.
	//
	// The manifest resolves against the address the person named, so a
	// document served from somewhere else would have its references looked
	// for at a host that never held them — dangling links, or worse, a
	// different host's files under this skill's name.
	//
	// The recorded source is that address too, and an update re-fetches it:
	// a redirect that moved between installs would silently repoint where a
	// skill comes from, which planInstall refuses to let a person do
	// deliberately.
	//
	// And the address is what the person read and approved. A vanity host
	// that forwards elsewhere is not refused as suspicious — it is refused
	// because the four other facts we keep about a skill are all about the
	// origin in the address, and none of them would be true of the one that
	// answered. The remedy is one paste of the address it forwards to, which
	// design §5 has the assistant resolve anyway.
	doc, err := s.fetcher.FetchText(ctx, apifetch.TextRequest{
		URL: rawURL, MaxBytes: maxSkillFileBytes, SameOriginOnly: true,
	})
	if err != nil {
		if errors.Is(err, apifetch.ErrTooLarge) {
			return "", fmt.Errorf(
				"that document is larger than the %d KiB a skill file may be, so it was refused before it was parsed%s",
				maxSkillFileBytes>>10, nextStepForAPage)
		}
		return "", fmt.Errorf("that skill could not be fetched: %w", err)
	}
	// Lossy is the fetch seam reporting that the bytes could not be decoded
	// as the text they claimed to be. A skill is instructions; instructions
	// with replacement characters in them are not the instructions anybody
	// wrote.
	if doc.Lossy {
		return "", errors.New("that document is not UTF-8 text, so it is not a skill file" + nextStepForAPage)
	}
	return doc.Text, nil
}

// documentPreview parses, validates and scans one already-acquired document.
// It is the whole of steps 2 and 4 of the pipeline (design §5) and it has one
// implementation, which is what lets the install re-run them over the bytes it
// is about to write rather than trusting that the preview's answer still
// describes them.
func documentPreview(text, rawURL string) (PreviewResult, error) {
	// The same parser discovery uses, and the CONTENT-TYPE IS NOT CONSULTED:
	// the file answers what it is definitively, and trusting a header when
	// the bytes are present would be a second derivation of one fact.
	fm, offset, ok := parseFrontmatter([]byte(text))
	if !ok {
		return PreviewResult{}, errors.New(
			"that document is not a SKILL.md: it must open with a YAML frontmatter block delimited by --- and close it again" +
				nextStepForAPage)
	}

	// The name comes from the frontmatter, never from the URL's last path
	// segment: a URL cannot name a skill, only a skill can. It is checked
	// against discovery's pattern AS WRITTEN rather than through
	// normalizeName, because normalizing would accept a document discovery
	// will later refuse — the file keeps the name it carries, and a name
	// that only becomes canonical after we lower-case it never matches.
	name := strings.TrimSpace(fm.Name)
	if name == "" {
		return PreviewResult{}, errors.New("that document's frontmatter carries no name, so there is no skill to install")
	}
	if !skillNamePattern.MatchString(name) {
		return PreviewResult{}, fmt.Errorf(
			"that document names the skill %q, which is not a usable name: a name is lower-case letters, digits and hyphens, "+
				"starts with a letter or digit, and is at most 64 characters", name)
	}
	description := sanitizeDescription(fm.Description)
	if description == "" {
		return PreviewResult{}, fmt.Errorf(
			"that document's frontmatter carries no description for %q, and a skill without one is never offered to the assistant", name)
	}
	// Asked HERE and not only at the write, for the reason the name check
	// above is asked here: a preview that showed a document the install would
	// refuse offers the person a button that can only fail. The cap belongs to
	// write.go, which is where the number is, so this is the same comparison
	// and not a second one.
	if err := checkDescriptionLength(name, description); err != nil {
		return PreviewResult{}, err
	}
	body := text[offset:]
	if strings.TrimSpace(body) == "" {
		return PreviewResult{}, fmt.Errorf("that document has frontmatter for %q and no body, so there are no instructions to read", name)
	}

	// The scan is advisory and stays advisory (scan.go): a finding is
	// evidence for the person, never a refusal. It reads the WHOLE DOCUMENT
	// under the name SKILL.md rather than the body alone, so the line number
	// counts the file the finding names — see PreviewResult.Findings for why
	// the frontmatter is not the half to leave out.
	findings := Scan("SKILL.md", []byte(text))
	if findings == nil {
		findings = []Finding{}
	}
	// Files is left empty HERE and filled by the caller. This function is
	// what the install re-runs over the bytes it is about to write, and it is
	// pure — it parses and scans and reaches nothing — while a manifest costs
	// a request per entry. Keeping the fetch out of it is what lets the
	// install re-run the parse without re-running the network twice.
	return PreviewResult{
		Name:        name,
		Description: description,
		Body:        body,
		URL:         rawURL,
		Findings:    findings,
		Files:       []string{"SKILL.md"},
	}, nil
}
