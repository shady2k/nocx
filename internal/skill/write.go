package skill

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/apifetch"
	"github.com/shady2k/nocx/internal/storage"
)

var errUnavailable = errors.New("skill library is unavailable")

const (
	managedSkillDirMode  os.FileMode = 0o700
	managedSkillFileMode os.FileMode = 0o600
	maxSkillFileBytes                = 64 << 10
	// maxDescriptionRunes bounds the one field of a skill that is copied
	// verbatim into the SYSTEM prompt, under our own sentence vouching for it,
	// on every ask (internal/assistant/systemprompt.go). The file ceiling above
	// is the only other bound in play, and 64 KiB of somebody else's prose in
	// the most trusted region of the context is not a bound at all.
	//
	// The number is measured, not round. Across the 140 SKILL.md files on this
	// machine — this repo's builtin, agentskills.io publishers, Anthropic's own
	// plugin marketplace — the median description is about 200 characters, the
	// 95th percentile about 530, and the longest genuine one 1506. AgentMail's,
	// the skill this feature was built to install, is 449. So 2048 refuses
	// nothing anybody has actually published while cutting the worst case a
	// single skill can spend from roughly 16000 tokens to about 500; a cap that
	// turned away a legitimate published skill would be a worse defect than the
	// one it fixes, which is why 512 and 1024 were both rejected — each of them
	// refuses skills Anthropic ships today.
	//
	// The read budget is DERIVED from this number rather than chosen beside it
	// (MaxFrontmatterBytes, skill.go), because a description this cap admits
	// and discovery cannot parse would be written, accepted, and then invisible
	// — which is the failure this file exists to refuse, arriving by the other
	// door.
	//
	// RUNES, NOT BYTES. A byte cap allows a Cyrillic or Japanese author half or
	// a third of the description an English author gets for the same prose, and
	// what is being rationed here is the reader's attention and the model's
	// context, neither of which is denominated in UTF-8. The residue — that a
	// four-byte script can spend 8 KiB inside the cap — is bounded absolutely by
	// maxSkillFileBytes, and sanitizeDescription already counts in runes, so
	// this is also the unit the rest of the field is measured in.
	maxDescriptionRunes = 2048
)

// FileSystem is the filesystem surface used by Store's writes. Sync accepts
// both files and directories: the former makes the temporary bytes durable,
// and the latter makes the rename durable.
type FileSystem interface {
	MkdirAll(path string, perm os.FileMode) error
	OpenFile(name string, flag int, perm os.FileMode) (io.WriteCloser, error)
	Rename(oldPath, newPath string) error
	Sync(path string) error
	Remove(path string) error
}

// OSFileSystem is the production FileSystem implementation.
type OSFileSystem struct{}

func (OSFileSystem) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (OSFileSystem) OpenFile(name string, flag int, perm os.FileMode) (io.WriteCloser, error) {
	return os.OpenFile(name, flag, perm) //nolint:gosec // the Store derives the path from a validated skill name
}

func (OSFileSystem) Rename(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}

