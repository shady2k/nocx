// Package masking owns detection for the two consumers of the ONE recognizer
// (ADR-0021, design §7.1): the durable path that masks what it stores, and
// the egress gate that refuses what a finding would send to a provider.
// Both call here; neither re-implements detection — one recognizer, two
// policies (nocx-a21v, nocx-0p7y2).
//
// The recognizer itself stays in internal/secrets, factored the way ADR-0021
// already split it: Detect (what looks like a secret) beside Mask (what we do
// about it). This package is the service both policies call, so a detection
// contract added here — the fail-closed belt below — lands once.
//
// The fail-closed belt is the service's contract: the recognizer's failure
// mode is a panic, and a caller that lets a panic escape ships the raw text —
// a raw command to a durable row, a raw tool result to a model provider. The
// guard converts a panic into an error the caller fails closed on.
package masking

import (
	"fmt"

	"github.com/shady2k/nocx/internal/secrets"
)

// Detect returns the findings for secret-shaped regions of text — a
// submitted command, a tool result, an error string — through the ONE
// recognizer. A detection failure is an error; the caller must fail closed
// rather than let the text continue.
func Detect(s string) (findings []secrets.Finding, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("detection panicked: %v", r)
		}
	}()
	return secrets.Detect(s), nil
}

// MaskWithSegments is the durable shape of a masking pass: the masked text
// plus, per finding, the segment describing exactly that replacement (span
// in the masked string, mask head/tail). It is the shape the durable row
// keeps, and the same fail-closed contract as Detect.
func MaskWithSegments(s string) (masked string, findings []secrets.Finding, segs []secrets.Segment, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("detection panicked: %v", r)
		}
	}()
	masked, findings, segs = secrets.MaskWithSegments(s)
	return masked, findings, segs, nil
}
