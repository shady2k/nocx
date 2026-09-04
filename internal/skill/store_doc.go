package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/storage"
)

const DocumentName = "skills.json"

// skillsSchemaVersion is this module's current document version. It is a
// named constant rather than a literal in Module because the migration rung
// below has to stamp the same number, and a func referencing Module inside
// Module's own initializer is an initialization cycle.
const skillsSchemaVersion storage.SchemaVersion = 3

// Module declares the skill settings document's schema version. The document
// owns dynamic per-skill enablement, the digest recorded when the person
// approved a skill's bytes, and — since version 2 — where an installed skill
// came from; the global feature switch remains a normal settings declaration.
var Module = storage.Module{
	Name:    "skills",
	Current: skillsSchemaVersion,
	Migrations: []storage.Migration{
		{From: 1, To: 2, Up: restampTo(2)},
		{From: 2, To: 3, Up: restampTo(3)},
	},
}

// restampTo builds a rung that carries a document forward without converting
// anything. Both of this module's version steps are that shape, and both are
// PURELY ADDITIVE: version 2 is version 1 plus an optional `sources` map, and
// version 3 is version 2 plus an optional `enabled` list, so a document
// written before either existed simply has neither, and an absent map or list
// reads as an empty one.
//
// The rungs still have to exist, and this is the whole reason they do.
// storage.Module.Migrate refuses a stored version it has no migration FROM, so
// without one every skills.json already on disk would fail the entire list
// closed the moment Current moved — it is the version bump that needs
// carrying, not the shape. Restamping keeps the in-memory document honest
// about which shape the reader just applied, and each rung stamps the version
// it declares rather than whatever Current happens to be, so adding the next
// one cannot silently make an earlier rung claim to have done more than it did.
//
// WHAT AN OLD ENTRY MEANS, said rather than inferred (nocx-0bsa4.2): a version
// 2 document records no `enabled` list, so every installed skill it names
// arrives inert and waits for the person to turn it on — including one that
// was in play before the bump. That is the intended reading and not a loss:
// nobody has ever looked at those bytes on the Skills page, which is exactly
// the look design §8 makes carrying them defensible. There is deliberately no
// shim that turns them all on to preserve the old behaviour.
//
// A version 0 document — one with no schemaVersion at all — is deliberately
// still refused, exactly as it was at version 1: there is no rung from 0, and
// inventing one would mean guessing at a shape nothing ever wrote.
func restampTo(version storage.SchemaVersion) func([]byte) ([]byte, error) {
	return func(data []byte) ([]byte, error) {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			return nil, err
		}
		if fields == nil {
			return nil, errors.New("document is not a JSON object")
		}
		fields["schemaVersion"] = json.RawMessage(strconv.Itoa(int(version)))
		return json.Marshal(fields)
	}
}

type document struct {
	SchemaVersion storage.SchemaVersion `json:"schemaVersion"`
	// Disabled names the skills the person turned OFF among the roots that
	// arrive on, and Enabled names the ones they turned ON among the roots
	// that arrive off (Provenance.inertOnArrival). Both are DEPARTURES from a
	// default the root decides, which is why there are two of them and why
	// neither is a statement about a skill the other one names: a skill's
	// root settles which list is consulted before either is read, so a name
	// in both cannot make the document mean two things.
	//
	// The alternative — one list of enabled names — would have to be
	// rewritten for every skill on the machine the first time anything moved
	// a default, and would turn "the person has never touched this" into a
	// row that says they turned it off.
	Disabled []string          `json:"disabled"`
	Enabled  []string          `json:"enabled,omitempty"`
	Digests  map[string]string `json:"digests,omitempty"`
	Sources  map[string]Source `json:"sources,omitempty"`
}

// Source records where an installed skill came from, keyed by skill name like
// Digests. It exists so an update can re-run the install against the URL the
// person actually installed from; without it an update could only search for
// the name somewhere else, and skill names are not namespaced across sources,
// so a same-named skill elsewhere would silently reassign provenance.
//
// It is also what ListedSkill carries out to the settings page (nocx-qja4m.9),
// as ONE type rather than two: the document's copy and the wire's copy are the
// same fact, and a second declaration of the shape would be free to drift from
// the one the strict reader above validates.
//
// It is NEVER read as provenance. Provenance is the root (skill.go) and this
// row is content in a file anything able to write skills.json could write — so
// a source entry for a name the authored root holds changes nothing at all
// about that skill, and there is a test for exactly that direction.
type Source struct {
	URL         string `json:"url"`
	InstalledAt string `json:"installedAt"`
}

