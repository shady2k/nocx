package agentcalib

// The labelled set: what a calibration produces, and the only artifact the
// rest of the design reads.
//
// # It holds MARKS, not frames
//
// A set is a capture and one mark in it per label. The frame a person produced
// is derived by replaying the capture to the mark, never stored beside it —
// one owner of one truth rather than two that agree until the day they do not.
// It also means the set is inspectable with a command a person already has:
//
//	go run ./cmd/agent-capture replay -at 0,1,2 <set>/capture.jsonl

import (
	"fmt"

	"github.com/shady2k/nocx/internal/agentcapture"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panegrid"
)

// Record is one step's outcome: the label, and either the mark it sits at or
// the fact that the person declined to produce it.
//
// A declined step is written down rather than dropped, and the difference is
// load-bearing. Absent means nobody was ever asked — a state added to the walk
// after this set was taken. Skipped means the person was asked and said no.
// Both are uncalibrated and both fall to unknown, but only one of them is a
// decision, and a surface that cannot tell them apart cannot offer to fill the
// gap in.
type Record struct {
	Label Label `json:"label"`
	// AtMs is the mark in the capture. A POINTER, and that is not fussiness:
	// the first captured label sits at mark zero, and an omitempty int64
	// wrote it out as absent — indistinguishable on disk from the skipped
	// label that genuinely has no mark. Measured on a real set before this
	// was a pointer.
	AtMs *int64 `json:"atMs,omitempty"`
	// Skipped is true for a state the person was asked for and declined.
	Skipped bool `json:"skipped,omitempty"`
}

// Set is a calibration's whole output.
type Set struct {
	Agent  string
	Header agentcapture.Header
	Chunks []agentcapture.Chunk
	// Labels is one record per step the person was ASKED, in the order they
	// were asked.
	Labels []Record
}

// LabelledFrame is one labelled screen, replayed.
type LabelledFrame struct {
	Label Label
	AtMs  int64
	Frame panegrid.Frame
}

// Complete reports whether the set carries every required label with a frame
// behind it.
//
// It is COMPUTED from the labels present and never stored, and that is what
// makes the bead's second falsifier structural: a set arrives from a file a
// person can edit, so a stored "complete" flag would be a claim rather than a
// fact, and a set that lost its asks-you label to an editor would go on
// claiming it could verify a rule against a state it no longer holds.
func (s Set) Complete() bool {
	for _, step := range steps {
		if !step.Required {
			continue
		}
		if !s.Calibrated(step.Label) {
			return false
		}
	}
	return true
}

// Calibrated reports whether this label has a frame behind it. A skipped or
// never-asked label is not calibrated, which is what makes its state fall to
// unknown — busy, and so a refusal rather than a wrong answer.
func (s Set) Calibrated(l Label) bool {
	rec, ok := s.Record(l)
	return ok && !rec.Skipped && rec.AtMs != nil
}

// Record returns what the set says about a label, and false when the person
// was never asked for it.
func (s Set) Record(l Label) (Record, bool) {
	for _, rec := range s.Labels {
		if rec.Label == l {
			return rec, true
		}
	}
	return Record{}, false
}

// Frames replays the capture and hands back each labelled screen, in label
// order. A skipped label contributes nothing, because nothing was captured
// for it.
func (s Set) Frames(lg log.Logger) ([]LabelledFrame, error) {
	marks := make([]int64, 0, len(s.Labels))
	labelled := make([]Record, 0, len(s.Labels))
	for _, rec := range s.Labels {
		if rec.Skipped {
			continue
		}
		if rec.AtMs == nil {
			return nil, fmt.Errorf(
				"agentcalib: %s was captured and carries no mark, so there is no moment to replay to",
				rec.Label)
		}
		if len(marks) > 0 && *rec.AtMs < marks[len(marks)-1] {
			return nil, fmt.Errorf(
				"agentcalib: %s marks %dms, which is before the label before it; a capture replays forward only",
				rec.Label, *rec.AtMs)
		}
		marks = append(marks, *rec.AtMs)
		labelled = append(labelled, rec)
	}
	moments, err := agentcapture.Frames(lg, s.Header, s.Chunks, marks)
	if err != nil {
		return nil, err
	}
	out := make([]LabelledFrame, 0, len(moments))
	for i, m := range moments {
		out = append(out, LabelledFrame{Label: labelled[i].Label, AtMs: m.AtMs, Frame: m.Frame})
	}
	return out, nil
}
