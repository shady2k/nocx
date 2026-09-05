package skill

// The bundle: a skill's own referenced files travel with it (design §5, §8).
//
// The AgentMail skill — the one this whole epic started from — has a body
// that says `Read [typescript.md](references/typescript.md)` seven times over.
// Installing SKILL.md alone gave the assistant seven instructions to open
// files that were never fetched, so the skill that landed was not the skill
// anybody read. This file is what makes "the bundle travels whole" true.
//
// THE DOCUMENT NAMES ITS OWN DEPENDENCIES, AND THE REPOSITORY IS NEVER
// ENUMERATED. A bare URL cannot list a directory — there is no directory, only
// an address that answers with bytes — so the only honest source for what
// belongs to a skill is the skill's own body. hermes's UrlSource reaches the
// same conclusion for the same reason (`tools/skills_hub.py`), and its
// GitHubSource enumerates a tree only because a tree API exists there; design
// §5 deletes our GitHub adapter, so link-driven acquisition is not a weaker
// version of enumeration here, it is the whole of what is available.
//
// ONE HOP. Links in SKILL.md are followed; links found inside a support file
// are not. The person read SKILL.md and nothing else, so SKILL.md is the only
// document whose claims about what belongs to the skill they have actually
// checked — a second hop would let a fetched file add to the manifest after
// the review, which is the review being made meaningless in one step. It also
// bounds the fetch by construction rather than by a counter.
//
// TIGHTENED IN FOUR PLACES AGAINST hermes, each because its choice was
// measured against a different product:
//
// The allowlist here is `references/` and `scripts/` and not hermes's five.
// `templates/`, `assets/` and `examples/` are directories our scan has never
// seen and our viewer has no way to show — an asset is by definition not text,
// and this path carries only text (below). They are cheap to add the day a
// skill needs one; carrying them now would be carrying capability for a case
// nobody has.
//
// A REFERENCED FILE THAT 404s FAILS THE INSTALL. hermes warns and installs
// without it, which is the one decision in its design that cannot be right for
// us: an install that silently drops a file produces a skill on disk that
// differs from the one the dialog showed, and the whole of design §8 is that
// the person reviewed what landed.
//
// THE SAME EFFECTIVE ORIGIN ACROSS EVERY REDIRECT, not on the first request.
// hermes compares the support URL's host against the document URL's host and
// then follows redirects freely, so a chain that leaves on its second hop is
// not seen at all. The rule belongs to whoever owns redirects, so it is
// httppolicy's (WithSameOriginOnly) and it is asked on every hop.
//
// UTF-8 ONLY, AND A BINARY IS REFUSED BY ITS NUL BYTE. hermes fetches support
// files as raw bytes; we do not, and the reason is written in their own
// terminal guard: feeding machine code to a scanner "tokenizes machine code
// into bogus NUL-bearing paths and crashes the scanner". Everything in this
// bundle can reach a reader — the file viewer, the scan, the audit — so
// everything in it is text, and apifetch's FetchText already refuses a body
// with a NUL in it before any of them sees it.
//
// AND THE READ IS BOUNDED AT THE SOURCE. Their other incident: a `cat` of a
// 166 MB ELF "pinned the gateway's tool thread on a superlinear shlex scan for
// 30+ minutes", fixed by reading with `head -c` on the far side. apifetch's
// TextRequest.MaxBytes is our `head -c`, applied per file, and the aggregate
// below is the same discipline applied to the sum — a per-file ceiling with no
// total is not a bound, it is a bound multiplied by however many links a body
// can hold.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/shady2k/nocx/internal/apifetch"
	"github.com/shady2k/nocx/internal/httppolicy"
)

const (
	// maxBundleFiles bounds how many support files one document may name.
	// It is a refusal and not a truncation, for the reason a missing file is
	// a refusal: taking the first thirty-two of forty links installs a skill
	// with eight dangling ones, which is the defect this file exists to fix
	// wearing a bound's clothes. Thirty-two is generous against what anybody
	// publishes — AgentMail, the skill that motivated this, names seven.
	maxBundleFiles = 32
	// maxBundleBytes bounds the SUM. Each file is already held to
	// maxSkillFileBytes by the fetch, which by itself permits a body with
	// four hundred links to pull twenty-five megabytes into memory and onto
	// the person's disk under one approval.
	maxBundleBytes = 512 << 10
)