func (OSFileSystem) Sync(path string) error {
	file, err := os.Open(path) //nolint:gosec // the Store derives the path and opens only its own file or directory
	if err != nil {
		return err
	}
	err = file.Sync()
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

func (OSFileSystem) Remove(path string) error {
	return os.Remove(path)
}

// Store owns the managed and installed skill roots and retains the other roots
// so writes can refuse names already claimed by an authored or builtin skill.
//
// The two writable roots are held separately rather than looked up per write,
// because they answer different questions: managedDir is where the ASSISTANT
// may write, and installedDir is where a document the PERSON approved from a
// URL lands (install.go). Neither may be reached from the other's operations.
type Store struct {
	fs           FileSystem
	roots        []Root
	managedDir   string
	installedDir string
	docStore     storage.DocumentStore
	// fetcher acquires a skill document by URL (preview.go). It is optional
	// because only the composition root has one: a store built by a test that
	// never installs anything needs no network seam, and Preview says so
	// rather than reaching for a client of its own.
	fetcher apifetch.TextFetcher

	mu    sync.Mutex
	locks map[string]*sync.Mutex
	seq   atomic.Uint64

	docMu      sync.Mutex
	docFailure error

	// previewed is the one document Preview last showed, which is the only
	// thing Install compares its second fetch against (preview.go).
	previewMu sync.Mutex
	previewed *previewedDocument
}

// StoreOption configures a Store at construction. It is variadic rather than
// another positional parameter because the seams it carries are optional by
// nature: every caller needs roots and a document store, and only the
// composition root has a network fetcher to hand over.
type StoreOption func(*Store)

// WithFetcher wires the seam Preview acquires a skill document through
// (preview.go). It takes apifetch's TextFetcher rather than an interface of
// this package's own so there is ONE owner of "fetch a text document over the
// guarded transport" — the same seam internal/assistant and internal/transport
// already hold.
func WithFetcher(fetcher apifetch.TextFetcher) StoreOption {
	return func(s *Store) { s.fetcher = fetcher }
}

// NewStore builds a skill store. The roots must include one managed directory;
// authored and builtin roots are retained for collision checks and precedence.
func NewStore(fsys FileSystem, roots []Root, docStore storage.DocumentStore, opts ...StoreOption) *Store {
	s := newStore(fsys, roots, docStore)
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// FS exposes the injected write surface for failure-path tests and composition
// checks; production callers should otherwise use Store's operations.
func (s *Store) FS() FileSystem {
	if s == nil {
		return nil
	}
	return s.fs
}

// Index and Read make Store the complete assistant skill library seam while
// preserving one root order for discovery, reading and writing.
func (s *Store) Index() []Skill {
	if s == nil {
		return nil
	}
	index := Discover(s.roots)
	ready := index[:0]
	for _, item := range index {
		if item.Status != StatusChanged {
			ready = append(ready, item)
		}
	}
	index = ready
	if len(index) > MaxIndexed {
		slog.Warn("skill: index cap reached", "cap", MaxIndexed)
		index = index[:MaxIndexed]
	}
	return index
}

func (s *Store) Read(name, relPath string) (Content, error) {
	if s == nil {
		return Content{}, errUnavailable
	}
	return Read(s.roots, name, relPath)
}

// File is the person's read path over the same roots and the same
// containment (file.go). It is a method here, beside Read, so a caller cannot
// reach one of them with a different root order than the other.
func (s *Store) File(name, relPath string) (FileResult, error) {
	if s == nil {
		return FileResult{}, errUnavailable
	}
	return File(s.roots, name, relPath)
}

// Create writes a new managed skill. An empty leftover directory is completed;
// a directory containing SKILL.md is an existing name and is refused.
func (s *Store) Create(name, description, body string) error {
	name, data, err := prepareSkill(name, description, body)
	if err != nil {
		return err
	}
	unlock, err := s.lockName(name)
	if err != nil {
		return err
	}
	defer unlock()

	if rootErr := s.requireManagedRoot(); rootErr != nil {
		return rootErr
	}
	if collisionErr := s.refuseForeignCollision(name); collisionErr != nil {
		return collisionErr
	}
	dir, target, err := s.managedPaths(name)
	if err != nil {
		return err
	}
	if info, statErr := os.Lstat(target); statErr == nil {
		if info.IsDir() {
			return fmt.Errorf("skill %q already exists: SKILL.md is a directory", name)
		}
		if err := s.checkExistingPath(s.managedDir, dir, target, false); err != nil {
			return err
		}
		return fmt.Errorf("skill %q already exists in the managed library", name)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("skill %q: inspect target: %w", name, statErr)
	}
	if err := s.checkDirectory(s.managedDir, dir); err != nil {
		return err
	}
	if err := s.fs.MkdirAll(dir, managedSkillDirMode); err != nil {
		return fmt.Errorf("skill %q: create directory: %w", name, err)
	}
	if err := s.atomicWrite(dir, target, data); err != nil {
		return err
	}
	// A managed skill has no source: the assistant drafted it, so there is
	// no address to record and nothing an update could be pinned to.
	return s.recordApprovalDigest(name, dir, "")
}

// Update replaces an existing managed skill atomically.
func (s *Store) Update(name, description, body string) error {
	name, data, err := prepareSkill(name, description, body)
	if err != nil {
		return err
	}
	unlock, err := s.lockName(name)
	if err != nil {
		return err
	}
	defer unlock()

	if rootErr := s.requireManagedRoot(); rootErr != nil {
		return rootErr
	}
	if collisionErr := s.refuseForeignCollision(name); collisionErr != nil {
		return collisionErr
	}
	dir, target, err := s.managedPaths(name)
	if err != nil {
		return err
	}
	if err := s.checkDirectory(s.managedDir, dir); err != nil {
		return err
	}
	if err := s.checkExistingPath(s.managedDir, dir, target, true); err != nil {
		return err
	}
	if err := s.atomicWrite(dir, target, data); err != nil {
		return err
	}
	// A managed skill has no source: the assistant drafted it, so there is
	// no address to record and nothing an update could be pinned to.
	return s.recordApprovalDigest(name, dir, "")
}

// Delete removes only a managed SKILL.md and leaves its now-empty directory so
// a later Create can safely complete it.
func (s *Store) Delete(name string) error {
	name, err := normalizeName(name)
	if err != nil {
		return err
	}
	unlock, err := s.lockName(name)
	if err != nil {
		return err
	}
	defer unlock()

	if rootErr := s.requireManagedRoot(); rootErr != nil {
		return rootErr
	}
	if collisionErr := s.refuseForeignCollision(name); collisionErr != nil {
		return collisionErr
	}
	dir, target, err := s.managedPaths(name)
	if err != nil {
		return err
	}
	if err := s.checkDirectory(s.managedDir, dir); err != nil {
		return err
	}
	if err := s.checkExistingPath(s.managedDir, dir, target, true); err != nil {
		return err
	}
	if err := s.fs.Remove(target); err != nil {
		return fmt.Errorf("skill %q: delete: %w", name, err)
	}
	if err := s.fs.Sync(dir); err != nil {
		return fmt.Errorf("skill %q: sync directory after delete: %w", name, err)
	}
	if err := s.clearApprovalDigest(name); err != nil {
		return fmt.Errorf("skill %q: clear approval: %w", name, err)
	}
	return nil
}

func (s *Store) lockName(name string) (func(), error) {
	if s == nil {
		return nil, errors.New("skill store is unavailable")
	}
	s.mu.Lock()
	lock := s.locks[name]
	if lock == nil {
		lock = &sync.Mutex{}
		s.locks[name] = lock
	}
	s.mu.Unlock()
	lock.Lock()
	return lock.Unlock, nil
}

func (s *Store) requireManagedRoot() error {
	if s.managedDir == "" {
		return errors.New("skill store has no managed root")
	}
	return requireWritableRoot(s.managedDir, "skill root")
}

// requireWritableRoot checks that a root a write is about to reach into is
// what it claims to be. An absent root is fine — it is created by the write —
// but a root that is a symlink or a plain file is refused, because everything
// downstream reasons about containment from this directory.
//
// It takes the directory and its name in the person's words rather than
// reading s.managedDir, so the installed root gets the same check instead of
// a second, subtly different one.
func requireWritableRoot(dir, label string) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: inspect: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New(label + " is a symlink")
	}
	if !info.IsDir() {
		return errors.New(label + " is not a directory")
	}
	return nil
}

