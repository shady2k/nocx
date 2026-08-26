package agenttools

// The descriptor digest (nocx-d6gn4.9): a short, content-addressed name for
// the VERSION of a tool a call was made against.
//
// The program-carrier experiment compares two cohorts across many runs. If a
// tool's description, schema, effect or resource kinds change halfway through,
// the two halves stop being comparable — and nothing recorded today would say
// so, which is the failure where a measurement quietly answers a different
// question than the one asked.
//
// A hand-maintained version field is the wrong shape for this. It is bumped by
// remembering to, and the occasion that matters is the occasion somebody
// forgot; a number that can disagree with the thing it names is worse than no
// number, because it is believed. A digest over the declaration's own content
// cannot drift from the declaration.
//
// WHAT GOES IN, and why each: the name, the description and the params schema,
// because those are what the MODEL is shown and they steer what it calls; and
// the effect and the resource kinds, because those are what the POLICY decides
// on — a call whose effect class changed means something different, whatever
// the model saw. What stays out is everything with no bearing on either: the
// execution site, the capability constructor, and whether the call opens a
// block are how the call is CARRIED OUT, not what it is.

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strconv"
)

// descriptorDigestLen is how much of the hash is kept. Twelve hex characters
// is short enough to read in a record and far past collision for a table of
// five rows and its foreseeable growth.
const descriptorDigestLen = 12

// DescriptorDigest names this tool's declaration as the model and the policy
// see it. Equal digests mean two calls were made against the same tool
// version; different digests mean a comparison across them needs saying so.
func (t Tool) DescriptorDigest() string {
	h := sha256.New()
	// Length-prefixed rather than delimited: a description ending in the
	// delimiter would otherwise be able to impersonate a different split of
	// the same bytes, and a digest that can be steered is not an identity.
	write := func(s string) {
		_, _ = io.WriteString(h, strconv.Itoa(len(s)))
		_, _ = io.WriteString(h, ":")
		_, _ = io.WriteString(h, s)
	}
	write(t.Name)
	write(t.Description)
	write(string(t.Effect))
	for _, r := range t.Resources {
		write(string(r))
	}
	write(string(t.ParamsSchema))
	return hex.EncodeToString(h.Sum(nil))[:descriptorDigestLen]
}