// allowedSupportDirs is the first path component a referenced file may sit
// under. See the header for why it is two and not hermes's five.
var allowedSupportDirs = map[string]struct{}{
	"references": {},
	"scripts":    {},
}

// supportLinkPattern finds a candidate reference: a path under one of the
// allowlisted directories, appearing as a markdown link target, inside
// backticks, or standing alone in prose. It is hermes's regex with our
// shorter directory list, and the shape is worth keeping — a markdown link is
// not the only way a body sends the assistant to a file, and a body that says
// "run `scripts/setup.sh`" means it exactly as much as one that links it.
var supportLinkPattern = regexp.MustCompile(
	"(?m)(?:\\]\\(|`|(?:^|[\\s\"']))((?:references|scripts)/[^\\s)`\"'<>]+)")

// supportTraversalPattern is asked FIRST and over the whole document, and it
// refuses rather than skipping. A body containing `references/../../etc/passwd`
// has told us what it is; dropping that one link and installing the rest would
// be treating an attempt as a typo. Fail closed, once, for the whole bundle.
var supportTraversalPattern = regexp.MustCompile(
	"(?m)(?:references|scripts)/(?:[^\\s)`\"'<>]*/)?\\.\\.(?:/|$)")

// BundleFile is one file of a bundle as it will be written: the path relative
// to the skill's own directory, slash-separated, and the text.
//
// Text and not bytes. Everything here can reach a reader, so everything here
// is text — see the header.
//
// It is EXPORTED because a preview now hands the whole bundle out of this
// package (preview.go): the approval question the assistant's install raises
// carries every file's bytes, so a person can read what will land before they
// answer. A second struct declared out there for the same pair of strings
// would be the second spelling of "one file of a bundle", and the two would
// agree until the day one of them grew a field.
type BundleFile struct {
	Path string
	Text string
}

// referencedSupportPaths answers which files this document says belong to it.
//
// The error and the skip are different answers to different facts, and the
// distinction is the whole of the function. A TRAVERSAL is an error: the
// document tried to name something outside itself and the bundle is refused
// whole. A candidate that is not shaped like a file — a glob, a prose
// placeholder such as `references/type-<name>.md`, a path with a query string
// — is SKIPPED: it was never a file reference, so there is nothing to refuse
// and nothing to fetch.
func referencedSupportPaths(body string) ([]string, error) {
	// Backslashes are normalised first so a Windows-shaped path cannot walk
	// past the traversal check by spelling its separator differently.
	normalized := strings.ReplaceAll(body, "\\", "/")
	if supportTraversalPattern.MatchString(normalized) {
		return nil, errors.New(
			"that document refers to a file outside its own directory, so nothing was installed: " +
				"a skill may only carry files beneath references/ or scripts/")
	}

	seen := make(map[string]struct{})
	paths := make([]string, 0, 8)
	for _, match := range supportLinkPattern.FindAllStringSubmatch(normalized, -1) {
		candidate := strings.TrimRight(match[1], ".,;:")
		if candidate == "" {
			continue
		}
		// A FRAGMENT IS STRIPPED AND A QUERY IS NOT FOLLOWED. They look
		// alike and are not: `references/notes.md#setup` names a place
		// inside a file we are already carrying, so the file is the same
		// file. A query means the author is addressing a server view rather
		// than a file — and `references/type-?.md` in prose is
		// indistinguishable from `references/x.md?raw=1` without guessing at
		// parameter names, which is what hermes spends a function doing.
		// Neither shape is a file in a bundle, so both are left alone.
		if idx := strings.IndexByte(candidate, '#'); idx >= 0 {
			candidate = candidate[:idx]
		}
		if strings.IndexByte(candidate, '?') >= 0 {
			continue
		}
		// Percent-escapes are decoded here, before the path is validated, so
		// `references/%2e%2e/x` cannot arrive at the filesystem as a
		// traversal the check above never saw.
		decoded, err := url.PathUnescape(candidate)
		if err != nil {
			continue
		}
		clean, err := bundleRelPath(decoded)
		if err != nil {
			return nil, err
		}
		first, _, _ := strings.Cut(clean, "/")
		if _, allowed := allowedSupportDirs[first]; !allowed {
			continue
		}
		if !fileShaped(clean) {
			continue
		}
		if _, already := seen[clean]; already {
			continue
		}
		seen[clean] = struct{}{}
		paths = append(paths, clean)
	}
	if len(paths) > maxBundleFiles {
		return nil, fmt.Errorf(
			"that document refers to %d files and a skill may carry at most %d, so nothing was installed: "+
				"it is refused rather than trimmed, because installing the first %d would leave the rest dangling",
			len(paths), maxBundleFiles, maxBundleFiles)
	}
	// Sorted, because the digest that binds an approval to an install is
	// computed over this list and the order links happen to appear in a body
	// is not a property of the bundle.
	sort.Strings(paths)
	return paths, nil
}