func (s *Store) managedPaths(name string) (string, string, error) {
	return pathsUnder(s.managedDir, name)
}

// pathsUnder is where a skill's directory and its SKILL.md live under a root.
// One derivation for both writable roots: a second one would be a second
// answer to what a skill's path is, and every containment check below is
// stated against exactly this shape.
func pathsUnder(root, name string) (string, string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("skill %q: root: %w", name, err)
	}
	return filepath.Join(abs, name), filepath.Join(abs, name, "SKILL.md"), nil
}

// holder answers WHO ALREADY HOLDS a name, across every root and including
// disabled skills, by asking discoverDetailed — which owns precedence, the
// seen map and therefore the collision rule. One answer, because the two
// callers ask the same question for different reasons: a write asks whether
// the assistant may touch this name, and a preview asks whether the name is
// free at all (preview.go).
func (s *Store) holder(name string) (Provenance, bool) {
	for _, found := range discoverDetailed(s.roots, true) {
		if found.Name == name {
			return found.Provenance, true
		}
	}
	return "", false
}

func (s *Store) refuseForeignCollision(name string) error {
	// Managed is exempt HERE and nowhere else: this is the assistant asking
	// to change a skill it wrote. An install has no such exemption — every
	// provenance is in the way of a name it wants.
	if provenance, held := s.holder(name); held && provenance != ProvenanceManaged {
		return fmt.Errorf("skill %q belongs to %s and cannot be changed by the assistant", name, holderPhrase(provenance))
	}
	return nil
}

