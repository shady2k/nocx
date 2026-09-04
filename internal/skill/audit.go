package skill

// What an audit READS (design §7).
//
// An audit is asked for by the person, about a skill they already hold, and
// produces a reading they act on themselves. This file owns the half of it
// that is bytes: which files of the bundle go to the model, in what order,
// under what budget, and what the static scan matched in each of them. The
// model call is internal/assistant's; the sentence a person reads is the
// surface's. Nothing here decides anything about the skill.
//
// WHY IT IS A FUNCTION HERE AND NOT A COMPOSITION IN THE TRANSPORT. The
// transport can already reach Files and File, so it could have walked the
// manifest and read each path itself. That would be a second answer to "what
// is this skill made of" living beside Files' — they would agree on every
// bundle anybody tried and disagree the first time a symlink, an unreadable
// directory or the 256-file cap turned up, because only one of them would
// have been taught about it. Files is the walk; this reuses it.
//
// THE BUDGET, and why it is reported rather than silently applied. A bundle
// can be anything a person copied into the directory, and the whole of it
// goes into a model's context if nothing bounds it — which is money, and on a
// large enough directory is a call that fails rather than answers. So the
// document is bounded, and every file that did not make it is NAMED with the
// reason, because a report about a subset the reader cannot identify is worse
// than no report: it reads exactly like a report about the whole thing.

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxAuditBytes bounds the composed document one audit sends to a model.
//
// The number is a cost bound, not a safety one. 128 KiB is roughly 30k tokens
// of somebody else's prose in one call the person pressed a button for; the
// bundles this feature was built for — a SKILL.md and a handful of
// `references/` files — are one to two orders of magnitude under it, so the
// cap refuses nothing anybody has actually published while putting a ceiling
// on the directory somebody drops a vendored dependency tree into. Per FILE
// the bound is MaxReadBytes, which is the same ceiling the person's own
// viewer applies: a file the card will not show whole is not a file the
// audit should describe whole either.
const MaxAuditBytes = 128 << 10

// AuditOmissionReason names why one file of a bundle is not in the document.
// The set is closed and the wire declares it.
//
// It is NOT FileRefusal, though two of the three words are the same.
// FileRefusal is a fact about one file asked for on its own — this is a PNG,
// this is bigger than the budget — and a viewer states it beside that file's
// own name. "The budget was already spent" is not a fact about the file at
// all; it is a fact about the file's POSITION in a bundle, and the same file
// asked for alone would be shown without complaint. Widening FileRefusal to
// carry it would put a value in skills.file's closed union that skills.file
// can never return, which is a contract that lies about its own range.
type AuditOmissionReason string

const (
	// AuditOmittedTooLarge means the file alone is over MaxReadBytes.
	AuditOmittedTooLarge AuditOmissionReason = "too-large"
	// AuditOmittedNotText means the bytes are not UTF-8. They are named
	// rather than transliterated: a report describing replacement runes
	// would be describing something nobody wrote.
	AuditOmittedNotText AuditOmissionReason = "not-text"
	// AuditOmittedBudgetSpent means the document was already full when this
	// file's turn came. Manifest order decides who is inside the budget, so
	// this always falls on the tail of the list and never on SKILL.md.
	AuditOmittedBudgetSpent AuditOmissionReason = "budget-spent"
	// AuditOmittedUnreadable means the file is named by the manifest and
	// could not be read now — it was deleted, or its permissions changed,
	// between the walk and the read. Naming it keeps the manifest and the
	// document reconcilable; dropping it silently would make the document
	// look like the whole skill.
	AuditOmittedUnreadable AuditOmissionReason = "unreadable"
)

// AuditOmission is one file the document does not carry, and why.
type AuditOmission struct {
	Path   string              `json:"path"`
	Reason AuditOmissionReason `json:"reason"`
}

// AuditMaterial is one skill's bytes as an audit reads them: what was read,
// what was left out, what the scan matched, and the single document those
// bytes were composed into.
//
// It carries no judgement and no field a surface could count into one. Every
// member is either a fact about the REQUEST (which skill, which root), a fact
// about what was READ, or the scan's own output — and the scan is advisory by
// construction (scan.go) and has been since before this feature existed.
type AuditMaterial struct {
	// Name and Provenance are the skill as RESOLVED by root precedence, for
	// FileResult's reason: a reader labels what it is describing rather than
	// what was asked for, and the two differ exactly when two roots hold one
	// name.
	Name       string     `json:"name"`
	Provenance Provenance `json:"provenance"`
	// Read are the paths whose bytes are in Document, in manifest order.
	Read []string `json:"read"`
	// Omitted are the paths that are not, each with its reason. Never nil.
	Omitted []AuditOmission `json:"omitted"`
	// Findings are the scan's matches over EXACTLY the bytes in Document —
	// a file that was omitted was not read, so it contributes none. Each
	// names the file it matched in, because a line number counted through a
	// document made of four files points at nothing a person can open; that
	// path is a field of Finding itself (scan.go) rather than a wrapper this
	// package puts round one, so the audit's findings and the preview's are
	// the same shape. Never nil: no matches is [].
	Findings []Finding `json:"findings"`
	// Document is what a model is given. It is not on the wire — the person
	// reads the files through skills.file, which is the same bytes without a
	// second copy of them crossing the socket.
	Document string `json:"-"`
	// MaxBytes is the budget the composition was measured against, so the
	// sentence about a cut can name the number that made it rather than
	// keeping a second copy of it.
	MaxBytes int `json:"maxBytes"`
}

