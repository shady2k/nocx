package update

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// ---------------------------------------------------------------------------
// Check
// ---------------------------------------------------------------------------

// Check implements [Updater.Check].
func (u *updater) Check(ctx context.Context) (*UpdateInfo, error) {
	// Refuse dev builds — wails dev must never offer an update.
	if u.currentVersion == "dev" {
		u.log.Debug("update check: dev build, skipping")
		return nil, nil
	}

	// Fetch manifest + signature.
	body, sig, err := u.fetcher.Fetch(ctx)
	if err != nil {
		return nil, fmt.Errorf("update check: fetch manifest: %w", err)
	}

	// Verify signature before parsing.
	m, err := VerifyManifest(body, string(sig), u.keyring)
	if err != nil {
		return nil, fmt.Errorf("update check: manifest verification: %w", err)
	}

	// Semver comparison.
	if !IsNewer(u.currentVersion, m.Version) {
		u.log.Debug("update check: already current",
			"current", u.currentVersion, "remote", m.Version)
		return nil, nil
	}

	// Artefact matching for this platform.
	id := u.platform.ArtifactID()
	art, err := MatchArtifact(m.Artifacts, id)
	if err != nil {
		return nil, fmt.Errorf("update check: no artefact for %s/%s/%s: %w", id.OS, id.Arch, id.Format, err)
	}

	return &UpdateInfo{
		Version:     m.Version,
		NotesURL:    m.NotesURL,
		URL:         art.URL,
		SHA256:      art.SHA256,
		Size:        art.Size,
		manifestRaw: body,
	}, nil
}

// ---------------------------------------------------------------------------
// Apply — §7.4
// ---------------------------------------------------------------------------

// Apply implements [Updater.Apply].
func (u *updater) Apply(ctx context.Context, info *UpdateInfo) error {
	// Step 1: acquire lock.
	lk, err := acquireLock(u.lockPath)
	if err != nil {
		return fmt.Errorf("update apply: %w", err)
	}
	defer lk.release()

	// Step 2: reconcile; refuse if transaction already in flight.
	if err := u.reconcileLocked(ctx); err != nil {
		return fmt.Errorf("update apply: reconcile before apply: %w", err)
	}

	// Abort if there is still a journal — a transaction is in flight.
	jp := journalPath(u.installPath)
	if rec, _ := readJournal(jp); rec != nil {
		return fmt.Errorf("update apply: a previous transaction is still in flight — run Reconcile first")
	}

	// Step 3: preflight check through platform seam.
	if err := u.platform.Preflight(ctx, u.installPath); err != nil {
		return fmt.Errorf("update apply: preflight refused: %w", err)
	}

	// Step 4: write the journal record BEFORE touching the disk.
	// This explains every file this transaction creates so
	// reconciliation can classify debris after a crash.
	oldID, err := statBundleID(u.installPath)
	if err != nil {
		return fmt.Errorf("update apply: stat install bundle: %w", err)
	}
	if oldID.isZero() {
		return fmt.Errorf("update apply: install bundle not found at %s", u.installPath)
	}

	txID := info.Version // simple, unique per release
	rec := &journalRecord{
		TxID:           txID,
		InstallPath:    u.installPath,
		OldBundleID:    oldID,
		FromVersion:    u.currentVersion,
		ToVersion:      info.Version,
		ArtifactSHA256: info.SHA256,
	}
	if err := writeJournal(jp, rec); err != nil {
		return fmt.Errorf("update apply: write journal: %w", err)
	}

	u.log.Info("update apply: journal written, starting download",
		"from", u.currentVersion, "to", info.Version)

	// Step 5: create extraction directory.
	extractDir := extractionDir(u.installPath, txID)
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return fmt.Errorf("update apply: create extraction dir: %w", err)
	}

	// Step 6: download the artefact.
	archivePath := filepath.Join(extractDir, "download")
	if err := u.downloadVerified(ctx, info.URL, info.SHA256, info.Size, archivePath); err != nil {
		// Cleanup on download failure — the journal remains so
		// reconciliation cleans up the extraction dir.
		return fmt.Errorf("update apply: download artefact: %w", err)
	}

	// Step 7: platform extract into a SUBDIR so the archive file
	// (archivePath) is never inside the destination directory.
	// On Linux, Extract copies archivePath → destDir/<basename>;
	// if destDir == filepath.Dir(archivePath) the source and
	// destination are the SAME path — the file is truncated to
	// zero before io.Copy reads it. The subdir prevents that.
	stageDir := filepath.Join(extractDir, "stage")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return fmt.Errorf("update apply: create stage dir: %w", err)
	}
	if err := u.platform.Extract(ctx, archivePath, stageDir); err != nil {
		return fmt.Errorf("update apply: extract: %w", err)
	}

	// Step 8: platform verify.
	extractedBundle := u.extractedBundlePath(stageDir)
	if err := u.platform.VerifyExtracted(ctx, extractedBundle); err != nil {
		return fmt.Errorf("update apply: verify extracted: %w", err)
	}

	// Step 9: rename staged bundle to swap peer, record newBundleID.
	swap := swapPath(u.installPath)
	// Remove any leftover swap from a previous crashed transaction.
	os.RemoveAll(swap)
	if err := os.Rename(extractedBundle, swap); err != nil {
		return fmt.Errorf("update apply: rename staged → swap: %w", err)
	}

	newID, err := statBundleID(swap)
	if err != nil {
		return fmt.Errorf("update apply: stat swap bundle: %w", err)
	}
	rec.NewBundleID = newID
	if err := writeJournal(jp, rec); err != nil {
		return fmt.Errorf("update apply: update journal with newBundleID: %w", err)
	}

	// Step 10: re-verify the installed bundle identity immediately
	// before the exchange.
	currentID, err := statBundleID(u.installPath)
	if err != nil {
		return fmt.Errorf("update apply: pre-exchange identity check: %w", err)
	}
	if !currentID.equal(oldID) {
		return fmt.Errorf("update apply: install bundle changed during update (was %v, now %v) — refusing to exchange", oldID, currentID)
	}

	// Step 11: atomic exchange.
	if err := u.platform.Exchange(ctx, u.installPath, swap); err != nil {
		return fmt.Errorf("update apply: exchange failed: %w", err)
	}

	u.log.Info("update apply: exchange complete — restart to apply",
		"from", u.currentVersion, "to", info.Version)

	// No journal write follows the exchange — after step 11 the
	// identities on disk say unambiguously that it happened.

	// Cleanup the extraction directory (the archive was already
	// consumed; the bundle was moved to swap which is now the old
	// install at swapPath).
	os.RemoveAll(extractDir)

	return nil
}

