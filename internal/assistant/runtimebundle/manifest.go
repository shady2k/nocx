// Package runtimebundle reads the shipped experimental runtime manifest.
// It never starts Python or downloads packages. Installation verifies the
// entire tree before promotion on Linux and macOS; selected operations hold
// a live shared lease and resolve metadata without re-reading every library.
package runtimebundle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/jsoncheck"
)

const (
	ManifestName     = "manifest.json"
	InterpreterPath  = "python/bin/python3.13"
	EntrypointPath   = "app/entrypoint.py"
	maxManifestBytes = 16 << 20
	maxTreeEntries   = 100000
)

var ErrInvalid = errors.New("invalid experimental runtime bundle")

// View is the read-only filesystem seam shared by the locator and installer.
type View interface {
	Lstat(string) (fs.FileInfo, error)
	Open(string) (io.ReadCloser, error)
	ReadDir(string) ([]fs.DirEntry, error)
	Readlink(string) (string, error)
}

type Package struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
type Metadata struct {
	BridgeVersion int                        `json:"bridgeVersion"`
	Target        string                     `json:"target"`
	Interpreter   string                     `json:"interpreter"`
	Entrypoint    string                     `json:"entrypoint"`
	PythonVersion string                     `json:"pythonVersion"`
	LockSHA256    string                     `json:"lockSha256"`
	Packages      []Package                  `json:"packages"`
	Provenance    map[string]json.RawMessage `json:"provenance"`
}

func (m *Metadata) UnmarshalJSON(data []byte) error {
	type fields Metadata
	return exact(data, (*fields)(m), "bridgeVersion", "target", "interpreter", "entrypoint", "pythonVersion", "lockSha256", "packages", "provenance")
}

func (p *Package) UnmarshalJSON(data []byte) error {
	type fields Package
	return exact(data, (*fields)(p), "name", "version")
}

type Entry struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256,omitempty"`
	Size       int64  `json:"size,omitempty"`
	Executable bool   `json:"executable,omitempty"`
	Link       string `json:"link,omitempty"`
}