// bundleRelPath is the containment rule for a path a DOCUMENT chose, stated
// once. It refuses rather than sanitising: a path that has to be repaired
// before it is safe is a path whose author meant something we are not going
// to do.
func bundleRelPath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("a skill may not carry the file %q: a referenced file is named by a relative path", raw)
	}
	parts := make([]string, 0, 4)
	for _, part := range strings.Split(trimmed, "/") {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return "", fmt.Errorf("a skill may not carry the file %q: it names something outside the skill's own directory", raw)
		}
		// A colon in a component is a Windows drive (`C:foo`) or an NTFS
		// alternate data stream (`notes.md:payload`), and the second writes
		// bytes into a file the viewer and the scan both enumerate without
		// ever seeing. hermes refuses it for the same reason; nothing
		// portable needs it.
		if strings.ContainsRune(part, ':') {
			return "", fmt.Errorf("a skill may not carry the file %q: a path component may not contain a colon", raw)
		}
		parts = append(parts, part)
	}
	if len(parts) < 2 {
		return "", fmt.Errorf("a skill may not carry the file %q: it names no file beneath a support directory", raw)
	}
	return strings.Join(parts, "/"), nil
}

// fileShaped answers whether a candidate is a file reference at all, as
// opposed to prose the link pattern happened to match. It is the last skip
// before a path becomes a fetch.
func fileShaped(clean string) bool {
	if strings.ContainsAny(clean, "*?[]<>") {
		return false
	}
	base := path.Base(clean)
	if base == "" {
		return false
	}
	// A real filename ends in a letter or a digit. A truncated prose
	// placeholder — `references/type-<name>.md`, which the link pattern cuts
	// at the `<` — leaves a trailing separator, and no file uses one. No
	// extension is required: `references/LICENCE` is a legitimate file.
	last := rune(base[len(base)-1])
	return (last >= 'a' && last <= 'z') || (last >= 'A' && last <= 'Z') || (last >= '0' && last <= '9')
}

