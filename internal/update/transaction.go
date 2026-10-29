package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shady2k/nocx/internal/log"
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
	fetched, err := u.fetcher.Fetch(ctx)
	if err != nil {
		return nil, fmt.Errorf("update check: fetch manifest: %w", err)
	}

	// Verify signature before parsing.
	m, err := VerifyManifest(fetched.Body, string(fetched.Signature), u.keyring)
	if err != nil {
		return nil, fmt.Errorf("update check: manifest verification: %w", err)
	}

	// Stable stays semver-only. Nightly also admits a later distinct build
	// within the same valid version; it never admits a downgrade.
	if !IsNewer(u.currentVersion, m.Version) && !u.newerNightly(fetched.Channel, m) {
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
		Version:  m.Version,
		NotesURL: m.NotesURL,
		URL:      art.URL,
		SHA256:   art.SHA256,
		Size:     art.Size,
		build:    m.Build,
	}, nil
}

// ---------------------------------------------------------------------------
// Apply — §7.4
// ---------------------------------------------------------------------------

// Apply implements [Updater.Apply].
func (u *updater) Apply(ctx context.Context, info *UpdateInfo) error {
	// Refusals must not leave even a lock or journal behind.
	if err := u.platform.Preflight(ctx, u.installPath); err != nil {
		return fmt.Errorf("update apply: preflight refused: %w", err)
	}
	if u.stateDir == "" {
		return errors.New("update apply: state directory is not configured")
	}
	if err := os.MkdirAll(u.stateDir, 0o750); err != nil {
		return fmt.Errorf("update apply: create state directory: %w", err)
	}
	// Step 1: acquire lock.
	lk, err := acquireLock(u.lockPath)
	if err != nil {
		return fmt.Errorf("update apply: %w", err)
	}
	defer func() { _ = lk.release() }()

	// Step 2: reconcile; refuse if transaction already in flight.
	if err = u.reconcileLocked(ctx); err != nil {
		return fmt.Errorf("update apply: reconcile before apply: %w", err)
	}

	// Abort if there is still a journal — a transaction is in flight.
	jp := journalPath(u.stateDir)
	if rec, _ := readJournal(jp); rec != nil {
		return fmt.Errorf("update apply: a previous transaction is still in flight — run Reconcile first")
	}

	// Step 4: write the journal record BEFORE touching the disk.
	// This explains every file this transaction creates so
	// reconciliation can classify debris after a crash.
	oldID, err := statBundleID(u.installPath)
	if err != nil {
		return fmt.Errorf("update apply: stat install bundle: %w", err)
	}

	txID := info.Version // simple, unique per release
	rec := &journalRecord{
		TxID:           txID,
		InstallPath:    u.installPath,
		OldBundleID:    oldID,
		FromVersion:    u.currentVersion,
		ToVersion:      info.Version,
		ToBuild:        info.build,
		ArtifactSHA256: info.SHA256,
	}
	if err = writeJournal(jp, rec); err != nil {
		return fmt.Errorf("update apply: write journal: %w", err)
	}

	u.log.Info("update apply: journal written, starting download",
		"from", u.currentVersion, "to", info.Version)

	// Step 5: create extraction directory.
	extractDir := extractionDir(u.installPath, txID)
	if err = os.MkdirAll(extractDir, 0o750); err != nil {
		return fmt.Errorf("update apply: create extraction dir: %w", err)
	}

	// Step 6: download the artefact.
	archivePath := filepath.Join(extractDir, "download")
	if err = u.downloadVerified(ctx, info.URL, info.SHA256, info.Size, archivePath); err != nil {
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
	if err = os.MkdirAll(stageDir, 0o750); err != nil {
		return fmt.Errorf("update apply: create stage dir: %w", err)
	}
	if err = u.platform.Extract(ctx, archivePath, stageDir); err != nil {
		return fmt.Errorf("update apply: extract: %w", err)
	}

	// Step 8: platform verify.
	extractedBundle := u.extractedBundlePath(stageDir)
	if err = u.platform.VerifyExtracted(ctx, extractedBundle); err != nil {
		return fmt.Errorf("update apply: verify extracted: %w", err)
	}

	// Step 9: rename staged bundle to swap peer, record newBundleID.
	swap := swapPath(u.installPath)
	// Remove any leftover swap from a previous crashed transaction.
	_ = os.RemoveAll(swap)
	if err = os.Rename(extractedBundle, swap); err != nil {
		return fmt.Errorf("update apply: rename staged → swap: %w", err)
	}

	newID, err := statBundleID(swap)
	if err != nil {
		return fmt.Errorf("update apply: stat swap bundle: %w", err)
	}
	rec.NewBundleID = newID
	if err = writeJournal(jp, rec); err != nil {
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

	// A first installation has no exchange peer. Rename is still atomic
	// because staging is on the target filesystem.
	if oldID.isZero() {
		err = os.Rename(swap, u.installPath)
	} else {
		err = u.platform.Exchange(ctx, u.installPath, swap)
	}
	if err != nil {
		return fmt.Errorf("update apply: exchange failed: %w", err)
	}

	u.log.Info("update apply: exchange complete — restart to apply",
		"from", u.currentVersion, "to", info.Version)

	// No journal write follows the exchange — after step 11 the
	// identities on disk say unambiguously that it happened.

	// Cleanup the extraction directory (the archive was already
	// consumed; the bundle was moved to swap which is now the old
	// install at swapPath).
	_ = os.RemoveAll(extractDir)

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
	if u.stateDir == "" {
		return errors.New("reconcile: state directory is not configured")
	}
	if err := os.MkdirAll(u.stateDir, 0o750); err != nil {
		return fmt.Errorf("reconcile: create state directory: %w", err)
	}
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
	defer func() { _ = lk.release() }()

	return u.reconcileLocked(ctx)
}

// reconcileLocked performs reconciliation under an already-held lock.
func (u *updater) reconcileLocked(ctx context.Context) error {
	if u.stateDir == "" {
		return errors.New("reconcile: state directory is not configured")
	}
	jp := journalPath(u.stateDir)
	rec, err := readJournal(jp)
	if err != nil {
		return fmt.Errorf("reconcile: journal is unreadable or corrupt at %s: %w — remove this file manually to recover", jp, err)
	}
	if rec == nil {
		// A transaction begun by a build that kept its journal beside the
		// install path is still a transaction: the placement split moved the
		// journal into the profile's data directory (ADR-0106), and an update
		// that was in flight when the person upgraded must be settled rather
		// than read as debris. Adoption is read-then-write, so a read-only
		// install directory costs nothing: the old copy is removed on a best
		// effort, and the state directory's copy is authoritative from here.
		legacy := legacyJournalPath(u.installPath)
		if lrec, lerr := readJournal(legacy); lerr == nil && lrec != nil {
			if werr := writeJournal(jp, lrec); werr != nil {
				return fmt.Errorf("reconcile: adopt the journal at %s: %w", legacy, werr)
			}
			_ = deleteJournal(legacy)
			u.log.Info("reconcile: adopted a journal left by the previous layout",
				"fromVersion", lrec.FromVersion, "toVersion", lrec.ToVersion)
			rec = lrec
		}
	}
	if rec == nil {
		swap, backup := swapPath(u.installPath), backupPath(u.installPath)
		if u.pathExists(swap) || u.pathExists(backup) {
			return fmt.Errorf("reconcile: no journal but managed debris exists at %s and/or %s — these files may hold a rollback copy. Remove them by hand if they are stale, or consult the README rollback procedures.", swap, backup)
		}
		return nil
	}
	currentID, err := statBundleID(rec.InstallPath)
	if err != nil {
		return fmt.Errorf("reconcile: stat install bundle %s: %w", rec.InstallPath, err)
	}
	switch {
	case currentID.isZero() && !rec.OldBundleID.isZero():
		return fmt.Errorf("reconcile: install bundle missing at %s — a transaction was in flight but the bundle is gone. Remove %s and consult the README rollback procedures.", rec.InstallPath, jp)
	case currentID.equal(rec.OldBundleID):
		u.log.Info("reconcile: exchange did not happen, cleaning up", "fromVersion", rec.FromVersion, "toVersion", rec.ToVersion)
		_ = os.RemoveAll(swapPath(rec.InstallPath))
		_ = os.RemoveAll(extractionDir(rec.InstallPath, rec.TxID))
		if err := deleteJournal(jp); err != nil {
			return fmt.Errorf("reconcile: clear journal: %w", err)
		}
		return nil
	case currentID.equal(rec.NewBundleID):
		// A healthy window on a kept compatible coordinator is not a failed
		// launch. Preserve the rollback copy until the installed pair runs.
		if rec.HeldByCoordinator != "" {
			u.log.Info("reconcile: update held by the running coordinator", "heldByVersion", rec.HeldByCoordinator, "toVersion", rec.ToVersion)
			return nil
		}
		rec.LaunchAttempts++
		if err := writeJournal(jp, rec); err != nil {
			return fmt.Errorf("reconcile: update launch count: %w", err)
		}
		if rec.LaunchAttempts >= 3 {
			return u.rollback(ctx, jp, rec)
		}
		u.log.Info("reconcile: waiting for health confirmation", "attempt", rec.LaunchAttempts)
		return nil
	default:
		return fmt.Errorf("reconcile: install bundle at %s has unexpected identity %v — the journal recorded old=%v new=%v. The bundle was replaced by hand or by another process. Remove %s and consult the README rollback procedures.", rec.InstallPath, currentID, rec.OldBundleID, rec.NewBundleID, jp)
	}
}

// State implements [Updater.State]: what a window can tell a person about
// the transaction in flight. Read-only by construction — it opens the
// journal, derives, closes.
//
// The derivation is deliberately the same one reconcileLocked makes: a
// journal exists AND the install path holds the bundle the transaction
// staged. A journal whose exchange never happened is debris Reconcile is
// about to clean, not pending work a person should be told about.
func (u *updater) State(ctx context.Context) UpdateState {
	jp := journalPath(u.stateDir)
	rec, err := readJournal(jp)
	if err != nil || rec == nil {
		return UpdateState{}
	}
	currentID, err := statBundleID(rec.InstallPath)
	if err != nil || !currentID.equal(rec.NewBundleID) {
		return UpdateState{}
	}
	return UpdateState{
		Pending:                true,
		Version:                rec.ToVersion,
		HeldByOlderCoordinator: rec.HeldByCoordinator != "",
	}
}

// rollback exchanges the swap bundle back into the install path,
// deleting the journal afterward.
func (u *updater) rollback(ctx context.Context, jp string, rec *journalRecord) error {
	if rec.OldBundleID.isZero() {
		return errors.New("rollback: this was a first installation, with no previous target to restore; launch the original copy or install a known-good release")
	}
	swap := swapPath(rec.InstallPath)

	// Verify the swap peer still exists and holds newBundleID.
	swapID, err := statBundleID(swap)
	if err != nil {
		return fmt.Errorf("rollback: stat swap bundle: %w", err)
	}
	if swapID.isZero() {
		return fmt.Errorf("rollback: swap bundle missing at %s — cannot roll back automatically. Remove %s and follow the README manual rollback procedure.", swap, jp)
	}

	// Exchange back.
	if err := u.platform.Exchange(context.Background(), rec.InstallPath, swap); err != nil {
		return fmt.Errorf("rollback: exchange back failed: %w", err)
	}

	log.From(ctx).Warn("rollback: reverted to previous version", "from", rec.ToVersion, "to", rec.FromVersion,
		"fromCommit", rec.ToBuild.Commit, "fromBuiltAt", rec.ToBuild.BuiltAt)

	// Clean up journal and extraction debris.
	if err := deleteJournal(jp); err != nil {
		return fmt.Errorf("rollback: clear journal: %w", err)
	}
	extractDir := extractionDir(rec.InstallPath, rec.TxID)
	_ = os.RemoveAll(extractDir)

	return nil
}

// ---------------------------------------------------------------------------
// ReportHealthy — §7.5
// ---------------------------------------------------------------------------

// ReportHealthy implements [Updater.ReportHealthy].
//
// Finalisation deletes the rollback journal, so everything that decides
// whether this build is trustworthy has to happen BEFORE the first
// destructive step below. The order is: is a transaction in flight, did
// the exchange happen, and — the pair check, D4 — is the software now
// running actually the software that was installed.
func (u *updater) ReportHealthy(ctx context.Context) error {
	if u.stateDir == "" {
		return errors.New("report healthy: state directory is not configured")
	}
	if err := os.MkdirAll(u.stateDir, 0o750); err != nil {
		return fmt.Errorf("report healthy: create state directory: %w", err)
	}
	lk, err := acquireLock(u.lockPath)
	if err != nil {
		return fmt.Errorf("report healthy: lock: %w", err)
	}
	defer func() { _ = lk.release() }()

	jp := journalPath(u.stateDir)
	rec, err := readJournal(jp)
	if err != nil {
		return fmt.Errorf("report healthy: read journal: %w", err)
	}
	if rec == nil {
		// No record — nothing to finalise, not an error.
		return nil
	}

	currentID, err := statBundleID(rec.InstallPath)
	if err != nil {
		return fmt.Errorf("report healthy: stat install bundle: %w", err)
	}

	// Must hold newBundleID — only finalise if the exchange actually happened.
	if !currentID.equal(rec.NewBundleID) {
		return nil // exchange not yet applied, nothing to report
	}

	// The pair check. From here on the journal is about to be deleted and
	// automatic rollback disarmed, so this is the last moment at which
	// being wrong is still recoverable.
	if err := u.certifyPair(ctx, rec); err != nil {
		if errors.Is(err, ErrPairHeld) {
			// The held pair (ADR-0101): certifyPair has recorded the hold
			// ON the record — writing is the caller's, like every other
			// write here — and the journal must now carry it, so the next
			// launches reconcile against a hold rather than a silence.
			if wErr := writeJournal(jp, rec); wErr != nil {
				return fmt.Errorf("report healthy: recording the held pair: %w", wErr)
			}
		}
		return err
	}

	// Finalisation (idempotent):
	// 1. Delete old backup (if exists and not the current newID).
	backup := backupPath(rec.InstallPath)
	backupID, _ := statBundleID(backup)
	if !backupID.isZero() && !backupID.equal(rec.NewBundleID) {
		_ = os.RemoveAll(backup)
	}

	// 2. Rename swap (which now holds old bundle) to backup.
	swap := swapPath(rec.InstallPath)
	if u.pathExists(swap) {
		_ = os.RemoveAll(backup)
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

// certifyPair requires the reporting window's version AND build identity to
// match the signed target recorded in the journal. The coordinator must match
// the version, not the identity: ADR-0104 permits a compatible same-version
// coordinator to be kept across nightly builds. It never deletes, renames or
// writes anything — its whole job is to say no before the caller does.
//
// TWO halves, and each was its own hole.
//
// The UI half: installPath holding newBundleID says the bundle on disk was
// exchanged, and says nothing whatever about which binary is executing.
// The process that CALLED Apply is still the old one, still running from
// the swapped-out inode, and it satisfies that identity check the instant
// Apply returns — so the pre-restart app could finalise its own update and
// delete the journal without any new code having run at all.
//
// The server half is D4's: with a detached coordinator, the old daemon
// survives the swap and answers the new window (design §4, ADR-0007's
// health gate). Version N+1's renderer mounts against version N's backend,
// reports healthy, and rollback is disarmed for a release whose server has
// never started.
//
// A refusal here is not a failed update. The bundle is installed and the
// journal survives, so the next launch counts again and reconciliation
// rolls back on the third — which is exactly what should happen to a
// version that cannot get its own two halves onto the same build.
func (u *updater) certifyPair(ctx context.Context, rec *journalRecord) error {
	if u.currentVersion != rec.ToVersion {
		u.log.Warn("report healthy: refused — the reporting build is not the installed version",
			"reporting", u.currentVersion, "installed", rec.ToVersion)
		return fmt.Errorf(
			"report healthy: %w: this build reports %s but the update installed %s — "+
				"health is reported by the NEW build after a restart, not by the one that applied the update; "+
				"the rollback journal is left intact",
			ErrPairMismatch, u.currentVersion, rec.ToVersion)
	}

	if _, valid := buildTime(rec.ToBuild); !valid {
		return fmt.Errorf("report healthy: %w: the journal has no complete installed build identity; "+
			"the rollback journal is left intact — restart to roll back or install a known-good release",
			ErrPairUnverifiable)
	}
	if u.currentBuild != rec.ToBuild {
		log.From(ctx).Warn("report healthy: refused — the reporting identity is not the installed build",
			"reportingCommit", u.currentBuild.Commit, "reportingBuiltAt", u.currentBuild.BuiltAt,
			"installedCommit", rec.ToBuild.Commit, "installedBuiltAt", rec.ToBuild.BuiltAt)
		return fmt.Errorf("report healthy: %w: this window reports %s built at %s but the update installed %s built at %s — "+
			"restart into the installed build; the rollback journal is left intact",
			ErrPairMismatch, u.currentBuild.Commit, u.currentBuild.BuiltAt, rec.ToBuild.Commit, rec.ToBuild.BuiltAt)
	}

	if u.coordinator == nil {
		u.log.Warn("report healthy: refused — no coordinator probe is wired",
			"installed", rec.ToVersion)
		return fmt.Errorf(
			"report healthy: %w: no coordinator probe was wired into the updater, so the backend "+
				"answering this window cannot be identified; the rollback journal is left intact",
			ErrPairUnverifiable)
	}

	build, err := u.coordinator.AnsweringCoordinator(ctx)
	if err != nil {
		u.log.Warn("report healthy: refused — the coordinator could not be identified",
			"installed", rec.ToVersion, "error", err)
		return fmt.Errorf("report healthy: %w: %w; the rollback journal is left intact",
			ErrPairUnverifiable, err)
	}

	if build.Version != rec.ToVersion {
		if build.Compatible {
			// An already attached ADR-0101 pair can still report this
			// hold; new launches take over across Versions (ADR-0104).
			// The UI reporting against the older coordinator is
			// healthy by the act of reporting, and the only thing the
			// update waits for is the coordinator's turnover. Record the
			// hold ON the record and reset the launch counter — the
			// counter exists to catch a build that cannot get healthy
			// enough to report, and this one just did. Writing the record
			// is the caller's, like every write here; certification is
			// still refused, because the new server has not run.
			rec.HeldByCoordinator = build.Version
			rec.HeldAt = time.Now()
			rec.LaunchAttempts = 0
			u.log.Info("report healthy: the update is held by a kept coordinator",
				"coordinator", build.Version, "coordinatorCommit", build.Commit,
				"installed", rec.ToVersion)
			return fmt.Errorf(
				"report healthy: %w: the coordinator answering is nocx %s and this update installed %s — "+
					"the pair is compatible and the update finishes when the coordinator turns over; "+
					"restart nocx fully to finish it now, or keep working",
				ErrPairHeld, build.Version, rec.ToVersion)
		}
		u.log.Warn("report healthy: refused — a mixed pair answered",
			"coordinator", build.Version, "coordinatorCommit", build.Commit,
			"installed", rec.ToVersion)
		return fmt.Errorf(
			"report healthy: %w: the coordinator answering reports %s and this update installed %s — "+
				"the old backend survived the swap and must be replaced before this version can be "+
				"certified; the rollback journal is left intact",
			ErrPairMismatch, build.Version, rec.ToVersion)
	}

	u.log.Info("report healthy: pair certified",
		"version", rec.ToVersion, "coordinatorCommit", build.Commit)
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func (u *updater) pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
