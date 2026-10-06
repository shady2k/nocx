//go:build linux || darwin

package sandbox

import (
	"bytes"
	"debug/elf"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

func createRuntime(base string) (RuntimePaths, error) {
	if !filepath.IsAbs(base) || strings.ContainsAny(base, "\x00\r\n") {
		return RuntimePaths{}, fmt.Errorf("invalid runtime base")
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return RuntimePaths{}, err
	}
	var state unix.Stat_t
	if err := unix.Lstat(base, &state); err != nil {
		return RuntimePaths{}, err
	}
	if state.Mode&unix.S_IFMT != unix.S_IFDIR {
		return RuntimePaths{}, fmt.Errorf("invalid runtime base")
	}
	if int64(state.Uid) != int64(os.Geteuid()) || state.Mode&0o77 != 0 {
		return RuntimePaths{}, fmt.Errorf("unsafe runtime base")
	}
	abs, e := canonicalDir(base)
	if e != nil {
		return RuntimePaths{}, e
	}
	root, e := os.MkdirTemp(abs, "launch-")
	if e != nil {
		return RuntimePaths{}, e
	}
	// #nosec G302 -- owner-only directory requires traversal permission.
	if e = os.Chmod(root, 0o700); e != nil {
		_ = os.Remove(root)
		return RuntimePaths{}, e
	}
	for _, d := range []string{"home", "config", "data", "cache", "state", "tmp"} {
		p := filepath.Join(root, d)
		if e = os.Mkdir(p, 0o700); e != nil {
			_ = os.RemoveAll(root)
			return RuntimePaths{}, e
		}
	}
	return RuntimePaths{Root: root, Home: filepath.Join(root, "home"), Config: filepath.Join(root, "config"), Data: filepath.Join(root, "data"), Cache: filepath.Join(root, "cache"), State: filepath.Join(root, "state"), Temp: filepath.Join(root, "tmp")}, nil
}

const maxELFSection = 1 << 20

func createProjections(p *Prepared, hostHome, cwd string, profile, delta ProfileRoots) error {
	raw := append([]string{cwd}, profile.ReadOnlyDirs...)
	raw = append(raw, profile.ReadWriteDirs...)
	raw = append(raw, delta.ReadOnlyDirs...)
	raw = append(raw, delta.ReadWriteDirs...)
	targets := make([]string, 0, len(raw))
	for i, path := range raw {
		canonical, e := expandCanonical(path, hostHome, cwd)
		if e != nil {
			return buildErr("projection_target_invalid", "projection", i, e)
		}
		if canonical != hostHome && contained(hostHome, canonical) {
			targets = append(targets, canonical)
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		di, dj := strings.Count(targets[i], string(os.PathSeparator)), strings.Count(targets[j], string(os.PathSeparator))
		if di != dj {
			return di < dj
		}
		return targets[i] < targets[j]
	})
	projected := make([]string, 0, len(targets))
	for i, target := range targets {
		skip := false
		for _, parent := range projected {
			if contained(parent, target) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		rel, e := filepath.Rel(hostHome, target)
		if e != nil {
			return buildErr("projection_target_invalid", "projection", i, e)
		}
		dst := filepath.Join(p.Runtime.Home, rel)
		if e := mkdirNoFollow(p.Runtime.Home, filepath.Dir(dst)); e != nil {
			return buildErr("projection_parent_failed", "projection", i, e)
		}
		if _, e := os.Lstat(dst); e == nil {
			return buildErr("projection_collision", "projection", i, nil)
		} else if !os.IsNotExist(e) {
			return buildErr("projection_collision", "projection", i, e)
		}
		if e := unix.Symlinkat(target, int(unix.AT_FDCWD), dst); e != nil {
			return buildErr("projection_create_failed", "projection", i, e)
		}
		projected = append(projected, target)
	}
	return nil
}

func mkdirNoFollow(root, path string) error {
	rel, e := filepath.Rel(root, path)
	if e != nil {
		return e
	}
	if rel == "." {
		return nil
	}
	fd, e := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if e != nil {
		return e
	}
	defer func() { _ = unix.Close(fd) }()
	if e = checkPrivateDir(fd); e != nil {
		return e
	}
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("invalid projection path")
		}
		if e = unix.Mkdirat(fd, part, 0o700); e != nil && e != unix.EEXIST {
			return e
		}
		next, oe := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if oe != nil {
			return oe
		}
		if oe = checkPrivateDir(next); oe != nil {
			_ = unix.Close(next)
			return oe
		}
		_ = unix.Close(fd)
		fd = next
	}
	return nil
}

