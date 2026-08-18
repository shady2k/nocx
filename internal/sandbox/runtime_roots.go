package sandbox

import (
	"bytes"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Budgets for runtime PATH scanning to bound work before the final policy
// limits are applied. Exceeding either returns a path-free error.
var (
	runtimePathEntryBudget     = 16384
	runtimePathCandidateBudget = 16384
)

var (
	errRuntimePathEntryBudget     = errors.New("sandbox: PATH scanning exceeds directory entry limit")
	errRuntimePathCandidateBudget = errors.New("sandbox: PATH scanning exceeds executable candidate limit")
)

// Bounds for ELF metadata parsing to prevent resource exhaustion
// from maliciously crafted ELF files.
var (
	runtimeELFMaxInterp          = uint64(4096) // max PT_INTERP section size
	runtimeELFMaxDynLen          = uint64(4096) // max one dynamic string
	runtimeELFMaxDynStrings      = 256          // max DT_NEEDED/RUNPATH/RPATH entries per tag
	runtimeELFMaxFiles           = 65536        // max ELF files in a dependency graph
	runtimeELFMaxDepth           = 64           // max synchronous dependency depth
	runtimeELFMaxResolveAttempts = 65536        // max filesystem probes while resolving dependencies
)

var (
	errRuntimeELFInterp    = errors.New("sandbox: ELF interpreter section too large")
	errRuntimeELFNeeded    = errors.New("sandbox: ELF dependency list too large")
	errRuntimeELFSearchDir = errors.New("sandbox: ELF search directory list too large")
	errRuntimeELFDynString = errors.New("sandbox: ELF dynamic string too large")
	errRuntimeELFAggregate = errors.New("sandbox: ELF dependency graph too large")
)

var (
	runtimeELFMaxSectionBytes   = uint64(1 << 20)
	runtimeELFMaxAggregateBytes = uint64(64 << 20)
)

var (
	errRuntimeELFMetadataBudget = errors.New("sandbox: ELF metadata exceeds section byte limit")
	errRuntimeELFWorkBudget     = errors.New("sandbox: ELF analysis exceeds work limit")
)

// runtimeReadOnlyRoots derives the extra read-only directories needed to
// execute the resolved shell and binaries exposed by PATH. FHS roots already
// cover their usual loader locations; this closes the non-FHS case where an
// executable's ELF interpreter and libraries live in another absolute tree
// (notably /nix/store). It parses ELF metadata directly: executing ldd would
// run an untrusted program while building its policy.
func runtimeReadOnlyRoots(shell string, env []string) ([]string, error) {
	candidates := []string{shell}
	seenDirs := make(map[string]bool)
	const readBatch = 128
	var totalEntries, totalCandidates int

	for _, pathDir := range pathEntries(env) {
		if !filepath.IsAbs(pathDir) {
			continue
		}
		dir, ok, err := canonicalOptionalDir(pathDir)
		if err != nil {
			return nil, fmt.Errorf("PATH dir %q: %w", pathDir, err)
		}
		if !ok || seenDirs[dir] {
			continue
		}
		seenDirs[dir] = true

		// #nosec G304 -- dir is a canonical backend-process PATH directory; scanning it is the policy builder's purpose.
		f, err := os.Open(dir)
		if err != nil {
			return nil, fmt.Errorf("read PATH dir %q: %w", dir, err)
		}
		scanErr := func() error {
			defer func() { _ = f.Close() }()
			for {
				entries, err := f.ReadDir(readBatch)
				for _, entry := range entries {
					totalEntries++
					if totalEntries > runtimePathEntryBudget {
						return errRuntimePathEntryBudget
					}
					candidate := filepath.Join(dir, entry.Name())
					info, statErr := os.Stat(candidate)
					if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
						continue
					}
					totalCandidates++
					if totalCandidates > runtimePathCandidateBudget {
						return errRuntimePathCandidateBudget
					}
					candidates = append(candidates, candidate)
				}
				if err != nil {
					if err == io.EOF {
						return nil
					}
					return fmt.Errorf("read PATH dir %q: %w", dir, err)
				}
			}
		}()
		if scanErr != nil {
			return nil, scanErr
		}
	}
	roots := make([]string, 0, len(candidates))
	seenFiles := make(map[string]bool, len(candidates))
	seenRoots := make(map[string]bool, len(candidates))
	var elfCount, resolveAttempts int
	var elfBytes uint64
	for i, candidate := range candidates {
		if err := addExecutableRoots(candidate, &roots, seenFiles, seenRoots, &elfCount, &elfBytes, &resolveAttempts); err != nil {
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

func addExecutableRoots(path string, roots *[]string, seenFiles, seenRoots map[string]bool, elfCount *int, elfBytes *uint64, resolveAttempts *int) error {
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
	return addELFRoots(canonical, roots, seenFiles, seenRoots, elfCount, elfBytes, resolveAttempts, 0)
}

func addELFRoots(path string, roots *[]string, seenFiles, seenRoots map[string]bool, elfCount *int, elfBytes *uint64, resolveAttempts *int, depth int) error {
	if seenFiles[path] {
		return nil
	}
	if depth >= runtimeELFMaxDepth {
		return errRuntimeELFWorkBudget
	}
	*elfCount = *elfCount + 1
	if *elfCount > runtimeELFMaxFiles {
		return errRuntimeELFAggregate
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
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()

	var fileBytes uint64
	searchDirs, err := elfSearchDirs(file, path, &fileBytes, elfBytes, resolveAttempts)
	if err != nil {
		if errors.Is(err, errRuntimeELFSearchDir) || errors.Is(err, errRuntimeELFDynString) ||
			errors.Is(err, errRuntimeELFMetadataBudget) || errors.Is(err, errRuntimeELFWorkBudget) {
			return err
		}
		return fmt.Errorf("read ELF search paths %q: %w", path, err)
	}
	for _, dir := range searchDirs {
		if seenRoots[dir] {
			continue
		}
		seenRoots[dir] = true
		*roots = append(*roots, dir)
	}

	interpreter, err := elfInterpreter(file, &fileBytes, elfBytes, resolveAttempts)
	if err != nil {
		if errors.Is(err, errRuntimeELFInterp) || errors.Is(err, errRuntimeELFMetadataBudget) ||
			errors.Is(err, errRuntimeELFWorkBudget) {
			return err
		}
		return fmt.Errorf("read ELF interpreter %q: %w", path, err)
	}

	needed, err := elfDynamicStrings(file, elf.DT_NEEDED, errRuntimeELFNeeded, &fileBytes, elfBytes)
	if err != nil {
		if errors.Is(err, errRuntimeELFNeeded) || errors.Is(err, errRuntimeELFDynString) ||
			errors.Is(err, errRuntimeELFMetadataBudget) || errors.Is(err, errRuntimeELFWorkBudget) {
			return err
		}
		return nil // static ELF has no dynamic section
	}
	if closeErr := file.Close(); closeErr != nil {
		return fmt.Errorf("close ELF %q: %w", path, closeErr)
	}
	closed = true

	if interpreter != "" {
		if depErr := addELFRoots(interpreter, roots, seenFiles, seenRoots, elfCount, elfBytes, resolveAttempts, depth+1); depErr != nil {
			return depErr
		}
	}
	for _, name := range needed {
		dependency, ok, resolveErr := resolveELFDependency(name, searchDirs, resolveAttempts)
		if resolveErr != nil {
			return resolveErr
		}
		if !ok {
			// Standard FHS loader locations are already covered by the documented
			// system roots. A dependency outside those roots must be declared by an
			// absolute RUNPATH/RPATH to launch reliably, otherwise fail closed.
			continue
		}
		if err := addELFRoots(dependency, roots, seenFiles, seenRoots, elfCount, elfBytes, resolveAttempts, depth+1); err != nil {
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

func elfSearchDirs(file *elf.File, path string, fileBytes, aggregateBytes *uint64, resolveAttempts *int) ([]string, error) {
	origin := filepath.Dir(path)
	paths := make([]string, 0, runtimeELFMaxDynStrings)
	for _, tag := range []elf.DynTag{elf.DT_RUNPATH, elf.DT_RPATH} {
		entries, err := elfDynamicStrings(file, tag, errRuntimeELFSearchDir, fileBytes, aggregateBytes)
		if err != nil {
			if errors.Is(err, errRuntimeELFSearchDir) || errors.Is(err, errRuntimeELFDynString) {
				return nil, err
			}
			continue
		}
		for _, entry := range entries {
			for _, dir := range strings.Split(entry, ":") {
				dir = strings.ReplaceAll(dir, "$ORIGIN", origin)
				if filepath.IsAbs(dir) {
					paths = append(paths, dir)
					if len(paths) > runtimeELFMaxDynStrings {
						return nil, errRuntimeELFSearchDir
					}
				}
			}
		}
	}
	return canonicalELFSearchDirs(origin, paths, resolveAttempts)
}

func canonicalELFSearchDirs(origin string, paths []string, resolveAttempts *int) ([]string, error) {
	roots := make([]string, 0, len(paths)+1)
	roots = append(roots, origin)
	seen := map[string]bool{origin: true}
	for _, path := range paths {
		if budgetErr := chargeELFResolution(resolveAttempts); budgetErr != nil {
			return nil, budgetErr
		}
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

func elfInterpreter(file *elf.File, fileBytes, aggregateBytes *uint64, resolveAttempts *int) (string, error) {
	section := file.Section(".interp")
	if section == nil {
		return "", nil
	}
	if section.Size > runtimeELFMaxInterp {
		return "", errRuntimeELFInterp
	}
	data, err := elfReadSection(section, fileBytes, aggregateBytes)
	if err != nil {
		return "", err
	}
	interpreter := strings.TrimSuffix(string(data), "\x00")
	if interpreter == "" || !filepath.IsAbs(interpreter) {
		return "", nil
	}
	if budgetErr := chargeELFResolution(resolveAttempts); budgetErr != nil {
		return "", budgetErr
	}
	canonical, err := filepath.EvalSymlinks(interpreter)
	if err != nil {
		return "", err
	}
	return canonical, nil
}

func elfReadSection(section *elf.Section, fileBytes, aggregateBytes *uint64) ([]byte, error) {
	if section.Size > runtimeELFMaxSectionBytes {
		return nil, errRuntimeELFMetadataBudget
	}
	if *fileBytes > runtimeELFMaxSectionBytes ||
		section.Size > runtimeELFMaxSectionBytes-*fileBytes ||
		*aggregateBytes > runtimeELFMaxAggregateBytes ||
		section.Size > runtimeELFMaxAggregateBytes-*aggregateBytes {
		return nil, errRuntimeELFWorkBudget
	}
	if section.Size == 0 {
		return nil, nil
	}
	if section.ReaderAt == nil {
		return nil, errRuntimeELFMetadataBudget
	}
	// #nosec G115 -- section.Size is capped at 1 MiB above.
	data := make([]byte, int(section.Size)) //nolint:gosec
	n, err := section.ReaderAt.ReadAt(data, 0)
	if err != nil && !(errors.Is(err, io.EOF) && n == len(data)) {
		return nil, err
	}
	if n != len(data) {
		return nil, io.ErrUnexpectedEOF
	}
	*fileBytes += section.Size
	*aggregateBytes += section.Size
	return data, nil
}

func elfDynamicStrings(file *elf.File, tag elf.DynTag, tooMany error, fileBytes, aggregateBytes *uint64) ([]string, error) {
	section := file.SectionByType(elf.SHT_DYNAMIC)
	if section == nil {
		return nil, nil
	}
	// #nosec G115 -- ELF section indexes are uint32 by format.
	if section.Link == 0 || section.Link >= uint32(len(file.Sections)) { //nolint:gosec
		return nil, errRuntimeELFMetadataBudget
	}
	stringsSection := file.Sections[section.Link]
	if stringsSection.Size > runtimeELFMaxSectionBytes {
		return nil, errRuntimeELFMetadataBudget
	}
	data, err := elfReadSection(section, fileBytes, aggregateBytes)
	if err != nil {
		return nil, err
	}
	dynSize := 8
	if file.Class == elf.ELFCLASS64 {
		dynSize = 16
	}
	if len(data)%dynSize != 0 {
		return nil, errRuntimeELFMetadataBudget
	}
	var values []string
	for len(data) >= dynSize {
		var currentTag elf.DynTag
		var value uint64
		if file.Class == elf.ELFCLASS64 {
			// #nosec G115 -- ELF64 dynamic tags are encoded in this unsigned word.
			currentTag = elf.DynTag(file.ByteOrder.Uint64(data[:8])) //nolint:gosec
			value = file.ByteOrder.Uint64(data[8:16])
		} else {
			currentTag = elf.DynTag(file.ByteOrder.Uint32(data[:4]))
			value = uint64(file.ByteOrder.Uint32(data[4:8]))
		}
		data = data[dynSize:]
		if currentTag != tag {
			continue
		}
		if len(values) >= runtimeELFMaxDynStrings {
			return nil, tooMany
		}
		name, ok, err := elfDynamicString(stringsSection, value, fileBytes, aggregateBytes)
		if err != nil {
			return nil, err
		}
		if ok {
			values = append(values, name)
		}
	}
	return values, nil
}

func elfDynamicString(section *elf.Section, offset uint64, fileBytes, aggregateBytes *uint64) (string, bool, error) {
	if offset >= section.Size {
		return "", false, nil
	}
	remaining := section.Size - offset
	limit := runtimeELFMaxDynLen + 1
	if remaining < limit {
		limit = remaining
	}
	if *fileBytes > runtimeELFMaxSectionBytes ||
		limit > runtimeELFMaxSectionBytes-*fileBytes ||
		*aggregateBytes > runtimeELFMaxAggregateBytes ||
		limit > runtimeELFMaxAggregateBytes-*aggregateBytes {
		return "", false, errRuntimeELFWorkBudget
	}
	if section.ReaderAt == nil {
		return "", false, errRuntimeELFMetadataBudget
	}
	// #nosec G115 -- limit is capped at 4097 bytes above.
	data := make([]byte, int(limit)) //nolint:gosec
	// #nosec G115 -- offset is below the 1 MiB section cap.
	n, err := section.ReaderAt.ReadAt(data, int64(offset)) //nolint:gosec
	if err != nil && !(errors.Is(err, io.EOF) && n > 0) {
		return "", false, err
	}
	data = data[:n]
	// #nosec G115 -- io.ReaderAt returns a non-negative byte count.
	*fileBytes += uint64(n) //nolint:gosec
	// #nosec G115 -- io.ReaderAt returns a non-negative byte count.
	*aggregateBytes += uint64(n) //nolint:gosec
	if end := bytes.IndexByte(data, 0); end >= 0 {
		return string(data[:end]), true, nil
	}
	// #nosec G115 -- io.ReaderAt returns a non-negative byte count.
	if uint64(n) == runtimeELFMaxDynLen+1 { //nolint:gosec
		return "", false, errRuntimeELFDynString
	}
	return "", false, nil
}

func chargeELFResolution(attempts *int) error {
	if *attempts >= runtimeELFMaxResolveAttempts {
		return errRuntimeELFWorkBudget
	}
	*attempts = *attempts + 1
	return nil
}

func resolveELFDependency(name string, searchDirs []string, attempts *int) (string, bool, error) {
	if filepath.IsAbs(name) {
		if err := chargeELFResolution(attempts); err != nil {
			return "", false, err
		}
		if path, err := filepath.EvalSymlinks(name); err == nil {
			return path, true, nil
		}
		return "", false, nil
	}
	for _, dir := range searchDirs {
		if err := chargeELFResolution(attempts); err != nil {
			return "", false, err
		}
		path := filepath.Join(dir, name)
		canonical, err := filepath.EvalSymlinks(path)
		if err == nil {
			return canonical, true, nil
		}
	}
	return "", false, nil
}
