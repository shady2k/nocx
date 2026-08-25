package sandbox

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const maxHomeProjections = 1 + 4*maxUserPaths

// HomeProjection is an immutable discoverability alias from the isolated
// runtime HOME to one exact canonical host root. It carries no access class;
// WritableRoots and ReadOnlyRoots remain the native authorization authority.
type HomeProjection struct {
	HostPath     string `json:"hostPath"`
	RelativePath string `json:"relativePath"`
}

type homeProjectionLink struct {
	HostPath     string
	RelativePath string
}

func resolveHostHome(env []string) (string, error) {
	home := ""
	present := false
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "HOME="); ok {
			home = value
			present = true
		}
	}
	if !present {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", NewSetupErrorf("host HOME is unavailable")
		}
	}
	canonical, err := canonicalExistingDir(home)
	if err != nil {
		return "", NewSetupErrorf("host HOME is unavailable")
	}
	return canonical, nil
}

// planHomeProjections derives aliases only from the ordered, provenance-
// preserving effective user candidates supplied by BuildPolicy. All inputs
// are canonical existing directories.
func planHomeProjections(hostHome, runtimeRoot, runtimeHome string, candidates []string) []HomeProjection {
	out := make([]HomeProjection, 0, len(candidates))
	seenHost := make(map[string]struct{}, len(candidates))
	seenRelative := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if !pathWithin(hostHome, candidate) || sameDir(hostHome, candidate) {
			continue
		}
		// Reject both sides of an intersection. In practice the session root
		// usually lives below the host cache directory, so ancestor grants are
		// the important half of this check.
		if pathWithin(runtimeRoot, candidate) || pathWithin(candidate, runtimeRoot) {
			continue
		}
		relative, err := filepath.Rel(hostHome, candidate)
		if err != nil {
			continue
		}
		relative, ok := normalizeProjectionRelative(relative)
		if !ok {
			continue
		}
		guestPath := filepath.Join(runtimeHome, filepath.FromSlash(relative))
		if !pathWithin(runtimeHome, guestPath) || filepath.Clean(guestPath) == filepath.Clean(runtimeHome) {
			continue
		}
		if _, exists := seenHost[candidate]; exists {
			continue
		}
		if _, exists := seenRelative[relative]; exists {
			continue
		}
		seenHost[candidate] = struct{}{}
		seenRelative[relative] = struct{}{}
		out = append(out, HomeProjection{HostPath: candidate, RelativePath: relative})
	}
	return out
}

func normalizeProjectionRelative(relative string) (string, bool) {
	if relative == "" || filepath.IsAbs(relative) {
		return "", false
	}
	clean := filepath.Clean(relative)
	if clean == "." || clean == "" || filepath.IsAbs(clean) {
		return "", false
	}
	normalized := filepath.ToSlash(clean)
	if filepath.ToSlash(relative) != normalized {
		return "", false
	}
	for _, component := range strings.Split(normalized, "/") {
		if component == "" || component == "." || component == ".." {
			return "", false
		}
	}
	return normalized, true
}

func validateHomeProjections(policy *Policy) error {
	if policy.HomeProjections == nil {
		return NewSetupErrorf("home projections are missing")
	}
	if len(policy.HomeProjections) > maxHomeProjections {
		return NewSetupErrorf("home projections exceed the limit")
	}
	seenHosts := make([]string, 0, len(policy.HomeProjections))
	seenRelative := make(map[string]struct{}, len(policy.HomeProjections))
	for _, projection := range policy.HomeProjections {
		canonical, err := canonicalExistingDir(projection.HostPath)
		if err != nil || canonical != projection.HostPath {
			return NewSetupErrorf("home projection host is invalid")
		}
		for _, r := range projection.RelativePath {
			if unicode.IsControl(r) {
				return NewSetupErrorf("home projection path is invalid")
			}
		}
		relative, ok := normalizeProjectionRelative(filepath.FromSlash(projection.RelativePath))
		if !ok || relative != projection.RelativePath {
			return NewSetupErrorf("home projection path is invalid")
		}
		guestPath := filepath.Join(policy.Home, filepath.FromSlash(relative))
		if !pathWithin(policy.Home, guestPath) || filepath.Clean(guestPath) == filepath.Clean(policy.Home) {
			return NewSetupErrorf("home projection path is invalid")
		}
		for _, seen := range seenHosts {
			if sameDir(seen, projection.HostPath) {
				return NewSetupErrorf("home projection host is duplicated")
			}
		}
		if _, exists := seenRelative[relative]; exists {
			return NewSetupErrorf("home projection path is duplicated")
		}
		represented := sameDir(policy.Workspace, projection.HostPath)
		if !represented {
			for _, root := range append(append([]string{}, policy.WritableRoots...), policy.ReadOnlyRoots...) {
				if sameDir(root, projection.HostPath) {
					represented = true
					break
				}
			}
		}
		if !represented {
			return NewSetupErrorf("home projection host is not a realized root")
		}
		seenHosts = append(seenHosts, projection.HostPath)
		seenRelative[relative] = struct{}{}
	}
	return nil
}

func cloneHomeProjections(in []HomeProjection) []HomeProjection {
	out := make([]HomeProjection, len(in))
	copy(out, in)
	return out
}

func planHomeProjectionForest(projections []HomeProjection) ([]homeProjectionLink, error) {
	seenHost := make(map[string]struct{}, len(projections))
	seenRelative := make(map[string]struct{}, len(projections))
	for _, projection := range projections {
		relative, ok := normalizeProjectionRelative(filepath.FromSlash(projection.RelativePath))
		if !ok || relative != projection.RelativePath || projection.HostPath == "" || !filepath.IsAbs(projection.HostPath) {
			return nil, NewSetupErrorf("runtime home projection failed")
		}
		if _, exists := seenHost[projection.HostPath]; exists {
			return nil, NewSetupErrorf("runtime home projection failed")
		}
		if _, exists := seenRelative[projection.RelativePath]; exists {
			return nil, NewSetupErrorf("runtime home projection failed")
		}
		seenHost[projection.HostPath] = struct{}{}
		seenRelative[projection.RelativePath] = struct{}{}
	}

	links := make([]homeProjectionLink, 0, len(projections))
	for i, projection := range projections {
		hasProjectedAncestor := false
		for j, candidate := range projections {
			if i == j {
				continue
			}
			if projection.RelativePath != candidate.RelativePath && strings.HasPrefix(projection.RelativePath, candidate.RelativePath+"/") {
				hasProjectedAncestor = true
				break
			}
		}
		if !hasProjectedAncestor {
			links = append(links, homeProjectionLink(projection))
		}
	}
	sort.Slice(links, func(i, j int) bool {
		return links[i].RelativePath < links[j].RelativePath
	})
	return links, nil
}
