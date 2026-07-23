package update

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ---------------------------------------------------------------------------
// Bundle identity — device + inode, per §7.1
// ---------------------------------------------------------------------------

// bundleID identifies a filesystem object by device and inode. This
// survives renames, symlinks, and macOS /Applications vs
// /System/Volumes/Data/Applications aliases — a version string cannot
// do this job, and neither can a path string (§7.1 rationale).
type bundleID struct {
	Dev uint64 `json:"dev"`
	Ino uint64 `json:"ino"`
}

// zeroID is the sentinel for "not yet observed".
var zeroID = bundleID{}

// statBundleID returns the device+inode for the given path, or zeroID
// if the path does not exist (os.IsNotExist).
func statBundleID(path string) (bundleID, error) {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return zeroID, nil
		}
		return zeroID, err
	}
	stat, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return zeroID, fmt.Errorf("unsupported filesystem stat for %s", path)
	}
	return bundleID{Dev: uint64(stat.Dev), Ino: stat.Ino}, nil
}

func (b bundleID) isZero() bool   { return b == zeroID }
func (b bundleID) equal(o bundleID) bool { return b == o }

// ---------------------------------------------------------------------------
// Journal record — per §7.1
// ---------------------------------------------------------------------------

// journalRecord records a transaction's intent and the identities of
// the bundles involved. Reconciliation observes which identity sits at
// which path and derives state from that — the record never claims
// what happened.
//
// The schema version is included so future format changes can be
// recognised and a legible error produced instead of guessing.
type journalRecord struct {
	SchemaVersion  int       `json:"schemaVersion"`
	TxID           string    `json:"txID"`
	InstallPath    string    `json:"installPath"`
	OldBundleID    bundleID  `json:"oldBundleID"`
	NewBundleID    bundleID  `json:"newBundleID"` // zeroID until the staged swap file exists
	FromVersion    string    `json:"fromVersion"`
	ToVersion      string    `json:"toVersion"`
	ArtifactSHA256 string    `json:"artifactSHA256"`
	LaunchAttempts int       `json:"launchAttempts"`
}

const journalSchemaVersion = 1

// journalPath returns the path to the journal file for the given
// install path. The journal lives in the same directory as the
// installed bundle.
func journalPath(installPath string) string {
	return filepath.Join(filepath.Dir(installPath), ".nocx-update-journal.json")
}

// readJournal reads and parses the journal, or returns nil if the
// journal file does not exist.
func readJournal(path string) (*journalRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open journal %s: %w", path, err)
	}
	defer f.Close()

	var r journalRecord
	if err := json.NewDecoder(f).Decode(&r); err != nil {
		return nil, fmt.Errorf("parse journal %s: %w", path, err)
	}

	if r.SchemaVersion != journalSchemaVersion {
		return nil, fmt.Errorf("journal %s has unknown schema version %d (expected %d) — the journal must be removed manually to recover", path, r.SchemaVersion, journalSchemaVersion)
	}

	return &r, nil
}

// writeJournal atomically writes the journal record.
func writeJournal(path string, r *journalRecord) error {
	r.SchemaVersion = journalSchemaVersion

	tmpPath := path + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create journal tmp %s: %w", tmpPath, err)
	}

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("encode journal: %w", err)
	}

	// fsync the data to disk.
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("fsync journal tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close journal tmp: %w", err)
	}

	// Atomic rename.
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename journal tmp → journal: %w", err)
	}

	// fsync the directory so the rename is durable.
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open journal dir for fsync: %w", err)
	}
	dir.Sync()
	dir.Close()

	return nil
}

// deleteJournal removes the journal file. It is not an error if the
// file does not exist.
func deleteJournal(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove journal %s: %w", path, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Managed paths
// ---------------------------------------------------------------------------

// swapPath returns the path to the swap peer used for Exchange.
func swapPath(installPath string) string {
	dir := filepath.Dir(installPath)
	return filepath.Join(dir, ".nocx-swap.app")
}

// backupPath returns the path to the retained known-good backup.
func backupPath(installPath string) string {
	dir := filepath.Dir(installPath)
	return filepath.Join(dir, ".nocx-backup.app")
}

// extractionDir returns the path to the extraction directory for the
// given transaction ID.
func extractionDir(installPath, txID string) string {
	return filepath.Join(filepath.Dir(installPath), ".nocx-update-"+txID)
}