// UnmarshalJSON enforces the two disjoint manifest entry shapes. A missing
// false executable flag or zero size is not silently treated as supplied.
func (e *Entry) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if _, link := fields["link"]; link {
		if len(fields) != 2 || !requiredFields(fields, "path", "link") {
			return ErrInvalid
		}
		var value struct {
			Path string `json:"path"`
			Link string `json:"link"`
		}
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		*e = Entry{Path: value.Path, Link: value.Link}
		if e.Link == "" {
			return ErrInvalid
		}
		return nil
	}
	if len(fields) != 4 || !requiredFields(fields, "path", "sha256", "size", "executable") {
		return ErrInvalid
	}
	var value struct {
		Path       string `json:"path"`
		SHA256     string `json:"sha256"`
		Size       int64  `json:"size"`
		Executable bool   `json:"executable"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*e = Entry{Path: value.Path, SHA256: value.SHA256, Size: value.Size, Executable: value.Executable}
	return nil
}

type Manifest struct {
	FormatVersion int      `json:"formatVersion"`
	Metadata      Metadata `json:"metadata"`
	Files         []Entry  `json:"files"`
	Digest        string   `json:"-"`
	Bytes         []byte   `json:"-"`
	entries       map[string]Entry
	directories   map[string]bool
	resolved      map[string]string
}

// ValidDigest recognizes the one canonical manifest/lease directory identity.
func ValidDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func safePath(value string) bool {
	return value != "." && len(value) <= 4096 && fs.ValidPath(value) && !strings.ContainsAny(value, "\\\x00")
}

func exact(data []byte, dst any, required ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("%w: malformed object", ErrInvalid)
	}
	if len(fields) != len(required) || !requiredFields(fields, required...) {
		return fmt.Errorf("%w: non-exact fields", ErrInvalid)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return nil
}

func requiredFields(fields map[string]json.RawMessage, required ...string) bool {
	for _, key := range required {
		value, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
	}
	return true
}

// ReadManifest binds exact bytes, layout, target and bridge version. Provenance
// remains opaque data; it can never select a command or executable path.
func ReadManifest(ctx context.Context, view View, root, expected, target string) (*Manifest, error) {
	if !filepath.IsAbs(root) || !supportedTarget(target) || expected != "" && !ValidDigest(expected) {
		return nil, ErrInvalid
	}
	info, err := view.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalid
	}
	manifestPath := filepath.Join(root, ManifestName)
	info, err = view.Lstat(manifestPath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxManifestBytes {
		return nil, ErrInvalid
	}
	file, err := view.Open(manifestPath)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(contextReader{ctx, file}, maxManifestBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(data) > maxManifestBytes || !utf8.Valid(data) {
		return nil, ErrInvalid
	}
	if err := jsoncheck.UniqueKeys(data); err != nil {
		return nil, fmt.Errorf("%w: malformed JSON: %v", ErrInvalid, err)
	}
	digest := sha256.Sum256(data)
	hash := hex.EncodeToString(digest[:])
	if expected != "" && hash != expected {
		return nil, fmt.Errorf("%w: manifest digest mismatch", ErrInvalid)
	}
	var manifest Manifest
	if err := exact(data, &manifest, "formatVersion", "metadata", "files"); err != nil {
		return nil, err
	}
	m := manifest.Metadata
	if manifest.FormatVersion != 1 || m.BridgeVersion != 1 || m.Target != target || m.Interpreter != InterpreterPath || m.Entrypoint != EntrypointPath || m.PythonVersion == "" || !ValidDigest(m.LockSHA256) || len(m.Packages) == 0 || m.Provenance == nil || len(manifest.Files) == 0 || len(manifest.Files) > maxTreeEntries {
		return nil, ErrInvalid
	}
	packages := make(map[string]bool, len(m.Packages))
	for _, pkg := range m.Packages {
		key := strings.ToLower(strings.ReplaceAll(pkg.Name, "_", "-"))
		if key == "" || pkg.Version == "" || packages[key] {
			return nil, ErrInvalid
		}
		packages[key] = true
	}
	manifest.Digest, manifest.Bytes = hash, data
	manifest.entries = make(map[string]Entry, len(manifest.Files))
	manifest.directories = map[string]bool{".": true}
	manifest.resolved = make(map[string]string)
	for _, entry := range manifest.Files {
		if !safePath(entry.Path) || entry.Path == ManifestName || manifest.entries[entry.Path].Path != "" {
			return nil, ErrInvalid
		}
		if entry.Link == "" {
			if !ValidDigest(entry.SHA256) || entry.Size < 0 {
				return nil, ErrInvalid
			}
		} else if len(entry.Link) > 4096 || path.IsAbs(entry.Link) || strings.ContainsAny(entry.Link, "\\\x00") {
			return nil, ErrInvalid
		}
		manifest.entries[entry.Path] = entry
		for parent := path.Dir(entry.Path); parent != "."; parent = path.Dir(parent) {
			manifest.directories[parent] = true
		}
	}
	if manifest.directories[ManifestName] {
		return nil, ErrInvalid
	}
	for name := range manifest.entries {
		if manifest.directories[name] {
			return nil, fmt.Errorf("%w: file or symlink ancestor %s", ErrInvalid, name)
		}
	}
	for _, entry := range manifest.Files {
		if entry.Link != "" {
			if _, err := manifest.resolve(entry.Path); err != nil {
				return nil, err
			}
		}
	}
	for _, name := range []string{InterpreterPath, EntrypointPath} {
		resolved, err := manifest.resolve(name)
		if err != nil {
			return nil, err
		}
		entry, ok := manifest.entries[resolved]
		if !ok || entry.Link != "" || name == InterpreterPath && !entry.Executable {
			return nil, ErrInvalid
		}
	}
	return &manifest, nil
}

func supportedTarget(target string) bool {
	return target == "linux/amd64" || target == "darwin/amd64" || target == "darwin/arm64"
}

func (m *Manifest) resolve(name string) (string, error) {
	var chain []string
	for {
		if name != "." && !safePath(name) {
			return "", fmt.Errorf("%w: escaping symlink", ErrInvalid)
		}
		if resolved, ok := m.resolved[name]; ok {
			if resolved == "" {
				return "", fmt.Errorf("%w: cyclic symlink", ErrInvalid)
			}
			name = resolved
			break
		}
		if m.directories[name] {
			break
		}
		entry, ok := m.entries[name]
		if !ok {
			return "", fmt.Errorf("%w: unlisted symlink target %s", ErrInvalid, name)
		}
		if entry.Link == "" {
			break
		}
		m.resolved[name] = ""
		chain = append(chain, name)
		var err error
		name, err = m.linkTarget(name, nil)
		if err != nil {
			return "", err
		}
	}
	for _, link := range chain {
		m.resolved[link] = name
	}
	return name, nil
}

// linkTarget follows raw components before interpreting "..". Cleaning first
// is unsafe: "root/../file" escapes if root is a symlink back to the bundle
// root. Intermediate symlinks are refused; ordinary terminal chains remain
// supported by resolve and available.
func (m *Manifest) linkTarget(name string, checkDir func(string) error) (string, error) {
	current := path.Dir(name)
	parts := strings.Split(m.entries[name].Link, "/")
	for n, component := range parts {
		switch component {
		case "", ".":
			continue
		case "..":
			if current == "." {
				return "", fmt.Errorf("%w: escaping symlink", ErrInvalid)
			}
			current = path.Dir(current)
		default:
			if current == "." {
				current = component
			} else {
				current += "/" + component
			}
			if n < len(parts)-1 {
				if !m.directories[current] {
					return "", fmt.Errorf("%w: non-directory symlink component", ErrInvalid)
				}
				if checkDir != nil {
					if err := checkDir(current); err != nil {
						return "", err
					}
				}
			}
		}
	}
	return current, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// VerifyTree checks every regular file and symlink, and rejects unlisted bytes.
// It returns actual directories, including empty directories, for private copy.
func VerifyTree(ctx context.Context, view View, root string, manifest *Manifest) ([]string, error) {
	seen := make(map[string]bool, len(manifest.Files))
	dirs := []string{"."}
	count := 0
	buffer := make([]byte, 64<<10)
	var walk func(string) error
	walk = func(relative string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := view.ReadDir(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			return err
		}
		for _, item := range entries {
			count++
			if count > maxTreeEntries {
				return ErrInvalid
			}
			name := item.Name()
			if !safePath(name) || strings.Contains(name, "/") {
				return ErrInvalid
			}
			name = path.Join(relative, name)
			absolute := filepath.Join(root, filepath.FromSlash(name))
			info, err := view.Lstat(absolute)
			if err != nil {
				return err
			}
			if info.IsDir() {
				if _, listed := manifest.entries[name]; listed {
					return ErrInvalid
				}
				dirs = append(dirs, name)
				if err = walk(name); err != nil {
					return err
				}
				continue
			}
			if name == ManifestName {
				if !info.Mode().IsRegular() {
					return ErrInvalid
				}
				continue
			}
			entry, ok := manifest.entries[name]
			if !ok || seen[name] {
				return fmt.Errorf("%w: unlisted file %s", ErrInvalid, name)
			}
			seen[name] = true
			if entry.Link != "" {
				if info.Mode()&os.ModeSymlink == 0 {
					return ErrInvalid
				}
				target, linkErr := view.Readlink(absolute)
				if linkErr != nil {
					return linkErr
				}
				if target != entry.Link {
					return ErrInvalid
				}
				continue
			}
			if !info.Mode().IsRegular() || info.Size() != entry.Size || (info.Mode().Perm()&0o111 != 0) != entry.Executable || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
				return fmt.Errorf("%w: file metadata %s", ErrInvalid, name)
			}
			file, err := view.Open(absolute)
			if err != nil {
				return err
			}
			hash := sha256.New()
			n, readErr := io.CopyBuffer(hash, io.LimitReader(contextReader{ctx, file}, entry.Size+1), buffer)
			closeErr := file.Close()
			if readErr != nil {
				return readErr
			}
			if closeErr != nil {
				return closeErr
			}
			if n != entry.Size || hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
				return fmt.Errorf("%w: file digest %s", ErrInvalid, name)
			}
		}
		return nil
	}
	info, err := view.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalid
	}
	if err = walk("."); err != nil {
		return nil, err
	}
	if len(seen) != len(manifest.Files) {
		return nil, fmt.Errorf("%w: missing files", ErrInvalid)
	}
	// A source changed after ReadManifest must not promote a different identity.
	current, err := ReadManifest(ctx, view, root, manifest.Digest, manifest.Metadata.Target)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(current.Bytes, manifest.Bytes) {
		return nil, ErrInvalid
	}
	return dirs, nil
}