// auditFileHeader marks where one file's bytes begin in the composed
// document.
//
// It is a LABEL and not a boundary. A skill can write this exact line into
// its own text and make the document look as though a fifth file started
// there; nothing here can stop that, and the audit does not depend on it
// being unforgeable — the report is prose a person reads next to the file
// list, not a parse. Saying so here is the point: the alternative considered
// was a random per-call delimiter, which would buy the appearance of a
// boundary in a place where the real defence is that the model is told it is
// reading a document and the result changes nothing.
func auditFileHeader(path string) string {
	return "----- file: " + path + " -----\n"
}

// Audit composes one skill's bundle for a reading. It answers for ANY
// provenance and for a skill that is switched OFF, because a skill that is
// off is precisely the one this exists for: design §8 lands an installed
// skill inert so the person can look at it, and an audit that refused an off
// skill would make the look it exists for impossible.
//
// A skill no root holds is an ERROR and not an empty document, for file.go's
// reason: there is nothing to describe, so every field of a result would be
// an invention — and an empty report reads exactly like a clean one.
func Audit(roots []Root, name string) (AuditMaterial, error) {
	manifest, err := Files(roots, name)
	if err != nil {
		return AuditMaterial{}, err
	}
	// Resolution happened inside Files; this second locate is the handle on
	// the same skill's root and entry, which is what the reads are joined
	// onto. It cannot disagree with the first — both go through locate, and
	// locate is the one answer to root precedence and containment.
	at, err := locate(roots, name, "", true)
	if err != nil {
		return AuditMaterial{}, err
	}

	out := AuditMaterial{
		Name:       manifest.Name,
		Provenance: manifest.Provenance,
		Read:       []string{},
		Omitted:    []AuditOmission{},
		Findings:   []Finding{},
		MaxBytes:   MaxAuditBytes,
	}
	// The files the manifest could not name are already outside the document
	// and outside the person's view, so the cut is carried forward as an
	// omission with the reason the manifest gave it.
	var doc strings.Builder
	for _, path := range manifest.Files {
		// One byte past the per-file budget, file.go's trick: it settles "is
		// this over" for a directory root and the embedded one at once, and
		// costs one byte.
		data, readErr := readRootFile(at.skill.root, at.entry, path, MaxReadBytes+1)
		switch {
		case readErr != nil:
			out.Omitted = append(out.Omitted, AuditOmission{Path: path, Reason: AuditOmittedUnreadable})
			continue
		case len(data) > MaxReadBytes:
			// Asked before the text check for file.go's reason: an over-long
			// file is over-long whatever its bytes decode to, and reporting a
			// 40 MiB archive as "not text" names the less useful of two true
			// facts.
			out.Omitted = append(out.Omitted, AuditOmission{Path: path, Reason: AuditOmittedTooLarge})
			continue
		case !utf8.Valid(data):
			out.Omitted = append(out.Omitted, AuditOmission{Path: path, Reason: AuditOmittedNotText})
			continue
		}
		header := auditFileHeader(path)
		if doc.Len()+len(header)+len(data)+1 > MaxAuditBytes {
			out.Omitted = append(out.Omitted, AuditOmission{Path: path, Reason: AuditOmittedBudgetSpent})
			continue
		}
		doc.WriteString(header)
		doc.Write(data)
		doc.WriteByte('\n')
		out.Read = append(out.Read, path)
		out.Findings = append(out.Findings, Scan(path, data)...)
	}
	if len(out.Read) == 0 {
		// Every file was refused, which for a discovered skill means SKILL.md
		// itself could not be read — the file discovery just parsed. There is
		// no document, so there is nothing to send and nothing to describe.
		return AuditMaterial{}, fmt.Errorf("skill %q: none of its files could be read", manifest.Name)
	}
	out.Document = doc.String()
	return out, nil
}
