package agentdriver_test

// THE CORPUS'S EXPECTATIONS ARE DATA (nocx-nru89.3).
//
// A fixed moment's expected state used to be a Go test per moment. It is now one
// entry in testdata/captures/manifest.json, read through internal/agentcapture,
// so recording a new Claude version adds entries rather than tests, and a
// moment nobody could record says so as an unverified entry instead of
// disappearing. Tests that PAINT onto a replayed screen, check extraction, or
// check explanation behaviour stay tests: those are not "this moment is this
// state".

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/shady2k/nocx/internal/agentcapture"
	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/log"
)

const manifestPath = "testdata/captures/manifest.json"

// manifest is the owner's labels for the corpus. Inventory is the list of
// moments the design says must be covered (spec §6.4); every inventory moment
// has exactly one entry, recorded or unverified. Entries with no Moment are
// further recorded marks the corpus keeps as regressions.
type manifest struct {
	Version   int             `json:"version"`
	Agent     string          `json:"agent"`
	Inventory []string        `json:"inventory"`
	Entries   []manifestEntry `json:"entries"`
}

type manifestEntry struct {
	Moment     string            `json:"moment,omitempty"`
	Capture    string            `json:"capture,omitempty"`
	AtMs       *int64            `json:"atMs,omitempty"`
	State      agentdriver.State `json:"state,omitempty"`
	Branch     *int              `json:"branch,omitempty"`
	Unverified string            `json:"unverified,omitempty"`
	Note       string            `json:"note,omitempty"`
}

func (e manifestEntry) recorded() bool { return e.Unverified == "" }

func (e manifestEntry) String() string {
	if !e.recorded() {
		return fmt.Sprintf("moment %q (unverified)", e.Moment)
	}
	at := int64(-1)
	if e.AtMs != nil {
		at = *e.AtMs
	}
	return fmt.Sprintf("%s@%dms", e.Capture, at)
}

// loadManifest reads the manifest and refuses one that cannot be trusted,
// naming why. It checks the manifest's own shape only; whether the rule agrees
// is checkManifest's.
func loadManifest(path string) (manifest, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // a fixture path the test names
	if err != nil {
		return manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m manifest
	if err := dec.Decode(&m); err != nil {
		return manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if m.Version != 1 {
		return manifest{}, fmt.Errorf("manifest version %d, want 1", m.Version)
	}
	if m.Agent == "" {
		return manifest{}, errors.New("manifest names no agent")
	}
	inventory := make(map[string]int, len(m.Inventory))
	for _, id := range m.Inventory {
		if id == "" {
			return manifest{}, errors.New("manifest inventory has an empty moment id")
		}
		if _, dup := inventory[id]; dup {
			return manifest{}, fmt.Errorf("manifest inventory names %q twice", id)
		}
		inventory[id] = 0
	}
	for i, e := range m.Entries {
		if e.Moment != "" {
			if _, ok := inventory[e.Moment]; !ok {
				return manifest{}, fmt.Errorf("entry %d names moment %q, which is not in the inventory", i, e.Moment)
			}
			inventory[e.Moment]++
		}
		if !e.recorded() {
			if e.Moment == "" {
				return manifest{}, fmt.Errorf("entry %d is unverified but names no moment", i)
			}
			if e.Capture != "" || e.AtMs != nil || e.State != "" || e.Branch != nil {
				return manifest{}, fmt.Errorf("entry %d is unverified and also names a recording", i)
			}
			continue
		}
		switch {
		case e.Capture == "" || e.AtMs == nil:
			return manifest{}, fmt.Errorf("entry %d is recorded but names no capture and mark", i)
		case *e.AtMs < 0:
			return manifest{}, fmt.Errorf("entry %d (%s) has a negative mark", i, e)
		case !e.State.Valid():
			return manifest{}, fmt.Errorf("entry %d (%s) names state %q, which is not a driver state", i, e, e.State)
		case e.State == agentdriver.StateExited:
			return manifest{}, fmt.Errorf("entry %d (%s) names exited, which is a fact about the process and never a screen", i, e)
		}
	}
	for _, id := range m.Inventory {
		switch n := inventory[id]; n {
		case 0:
			return manifest{}, fmt.Errorf("inventory moment %q has no entry", id)
		case 1:
		default:
			return manifest{}, fmt.Errorf("inventory moment %q has %d entries, want exactly one", id, n)
		}
	}
	return m, nil
}

// checkManifest replays every recorded entry and asks the registry, returning
// one error per failure or disagreement. Entries are grouped by capture and
// replayed in mark order, because a capture only replays forward.
func checkManifest(m manifest, dir string, reg *agentdriver.Registry) []error {
	byCapture := map[string][]manifestEntry{}
	var names []string
	for _, e := range m.Entries {
		if !e.recorded() {
			continue
		}
		if _, seen := byCapture[e.Capture]; !seen {
			names = append(names, e.Capture)
		}
		byCapture[e.Capture] = append(byCapture[e.Capture], e)
	}
	sort.Strings(names)
	var errs []error
	for _, name := range names {
		entries := byCapture[name]
		sort.SliceStable(entries, func(i, j int) bool { return *entries[i].AtMs < *entries[j].AtMs })
		header, chunks, err := agentcapture.Read(filepath.Join(dir, name+".jsonl"))
		if err != nil {
			errs = append(errs, fmt.Errorf("capture %s: %w", name, err))
			continue
		}
		marks := make([]int64, len(entries))
		for i, e := range entries {
			marks[i] = *e.AtMs
		}
		moments, err := agentcapture.Frames(log.NewSlogAdapter(nil), header, chunks, marks)
		if err != nil {
			errs = append(errs, fmt.Errorf("replay %s: %w", name, err))
			continue
		}
		for i, e := range entries {
			ex := reg.Explain(m.Agent, moments[i].Frame)
			if ex.State != e.State {
				errs = append(errs, fmt.Errorf("%s: state %q, want %q", e, ex.State, e.State))
			}
			if e.Branch != nil && ex.Matched != *e.Branch {
				errs = append(errs, fmt.Errorf("%s: matched branch %d, want %d", e, ex.Matched, *e.Branch))
			}
		}
	}
	return errs
}

// TestTheManifestHolds is the rule check of nocx-nru89: every recorded moment
// of the corpus classifies to the owner's state.
func TestTheManifestHolds(t *testing.T) {
	m, err := loadManifest(manifestPath)
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	for _, err := range checkManifest(m, filepath.Dir(manifestPath), registry(t)) {
		t.Error(err)
	}
}
