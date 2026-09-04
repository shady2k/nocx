package skill

// Installing a skill by its URL, the half that writes (design §5 step 6, §6).
//
// Install is Preview's sibling and deliberately not a flag on it: preview
// shows a person the exact bytes, install adopts them. What joins the two is
// the only interesting decision in this file — HOW INSTALL KNOWS WHICH BYTES
// THE PERSON APPROVED.
//
// The params are one URL and nothing else, so the client cannot tell the
// backend what it read. That is the point rather than an omission: a body
// that made a round trip through the renderer is a body that could have
// changed on the way back, and the digest recorded here must be over the
// bytes actually written. So Preview keeps, ON THE SERVER, the digest of the
// document it showed; Install fetches the URL a SECOND time and compares
// against that. The client's URL SELECTS the record and can never SUPPLY one
// — there is no field in which a caller could assert what the bytes were,
// which is the whole of what makes the comparison worth making.
//
// If the second fetch answers with something else, the document changed
// between being read and being approved, and the install refuses: the person
// approved the first document and this is not it.
//
// THE INTERVAL THIS FILE EXISTS TO HOLD (design §10): from the moment bytes
// exist on disk, the skill is either recorded with BOTH its digest and its
// source, or it is absent. It matters because an installed skill with no
// matching digest is `changed`, and a changed skill is dropped from the
// prompt index entirely (write.go) — installed, listed, and never used. So a
// document write that fails after the file lands is UNDONE rather than
// reported and left: a fresh install's file is removed, and an update's
// previous bytes are put back, because for an update "absent" would be the
// wrong end state — the person still has the version they approved before.
//
// Where even the undo fails, the fail-closed digest is the backstop and the
// refusal says so: the skill is `changed`, which Settings shows as changed
// with an Approve beside it, and the assistant is never offered it. That is
// the same state a crash between the two writes would leave, which is why it
// is stated rather than hidden — it is the one arm of the interval that
// cannot be closed by code, only made visible.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/shady2k/nocx/internal/profile"
)

// InstallResult is what a person's approval produced. Provenance is a constant
// — an installed skill's root is the installed root and nothing else can put
// it there — and it travels anyway, so the row the renderer draws gets its
// kind from the same answer every other skill's row does.
type InstallResult struct {
	Name       string     `json:"name"`
	Provenance Provenance `json:"provenance"`
}