// fetchBundle acquires every file the document names, or refuses.
//
// It is called by BOTH the preview and the install, over their own fetch of
// the document, for documentPreview's reason: the preview must show what will
// land, and the install must write what was shown, and the only way both can
// be true is for one function to answer what the bundle is and for the digest
// to be taken over its whole answer.
func (s *Store) fetchBundle(ctx context.Context, docURL, body string) ([]BundleFile, error) {
	rels, err := referencedSupportPaths(body)
	if err != nil {
		return nil, err
	}
	if len(rels) == 0 {
		return nil, nil
	}
	// Resolved against the address the PERSON NAMED, never against wherever
	// a redirect landed. That address is what is recorded as the skill's
	// source and what an update re-fetches, so it is the one the whole bundle
	// has to be relative to; and preview.go pins the document's own fetch to
	// the same origin, so the two cannot come apart.
	base, err := url.Parse(docURL)
	if err != nil {
		return nil, fmt.Errorf("that skill's address cannot be resolved against: %w", err)
	}

	files := make([]BundleFile, 0, len(rels))
	var total int
	for _, rel := range rels {
		// The path is put through URL.Path rather than concatenated, so the
		// escaping is the one net/url does and not one written here.
		target := base.ResolveReference(&url.URL{Path: rel})
		// By construction a relative reference keeps the base's origin; it is
		// asserted anyway, because "by construction" is what every escape
		// this package refuses was also true of until it was not.
		if httppolicy.Origin(target) != httppolicy.Origin(base) {
			return nil, fmt.Errorf(
				"the file %q resolves to %s, which is not where the skill was read from, so nothing was installed",
				rel, httppolicy.Origin(target))
		}
		doc, fetchErr := s.fetcher.FetchText(ctx, apifetch.TextRequest{
			URL: target.String(), MaxBytes: maxSkillFileBytes, SameOriginOnly: true,
		})
		if fetchErr != nil {
			// A MISSING FILE FAILS THE INSTALL. hermes skips; skipping is how
			// an installed skill stops matching the one that was shown.
			return nil, fmt.Errorf(
				"that skill's body sends the assistant to %q and that file could not be fetched, so nothing was installed: "+
					"a skill missing a file its own instructions name is not the skill you read (%w)", rel, fetchErr)
		}
		if doc.Lossy {
			return nil, fmt.Errorf(
				"the file %q is not UTF-8 text, so nothing was installed: everything a skill carries is read by somebody", rel)
		}
		total += len(doc.Text)
		if total > maxBundleBytes {
			return nil, fmt.Errorf(
				"that skill's files come to more than the %d KiB a bundle may be, so nothing was installed",
				maxBundleBytes>>10)
		}
		files = append(files, BundleFile{Path: rel, Text: doc.Text})
	}
	return files, nil
}

// scanBundleFiles is the support files' half of a preview's findings: every
// text file of the bundle, scanned under its own path.
//
// EVERY file, and no filter on the name. The bundle is UTF-8 and NUL-free by
// the time it gets here (fetchBundle refuses anything else), so everything in
// it is text somebody can read and everything in it is therefore scannable;
// a rule that scanned `references/` and skipped `scripts/`, or the reverse,
// would be a second opinion about which of a skill's files matter, held in
// one place and contradicted by the manifest the person was just shown.
func scanBundleFiles(files []BundleFile) []Finding {
	findings := make([]Finding, 0)
	for _, file := range files {
		findings = append(findings, Scan(file.Path, []byte(file.Text))...)
	}
	return findings
}

// wholeBundle is SKILL.md and every support file, in the order they land —
// the document first, because it is the one the person is reading and the one
// the others were named by.
//
// ONE OWNER OF THAT ORDER. It used to live in three places at once: the
// digest wrote "SKILL.md" first, the manifest prepended it, and nothing
// carried the document's bytes beside its support files at all. Now the
// digest, the manifest and the bytes the approval question shows are all
// projections of this one list, so a fourth reader cannot come to a different
// answer about what a bundle IS or what order it is in — which matters
// because the digest is what binds an approval to an install, and a manifest
// that disagreed with it would name files the comparison never covered.
func wholeBundle(document string, files []BundleFile) []BundleFile {
	whole := make([]BundleFile, 0, len(files)+1)
	whole = append(whole, BundleFile{Path: "SKILL.md", Text: document})
	return append(whole, files...)
}

// digestOfBundle is the record an approval is kept as: SKILL.md and every
// support file, each framed with its own length so no rearrangement of paths
// and contents can produce the same sum.
//
// It replaces a digest over the document alone, which would have let a
// support file change between the read and the install without the comparison
// noticing — an approval that covers only the file the person happened to be
// looking at is not an approval of what lands.
func digestOfBundle(whole []BundleFile) string {
	h := sha256.New()
	for _, file := range whole {
		writeDigestPart(h, []byte(file.Path))
		writeDigestPart(h, []byte(file.Text))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// bundleManifest is what the person is shown: every path that will land,
// SKILL.md first because it is the file they are reading.
func bundleManifest(whole []BundleFile) []string {
	manifest := make([]string, 0, len(whole))
	for _, file := range whole {
		manifest = append(manifest, file.Path)
	}
	return manifest
}
