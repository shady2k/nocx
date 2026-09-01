package skill

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var skillNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type discovered struct {
	Skill
	root Root
}

// Discover reads only one level of each root and only the frontmatter of each
// candidate SKILL.md. Broken entries are logged and skipped so one bad file
// cannot make an ask fail.
func Discover(roots []Root) []Skill {
	detailed := discoverDetailed(roots, false)
	out := make([]Skill, 0, len(detailed))
	for _, candidate := range detailed {
		out = append(out, candidate.Skill)
	}
	return out
}

// discoverDetailed is the single source of truth for root precedence,
// validation, diagnostics, name deduplication, and enablement. Read selects
// from this same result so a skill cannot be indexed differently from how it
// is read.
func discoverDetailed(roots []Root, includeDisabled bool) []discovered {
	disabled := map[string]struct{}{}
	for _, root := range roots {
		if root.disabled == nil {
			continue
		}
		var err error
		disabled, err = root.disabled()
		if err != nil {
			return nil
		}
		break
	}
	seen := make(map[string]struct{})
	out := make([]discovered, 0)
	for _, root := range roots {
		entries, cut, err := rootEntries(root)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				slog.Warn("skill: root unavailable", "root", root.Dir, "error", err)
			}
			continue
		}
		if cut {
			slog.Warn("skill: root entry cap reached", "root", root.Dir, "cap", MaxEntriesPerRoot)
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.Type()&fs.ModeSymlink != 0 {
				slog.Warn("skill: skill directory is a symlink", "skill", name)
				continue
			}
			if !entry.IsDir() {
				continue
			}
			base := joinRootPath(root, name)
			if root.Dir != "" {
				// #nosec G304 -- path is a discovered skill file under a configured root.
				info, statErr := os.Lstat(filepath.Join(root.Dir, name, "SKILL.md"))
				if statErr != nil {
					if !errors.Is(statErr, fs.ErrNotExist) {
						slog.Warn("skill: SKILL.md unavailable", "skill", name, "error", statErr)
					}
					continue
				}
				if info.Mode()&os.ModeSymlink != 0 {
					slog.Warn("skill: SKILL.md is a symlink", "skill", name)
					continue
				}
			}
			data, readErr := readRootFile(root, name, "SKILL.md", MaxFrontmatterBytes)
			if readErr != nil {
				slog.Warn("skill: SKILL.md unreadable", "skill", name, "error", readErr)
				continue
			}
			fm, _, ok := parseFrontmatter(data)
			if !ok {
				slog.Warn("skill: invalid frontmatter", "skill", name)
				continue
			}
			skName := strings.TrimSpace(fm.Name)
			if skName == "" {
				skName = name
			}
			if !skillNamePattern.MatchString(skName) {
				slog.Warn("skill: invalid name", "skill", name, "name", skName)
				continue
			}
			description := strings.TrimSpace(fm.Description)
			if description == "" {
				slog.Warn("skill: missing description", "skill", name)
				continue
			}
			if _, exists := seen[skName]; exists {
				continue
			}
			seen[skName] = struct{}{}
			enabled := true
			if _, isDisabled := disabled[skName]; isDisabled {
				enabled = false
				if !includeDisabled {
					continue
				}
			}
			out = append(out, discovered{Skill: Skill{
				Name: skName, Description: description,
				Provenance: root.Provenance, BaseDir: base, Enabled: enabled,
			}, root: root})
		}
	}
	return out
}

func rootEntries(root Root) ([]fs.DirEntry, bool, error) {
	if root.FS != nil {
		entries, err := fs.ReadDir(root.FS, ".")
		if err != nil {
			return nil, false, err
		}
		cut := len(entries) > MaxEntriesPerRoot
		if cut {
			entries = entries[:MaxEntriesPerRoot]
		}
		return entries, cut, nil
	}
	if root.Dir == "" {
		return nil, false, fmt.Errorf("skill root has neither Dir nor FS")
	}
	f, err := os.Open(root.Dir)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			slog.Warn("skill: close root failed", "root", root.Dir, "error", closeErr)
		}
	}()
	entries, err := f.ReadDir(MaxEntriesPerRoot + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	cut := len(entries) > MaxEntriesPerRoot
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	if cut {
		entries = entries[:MaxEntriesPerRoot]
	}
	return entries, cut, nil
}

func joinRootPath(root Root, name string) string {
	if root.Dir != "" {
		return filepath.Join(root.Dir, name)
	}
	return name
}

func readRootFile(root Root, name, rel string, limit int) ([]byte, error) {
	var path string
	if root.Dir != "" {
		path = filepath.Join(root.Dir, name, rel)
	} else {
		path = name + "/" + rel
	}
	var r io.Reader
	if root.FS != nil {
		file, err := root.FS.Open(path)
		if err != nil {
			return nil, err
		}
		defer func() {
			if closeErr := file.Close(); closeErr != nil {
				slog.Warn("skill: close skill file failed", "path", path, "error", closeErr)
			}
		}()
		r = file
	} else {
		// #nosec G304 -- path is a discovered skill file under a configured root.
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer func() {
			if closeErr := file.Close(); closeErr != nil {
				slog.Warn("skill: close skill file failed", "path", path, "error", closeErr)
			}
		}()
		r = file
	}
	return io.ReadAll(io.LimitReader(r, int64(limit)))
}

func parseFrontmatter(data []byte) (frontmatter, int, bool) {
	if !bytes.HasPrefix(data, []byte("---\n")) && !bytes.HasPrefix(data, []byte("---\r\n")) {
		return frontmatter{}, 0, false
	}
	start := bytes.IndexByte(data, '\n')
	if start < 0 {
		return frontmatter{}, 0, false
	}
	end := bytes.Index(data[start+1:], []byte("\n---"))
	if end < 0 {
		return frontmatter{}, 0, false
	}
	end += start + 1
	closeEnd := end + len("\n---")
	if closeEnd < len(data) && data[closeEnd] == '\r' {
		closeEnd++
	}
	if closeEnd < len(data) && data[closeEnd] == '\n' {
		closeEnd++
	}
	var fm frontmatter
	if err := yaml.Unmarshal(data[start+1:end], &fm); err != nil {
		return frontmatter{}, 0, false
	}
	return fm, closeEnd, true
}