// Install adopts the document at rawURL, which the person has just read.
//
// It re-runs the whole pipeline over a second fetch rather than trusting
// anything about the first: the bytes are re-fetched, compared against what
// was shown, re-parsed, re-scanned, re-checked for a name it may not take,
// and only then written.
func (s *Store) Install(ctx context.Context, rawURL string) (InstallResult, error) {
	if s == nil {
		return InstallResult{}, errUnavailable
	}
	if s.fetcher == nil {
		return InstallResult{}, errors.New("installing a skill from a URL is unavailable: this backend has no fetch seam wired")
	}
	if s.installedDir == "" {
		return InstallResult{}, errors.New("installing a skill from a URL is unavailable: this backend has no installed skill root")
	}
	if err := profile.ValidateBaseURL(rawURL); err != nil {
		return InstallResult{}, fmt.Errorf("that is not an address a skill can be installed from: %w", err)
	}

	// Nothing is installed that has not been read. The order of the pipeline
	// IS the argument (design §5), and this is the step that enforces it from
	// the server rather than trusting the dialog to have shown anything.
	approved, read := s.approvedPreview(rawURL)
	if !read {
		return InstallResult{}, errors.New(
			"nothing has been read from that address in this session, so there is nothing to install: " +
				"read the document first, then install what you read")
	}

	text, err := s.fetchDocument(ctx, rawURL)
	if err != nil {
		return InstallResult{}, err
	}

	// The same parse and the same scan, over the bytes about to be written.
	// It is the SAME FUNCTION the preview ran rather than a second copy of
	// the pipeline, which is what makes "re-runs the pipeline" true and not
	// merely claimed.
	//
	// It happens BEFORE the digest comparison now, because the body is what
	// names the support files and the comparison is over the whole bundle.
	// Parsing bytes that have not yet been shown to match is not a new
	// exposure: the preview parsed them under the same conditions, this
	// function reaches nothing, and NOTHING IS WRITTEN until the comparison
	// below has passed.
	document, err := documentPreview(text, rawURL)
	if err != nil {
		return InstallResult{}, err
	}
	files, err := s.fetchBundle(ctx, rawURL, document.Body)
	if err != nil {
		return InstallResult{}, err
	}
	if digestOfBundle(text, files) != approved {
		return InstallResult{}, errors.New(
			"what is at that address is no longer what you read, so nothing was installed: " +
				"the document or one of the files it refers to has changed since — read it again and decide " +
				"about the version that is there now")
	}

	unlock, err := s.lockName(document.Name)
	if err != nil {
		return InstallResult{}, err
	}
	defer unlock()

	if rootErr := requireWritableRoot(s.installedDir, "installed skill root"); rootErr != nil {
		return InstallResult{}, rootErr
	}
	// Asked again under the lock, because the preview's answer was given
	// before it and the disk is allowed to have moved.
	update, err := s.planInstall(document.Name, rawURL)
	if err != nil {
		return InstallResult{}, err
	}

	// prepareSkill is what every SKILL.md this product writes goes through,
	// so the file that lands has the frontmatter discovery expects and
	// nothing else. Re-serialising rather than storing the fetched bytes
	// verbatim also drops any other frontmatter key the document carried,
	// including a `provenance:` one — content cannot forge which directory it
	// sits in, and it does not get to try through a field either (skill.go).
	_, data, err := prepareSkill(document.Name, document.Description, document.Body)
	if err != nil {
		return InstallResult{}, err
	}

	dir, target, err := pathsUnder(s.installedDir, document.Name)
	if err != nil {
		return InstallResult{}, err
	}
	if err := s.checkDirectory(s.installedDir, dir); err != nil {
		return InstallResult{}, err
	}
	// The snapshot is what an undo puts back, and it is the WHOLE DIRECTORY
	// rather than the previous SKILL.md: a bundle is several writes, and an
	// undo that restored one file would leave the other half of a replaced
	// bundle beside a restored document. For a fresh install it is empty,
	// which is exactly right — undoing then means removing everything that
	// landed.
	var previous map[string][]byte
	if update {
		if err := s.checkExistingPath(s.installedDir, dir, target, true); err != nil {
			return InstallResult{}, err
		}
		snapshot, readErr := snapshotSkillDirectory(dir)
		if readErr != nil {
			return InstallResult{}, fmt.Errorf("skill %q: read the version being replaced: %w", document.Name, readErr)
		}
		previous = snapshot
	} else {
		previous = map[string][]byte{}
	}
	if err := s.fs.MkdirAll(dir, managedSkillDirMode); err != nil {
		return InstallResult{}, fmt.Errorf("skill %q: create directory: %w", document.Name, err)
	}
	// The interval opens on the FIRST byte writeBundle lands and closes when
	// the record is written on the line after it, or is undone. A failure
	// part-way through the bundle is inside the interval exactly as a failed
	// record is, and takes the same undo — which is why the write and the
	// record share one error path here rather than each having its own.
	if err := s.writeBundle(document.Name, dir, data, files); err != nil {
		return InstallResult{}, s.undoInstall(document.Name, dir, previous, err)
	}
	if err := s.recordApprovalDigest(document.Name, dir, rawURL); err != nil {
		return InstallResult{}, s.undoInstall(document.Name, dir, previous, err)
	}

	// The approval is spent: it was for one document on one occasion, not a
	// standing permission to write those bytes again.
	s.forgetPreview(rawURL)
	// What was adopted, and what the scan had to say about it, in the log the
	// person's machine keeps. A finding is never a refusal; it is also not
	// something that should exist only in a dialog that has since closed.
	//
	// The count is over the WHOLE BUNDLE, the same reckoning the dialog was
	// showing a moment ago. It used to count the document's findings alone,
	// which after nocx-872jc.4 would have written "findings=0" into the
	// durable record for exactly the install whose bundled script matched —
	// a log that disagrees with the dialog it is the record of.
	slog.Info("skill: installed from a URL",
		"skill", document.Name, "url", rawURL,
		"findings", len(document.Findings)+len(scanBundleFiles(files)), "files", len(files)+1)
	return InstallResult{Name: document.Name, Provenance: ProvenanceInstalled}, nil
}