// ListedSkill is the settings page's complete view of a discovered skill.
type ListedSkill struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Provenance  Provenance `json:"provenance"`
	Path        string     `json:"path"`
	Enabled     bool       `json:"enabled"`
	Status      Status     `json:"status"`
	// Source is where these bytes came from, and it is a POINTER because the
	// answer "nothing recorded" has to be tellable apart from the answer "an
	// empty address": a skill somebody moved into the installed root by hand
	// has no source row, and a row of two empty strings would put an empty
	// line on the page claiming to say where it came from.
	//
	// Present only when the document records one, which — since install is
	// the only writer — means only for an installed skill. It is not a second
	// way to ask what a skill's provenance is, and cannot become one: the
	// implication runs one way only, because installed WITHOUT a source is a
	// state a person can create with `mv`.
	Source *Source `json:"source,omitempty"`
}

// ListResult is returned by the settings-page RPC. A document failure is a
// visible result rather than an empty successful list, and its path tells the
// person which file needs repair.
type ListResult struct {
	Skills        []ListedSkill `json:"skills"`
	DocumentPath  string        `json:"documentPath"`
	DocumentError string        `json:"documentError,omitempty"`
}

func newStore(fsys FileSystem, roots []Root, docStore storage.DocumentStore) *Store {
	if fsys == nil {
		fsys = OSFileSystem{}
	}
	copyRoots := append([]Root(nil), roots...)
	var managedDir, installedDir string
	for _, root := range copyRoots {
		if root.Dir == "" {
			continue
		}
		switch {
		case root.Provenance == ProvenanceManaged && managedDir == "":
			managedDir = root.Dir
		case root.Provenance == ProvenanceInstalled && installedDir == "":
			installedDir = root.Dir
		}
	}
	if docStore == nil {
		for _, dir := range FilesystemRoots(copyRoots) {
			docStore = storage.NewDocumentStore(filepath.Dir(dir))
			break
		}
	}
	s := &Store{
		fs: fsys, roots: copyRoots, managedDir: managedDir, installedDir: installedDir,
		docStore: docStore, locks: make(map[string]*sync.Mutex),
	}
	for i := range s.roots {
		s.roots[i].switches = s.loadSwitches
		s.roots[i].digests = s.loadApprovedDigests
	}
	return s
}

// loadSwitches hands discovery both of the document's departure lists in one
// read. One call rather than two, because a second read is a second chance
// for the two halves of one answer to come from two different versions of the
// file.
func (s *Store) loadSwitches() (switches, error) {
	s.docMu.Lock()
	defer s.docMu.Unlock()
	d, err := s.loadDocumentLocked()
	if err != nil {
		return switches{}, err
	}
	return switches{off: nameSet(d.Disabled), on: nameSet(d.Enabled)}, nil
}

func nameSet(names []string) map[string]struct{} {
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		set[name] = struct{}{}
	}
	return set
}

// sortedNames is the on-disk shape of a departure list: sorted, so a toggle
// that changes nothing about the set does not rewrite the file differently,
// and never nil, so the JSON carries an empty array rather than a null the
// next reader has to special-case.
func sortedNames(set map[string]struct{}) []string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (s *Store) loadApprovedDigests() (map[string]string, error) {
	s.docMu.Lock()
	defer s.docMu.Unlock()
	d, err := s.loadDocumentLocked()
	if err != nil {
		return nil, err
	}
	return d.Digests, nil
}

