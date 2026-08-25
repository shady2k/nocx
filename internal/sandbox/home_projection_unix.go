//go:build linux || darwin

package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type homeProjectionPhysicalLink struct {
	homeProjectionLink
	parentComponents []string
	leaf             string
}

func materializeHomeProjections(runtimeRoot string, policy *Policy) error {
	fail := func() error { return NewSetupErrorf("runtime home projection failed") }
	if policy == nil || policy.HomeProjections == nil || len(policy.HomeProjections) > maxHomeProjections {
		return fail()
	}
	links, err := planHomeProjectionForest(policy.HomeProjections)
	if err != nil {
		return fail()
	}
	physical := make([]homeProjectionPhysicalLink, 0, len(links))
	for _, link := range links {
		parts := strings.Split(link.RelativePath, "/")
		if len(parts) == 0 || parts[len(parts)-1] == "" {
			return fail()
		}
		physical = append(physical, homeProjectionPhysicalLink{
			homeProjectionLink: link,
			parentComponents:   append([]string{}, parts[:len(parts)-1]...),
			leaf:               parts[len(parts)-1],
		})
	}

	canonicalRoot, err := canonicalExistingDir(runtimeRoot)
	if err != nil {
		return fail()
	}
	canonicalHome, err := canonicalExistingDir(policy.Home)
	if err != nil || filepath.Dir(canonicalHome) != canonicalRoot || filepath.Base(canonicalHome) != "home" {
		return fail()
	}
	rootFD, err := unix.Open(runtimeRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fail()
	}
	defer func() { _ = unix.Close(rootFD) }()
	if verifyErr := verifyProjectionDirectory(rootFD); verifyErr != nil {
		return fail()
	}
	homeFD, err := unix.Openat(rootFD, "home", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fail()
	}
	defer func() { _ = unix.Close(homeFD) }()
	if verifyErr := verifyProjectionDirectory(homeFD); verifyErr != nil {
		return fail()
	}

	createdParents := make(map[string]struct{})
	for _, link := range physical {
		parentFD := homeFD
		ownedFD := -1
		prefix := ""
		for _, component := range link.parentComponents {
			if prefix == "" {
				prefix = component
			} else {
				prefix += "/" + component
			}
			if _, exists := createdParents[prefix]; !exists {
				if err := unix.Mkdirat(parentFD, component, 0o700); err != nil {
					if ownedFD >= 0 {
						_ = unix.Close(ownedFD)
					}
					return fail()
				}
				createdParents[prefix] = struct{}{}
			}
			nextFD, openErr := unix.Openat(parentFD, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			if ownedFD >= 0 {
				_ = unix.Close(ownedFD)
			}
			if openErr != nil {
				return fail()
			}
			ownedFD = nextFD
			parentFD = nextFD
			if err := verifyProjectionDirectory(parentFD); err != nil {
				_ = unix.Close(ownedFD)
				return fail()
			}
		}

		canonicalSource, resolveErr := canonicalExistingDir(link.HostPath)
		if resolveErr != nil || canonicalSource != link.HostPath {
			if ownedFD >= 0 {
				_ = unix.Close(ownedFD)
			}
			return fail()
		}
		linkErr := unix.Symlinkat(link.HostPath, parentFD, link.leaf)
		if ownedFD >= 0 {
			_ = unix.Close(ownedFD)
		}
		if linkErr != nil {
			return fail()
		}
	}
	return nil
}

func verifyProjectionDirectory(fd int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return errors.New("not a directory")
	}
	if stat.Mode&0o7777 != 0o700 {
		return errors.New("unsafe directory mode")
	}
	// #nosec G115 -- effective user IDs are non-negative platform uid values.
	if stat.Uid != uint32(os.Geteuid()) { //nolint:gosec // effective user IDs are non-negative platform uid values.
		return errors.New("foreign directory owner")
	}
	return nil
}