// planInstall answers whether this document may be written under this name
// from this URL, and whether doing so replaces a skill already there.
//
// One owner for the question, asked by the preview and again by the install.
// Every refusal it can give is a refusal the person should read BEFORE they
// read a body they are not allowed to adopt.
func (s *Store) planInstall(name, rawURL string) (bool, error) {
	var held *discovered
	for _, found := range discoverDetailed(s.roots, true) {
		if found.Name == name {
			candidate := found
			held = &candidate
			break
		}
	}
	if held == nil {
		return false, nil
	}

	// Discovery loses a collision with a bare continue, which is defensible
	// for discovery and not for an install: a person who installs a skill and
	// then cannot find it in their list has been told nothing (design §3).
	if held.Provenance != ProvenanceInstalled {
		return false, fmt.Errorf(
			"the name %q is already held by %s, and an installed skill may not shadow it", name, holderPhrase(held.Provenance))
	}

	source, recorded, err := s.recordedSource(name)
	if err != nil {
		return false, err
	}
	if !recorded {
		return false, fmt.Errorf(
			"the name %q is already held by %s with no recorded source, so nocx cannot tell whether this is an update of it: "+
				"remove it first if you mean to install this document", name, holderPhrase(held.Provenance))
	}
	// An update is pinned to the address the skill was installed from and may
	// not be pointed anywhere else. Skill names are not namespaced across
	// sources, so a same-named document elsewhere would silently reassign
	// where this skill came from — the cross-source fallback hermes deleted
	// as unsafe by construction, and the half of design §6 that says an
	// update may never change a skill's provenance.
	if source.URL != rawURL {
		return false, fmt.Errorf(
			"%q was installed from %s, and an update may not change where a skill came from: "+
				"remove it first if you mean to install it from somewhere else", name, source.URL)
	}
	// The person edited the file after installing it. Replacing their work is
	// an explicit answer and never the rerun default (design §6), so this
	// refuses rather than overwriting; approving or removing the skill is how
	// they say which they meant.
	if held.Status == StatusChanged {
		return false, fmt.Errorf(
			"%q has been edited since it was installed, so re-installing it would overwrite those edits: "+
				"approve or remove it first", name)
	}
	return true, nil
}

// undoInstall closes the interval when a write or the record fails after
// bytes have landed, and returns the error the person reads either way.
//
// It never returns nil: the install failed, and the only question this
// answers is whether the disk was left as it was found.
//
// WHAT IS ON DISK AFTERWARDS, enumerated because a bundle is several writes
// and AGENTS.md asks for the partial failures by name. If file three of five
// fails, restoreSkillDirectory removes files one and two and rewrites nothing
// (a fresh install), or rewrites every file the previous bundle had and
// removes every file it did not (an update) — so the next Discover sees
// either no skill at all or the version the person approved before, and never
// a directory holding half of each. The empty DIRECTORIES a removed bundle
// leaves behind are not a state: discovery keys on SKILL.md, and a directory
// without one is not a skill.
//
// Where the undo itself fails, the fail-closed digest is the backstop and the
// refusal says so: the skill is `changed`, which Settings shows with an
// Approve beside it, and the assistant is never offered it. That is the same
// state a crash between the writes would leave, and it is the one arm of the
// interval that cannot be closed by code, only made visible.
func (s *Store) undoInstall(name, dir string, previous map[string][]byte, cause error) error {
	if err := s.restoreSkillDirectory(dir, previous); err != nil {
		return fmt.Errorf(
			"skill %q: the install failed (%w), and undoing what it had written failed too (%v): "+
				"the files on disk are not the ones nocx has a digest for, so the skill is listed as changed and "+
				"is not offered to the assistant until you approve or remove it", name, cause, err)
	}
	return fmt.Errorf("skill %q: the install failed, so nothing was installed: %w", name, cause)
}