// extractedBundlePath returns the path to the extracted bundle inside
// the stage directory (a subdirectory of the extraction directory).
// The platform determines the layout.
func (u *updater) extractedBundlePath(stageDir string) string {
	id := u.platform.ArtifactID()
	switch id.OS {
	case "darwin":
		// ditto --keepParent extracts nocx.app into stageDir.
		return filepath.Join(stageDir, "nocx.app")
	case "linux":
		// Extract places the staged file in stageDir with its original
		// name (derived from the archive, which was named "download").
		entries, _ := os.ReadDir(stageDir)
		for _, e := range entries {
			if !e.IsDir() {
				return filepath.Join(stageDir, e.Name())
			}
		}
		return filepath.Join(stageDir, "nocx.AppImage") // fallback
	default:
		return stageDir
	}
}

// ---------------------------------------------------------------------------
// Reconcile — §7.3
// ---------------------------------------------------------------------------

// Reconcile implements [Updater.Reconcile].
func (u *updater) Reconcile(ctx context.Context) error {
	// Try-lock at startup so the terminal never blocks on an
	// in-progress download from another process.
	lk, err := tryLock(ctx, u.lockPath)
	if err != nil {
		return fmt.Errorf("reconcile: lock error: %w", err)
	}
	if lk == nil {
		u.log.Debug("reconcile: another process holds the lock, skipping this launch")
		return nil
	}
	defer lk.release()

	return u.reconcileLocked(ctx)
}