// checkDirectory and checkExistingPath take the root they are checking
// against rather than reading s.managedDir, so an install into the installed
// root passes through the SAME containment checks the assistant's writes do.
// No second writer is introduced by install.go; it reaches these.
func (s *Store) checkDirectory(rootDir, dir string) error {
	root, err := filepath.Abs(rootDir)
	if err != nil {
		return fmt.Errorf("skill directory: root: %w", err)
	}
	if info, statErr := os.Lstat(dir); statErr == nil {
		if !info.IsDir() {
			return fmt.Errorf("skill directory %q is not a directory", filepath.Base(dir))
		}
		rootEval, evalErr := filepath.EvalSymlinks(root)
		if evalErr != nil {
			return fmt.Errorf("skill root: %w", evalErr)
		}
		dirEval, evalErr := filepath.EvalSymlinks(dir)
		if evalErr != nil {
			return fmt.Errorf("skill directory: %w", evalErr)
		}
		if !within(rootEval, dirEval) {
			return fmt.Errorf("skill directory %q escapes its root", filepath.Base(dir))
		}
		return nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("skill directory %q: inspect: %w", filepath.Base(dir), statErr)
	}
	return nil
}

func (s *Store) checkExistingPath(rootDir, dir, target string, requireRegular bool) error {
	rootEval, err := filepath.EvalSymlinks(rootDir)
	if err != nil {
		return fmt.Errorf("skill root: %w", err)
	}
	targetEval, err := filepath.EvalSymlinks(target)
	if err != nil {
		return fmt.Errorf("skill file: %w", err)
	}
	if !within(rootEval, targetEval) {
		return fmt.Errorf("skill file %q escapes its root", filepath.Base(dir))
	}
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("skill file %q: inspect: %w", filepath.Base(dir), err)
	}
	if requireRegular && !info.Mode().IsRegular() {
		return fmt.Errorf("skill file %q is not a regular file", filepath.Base(dir))
	}
	if requireRegular && linkCount(info) > 1 {
		return fmt.Errorf("skill file %q has multiple links", filepath.Base(dir))
	}
	return nil
}

func (s *Store) atomicWrite(dir, target string, data []byte) error {
	tmp := filepath.Join(dir, "SKILL.md.tmp-"+strconv.FormatUint(s.seq.Add(1), 10))
	file, err := s.fs.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, managedSkillFileMode)
	if err != nil {
		return fmt.Errorf("open temporary skill file: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = s.fs.Remove(tmp)
		}
	}()

	written, writeErr := io.Copy(file, bytes.NewReader(data))
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("write temporary skill file: %w", writeErr)
	}
	if written != int64(len(data)) {
		return fmt.Errorf("write temporary skill file: short write: %d of %d bytes", written, len(data))
	}
	if closeErr != nil {
		return fmt.Errorf("close temporary skill file: %w", closeErr)
	}
	if err := s.fs.Sync(tmp); err != nil {
		return fmt.Errorf("sync temporary skill file: %w", err)
	}
	if err := s.fs.Rename(tmp, target); err != nil {
		return fmt.Errorf("rename temporary skill file: %w", err)
	}
	keep = true
	if err := s.fs.Sync(dir); err != nil {
		return fmt.Errorf("sync skill directory: %w", err)
	}
	return nil
}

