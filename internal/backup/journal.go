package backup

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/storage"
)

const (
	journalDocName = "backup-restore-journal.json"
	journalVersion = 1
)

// restoreJournal is the on-disk crash-recovery journal.
// Absent document = idle. Unknown version/state = recovery error.
type restoreJournal struct {
	Version     int                         `json:"version"`
	State       string                      `json:"state"` // "idle" | "prepared" | "committed"
	Connections *profile.ConnectionSnapshot `json:"connections,omitempty"`
	Settings    *map[string]any             `json:"settings,omitempty"`
}

// journalState is the in-memory representation.
type journalState struct {
	state       string
	connections *profile.ConnectionSnapshot
	settings    *map[string]any
}

// readJournal loads the journal document. Missing → idle.
func readJournal(doc storage.DocumentStore) (journalState, error) {
	var j restoreJournal
	found, err := doc.Read(journalDocName, &j)
	if err != nil {
		return journalState{}, fmt.Errorf("read journal: %w", err)
	}
	if !found {
		return journalState{state: "idle"}, nil
	}

	if j.Version != journalVersion {
		return journalState{}, fmt.Errorf("%w: unknown journal version %d", ErrRecoveryRequired, j.Version)
	}

	js := journalState{state: j.State}

	switch j.State {
	case "idle":
		// No payload expected; ignore any.
	case "prepared", "committed":
		if j.Connections == nil || j.Settings == nil {
			return journalState{}, fmt.Errorf("%w: %s journal missing snapshot", ErrRecoveryRequired, j.State)
		}
		js.connections = j.Connections
		js.settings = j.Settings
	default:
		return journalState{}, fmt.Errorf("%w: unknown journal state %q", ErrRecoveryRequired, j.State)
	}

	return js, nil
}

// writeJournal persists the journal. Passing nil writes "idle" without payload.
func writeJournal(doc storage.DocumentStore, state string, conn *profile.ConnectionSnapshot, settings *map[string]any) error {
	var j restoreJournal
	j.Version = journalVersion
	j.State = state
	if state == "prepared" || state == "committed" {
		if conn == nil || settings == nil {
			return errors.New("prepared/committed journal requires non-nil snapshots")
		}
		j.Connections = conn
		j.Settings = settings
	}
	b, err := json.Marshal(j)
	if err != nil {
		return fmt.Errorf("marshal journal: %w", err)
	}
	return doc.Write(journalDocName, json.RawMessage(b))
}

// cleanupJournal attempts to write idle. Failure is logged but not fatal.
func cleanupJournal(doc storage.DocumentStore) {
	_ = writeJournal(doc, "idle", nil, nil)
}
