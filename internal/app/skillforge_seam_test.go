package app

// A TEST SEAM FOR THE SKILL FORGE.
//
// It lives in a _test.go file because that is where an option nothing in the
// product passes belongs: the dead-code ratchet runs deadcode WITHOUT -test on
// purpose (.githooks/check-deadcode.mjs), so a production file carrying a
// test-only constructor option is a new unreachable function on every
// platform, and the baseline may only shrink. The real forge endpoints are
// the defaults; only a test needs to point them at a stub.

// WithSkillForge names the endpoints where the resolver finds repository
// metadata and raw files. Empty values retain the production GitHub defaults.
func WithSkillForge(apiBase, rawBase string) Option {
	return func(o *optionSet) {
		o.forgeAPIBase = apiBase
		o.forgeRawBase = rawBase
	}
}
