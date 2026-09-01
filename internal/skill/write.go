package skill

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unicode"
)

const (
	managedSkillDirMode  os.FileMode = 0o700
	managedSkillFileMode os.FileMode = 0o600
	maxSkillFileBytes                = 64 << 10
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

// Store owns the managed skill root and retains the other roots so writes can
// refuse names already claimed by an authored or builtin skill.
type Store struct {
	fs         FileSystem
	roots      []Root
	managedDir string

	mu    sync.Mutex
	locks map[string]*sync.Mutex
	seq   atomic.Uint64
}

// NewStore builds a skill store. The roots must include one managed directory;
// authored and builtin roots are retained for collision checks and precedence.
func NewStore(fsys FileSystem, roots []Root) *Store {
	if fsys == nil {
		fsys = OSFileSystem{}
	}
	copyRoots := append([]Root(nil), roots...)
	var managedDir string
	for _, root := range copyRoots {
		if root.Provenance == ProvenanceManaged && root.Dir != "" {
			managedDir = root.Dir
			break
		}
	}
	return &Store{fs: fsys, roots: copyRoots, managedDir: managedDir, locks: make(map[string]*sync.Mutex)}
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
	return NewLibrary(s.roots).Index()
}

func (s *Store) Read(name, relPath string) (Content, error) {
	if s == nil {
		return Content{}, errUnavailable
	}
	return Read(s.roots, name, relPath)
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
		if err := s.checkExistingPath(dir, target, false); err != nil {
			return err
		}
		return fmt.Errorf("skill %q already exists in the managed library", name)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("skill %q: inspect target: %w", name, statErr)
	}
	if err := s.checkDirectory(dir); err != nil {
		return err
	}
	if err := s.fs.MkdirAll(dir, managedSkillDirMode); err != nil {
		return fmt.Errorf("skill %q: create directory: %w", name, err)
	}
	return s.atomicWrite(dir, target, data)
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
	if err := s.checkDirectory(dir); err != nil {
		return err
	}
	if err := s.checkExistingPath(dir, target, true); err != nil {
		return err
	}
	return s.atomicWrite(dir, target, data)
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
	if err := s.checkDirectory(dir); err != nil {
		return err
	}
	if err := s.checkExistingPath(dir, target, true); err != nil {
		return err
	}
	if err := s.fs.Remove(target); err != nil {
		return fmt.Errorf("skill %q: delete: %w", name, err)
	}
	if err := s.fs.Sync(dir); err != nil {
		return fmt.Errorf("skill %q: sync directory after delete: %w", name, err)
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
	info, err := os.Lstat(s.managedDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("skill root: inspect: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("skill root is a symlink")
	}
	if !info.IsDir() {
		return errors.New("skill root is not a directory")
	}
	return nil
}

func (s *Store) managedPaths(name string) (string, string, error) {
	root, err := filepath.Abs(s.managedDir)
	if err != nil {
		return "", "", fmt.Errorf("skill %q: managed root: %w", name, err)
	}
	return filepath.Join(root, name), filepath.Join(root, name, "SKILL.md"), nil
}

func (s *Store) refuseForeignCollision(name string) error {
	for _, found := range Discover(s.roots) {
		if found.Name != name {
			continue
		}
		if found.Provenance == ProvenanceManaged {
			continue
		}
		return fmt.Errorf("skill %q belongs to a skill you wrote and cannot be changed by the assistant", name)
	}
	return nil
}

func (s *Store) checkDirectory(dir string) error {
	root, err := filepath.Abs(s.managedDir)
	if err != nil {
		return fmt.Errorf("skill directory: managed root: %w", err)
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
			return fmt.Errorf("skill directory %q escapes the managed root", filepath.Base(dir))
		}
		return nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("skill directory %q: inspect: %w", filepath.Base(dir), statErr)
	}
	return nil
}

func (s *Store) checkExistingPath(dir, target string, requireRegular bool) error {
	rootEval, err := filepath.EvalSymlinks(s.managedDir)
	if err != nil {
		return fmt.Errorf("skill root: %w", err)
	}
	targetEval, err := filepath.EvalSymlinks(target)
	if err != nil {
		return fmt.Errorf("skill file: %w", err)
	}
	if !within(rootEval, targetEval) {
		return fmt.Errorf("skill file %q escapes the managed root", filepath.Base(dir))
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

func linkCount(info os.FileInfo) uint64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return stat.Nlink
	}
	value := reflect.ValueOf(info.Sys())
	if value.IsValid() && value.Kind() == reflect.Pointer && !value.IsNil() {
		field := value.Elem().FieldByName("Nlink")
		if field.IsValid() && field.CanUint() {
			return field.Uint()
		}
	}
	return 1
}