func (s *Store) loadDocumentLocked() (document, error) {
	d := document{Disabled: []string{}, Enabled: []string{}, Digests: map[string]string{}, Sources: map[string]Source{}}
	if s.docStore == nil {
		s.docFailure = nil
		return d, nil
	}
	var raw json.RawMessage
	found, err := s.docStore.Read(DocumentName, &raw)
	if err != nil {
		s.docFailure = fmt.Errorf("read %s: %w", DocumentName, err)
		return document{}, s.docFailure
	}
	if !found {
		s.docFailure = nil
		return d, nil
	}
	var probe struct {
		SchemaVersion storage.SchemaVersion `json:"schemaVersion"`
	}
	if parseErr := json.Unmarshal(raw, &probe); parseErr != nil {
		s.docFailure = fmt.Errorf("parse %s: %w", DocumentName, parseErr)
		return document{}, s.docFailure
	}
	migrated, err := Module.Migrate(raw, probe.SchemaVersion)
	if err != nil {
		s.docFailure = fmt.Errorf("read %s: %w", DocumentName, err)
		return document{}, s.docFailure
	}
	if err := json.Unmarshal(migrated, &d); err != nil {
		s.docFailure = fmt.Errorf("parse %s: %w", DocumentName, err)
		return document{}, s.docFailure
	}
	if d.Digests == nil {
		d.Digests = map[string]string{}
	}
	if d.Sources == nil {
		d.Sources = map[string]Source{}
	}
	if d.Disabled == nil {
		d.Disabled = []string{}
	}
	if d.Enabled == nil {
		d.Enabled = []string{}
	}
	// Both departure lists are read exactly as strictly, because they are one
	// kind of row and a name nobody can address is a defect in the file
	// whichever direction it points.
	for _, list := range []struct {
		field string
		names []string
	}{{"disabled", d.Disabled}, {"enabled", d.Enabled}} {
		field, names := list.field, list.names
		for _, name := range names {
			canonical, err := normalizeName(name)
			if err != nil {
				s.docFailure = fmt.Errorf("parse %s: %s name %q: %w", DocumentName, field, name, err)
				return document{}, s.docFailure
			}
			if canonical != name {
				s.docFailure = fmt.Errorf("parse %s: %s name %q is not canonical", DocumentName, field, name)
				return document{}, s.docFailure
			}
		}
	}
	for name, digest := range d.Digests {
		canonical, err := normalizeName(name)
		if err != nil || canonical != name {
			s.docFailure = fmt.Errorf("parse %s: digest name %q is not canonical", DocumentName, name)
			return document{}, s.docFailure
		}
		if len(digest) != sha256HexSize || !isHexDigest(digest) {
			s.docFailure = fmt.Errorf("parse %s: digest for %q is invalid", DocumentName, name)
			return document{}, s.docFailure
		}
	}
	// Read exactly as strictly as the digests above, and for the same reason:
	// a row nobody can act on is a defect in the file, not a row to skip. A
	// source that silently vanished would leave a skill listed as installed
	// with no way to update it, which is the state this map exists to prevent.
	for name, source := range d.Sources {
		canonical, err := normalizeName(name)
		if err != nil || canonical != name {
			s.docFailure = fmt.Errorf("parse %s: source name %q is not canonical", DocumentName, name)
			return document{}, s.docFailure
		}
		// profile.ValidateBaseURL is the repo's one owner of "is this string
		// a fetchable http(s) address at all" — shape, not policy, with the
		// address rules enforced at dial time. internal/transport already
		// crosses a module boundary to reuse it rather than derive the answer
		// a second time. Its rejection of userinfo matters here too:
		// credentials must never come to rest in a plaintext document.
		if err := profile.ValidateBaseURL(source.URL); err != nil {
			s.docFailure = fmt.Errorf("parse %s: source url for %q is invalid: %w", DocumentName, name, err)
			return document{}, s.docFailure
		}
		if _, err := time.Parse(time.RFC3339, source.InstalledAt); err != nil {
			s.docFailure = fmt.Errorf("parse %s: source installedAt for %q is not an RFC3339 time", DocumentName, name)
			return document{}, s.docFailure
		}
	}
	s.docFailure = nil
	return d, nil
}

const sha256HexSize = sha256.Size * 2

func isHexDigest(value string) bool {
	var decoded [sha256.Size]byte
	_, err := hex.Decode(decoded[:], []byte(value))
	return err == nil
}

func (s *Store) documentError() error {
	s.docMu.Lock()
	defer s.docMu.Unlock()
	return s.docFailure
}

// List returns every discovered skill, including disabled and changed ones,
// for Settings. Prompt indexing and Read use discoverDetailed's exclusion mode.
func (s *Store) List() (ListResult, error) {
	if s == nil {
		return ListResult{}, errUnavailable
	}
	detailed := discoverDetailed(s.roots, true)
	result := ListResult{
		Skills:       make([]ListedSkill, 0, len(detailed)),
		DocumentPath: s.DocumentPath(),
	}
	if err := s.documentError(); err != nil {
		result.DocumentError = err.Error()
		return result, nil
	}
	// Read AFTER discovery and outside it, because discovery is the roots'
	// answer and this is the document's: recordedSources takes docMu, which
	// discoverDetailed's own disabled/digests callbacks take for themselves.
	sources, err := s.recordedSources()
	if err != nil {
		result.DocumentError = err.Error()
		return result, nil
	}
	for _, found := range detailed {
		listed := ListedSkill{
			Name:        found.Name,
			Description: found.Description,
			Provenance:  found.Provenance,
			Path:        filepath.Join(found.BaseDir, "SKILL.md"),
			Enabled:     found.Enabled,
			Status:      found.Status,
		}
		// The provenance gate is the shadowing defence restated on the wire:
		// a source row is content in a file anything able to write skills.json
		// could write, so a row naming a skill the AUTHORED root holds must
		// not travel as that skill's origin — it would say a stranger wrote
		// bytes the person wrote themselves. Install is the only writer of
		// sources and writes only into the installed root, so gating here
		// drops nothing that was honestly recorded.
		if found.Provenance == ProvenanceInstalled {
			if source, recorded := sources[found.Name]; recorded {
				row := source
				listed.Source = &row
			}
		}
		result.Skills = append(result.Skills, listed)
	}
	return result, nil
}

