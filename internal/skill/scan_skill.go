package skill

// The static scan's own answer for one discovered skill, by file
// (nocx-4m1n1) — the second half of "which files does the live scan mark",
// arriving separately from the manifest so the manifest never waits for it.
//
// WHY TWO CALLS AND NOT ONE. A first version folded this into Files, so a
// person's file list could not render until every file in the bundle had
// been read and scanned — trading the fan-out this exists to replace for a
// stall on exactly the bundles the fan-out hurt: a large, slow-to-read
// directory now blocked the tab's primary navigation from appearing at all.
// skills.files stays a bare `readdir`, unchanged, so the list renders
// immediately and is navigable while this call is still in flight — a
// person can pick a file and read it before the scan answers.
//
// WHY A COUNT AND NOT A LINE NUMBER. A line number is the file VIEW's fact:
// skills.file rescans the file itself, at the moment it is opened
// (file.go:115), and that is the only scan whose line numbers are checkable
// against the bytes on screen. A count taken here, from a walk at a
// DIFFERENT moment, asserting a specific line would be a claim this call
// cannot back — the file may have moved between the two reads. A count
// carries none of that claim.
//
// WHAT IT COUNTS. `Scan` (scan.go) reports AT MOST ONE finding per PATTERN
// per file — its first occurrence, not every line the pattern matches — so
// Count is the number of DISTINCT PATTERNS that matched at least once, not
// the number of matching lines. Changing that would mean changing `Scan`
// itself, which skills.file and skills.audit also call; their own findings
// (and the file view's line numbers) would move too, which is exactly the
// "keep skills.file unchanged" this call was built beside.
type ScanMatch struct {
	Path string `json:"path"`
	// Count is always >= 1: a path with no match is not one of these, ever
	// — see ScanResult.Matches.
	Count int `json:"count"`
}

// ScanResult is one skill's live scan: which of its files matched, how many
// lines each one matched, which files the scan could not read at all, and
// which files it DID read cleanly.
type ScanResult struct {
	// Name and Provenance are the skill as RESOLVED by root precedence, for
	// FileResult's reason: a viewer labels what it is describing rather than
	// what it asked for, and the two differ exactly when two roots hold one
	// name.
	Name       string     `json:"name"`
	Provenance Provenance `json:"provenance"`
	// Read names every file THIS SCAN actually read and examined, in
	// manifest order. IT IS WHAT MAKES "ABSENT FROM MATCHES MEANS CLEAN"
	// TRUE BY CONSTRUCTION RATHER THAN BY TIMING. ScanSkill walks the
	// skill's directory itself, at its own moment — a moment that can differ
	// from whatever walk produced the file list a viewer is holding (a file
	// created in between). Without this field, a path present in a viewer's
	// list but absent from both Matches and Omitted would be silently
	// indistinguishable from "scanned, clean" — the exact defect this
	// result exists to prevent, arriving through a race instead of a
	// fan-out. A viewer checks Read (or Omitted) before ever drawing a path
	// as clear; a path in neither is simply not yet known. Never nil: a
	// skill none of whose files could be read has Read == [].
	Read []string `json:"read"`
	// Matches names every file with at least one match. A path ABSENT from
	// this list is either clean (present in Read, nothing matched) or
	// skipped (see Omitted) — telling those two apart, and telling either
	// apart from "not yet known" (absent from Read too), is why a viewer
	// must check Read and Omitted before drawing any file as clear. Never
	// nil: no matches is [].
	Matches []ScanMatch `json:"matches"`
	// Omitted names every file the scan could NOT read, and why — too
	// large, not text, unreadable now, or the shared scan budget already
	// spent by an earlier file in manifest order. THIS IS THE FIELD THAT
	// KEEPS A SKIPPED FILE FROM LOOKING CLEAN: a file named here has no
	// entry in Matches by construction, which is otherwise indistinguishable
	// from a file the scan looked at and found nothing in (file.go:88 says
	// the same of skills.file's own empty Findings). Never nil: nothing
	// omitted is [].
	Omitted []AuditOmission `json:"omitted"`
	// MaxBytes is the shared budget the scan across every file was measured
	// against — the same number and the same meaning as skills.audit's own
	// MaxBytes, so a sentence about a budget-spent omission can name it.
	MaxBytes int `json:"maxBytes"`
}

// ScanSkill answers the live scan for every file of one discovered skill. It
// spends no model call and reads no more than skills.audit already would —
// it reuses scanBundle (audit.go), the same bounded read-and-scan loop, and
// discards the composed Document nobody here needs. It answers for ANY
// provenance and for a skill that is switched OFF, for Files' reason: a
// skill that is off is precisely the one design §8 needs this look at.
//
// A skill no root holds is an ERROR and not an empty result, for file.go's
// reason: there is nothing to describe, so every field of a result would be
// an invention.
func ScanSkill(roots []Root, name string) (ScanResult, error) {
	manifest, err := Files(roots, name)
	if err != nil {
		return ScanResult{}, err
	}
	// Resolution happened inside Files; this second locate is the handle on
	// the same skill's root and entry, which is what the reads are joined
	// onto — Audit's own reasoning for the same second call.
	at, err := locate(roots, name, "", true)
	if err != nil {
		return ScanResult{}, err
	}
	read, omitted, findings, _, _ := scanBundle(at.skill.root, at.entry, manifest.Files)
	return ScanResult{
		Name:       manifest.Name,
		Provenance: manifest.Provenance,
		Read:       read,
		Matches:    aggregateMatches(findings),
		Omitted:    omitted,
		MaxBytes:   MaxAuditBytes,
	}, nil
}

// aggregateMatches collapses per-pattern findings into a per-file count of
// how many distinct patterns matched (see ScanMatch's own comment for why
// that is what Count means), in the order each file first matched — which,
// because scanBundle walks the manifest in order, is manifest order
// restricted to the files that matched. Never nil: no matches is [].
func aggregateMatches(findings []Finding) []ScanMatch {
	out := []ScanMatch{}
	index := map[string]int{}
	for _, f := range findings {
		if i, ok := index[f.Path]; ok {
			out[i].Count++
			continue
		}
		index[f.Path] = len(out)
		out = append(out, ScanMatch{Path: f.Path, Count: 1})
	}
	return out
}