func prepareSkill(rawName, description, body string) (string, []byte, error) {
	name, err := normalizeName(rawName)
	if err != nil {
		return "", nil, err
	}
	description = sanitizeDescription(description)
	if err := checkDescriptionLength(name, description); err != nil {
		return "", nil, err
	}
	if strings.TrimSpace(body) == "" {
		return "", nil, errors.New("skill body must not be empty")
	}
	data := []byte(skillFrontmatter(name, description) + body)
	if len(data) > maxSkillFileBytes {
		return "", nil, fmt.Errorf("skill %q exceeds the 64 KiB file limit", name)
	}
	return name, data, nil
}

func normalizeName(raw string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	if !skillNamePattern.MatchString(name) {
		return "", fmt.Errorf("skill name %q must match [a-z0-9][a-z0-9-]{0,63}", raw)
	}
	return name, nil
}

// descriptionOverCap is the comparison itself, in one place, so the sentence
// a person reads and the line discovery logs can never come to disagree about
// what too long means. It answers the length too, because both callers say the
// number out loud and neither should arrive at it separately.
func descriptionOverCap(description string) (int, bool) {
	length := utf8.RuneCountInString(description)
	return length, length > maxDescriptionRunes
}

// checkDescriptionLength is the whole of the cap, and it is one function
// because the three places that ask are asking one question: the assistant's
// Create and Update (through prepareSkill), the person's preview and install
// (through documentPreview), and discovery, over bytes no write of ours ever
// touched. A second copy of the comparison would agree with this one until
// somebody moved the number.
//
// It counts what will be COPIED — the sanitized, trimmed description — so the
// number in the refusal is the number of characters that would have reached
// the prompt, not the number the caller happened to type.
func checkDescriptionLength(name, description string) error {
	length, over := descriptionOverCap(description)
	if !over {
		return nil
	}
	return fmt.Errorf(
		"the description for %q is %d characters and a skill's description may be at most %d: it is copied verbatim into the "+
			"assistant's system prompt on every ask, and it is refused rather than shortened, because a shortened description "+
			"is a claim its author did not make — what does not fit belongs in the body, which is read only when the skill is used",
		name, length, maxDescriptionRunes)
}

func sanitizeDescription(description string) string {
	var out strings.Builder
	for _, r := range description {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		out.WriteRune(r)
	}
	return strings.TrimSpace(out.String())
}

func skillFrontmatter(name, description string) string {
	return "---\nname: " + name + "\ndescription: " + strconv.Quote(description) + "\n---\n"
}

// linkCount answers how many names the file has, so a write can refuse a
// skill file somebody else also holds a link to.
//
// It reads Nlink REFLECTIVELY rather than through a *syscall.Stat_t type
// assertion, and that is deliberate: the field is uint64 on Linux and
// uint16 on Darwin, so a typed fast path either fails to compile on macOS
// or carries a conversion that unconvert calls unnecessary here. Splitting
// the access per OS would work and would move this whole package into the
// Makefile's OS partition (ci-os-split enforces that) — a large answer to
// one integer's width. reflect.Value.Uint widens either width, and this
// runs once per skill write.
func linkCount(info os.FileInfo) uint64 {
	value := reflect.ValueOf(info.Sys())
	if value.IsValid() && value.Kind() == reflect.Pointer && !value.IsNil() {
		field := value.Elem().FieldByName("Nlink")
		if field.IsValid() && field.CanUint() {
			return field.Uint()
		}
	}
	return 1
}