// reconcileLocked performs reconciliation under an already-held lock.
func (u *updater) reconcileLocked(ctx context.Context) error {
	jp := journalPath(u.installPath)
	rec, err := readJournal(jp)
	if err != nil {
		// Unreadable journal — refuse with recovery instructions.
		return fmt.Errorf("reconcile: journal is unreadable or corrupt at %s: %w — remove this file manually to recover", jp, err)
	}
	if rec == nil {
		// No record — check for orphaned managed debris.
		swap := swapPath(u.installPath)
		backup := backupPath(u.installPath)
		if u.pathExists(swap) || u.pathExists(backup) {
			return fmt.Errorf(
				"reconcile: no journal but managed debris exists at %s and/or %s — "+
					"these files may hold a rollback copy. Remove them by hand if they are stale, "+
					"or consult the README rollback procedures.",
				swap, backup,
			)
		}
		// Nothing in flight.
		return nil
	}

	// Check whether the install path still matches. We must handle
	// the linux case where the "bundle" is a single file, and the
	// darwin case where it's a directory — os.Stat works for both.
	currentID, err := statBundleID(u.installPath)
	if err != nil {
		return fmt.Errorf("reconcile: stat install bundle %s: %w", u.installPath, err)
	}

	switch {
	case currentID.isZero():
		// The install path does not exist — the user deleted it.
		return fmt.Errorf("reconcile: install bundle missing at %s — a transaction was in flight but the bundle is gone. Remove %s and consult the README rollback procedures.", u.installPath, jp)

	case currentID.equal(rec.OldBundleID):
		// Exchange did not happen. Clean up transaction debris.
		u.log.Info("reconcile: exchange did not happen, cleaning up",
			"fromVersion", rec.FromVersion, "toVersion", rec.ToVersion)

		swap := swapPath(u.installPath)
		os.RemoveAll(swap)
		extractDir := extractionDir(u.installPath, rec.TxID)
		os.RemoveAll(extractDir)
		if err := deleteJournal(jp); err != nil {
			return fmt.Errorf("reconcile: clear journal: %w", err)
		}
		return nil

	case currentID.equal(rec.NewBundleID):
		// Exchange happened, health unconfirmed — pendingRestart.
		u.log.Info("reconcile: exchange happened, health unconfirmed",
			"fromVersion", rec.FromVersion, "toVersion", rec.ToVersion)

		// Increment launch counter.
		rec.LaunchAttempts++
		if err := writeJournal(jp, rec); err != nil {
			return fmt.Errorf("reconcile: update launch count: %w", err)
		}

		// Auto-rollback after 3 launches without health confirmation.
		if rec.LaunchAttempts >= 3 {
			u.log.Warn("reconcile: auto-rollback triggered after 3 unhealthy launches",
				"toVersion", rec.ToVersion, "attempts", rec.LaunchAttempts)
			return u.rollback(jp, rec)
		}

		u.log.Info("reconcile: waiting for health confirmation",
			"attempt", rec.LaunchAttempts)
		return nil

	default:
		// installPath holds neither oldBundleID nor newBundleID.
		return fmt.Errorf(
			"reconcile: install bundle at %s has unexpected identity %v — "+
				"the journal recorded old=%v new=%v. The bundle was replaced by hand or by another process. "+
				"Remove %s and consult the README rollback procedures.",
			u.installPath, currentID, rec.OldBundleID, rec.NewBundleID, jp,
		)
	}
}

// rollback exchanges the swap bundle back into the install path,
// deleting the journal afterward.
func (u *updater) rollback(jp string, rec *journalRecord) error {
	swap := swapPath(u.installPath)

	// Verify the swap peer still exists and holds newBundleID.
	swapID, err := statBundleID(swap)
	if err != nil {
		return fmt.Errorf("rollback: stat swap bundle: %w", err)
	}
	if swapID.isZero() {
		return fmt.Errorf("rollback: swap bundle missing at %s — cannot roll back automatically. Remove %s and follow the README manual rollback procedure.", swap, jp)
	}

	// Exchange back.
	if err := u.platform.Exchange(context.Background(), u.installPath, swap); err != nil {
		return fmt.Errorf("rollback: exchange back failed: %w", err)
	}

	u.log.Warn("rollback: reverted to previous version", "from", rec.ToVersion, "to", rec.FromVersion)

	// Clean up journal and extraction debris.
	if err := deleteJournal(jp); err != nil {
		return fmt.Errorf("rollback: clear journal: %w", err)
	}
	extractDir := extractionDir(u.installPath, rec.TxID)
	os.RemoveAll(extractDir)

	return nil
}

// ---------------------------------------------------------------------------
// ReportHealthy — §7.5
// ---------------------------------------------------------------------------

// ReportHealthy implements [Updater.ReportHealthy].
func (u *updater) ReportHealthy(ctx context.Context) error {
	lk, err := acquireLock(u.lockPath)
	if err != nil {
		return fmt.Errorf("report healthy: lock: %w", err)
	}
	defer lk.release()

	jp := journalPath(u.installPath)
	rec, err := readJournal(jp)
	if err != nil {
		return fmt.Errorf("report healthy: read journal: %w", err)
	}
	if rec == nil {
		// No record — nothing to finalise, not an error.
		return nil
	}

	currentID, err := statBundleID(u.installPath)
	if err != nil {
		return fmt.Errorf("report healthy: stat install bundle: %w", err)
	}

	// Must hold newBundleID — only finalise if the exchange actually happened.
	if !currentID.equal(rec.NewBundleID) {
		return nil // exchange not yet applied, nothing to report
	}

	// Finalisation (idempotent):
	// 1. Delete old backup (if exists and not the current newID).
	backup := backupPath(u.installPath)
	backupID, _ := statBundleID(backup)
	if !backupID.isZero() && !backupID.equal(rec.NewBundleID) {
		os.RemoveAll(backup)
	}

	// 2. Rename swap (which now holds old bundle) to backup.
	swap := swapPath(u.installPath)
	if u.pathExists(swap) {
		os.RemoveAll(backup)
		if err := os.Rename(swap, backup); err != nil {
			u.log.Warn("report healthy: rename swap→backup failed", "error", err)
		}
	}

	// 3. Delete journal — its absence marks completion.
	if err := deleteJournal(jp); err != nil {
		return fmt.Errorf("report healthy: clear journal: %w", err)
	}

	u.log.Info("report healthy: update finalised", "version", rec.ToVersion)
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func (u *updater) pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