func checkPrivateDir(fd int) error {
	var st unix.Stat_t
	if e := unix.Fstat(fd, &st); e != nil {
		return e
	}
	if int64(st.Uid) != int64(os.Getuid()) || st.Mode&0o077 != 0 {
		return fmt.Errorf("unsafe projection directory")
	}
	return nil
}

func discoverDependencies(shell, pathEnv string) ([]string, error) {
	if runtime.GOOS == "darwin" {
		return discoverMachODependencies(shell)
	}
	if len(pathEnv) > MaxPolicyBytes {
		return nil, fmt.Errorf("PATH byte budget exceeded")
	}
	paths := strings.Split(pathEnv, string(os.PathListSeparator))
	if len(paths) > maxPATHEntries {
		return nil, fmt.Errorf("PATH entry budget exceeded")
	}
	for _, dir := range paths {
		if len(dir) > MaxPathBytes {
			return nil, fmt.Errorf("PATH entry length exceeded")
		}
	}
	queue := []elfNode{{path: shell, depth: 0}}
	seen := make(map[string]bool)
	out := make(map[string]bool)
	bytesRead := int64(0)
	rootBytes := 0
	record := func(path string) error {
		if len(path) > MaxPathBytes {
			return fmt.Errorf("dependency path budget exceeded")
		}
		if !out[path] && len(path) > MaxPolicyBytes-rootBytes {
			return fmt.Errorf("dependency path aggregate budget exceeded")
		}
		if !out[path] {
			out[path] = true
			rootBytes += len(path)
		}
		return nil
	}
	for len(queue) > 0 {
		if len(seen) >= maxELFNodes {
			return nil, fmt.Errorf("dependency node budget exceeded")
		}
		n := queue[0]
		queue = queue[1:]
		if n.depth > maxELFDepth {
			return nil, fmt.Errorf("dependency depth exceeded")
		}
		real, e := filepath.EvalSymlinks(n.path)
		if e != nil {
			return nil, fmt.Errorf("dependency unavailable")
		}
		real = filepath.Clean(real)
		if seen[real] {
			continue
		}
		seen[real] = true
		info, e := os.Stat(real)
		if e != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("dependency unavailable")
		}
		bytesRead += info.Size()
		if bytesRead > maxELFBytes {
			return nil, fmt.Errorf("dependency byte budget exceeded")
		}
		f, e := elf.Open(real)
		if e != nil {
			return nil, fmt.Errorf("ELF input invalid")
		}
		if !supportedELF(f) {
			_ = f.Close()
			return nil, fmt.Errorf("ELF format unsupported")
		}
		if dynamic := f.SectionByType(elf.SHT_DYNAMIC); dynamic != nil {
			if dynamic.Size > maxELFSection || dynamic.Entsize == 0 || dynamic.Size%dynamic.Entsize != 0 || dynamic.Size/dynamic.Entsize > maxELFTags || dynamic.Link == 0 || uint64(dynamic.Link) >= uint64(len(f.Sections)) || f.Sections[dynamic.Link].Size > maxELFSection {
				_ = f.Close()
				return nil, fmt.Errorf("ELF dynamic section budget exceeded")
			}
		}
		if real != shell {
			if recordErr := record(real); recordErr != nil {
				_ = f.Close()
				return nil, recordErr
			}
		}
		interp := ""
		for _, prog := range f.Progs {
			if prog.Type == elf.PT_INTERP {
				if prog.Filesz < 2 || prog.Filesz > maxELFString {
					_ = f.Close()
					return nil, fmt.Errorf("ELF interpreter budget exceeded")
				}
				b := make([]byte, prog.Filesz)
				if _, readErr := prog.ReadAt(b, 0); readErr != nil || b[len(b)-1] != 0 || bytes.IndexByte(b[:len(b)-1], 0) >= 0 {
					_ = f.Close()
					return nil, fmt.Errorf("ELF interpreter invalid")
				}
				interp = string(b[:len(b)-1])
				if !filepath.IsAbs(interp) {
					_ = f.Close()
					return nil, fmt.Errorf("ELF interpreter invalid")
				}
				break
			}
		}
		needed, e := f.DynString(elf.DT_NEEDED)
		if e != nil && e != elf.ErrNoSymbols {
			_ = f.Close()
			return nil, fmt.Errorf("ELF dependency table invalid")
		}
		if len(needed) > maxELFTags {
			_ = f.Close()
			return nil, fmt.Errorf("dependency tag budget exceeded")
		}
		rpath, runpathErr := f.DynString(elf.DT_RUNPATH)
		if runpathErr != nil && runpathErr != elf.ErrNoSymbols {
			_ = f.Close()
			return nil, fmt.Errorf("ELF search path invalid")
		}
		if len(rpath) == 0 {
			rpath, e = f.DynString(elf.DT_RPATH)
			if e != nil && e != elf.ErrNoSymbols {
				_ = f.Close()
				return nil, fmt.Errorf("ELF search path invalid")
			}
		}
		if len(needed)+len(rpath) > maxELFTags {
			_ = f.Close()
			return nil, fmt.Errorf("dependency tag budget exceeded")
		}
		for _, v := range needed {
			if len(v) == 0 || len(v) > maxELFString {
				_ = f.Close()
				return nil, fmt.Errorf("ELF string budget exceeded")
			}
		}
		for _, v := range rpath {
			if len(v) == 0 || len(v) > maxELFString {
				_ = f.Close()
				return nil, fmt.Errorf("ELF string budget exceeded")
			}
		}
		_ = f.Close()
		if interp != "" {
			resolved, e := filepath.EvalSymlinks(interp)
			if e != nil {
				return nil, fmt.Errorf("ELF interpreter unavailable")
			}
			info, e := os.Stat(resolved)
			if e != nil || !info.Mode().IsRegular() {
				return nil, fmt.Errorf("ELF interpreter unavailable")
			}
			interp = filepath.Clean(resolved)
			if len(queue)+len(seen) >= maxELFNodes {
				return nil, fmt.Errorf("dependency graph budget exceeded")
			}
			if e := record(interp); e != nil {
				return nil, e
			}
			queue = append(queue, elfNode{interp, n.depth + 1})
		}
		for _, name := range needed {
			candidate := resolveELF(name, real, rpath)
			if candidate == "" {
				return nil, fmt.Errorf("ELF dependency unresolved")
			}
			if len(queue)+len(seen) >= maxELFNodes {
				return nil, fmt.Errorf("dependency graph budget exceeded")
			}
			if e := record(candidate); e != nil {
				return nil, e
			}
			queue = append(queue, elfNode{candidate, n.depth + 1})
		}
	}
	result := make([]string, 0, len(out))
	for path := range out {
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}

