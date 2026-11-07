package update

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/shady2k/nocx/internal/version"
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
	return bundleID{Dev: uint64(stat.Dev), Ino: stat.Ino}, nil //nolint:unconvert,gosec // stat.Dev is int32 on darwin (conversion required; device id, overflow benign), uint64 on linux
}

func (b bundleID) isZero() bool          { return b == zeroID }
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
	SchemaVersion int      `json:"schemaVersion"`
	TxID          string   `json:"txID"`
	InstallPath   string   `json:"installPath"`
	OldBundleID   bundleID `json:"oldBundleID"`
	NewBundleID   bundleID `json:"newBundleID"` // zeroID until the staged swap file exists
	FromVersion   string   `json:"fromVersion"`
	ToVersion     string   `json:"toVersion"`
	// ToBuild is the identity in the signed manifest, recorded before any
	// download or exchange. Missing identities in older journals remain
	// readable for rollback, but cannot certify a build by version alone.
	ToBuild version.BuildIdentity `json:"toBuild"`
	// FromBuild is the identity of the build that STARTED the transaction —
	// what the log lines need to tell one nightly from the next when both sides
	// carry the same version (ADR-79b6cy61, nocx-vh2hr.1.25). It is recorded
	// here rather than re-read from the running process because the rollback
	// runs in a LATER launch, whose own build is not the one being replaced.
	// Absent in journals written before the field existed, and the log then
	// says "unknown" rather than inventing a build.
	FromBuild      version.BuildIdentity `json:"fromBuild"`
	ArtifactSHA256 string                `json:"artifactSHA256"`
	// LaunchAttempts counts starts of the build this transaction installed:
	// how many times a process running it reached startup without certifying
	// health. §7.5 rolls back on the third.
	//
	// LaunchAttemptsOwnedBy names the build that wrote the count
	// (nocx-vh2hr.1.20). A journal is the one file two builds share across an
	// update, and the build being REPLACED can write it too — the build before
	// nocx-vh2hr.1.19 reconciled on every state read, so one window spent two
	// launches with no restart at all. Those are not starts of the installed
	// build, and a count attributed to another build is discarded rather than
	// acted on. A record without the field was written by a build that did not
	// have it; its count is attributed to nobody and is discarded the same way.
	// The zero value means exactly that, and an older binary ignores the field
	// — which is why the schema version is unchanged: it refuses a version it
	// does not know, and it must go on reading what this one writes.
	LaunchAttempts        int                   `json:"launchAttempts"`
	LaunchAttemptsOwnedBy version.BuildIdentity `json:"launchAttemptsOwnedBy"`

	// HeldByCoordinator records that a healthy window reported against a
	// protocol-compatible OLDER coordinator kept under ADR-0101 (new
	// launches take over instead, ADR-0104),
	// and names that coordinator's version. HeldAt is when the report
	// arrived; every such report also resets LaunchAttempts, because the
	// one thing the counter measures — a build that cannot get healthy
	// enough to report — is disproven each time. Zero values mean no hold
	// has been recorded (older journals unmarshal to exactly that).
	HeldByCoordinator string    `json:"heldByCoordinator,omitempty"`
	HeldAt            time.Time `json:"heldAt,omitempty"`
}

const journalSchemaVersion = 1

// journalPath returns the transaction journal in the profile's data directory.
func journalPath(stateDir string) string {
	return filepath.Join(stateDir, ".nocx-update-journal.json")
}

// legacyJournalPath is where a build from before the placement split kept the
// journal: beside the installed bundle, which on Linux inside an AppImage is a
// read-only mount. It is read once, adopted into the state directory and then
// removed on a best effort — see reconcileLocked.
func legacyJournalPath(installPath string) string {
	return filepath.Join(filepath.Dir(installPath), ".nocx-update-journal.json")
}

// readJournal reads and parses the journal, or returns nil if the
// journal file does not exist.
func readJournal(path string) (*journalRecord, error) {
	f, err := os.Open(path) //nolint:gosec // path is constructed from the configured profile state directory
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open journal %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

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
	f, err := os.Create(tmpPath) //nolint:gosec // path is constructed from journalPath (caller-controlled)
	if err != nil {
		return fmt.Errorf("create journal tmp %s: %w", tmpPath, err)
	}

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err = enc.Encode(r); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("encode journal: %w", err)
	}

	// fsync the data to disk.
	if err = f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("fsync journal tmp: %w", err)
	}
	if err = f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close journal tmp: %w", err)
	}

	// Atomic rename.
	if err = os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename journal tmp → journal: %w", err)
	}

	// fsync the directory so the rename is durable.
	dir, err := os.Open(filepath.Dir(path)) //nolint:gosec // path is constructed from journalPath
	if err != nil {
		return fmt.Errorf("open journal dir for fsync: %w", err)
	}
	_ = dir.Sync()
	_ = dir.Close()

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
