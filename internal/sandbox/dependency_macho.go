//go:build darwin

package sandbox

import (
	"debug/macho"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type machoNode struct {
	path  string
	depth int
	rpath []string
}

func discoverMachODependencies(executable string) (resultPaths []string, resultErr error) {
	defer func() {
		if recover() != nil {
			resultPaths = nil
			resultErr = fmt.Errorf("Mach-O dependency input invalid")
		}
	}()
	queue := []machoNode{{path: executable}}
	seen, result := make(map[string]bool), make(map[string]bool)
	bytesRead := int64(0)
	rootBytes := 0
	for len(queue) != 0 {
		if len(seen) >= maxELFNodes {
			return nil, fmt.Errorf("Mach-O dependency node budget exceeded")
		}
		n := queue[0]
		queue = queue[1:]
		if n.depth > maxELFDepth {
			return nil, fmt.Errorf("Mach-O dependency depth exceeded")
		}
		real, err := filepath.EvalSymlinks(n.path)
		if err != nil {
			return nil, fmt.Errorf("Mach-O dependency unavailable")
		}
		real = filepath.Clean(real)
		if seen[real] {
			continue
		}
		seen[real] = true
		info, err := os.Stat(real)
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("Mach-O dependency unavailable")
		}
		bytesRead += info.Size()
		if bytesRead > maxELFBytes {
			return nil, fmt.Errorf("Mach-O dependency byte budget exceeded")
		}
		loads, paths, err := machoLoadsAt(real)
		if err != nil || len(loads)+len(paths) > maxELFTags {
			return nil, fmt.Errorf("Mach-O load-command budget exceeded")
		}
		allRpaths := append(append([]string(nil), n.rpath...), paths...)
		for _, rpath := range allRpaths {
			if len(rpath) == 0 || len(rpath) > maxELFString || strings.ContainsRune(rpath, 0) {
				return nil, fmt.Errorf("Mach-O rpath invalid")
			}
		}
		for _, name := range loads {
			if len(name) == 0 || len(name) > maxELFString || strings.ContainsRune(name, 0) {
				return nil, fmt.Errorf("Mach-O dependency name invalid")
			}
			dep := resolveMachODependency(name, real, executable, allRpaths)
			if dep == "" {
				return nil, fmt.Errorf("Mach-O dependency unresolved")
			}
			if dep == systemMachOSentinel {
				continue
			}
			if darwinBaselineCovers(dep) {
				continue
			}
			if dep != executable && !result[dep] {
				if len(dep) > MaxPathBytes || len(dep) > MaxPolicyBytes-rootBytes {
					return nil, fmt.Errorf("Mach-O dependency path budget exceeded")
				}
				rootBytes += len(dep)
				result[dep] = true
			}
			if !seen[dep] {
				if len(queue)+len(seen) >= maxELFNodes {
					return nil, fmt.Errorf("Mach-O dependency graph budget exceeded")
				}
				queue = append(queue, machoNode{path: dep, depth: n.depth + 1, rpath: allRpaths})
			}
		}
	}
	out := make([]string, 0, len(result))
	for path := range result {
		out = append(out, path)
	}
	sort.Strings(out)
	return out, nil
}

func machoLoadsAt(path string) ([]string, []string, error) {
	if file, err := macho.Open(path); err == nil {
		defer file.Close()
		if !currentMachOCPU(file.Cpu) {
			return nil, nil, fmt.Errorf("wrong Mach-O architecture")
		}
		return machoLoads(file)
	}
	fat, err := macho.OpenFat(path)
	if err != nil {
		return nil, nil, err
	}
	defer fat.Close()
	for _, arch := range fat.Arches {
		if currentMachOCPU(arch.Cpu) {
			return machoLoads(arch.File)
		}
	}
	return nil, nil, fmt.Errorf("Mach-O architecture unavailable")
}

func currentMachOCPU(cpu macho.Cpu) bool {
	switch runtime.GOARCH {
	case "amd64":
		return cpu == macho.CpuAmd64
	case "arm64":
		return cpu == macho.CpuArm64
	default:
		return false
	}
}

func machoLoads(file *macho.File) ([]string, []string, error) {
	var dylibs, rpaths []string
	for _, command := range file.Loads {
		switch load := command.(type) {
		case *macho.Dylib:
			dylibs = append(dylibs, load.Name)
		case *macho.Rpath:
			rpaths = append(rpaths, load.Path)
		}
		if len(dylibs)+len(rpaths) > maxELFTags {
			return nil, nil, fmt.Errorf("load-command budget exceeded")
		}
	}
	return dylibs, rpaths, nil
}

const systemMachOSentinel = "\x00system-covered"

func darwinBaselineCovers(path string) bool {
	for _, base := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/System/Library", "/System/Volumes/Preboot/Cryptexes", "/Library/Developer/CommandLineTools", "/private/etc", "/private/var/db"} {
		root, err := canonicalActual(base)
		if err == nil && contained(root, path) {
			return true
		}
	}
	return false
}

func resolveMachODependency(name, loader, executable string, rpaths []string) string {
	candidates := make([]string, 0, len(rpaths)+4)
	switch {
	case filepath.IsAbs(name):
		candidates = append(candidates, name)
	case strings.HasPrefix(name, "@loader_path/"):
		candidates = append(candidates, filepath.Join(filepath.Dir(loader), strings.TrimPrefix(name, "@loader_path/")))
	case strings.HasPrefix(name, "@executable_path/"):
		candidates = append(candidates, filepath.Join(filepath.Dir(executable), strings.TrimPrefix(name, "@executable_path/")))
	case strings.HasPrefix(name, "@rpath/"):
		suffix := strings.TrimPrefix(name, "@rpath/")
		for _, rpath := range rpaths {
			switch {
			case strings.HasPrefix(rpath, "@loader_path/"):
				candidates = append(candidates, filepath.Join(filepath.Dir(loader), strings.TrimPrefix(rpath, "@loader_path/"), suffix))
			case strings.HasPrefix(rpath, "@executable_path/"):
				candidates = append(candidates, filepath.Join(filepath.Dir(executable), strings.TrimPrefix(rpath, "@executable_path/"), suffix))
			case filepath.IsAbs(rpath):
				candidates = append(candidates, filepath.Join(rpath, suffix))
			default:
				return ""
			}
		}
	default:
		return ""
	}
	systemCovered := false
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			if real, err := filepath.EvalSymlinks(candidate); err == nil {
				return filepath.Clean(real)
			}
		}
		for _, base := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/System/Library", "/System/Volumes/Preboot/Cryptexes", "/Library/Developer/CommandLineTools", "/private/etc", "/private/var/db"} {
			if contained(base, candidate) {
				systemCovered = true
			}
		}
	}
	if systemCovered {
		return systemMachOSentinel
	}
	return ""
}