type elfNode struct {
	path  string
	depth int
}

func supportedELF(f *elf.File) bool {
	var class elf.Class
	var data elf.Data
	var machine elf.Machine
	switch runtime.GOARCH {
	case "amd64":
		class, data, machine = elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_X86_64
	case "arm64":
		class, data, machine = elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_AARCH64
	case "386":
		class, data, machine = elf.ELFCLASS32, elf.ELFDATA2LSB, elf.EM_386
	case "arm":
		class, data, machine = elf.ELFCLASS32, elf.ELFDATA2LSB, elf.EM_ARM
	case "ppc64le":
		class, data, machine = elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_PPC64
	case "s390x":
		class, data, machine = elf.ELFCLASS64, elf.ELFDATA2MSB, elf.EM_S390
	default:
		return false
	}
	return f.Class == class && f.Data == data && f.Machine == machine && (f.Type == elf.ET_EXEC || f.Type == elf.ET_DYN)
}

func resolveELF(name, origin string, rpaths []string) string {
	if filepath.IsAbs(name) {
		if st, e := os.Stat(name); e == nil && st.Mode().IsRegular() {
			real, e := filepath.EvalSymlinks(name)
			if e == nil {
				return real
			}
		}
		return ""
	}
	candidates := make([]string, 0, len(rpaths)+12)
	for _, tag := range rpaths {
		if tag == "" || len(tag) > maxELFString {
			continue
		}
		for _, r := range filepath.SplitList(tag) {
			r = strings.ReplaceAll(r, "${ORIGIN}", filepath.Dir(origin))
			r = strings.ReplaceAll(r, "$ORIGIN", filepath.Dir(origin))
			if !filepath.IsAbs(r) {
				r = filepath.Join(filepath.Dir(origin), r)
			}
			candidates = append(candidates, filepath.Clean(r))
		}
	}
	candidates = append(candidates, "/lib", "/lib64", "/usr/lib", "/usr/lib64", "/usr/local/lib")
	var triple string
	switch runtime.GOARCH {
	case "amd64":
		triple = "x86_64-linux-gnu"
	case "arm64":
		triple = "aarch64-linux-gnu"
	case "386":
		triple = "i386-linux-gnu"
	case "arm":
		triple = "arm-linux-gnueabihf"
	case "ppc64le":
		triple = "powerpc64le-linux-gnu"
	case "s390x":
		triple = "s390x-linux-gnu"
	}
	if triple != "" {
		candidates = append(candidates, filepath.Join("/lib", triple), filepath.Join("/usr/lib", triple))
	}
	for _, d := range candidates {
		p := filepath.Join(d, name)
		if st, e := os.Stat(p); e == nil && st.Mode().IsRegular() {
			real, e := filepath.EvalSymlinks(p)
			if e == nil {
				return real
			}
		}
	}
	return ""
}

