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
	"crypto/sha256"
	"encoding/hex"
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
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Body        string    `json:"body"`
	URL         string    `json:"url"`
	Findings    []Finding `json:"findings"`
}

// previewedDocument is what Preview last showed a person, kept on the SERVER
// so Install has something to compare a second fetch against (install.go).
//
// It is ONE slot rather than a map, because the surface it serves holds one
// source at a time (design §9: "One source is held, visibly, and can be taken
// back"). A single slot needs no eviction policy, no expiry and no bound, and
// there is never any question about which preview an install refers to. The
// cost is that previewing a second address forgets the first, and the refusal
// that produces — read it again — is recoverable in one click.
//
// It holds the DIGEST and not the bytes: nothing here ever needs to serve the
// document a second time, only to answer whether the document that comes back
// is the one the person read.
type previewedDocument struct {
	url    string
	digest string
}

// rememberPreview records what was just shown. Only a successful preview is
// remembered: a document that was refused was never shown, so there is
// nothing an install could refer back to.
func (s *Store) rememberPreview(rawURL, text string) {
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	s.previewed = &previewedDocument{url: rawURL, digest: digestOfDocument(text)}
}

// approvedPreview answers with the digest of the document Preview showed for
// this exact URL, if that is still the document being looked at.
func (s *Store) approvedPreview(rawURL string) (string, bool) {
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	if s.previewed == nil || s.previewed.url != rawURL {
		return "", false
	}
	return s.previewed.digest, true
}

// forgetPreview spends the approval. An approval is for one document on one
// occasion, so installing it consumes it rather than leaving a standing
// permission to write those bytes again.
func (s *Store) forgetPreview(rawURL string) {
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	if s.previewed != nil && s.previewed.url == rawURL {
		s.previewed = nil
	}
}

func digestOfDocument(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
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
	if _, err := s.planInstall(result.Name, rawURL); err != nil {
		return PreviewResult{}, err
	}

	s.rememberPreview(rawURL, text)
	return result, nil
}

// fetchDocument acquires one skill document through the guarded seam. It is
// shared with Install rather than copied there, because the second fetch has
// to be the SAME fetch under the same bounds — a second acquisition path
// would be a second answer to what a skill document may be.
func (s *Store) fetchDocument(ctx context.Context, rawURL string) (string, error) {
	// The ceiling is applied by the fetch, so an over-long document is
	// refused BEFORE it is parsed rather than truncated: a truncated skill is
	// a skill whose instructions end in the middle of a sentence.
	doc, err := s.fetcher.FetchText(ctx, apifetch.TextRequest{URL: rawURL, MaxBytes: maxSkillFileBytes})
	if err != nil {
		if errors.Is(err, apifetch.ErrTooLarge) {
			return "", fmt.Errorf(
				"that document is larger than the %d KiB a skill file may be, so it was refused before it was parsed",
				maxSkillFileBytes>>10)
		}
		return "", fmt.Errorf("that skill could not be fetched: %w", err)
	}
	// Lossy is the fetch seam reporting that the bytes could not be decoded
	// as the text they claimed to be. A skill is instructions; instructions
	// with replacement characters in them are not the instructions anybody
	// wrote.
	if doc.Lossy {
		return "", errors.New("that document is not UTF-8 text, so it is not a skill file")
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
			"that document is not a SKILL.md: it must open with a YAML frontmatter block delimited by --- and close it again")
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

	// The scan is advisory and stays advisory (scan.go:55-57): a finding is
	// evidence for the person, never a refusal. It reads the BODY, which is
	// what every other caller of Scan reads and what the person is shown, so
	// a finding's line number counts the lines they are looking at.
	findings := Scan([]byte(body))
	if findings == nil {
		findings = []Finding{}
	}
	return PreviewResult{
		Name:        name,
		Description: description,
		Body:        body,
		URL:         rawURL,
		Findings:    findings,
	}, nil
}
