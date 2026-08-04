package sandbox

import (
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// runtimeReadOnlyRoots derives the extra read-only directories needed to
// execute the resolved shell and binaries exposed by PATH. FHS roots already
// cover their usual loader locations; this closes the non-FHS case where an
// executable's ELF interpreter and libraries live in another absolute tree
// (notably /nix/store). It parses ELF metadata directly: executing ldd would
// run an untrusted program while building its policy.
func runtimeReadOnlyRoots(shell string, env []string) ([]string, error) {
	candidates := []string{shell}
	for _, pathDir := range pathEntries(env) {
		dir, ok, err := canonicalOptionalDir(pathDir)
		if err != nil {
			return nil, fmt.Errorf("PATH dir %q: %w", pathDir, err)
		}
		if !ok {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("read PATH dir %q: %w", dir, err)
		}
		for _, entry := range entries {
			candidate := filepath.Join(dir, entry.Name())
			info, err := os.Stat(candidate)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
				continue
			}
			candidates = append(candidates, candidate)
		}
	}

	roots := make([]string, 0, len(candidates))
	seenFiles := make(map[string]bool, len(candidates))
	seenRoots := make(map[string]bool, len(candidates))
	for i, candidate := range candidates {
		if err := addExecutableRoots(candidate, &roots, seenFiles, seenRoots); err != nil {
			if i == 0 {
				return nil, err // the shell itself must always be executable
			}
			// PATH can contain root-owned administration programs which the
			// current user cannot read. They were never runnable by this shell;
			// skip them rather than making an unrelated usable shell fail setup.
			continue
		}
	}
	return roots, nil
}

func addExecutableRoots(path string, roots *[]string, seenFiles, seenRoots map[string]bool) error {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve executable %q: %w", path, err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return fmt.Errorf("stat executable %q: %w", canonical, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("executable %q is not a regular file", canonical)
	}
	return addELFRoots(canonical, roots, seenFiles, seenRoots)
}

func addELFRoots(path string, roots *[]string, seenFiles, seenRoots map[string]bool) error {
	if seenFiles[path] {
		return nil
	}
	seenFiles[path] = true
	addRoot(filepath.Dir(path), roots, seenRoots)

	file, err := elf.Open(path)
	if err != nil {
		var formatErr *elf.FormatError
		if errors.As(err, &formatErr) || errors.Is(err, elf.ErrNoSymbols) {
			return nil // executable scripts have no ELF dependency graph
		}
		return fmt.Errorf("read ELF %q: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	searchDirs, err := elfSearchDirs(file, path)
	if err != nil {
		return fmt.Errorf("read ELF search paths %q: %w", path, err)
	}
	for _, dir := range searchDirs {
		addRoot(dir, roots, seenRoots)
	}

	interpreter, err := elfInterpreter(file)
	if err != nil {
		return fmt.Errorf("read ELF interpreter %q: %w", path, err)
	}
	if interpreter != "" {
		if depErr := addELFRoots(interpreter, roots, seenFiles, seenRoots); depErr != nil {
			return depErr
		}
	}

	needed, err := file.DynString(elf.DT_NEEDED)
	if err != nil {
		return nil // static ELF has no dynamic section
	}
	for _, name := range needed {
		dependency, ok := resolveELFDependency(name, searchDirs)
		if !ok {
			// Standard FHS loader locations are already covered by the documented
			// system roots. A dependency outside those roots must be declared by an
			// absolute RUNPATH/RPATH to launch reliably, otherwise fail closed.
			continue
		}
		if err := addELFRoots(dependency, roots, seenFiles, seenRoots); err != nil {
			return err
		}
	}
	return nil
}

func addRoot(path string, roots *[]string, seen map[string]bool) {
	canonical, ok, err := canonicalOptionalDir(path)
	if err != nil || !ok || seen[canonical] {
		return
	}
	seen[canonical] = true
	*roots = append(*roots, canonical)
}

func elfSearchDirs(file *elf.File, path string) ([]string, error) {
	origin := filepath.Dir(path)
	paths := []string{origin}
	for _, tag := range []elf.DynTag{elf.DT_RUNPATH, elf.DT_RPATH} {
		entries, err := file.DynString(tag)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			for _, dir := range strings.Split(entry, ":") {
				dir = strings.ReplaceAll(dir, "$ORIGIN", origin)
				if filepath.IsAbs(dir) {
					paths = append(paths, dir)
				}
			}
		}
	}
	roots := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		canonical, ok, err := canonicalOptionalDir(path)
		if err != nil {
			return nil, err
		}
		if ok && !seen[canonical] {
			seen[canonical] = true
			roots = append(roots, canonical)
		}
	}
	return roots, nil
}

func elfInterpreter(file *elf.File) (string, error) {
	section := file.Section(".interp")
	if section == nil {
		return "", nil
	}
	data, err := io.ReadAll(section.Open())
	if err != nil {
		return "", err
	}
	interpreter := strings.TrimSuffix(string(data), "\x00")
	if interpreter == "" || !filepath.IsAbs(interpreter) {
		return "", nil
	}
	canonical, err := filepath.EvalSymlinks(interpreter)
	if err != nil {
		return "", err
	}
	return canonical, nil
}

func resolveELFDependency(name string, searchDirs []string) (string, bool) {
	if filepath.IsAbs(name) {
		if path, err := filepath.EvalSymlinks(name); err == nil {
			return path, true
		}
		return "", false
	}
	for _, dir := range searchDirs {
		path := filepath.Join(dir, name)
		canonical, err := filepath.EvalSymlinks(path)
		if err == nil {
			return canonical, true
		}
	}
	return "", false
}
