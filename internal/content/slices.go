package content

// nonNil normalises a nil slice to an empty one and leaves every other slice
// untouched. It exists because JSON round-trips through this package in both
// directions (marshal on the way in, unmarshal on the way out) and Go's
// encoding/json marshals a nil slice as `null` while unmarshalling an empty
// JSON array back into a non-nil empty slice — so without this, a value that
// went in nil comes back as [], which is exactly the asymmetry a
// reflect.DeepEqual round trip cannot tolerate. Called at both ends (before
// marshal AND after unmarshal) makes the two agree regardless of which side
// happened to hand over nil.
//
// One generic in place of a same-shaped function per slice type:
// api_run_sqlite.go's nonNilStrings, nonNilSpans, nonNilHeaders and
// nonNilCertificates, and skill_check_sqlite.go's
// nonNilSkillCheckOmissions/nonNilSkillCheckFindings, were six copies of
// these three lines with only the element type changed.
//
// It lives here rather than in either feature file it started in: it is
// called from api_run_sqlite.go AND skill_check_sqlite.go, and a shared
// normaliser addressed by whichever feature file happened to define it first
// means deleting that feature later takes the other one's round trip with
// it. A file with no feature of its own is the address a helper with two
// unrelated callers should have.
func nonNil[T any](in []T) []T {
	if in == nil {
		return []T{}
	}
	return in
}