func resolveExecutable(name, pathEnv string) (string, error) {
	if len(pathEnv) > MaxPolicyBytes {
		return "", fmt.Errorf("PATH byte budget exceeded")
	}
	paths := strings.Split(pathEnv, string(os.PathListSeparator))
	if len(paths) > maxPATHEntries {
		return "", fmt.Errorf("PATH entry budget exceeded")
	}
	for _, dir := range paths {
		if len(dir) > MaxPathBytes {
			return "", fmt.Errorf("PATH entry length exceeded")
		}
		if !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, name)
		st, e := os.Stat(candidate)
		if e == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0 {
			return filepath.EvalSymlinks(candidate)
		}
	}
	return "", fmt.Errorf("shell not found in PATH")
}

func linkedGitRoots(cwd string) ([]string, error) {
	gitfile := filepath.Join(cwd, ".git")
	st, e := os.Lstat(gitfile)
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if st.IsDir() {
		return nil, nil
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("invalid git metadata type")
	}
	b, e := readMetadata(gitfile, 4096)
	if e != nil {
		return nil, e
	}
	line := strings.TrimSpace(string(b))
	if !strings.HasPrefix(line, "gitdir: ") {
		return nil, fmt.Errorf("invalid gitdir metadata")
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir: "))
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(cwd, gitdir)
	}
	gitdir, e = canonicalDir(gitdir)
	if e != nil {
		return nil, e
	}
	commonFile := filepath.Join(gitdir, "commondir")
	common, ce := readMetadata(commonFile, 4096)
	if os.IsNotExist(ce) {
		return nil, nil
	}
	if ce != nil {
		return nil, fmt.Errorf("invalid common-dir metadata")
	}
	commonPath := strings.TrimSpace(string(common))
	if !filepath.IsAbs(commonPath) {
		commonPath = filepath.Join(gitdir, commonPath)
	}
	commonPath, e = canonicalDir(commonPath)
	if e != nil {
		return nil, e
	}
	expectedGitDir, e := canonicalDir(filepath.Join(commonPath, "worktrees", filepath.Base(gitdir)))
	if e != nil {
		return nil, e
	}
	if expectedGitDir != gitdir {
		return nil, fmt.Errorf("gitdir is not under common worktrees")
	}
	backlinkPath := filepath.Join(commonPath, "worktrees", filepath.Base(gitdir), "gitdir")
	backlink, e := readMetadata(backlinkPath, 4096)
	if e != nil {
		return nil, e
	}
	back := strings.TrimSpace(string(backlink))
	if !filepath.IsAbs(back) {
		back = filepath.Join(filepath.Dir(backlinkPath), back)
	}
	back, e = filepath.EvalSymlinks(back)
	if e != nil {
		return nil, e
	}
	want, e := filepath.EvalSymlinks(gitfile)
	if e != nil {
		return nil, e
	}
	if filepath.Clean(back) != filepath.Clean(want) {
		return nil, fmt.Errorf("git backlink mismatch")
	}
	return []string{commonPath}, nil
}

func readMetadata(path string, max int) ([]byte, error) {
	fd, e := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), "sandbox-metadata")
	defer func() { _ = f.Close() }()
	st, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("invalid metadata type")
	}
	b, e := io.ReadAll(io.LimitReader(f, int64(max+1)))
	if e != nil {
		return nil, e
	}
	if len(b) > max {
		return nil, fmt.Errorf("metadata budget exceeded")
	}
	return b, nil
}
