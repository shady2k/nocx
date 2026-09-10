package skill

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
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
	detailed, _ := discoverAll(roots, false)
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
// discoverDetailed is the projection every caller but the list wants: the
// skills, without the directories that are not skills.
func discoverDetailed(roots []Root, includeDisabled bool) []discovered {
	found, _ := discoverAll(roots, includeDisabled)
	return found
}

// discoverAll is the one walk. It answers with what it INDEXED and with what
// it REFUSED, because both are things a person put on disk and only one of
// them used to be tellable from an empty root (nocx-j0lei). Refusals are
// returned rather than logged-and-forgotten; the log lines stay, since they
// carry the operator's detail and this carries the person's.
func discoverAll(roots []Root, includeDisabled bool) ([]discovered, []Refusal) {
	set := switches{off: map[string]struct{}{}, on: map[string]struct{}{}}
	digests := map[string]string{}
	for _, root := range roots {
		if root.switches != nil {
			var err error
			set, err = root.switches()
			if err != nil {
				return nil, nil
			}
		}
		if root.digests != nil {
			var err error
			digests, err = root.digests()
			if err != nil {
				return nil, nil
			}
		}
		if root.switches != nil || root.digests != nil {
			break
		}
	}
	seen := make(map[string]struct{})
	out := make([]discovered, 0)
	refused := make([]Refusal, 0)
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
				refused = append(refused, refusalAt(root, name, RefusedSymlink, refusedSymlink("skill folder")))
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
					// A directory with NO SKILL.md is not a refusal, it is a
					// directory: the roots hold ordinary folders and a row
					// for each would teach people to ignore the list.
					if !errors.Is(statErr, fs.ErrNotExist) {
						slog.Warn("skill: SKILL.md unavailable", "skill", name, "error", statErr)
						refused = append(refused, refusalAt(root, name, RefusedUnreadable, refusedUnreadable(statErr)))
					}
					continue
				}
				if info.Mode()&os.ModeSymlink != 0 {
					slog.Warn("skill: SKILL.md is a symlink", "skill", name)
					refused = append(refused, refusalAt(root, name, RefusedSymlink, refusedSymlink("SKILL.md")))
					continue
				}
			}
			data, readErr := readRootFile(root, name, "SKILL.md", MaxFrontmatterBytes)
			if readErr != nil {
				slog.Warn("skill: SKILL.md unreadable", "skill", name, "error", readErr)
				refused = append(refused, refusalAt(root, name, RefusedUnreadable, refusedUnreadable(readErr)))
				continue
			}
			fm, _, ok := parseFrontmatter(data)
			if !ok {
				slog.Warn("skill: invalid frontmatter", "skill", name)
				refused = append(refused, refusalAt(root, name, RefusedFrontmatter, refusedFrontmatterDetail))
				continue
			}
			skName := strings.TrimSpace(fm.Name)
			if skName == "" {
				skName = name
			}
			if !skillNamePattern.MatchString(skName) {
				slog.Warn("skill: invalid name", "skill", name, "name", skName)
				refused = append(refused, refusalAt(root, name, RefusedName, refusedNameDetail(skName)))
				continue
			}
			description := strings.TrimSpace(fm.Description)
			if description == "" {
				slog.Warn("skill: missing description", "skill", name)
				refused = append(refused, refusalAt(root, name, RefusedNoDescription, refusedNoDescriptionDetail))
				continue
			}
			// The read path is where a description no write of ours ever saw
			// arrives: a directory placed by hand, restored from a backup, or
			// written before the cap existed. It is REFUSED rather than
			// clamped, and that is the decision. Clamping would put a
			// sentence into the system prompt that stops mid-clause and reads
			// as though its author had written it — a claim they did not
			// make, which is precisely what the write refuses to manufacture;
			// and the description is the whole of what a skill offers the
			// model, so half of one is not a lesser version of the skill, it
			// is a different one.
			//
			// The skill does not leave the list in silence any more: it
			// gets a refused row naming both numbers, like every other
			// refusal in this loop (nocx-j0lei). The person's remedy is to
			// shorten the description in the file, which is a thing they can
			// only do if they are told.
			if length, over := descriptionOverCap(description); over {
				slog.Warn("skill: description too long", "skill", name, "characters", length, "limit", maxDescriptionRunes)
				refused = append(refused, refusalAt(root, name, RefusedDescriptionTooLong, refusedTooLongDetail(length, maxDescriptionRunes)))
				continue
			}
			if _, exists := seen[skName]; exists {
				continue
			}
			seen[skName] = struct{}{}
			// The person's switch, defaulted by the ROOT and then moved by
			// whatever the document records about this name. An installed
			// skill arrives off and the document says who turned it on;
			// everything else arrives on and the document says who turned it
			// off. Both directions are read here, in the one place that owns
			// enablement, rather than at the two call sites that care.
			enabled := !root.Provenance.inertOnArrival()
			if enabled {
				if _, turnedOff := set.off[skName]; turnedOff {
					enabled = false
				}
			} else if _, turnedOn := set.on[skName]; turnedOn {
				enabled = true
			}
			if !enabled && !includeDisabled {
				continue
			}
			changed := false
			if root.Provenance.digested() {
				expected, approved := digests[skName]
				actual, hashErr := hashSkillDirectory(base)
				if hashErr != nil {
					slog.Warn("skill: cannot hash skill", "skill", skName, "provenance", root.Provenance, "error", hashErr)
					changed = true
				} else {
					changed = !approved || actual != expected
				}
			}
			out = append(out, discovered{Skill: Skill{
				Name: skName, Description: description,
				Provenance: root.Provenance, BaseDir: base, Enabled: enabled,
				Status: statusFor(root.Provenance, changed),
			}, root: root})
		}
	}
	return out, refused
}

// refusalAt names the folder, the root it sits in and the file to open.
func refusalAt(root Root, dir string, reason RefusalReason, detail string) Refusal {
	return Refusal{
		Directory:  dir,
		Provenance: root.Provenance,
		Path:       filepath.Join(joinRootPath(root, dir), "SKILL.md"),
		Reason:     reason,
		Detail:     detail,
	}
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

func hashSkillDirectory(base string) (string, error) {
	h := sha256.New()
	root, err := os.OpenRoot(base)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()

	err = filepath.WalkDir(base, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(base, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		var data []byte
		if entry.Type()&fs.ModeSymlink != 0 {
			target, targetErr := os.Readlink(path)
			if targetErr != nil {
				return targetErr
			}
			data = []byte("symlink:" + target)
		} else {
			info, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%s is not a regular file", rel)
			}
			file, openErr := root.Open(filepath.FromSlash(rel))
			if openErr != nil {
				return openErr
			}
			var readErr error
			data, readErr = io.ReadAll(file)
			closeErr := file.Close()
			if readErr != nil {
				return readErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		writeDigestPart(h, []byte(rel))
		writeDigestPart(h, data)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeDigestPart(h hash.Hash, data []byte) {
	var length [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(length[:], uint64(len(data)))
	_, _ = h.Write(length[:n])
	_, _ = h.Write(data)
}