// recordedSources is every source the document holds, for the one caller that
// needs them all at once. recordedSource answers for a single name; asking it
// per skill would re-read and re-validate the whole document once per row.
func (s *Store) recordedSources() (map[string]Source, error) {
	s.docMu.Lock()
	defer s.docMu.Unlock()
	d, err := s.loadDocumentLocked()
	if err != nil {
		return nil, err
	}
	return d.Sources, nil
}

// DocumentPath is the actual skills.json path used by the store.
func (s *Store) DocumentPath() string {
	if s == nil {
		return DocumentName
	}
	for _, dir := range FilesystemRoots(s.roots) {
		return filepath.Join(filepath.Dir(dir), DocumentName)
	}
	return DocumentName
}

func (s *Store) SetEnabled(name string, enabled bool) error {
	name, err := normalizeName(name)
	if err != nil {
		return err
	}
	listed, err := s.List()
	if err != nil {
		return err
	}
	if listed.DocumentError != "" {
		return errors.New(listed.DocumentError)
	}
	var provenance Provenance
	found := false
	for _, item := range listed.Skills {
		if item.Name == name {
			provenance = item.Provenance
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("skill %q was not found", name)
	}

	s.docMu.Lock()
	defer s.docMu.Unlock()
	d, err := s.loadDocumentLocked()
	if err != nil {
		return err
	}
	// Which list the switch is recorded in is settled by the ROOT, exactly as
	// discovery settles which one it reads. A skill that arrives inert is
	// remembered by the names turned ON; everything else by the names turned
	// OFF. The two are never written together, so a name can appear in the
	// other list only if the skill has since moved roots — in which case the
	// list its new root reads is the one that speaks about it, and the stale
	// row means nothing until it moves back.
	//
	// The digest is deliberately NOT re-recorded here. The snapshot belongs to
	// the moment the bytes landed (install) or were adopted (Approve), and
	// recording it on a toggle would let flipping the switch quietly adopt
	// bytes the person never read — turning the one control that is supposed
	// to be cheap into the one that admits a stranger's edit.
	if provenance.inertOnArrival() {
		on := nameSet(d.Enabled)
		if enabled {
			on[name] = struct{}{}
		} else {
			delete(on, name)
		}
		d.Enabled = sortedNames(on)
	} else {
		off := nameSet(d.Disabled)
		if enabled {
			delete(off, name)
		} else {
			off[name] = struct{}{}
		}
		d.Disabled = sortedNames(off)
	}
	return s.writeDocumentLocked(d)
}

// writeDocumentLocked persists the document that was loaded. It takes the
// whole loaded value and NOTHING ELSE, ON PURPOSE: every mutation rebuilds the
// file from what it read, so a field a writer forgets to carry is a field the
// next toggle silently deletes. Passing the document through — rather than a
// parameter per list the caller happens to be changing — means a new field is
// carried by construction instead of by four call sites remembering it. It
// used to take the disabled set as a second argument, and the second departure
// list is exactly the field that would have been dropped.
func (s *Store) writeDocumentLocked(d document) error {
	if s.docStore == nil {
		return errors.New("skill settings document is unavailable")
	}
	d.SchemaVersion = Module.Current
	if d.Disabled == nil {
		d.Disabled = []string{}
	}
	if err := s.docStore.Write(DocumentName, d); err != nil {
		return fmt.Errorf("write %s: %w", DocumentName, err)
	}
	s.docFailure = nil
	return nil
}

// recordApprovalDigest writes down what the person approved: the digest of the
// bytes now on disk, and — when the skill came from an address — where they
// came from, in ONE document write.
//
// The two halves are not separable and there is no second call that adds the
// source afterwards. An installed skill whose digest is recorded and whose
// source is not can never be updated; one whose source is recorded and whose
// digest is not is `changed`, which is dropped from the prompt index entirely.
// Both are states this repo would rather not be able to reach, so they are
// reached through one writeDocumentLocked or not at all (install.go states the
// interval this closes).
//
// sourceURL is empty for everything the assistant writes and for an Approve:
// there is no address behind a managed skill, and approving an edited
// installed skill changes its bytes rather than where it came from — so an
// empty sourceURL LEAVES the recorded source alone rather than clearing it.
// Forgetting a source is clearApprovalDigest's job and happens only when the
// skill itself goes.
//
// The digest is computed from the DIRECTORY, after the write, not from
// whatever the caller believed it was writing. That is what makes it a record
// of the bytes on disk rather than a record of an intention.
func (s *Store) recordApprovalDigest(name, dir, sourceURL string) error {
	digest, err := hashSkillDirectory(dir)
	if err != nil {
		return fmt.Errorf("skill %q: hash: %w", name, err)
	}
	s.docMu.Lock()
	defer s.docMu.Unlock()
	d, err := s.loadDocumentLocked()
	if err != nil {
		return err
	}
	if d.Digests == nil {
		d.Digests = make(map[string]string)
	}
	d.Digests[name] = digest
	if sourceURL != "" {
		if d.Sources == nil {
			d.Sources = make(map[string]Source)
		}
		d.Sources[name] = Source{URL: sourceURL, InstalledAt: time.Now().UTC().Format(time.RFC3339)}
	}
	// The person's switch is left exactly as it was. An install of a name
	// they have never turned on leaves it inert, which is the whole of design
	// §8; an UPDATE of one they did turn on leaves it on, because they just
	// read the new document in the preview and said to adopt it — turning it
	// off there would take a working skill out of play at the moment they
	// asked for the newer version of it.
	return s.writeDocumentLocked(d)
}

// recordedSource answers where an installed skill was installed from. It is
// the document's copy and never provenance: provenance is the root, and a row
// here for a name the authored root holds means nothing at all.
func (s *Store) recordedSource(name string) (Source, bool, error) {
	s.docMu.Lock()
	defer s.docMu.Unlock()
	d, err := s.loadDocumentLocked()
	if err != nil {
		return Source{}, false, err
	}
	source, found := d.Sources[name]
	return source, found, nil
}

// clearApprovalDigest forgets everything the document records about one skill:
// the digest, the source AND the switch the person set, together, because they
// are one record of a skill the person did not write. A row outliving its
// skill is a row for a name that is not on disk, which the next install of
// that name would read as its own history — and for the switch that is not
// merely untidy: it would let a skill removed while it was on come BACK on,
// reinstalled from anywhere, without the look design §8 exists to require.
func (s *Store) clearApprovalDigest(name string) error {
	s.docMu.Lock()
	defer s.docMu.Unlock()
	d, err := s.loadDocumentLocked()
	if err != nil {
		return err
	}
	delete(d.Digests, name)
	delete(d.Sources, name)
	on := nameSet(d.Enabled)
	delete(on, name)
	d.Enabled = sortedNames(on)
	return s.writeDocumentLocked(d)
}

// Approve records the current bytes of a changed managed or installed skill.
// Authored and builtin skills have no approval digest to diverge from and
// cannot use this path.
func (s *Store) Approve(name string) error {
	name, err := normalizeName(name)
	if err != nil {
		return err
	}
	unlock, err := s.lockName(name)
	if err != nil {
		return err
	}
	defer unlock()
	// There is deliberately no root check here (nocx-ablu5). This used to call
	// requireManagedRoot, which is the precondition of a write INTO the managed
	// root — and Approve performs no such write. Its only write is the document,
	// which does not live under the managed root at all: DocumentPath puts
	// skills.json beside the parent of the first filesystem root, whichever that
	// is. So the guard was already vestigial for a managed skill, and once the
	// installed root landed it was actively wrong: approving an INSTALLED skill
	// on a machine whose managed-skills is a symlink failed with "skill root is
	// a symlink", about a directory the call never touches.
	//
	// The precondition Approve actually has is the provenance and status check
	// immediately below — the skill must exist, must be one whose bytes are
	// digested, and must have changed. Nothing is checked about the skill's own
	// root either: Approve only READS that directory, to hash it, and a
	// symlinked skill directory that hashes fine is not a failure worth
	// manufacturing. The three remaining requireManagedRoot callers in write.go
	// do write into the managed root and keep it.
	var target *discovered
	for _, found := range discoverDetailed(s.roots, true) {
		if found.Name == name {
			copy := found
			target = &copy
			break
		}
	}
	if target == nil || !target.Provenance.digested() {
		return fmt.Errorf("skill %q is not a changed managed or installed skill", name)
	}
	if target.Status != StatusChanged {
		return fmt.Errorf("skill %q is not changed", name)
	}
	// Approving records the bytes, never the address: an edited installed
	// skill still came from where it came from.
	return s.recordApprovalDigest(name, target.BaseDir, "")
}

// Remove deletes the person-facing authored, managed or installed skill.
// Builtins are embedded and therefore refuse explicitly so the page never
// offers a click that can only fail.
func (s *Store) Remove(name string) error {
	name, err := normalizeName(name)
	if err != nil {
		return err
	}
	listed, err := s.List()
	if err != nil {
		return err
	}
	if listed.DocumentError != "" {
		return errors.New(listed.DocumentError)
	}
	var target *discovered
	for _, found := range discoverDetailed(s.roots, true) {
		if found.Name == name {
			copy := found
			target = &copy
			break
		}
	}
	if target == nil {
		return fmt.Errorf("skill %q was not found", name)
	}
	if target.Provenance == ProvenanceBuiltin {
		return fmt.Errorf("builtin skill %q cannot be deleted", name)
	}
	unlock, err := s.lockName(name)
	if err != nil {
		return err
	}
	defer unlock()
	return s.removeDiscovered(*target)
}

func (s *Store) removeDiscovered(found discovered) error {
	if found.root.Dir == "" {
		return fmt.Errorf("skill %q has no removable filesystem path", found.Name)
	}
	root, err := filepath.Abs(found.root.Dir)
	if err != nil {
		return fmt.Errorf("skill %q: root: %w", found.Name, err)
	}
	dir, err := filepath.Abs(found.BaseDir)
	if err != nil {
		return fmt.Errorf("skill %q: directory: %w", found.Name, err)
	}
	rootEval, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("skill %q: root: %w", found.Name, err)
	}
	dirEval, err := filepath.EvalSymlinks(dir)
	if err != nil || !within(rootEval, dirEval) {
		return fmt.Errorf("skill %q: directory escapes its root", found.Name)
	}
	target := filepath.Join(dir, "SKILL.md")
	info, err := os.Lstat(target)
	if err != nil {
		return fmt.Errorf("skill %q: inspect: %w", found.Name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || linkCount(info) > 1 {
		return fmt.Errorf("skill %q: SKILL.md is not a removable regular file", found.Name)
	}
	// THE REST OF THE BUNDLE GOES FIRST, for an installed skill. Until a
	// skill could carry support files, removing SKILL.md removed the skill
	// whole; now an installed skill's directory may hold references/ and
	// scripts/ that this product wrote from one document, and leaving them
	// behind would leave a downloaded script on the person's disk after they
	// deleted the skill that brought it (design §8 is exactly about not
	// carrying executable text further than the person agreed).
	//
	// INSTALLED ONLY. An authored skill's directory is the person's own and
	// may hold anything they put beside it; a managed one is written by the
	// assistant and has never held more than its SKILL.md. Neither invites a
	// recursive delete, and provenance is the root, so this cannot be
	// redirected by a file's content.
	//
	// BEFORE the SKILL.md and not after, which is the whole of the ordering
	// argument. A prune that failed after SKILL.md was gone would leave the
	// skill undiscoverable with its digest still recorded and its support
	// files still on disk — a state nothing in the product names. Failing
	// first leaves the skill discoverable with some of its files missing,
	// which is `changed`: Settings shows it, the assistant is not offered it,
	// and Remove can simply be pressed again.
	if found.Provenance == ProvenanceInstalled {
		if err := s.pruneSkillDirectory(found.Name, dir, map[string]struct{}{"SKILL.md": {}}); err != nil {
			return err
		}
	}
	if err := s.fs.Remove(target); err != nil {
		return fmt.Errorf("skill %q: delete: %w", found.Name, err)
	}
	if err := s.fs.Sync(dir); err != nil {
		return fmt.Errorf("skill %q: sync directory after delete: %w", found.Name, err)
	}
	if found.Provenance.digested() {
		if err := s.clearApprovalDigest(found.Name); err != nil {
			return fmt.Errorf("skill %q: clear approval: %w", found.Name, err)
		}
	}
	return nil
}
