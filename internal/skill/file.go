package skill

// The person's read path for one file of one skill.
//
// Why it is not Read. Read serves the ASSISTANT: it wants bytes to put in a
// prompt, and it is content to be handed as much of a long file as the budget
// allows. A person opening a file wants the file, and a viewer that showed
// them the first 64 KiB of a 40 MiB log while calling it the file would be
// lying on the viewer's behalf. So the two share their containment — locate,
// in read.go, which is the ONLY answer to "is this path inside that skill" —
// and differ only in what they do with a file they cannot serve whole.
//
// WHICH REFUSALS ARE ANSWERS AND WHICH ARE ERRORS. Three facts a viewer has
// to be able to state in the person's words, decided one at a time rather
// than by a blanket rule:
//
// "This file is not text" and "this file is larger than nocx reads" are
// RESULTS. The request was well formed and the answer is a true sentence
// about a file that exists inside the skill — this is a PNG; this is bigger
// than the budget. A viewer given a result can say that beside the file's own
// name, with its provenance and the limit that refused it, in the same list
// as every other file. A viewer given a JSON-RPC error can only paint a red
// box, because an error carries no path, no provenance and no number to name.
// The alternative rejected here is one error per refusal with the sentence in
// its message, which is what Preview does — and Preview is right to, because
// a fetch that fails has no subject to describe, whereas this file is on disk
// and describable either way.
//
// "This file is gone" is an ERROR, and so are a path that leaves the skill
// and a name no root holds. There is nothing to describe: a result would have
// to carry a path to a file that is not there and a provenance for bytes that
// do not exist, so every field of it would be an invention. A refusal of the
// request is the honest answer, and that is what an error is.

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// FileRefusal names why a file's bytes are not being shown. The set is closed
// and the wire declares it; the empty value is the ordinary case and means
// Text is the file.
type FileRefusal string

const (
	// FileRefusalNone means nothing was refused and Text holds the file.
	FileRefusalNone FileRefusal = ""
	// FileRefusalNotText means the bytes are not UTF-8, so there is no text
	// to show. Preview says the same of a fetched document that will not
	// decode, and this is deliberately the same sentence about the same fact.
	FileRefusalNotText FileRefusal = "not-text"
	// FileRefusalTooLarge means the file is bigger than MaxReadBytes, which
	// the result carries so the sentence can name the limit.
	FileRefusalTooLarge FileRefusal = "too-large"
)

// FileResult is one file of one discovered skill as a person reads it: where
// it came from, what it says, and — when it says nothing here — why not.
type FileResult struct {
	// Name and Path are the skill and the file as they were RESOLVED, not as
	// they were asked for, so a viewer labels what it is showing rather than
	// what it requested.
	Name       string     `json:"name"`
	Path       string     `json:"path"`
	Provenance Provenance `json:"provenance"`
	// Text is the file verbatim, frontmatter included: a person reading
	// SKILL.md is reading the file on disk, not the body the assistant is
	// given. It is empty whenever Refusal is set, because half a refused file
	// is neither the file nor a refusal.
	Text     string      `json:"text"`
	Refusal  FileRefusal `json:"refusal"`
	MaxBytes int         `json:"maxBytes"`
	// Findings are the static scan's matches over EXACTLY the bytes in Text,
	// so the line each names is a line of what is on screen and a viewer can
	// mark it where it sits rather than restating it underneath.
	//
	// IT IS SCANNED HERE, in the read, and that is the whole reason this
	// field exists rather than the viewer asking the audit. An audit spends a
	// model call, and a person opening a support file to look at it must not
	// have to buy a model reading to learn that a line in it matched
	// (nocx-872jc.4). The scan is pure, local and already run on every other
	// path a skill's bytes travel; running it here is one capability in a
	// third place, not a fourth answer to what a finding is.
	//
	// A REFUSED FILE HAS NONE, and that is a fact about bytes rather than a
	// policy: nothing was read, so nothing was scanned. It is never nil —
	// no matches is [] — and the emptiness of it says nothing about the file
	// either way, which is why a viewer must not draw an all-clear from it.
	Findings []Finding `json:"findings"`
}

// File answers with one file of one discovered skill. It answers for ANY
// provenance, builtin included: reading is not writing, and the person may
// read what the assistant reads.
func File(roots []Root, name, relPath string) (FileResult, error) {
	// Naming a file is the whole of this request, so an empty path is a
	// refusal rather than a default. Read defaults to SKILL.md because a tool
	// asking for "the skill" means its body; a person clicked on something.
	if strings.TrimSpace(relPath) == "" {
		return FileResult{}, errors.New("no file was named, and this reads one file of a skill")
	}
	at, err := locate(roots, name, relPath, true)
	if err != nil {
		return FileResult{}, err
	}

	// One byte past the budget is how the size is learned, rather than a
	// stat: a stat needs one branch for a directory root and another for the
	// embedded one, and answers about the file as it was before the read
	// rather than about the bytes actually obtained. Reading MaxReadBytes+1
	// settles "is this over the budget" for every root at once, and costs one
	// byte.
	data, err := readRootFile(at.skill.root, at.entry, at.path, MaxReadBytes+1)
	if err != nil {
		return FileResult{}, fmt.Errorf("skill %q path %q: %w", name, relPath, err)
	}

	out := FileResult{
		Name:       at.skill.Name,
		Path:       at.path,
		Provenance: at.skill.Provenance,
		MaxBytes:   MaxReadBytes,
		Findings:   []Finding{},
	}
	switch {
	case len(data) > MaxReadBytes:
		// Asked BEFORE the text check, because an over-long file is over-long
		// whatever its bytes decode to, and reporting a 40 MiB archive as
		// "not text" would name the less useful of two true facts.
		out.Refusal = FileRefusalTooLarge
	case !utf8.Valid(data):
		out.Refusal = FileRefusalNotText
	default:
		out.Text = string(data)
		// The RESOLVED path, not the requested one, for the reason Name and
		// Path are resolved above: the finding is about the file that was
		// read, and a viewer marks the bytes it is showing.
		out.Findings = Scan(at.path, data)
	}
	return out, nil
}
