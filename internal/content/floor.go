package content

import (
	"path/filepath"
	"strings"
)

// Floor is the part of agent authority that policy settings cannot widen.
// Its roots are fixed when the composition root constructs the floor and are
// carried into each run's immutable grant through EffectPolicy.
type Floor struct {
	enabled bool
	roots   []floorRoot
}

type floorRoot struct {
	path   string
	reason string
}

// NewFloor constructs the non-overridable floor from the application's own
// configuration and data directories; empty or relative roots are ignored.
func NewFloor(configDir, dataDir string) Floor {
	var roots []floorRoot
	for _, root := range []struct {
		path   string
		reason string
	}{
		// The policy document is nocx-controlled state that an agent cannot inspect or modify.
		{configDir, "The nocx configuration directory is protected by the floor as nocx-controlled state that an agent can never inspect or modify."},
		// The vault, ledger, and shell manifest are nocx-controlled state that an agent cannot inspect or modify.
		{dataDir, "The nocx data directory is protected by the floor as nocx-controlled state that an agent can never inspect or modify."},
	} {
		if root.path == "" || !filepath.IsAbs(root.path) {
			continue
		}
		roots = append(roots, floorRoot{path: filepath.Clean(root.path), reason: root.reason})
	}
	return Floor{enabled: true, roots: roots}
}

// NoFloor explicitly opts a test that does not exercise floor enforcement out
// of the application safety floor; production callers must use NewFloor.
func NoFloor() Floor {
	return Floor{}
}

// WithFloor returns a policy carrying the fixed floor. The unexported field
// is omitted from JSON, so no policy document or session overlay can alter it.
func (p EffectPolicy) WithFloor(f Floor) EffectPolicy {
	p.floor = &f
	return p
}

// Refusal reports the first floor rule that matches the already validated
// invocation or resolved resources. It runs before the effect matrix and its
// overlays, so no permit or standing rule can answer it. Reads are included:
// policy, vault, ledger, and shell-manifest state are control material that an
// agent must neither inspect nor modify.
func (f Floor) Refusal(inv Invocation, resources []GrantScope) (string, bool) {
	if !f.enabled {
		return "", false
	}
	for _, resource := range resources {
		if resource.Kind != ResourcePath {
			continue
		}
		for _, root := range f.roots {
			if (GrantScope{Kind: ResourcePath, ID: root.path}).Contains(resource) {
				return root.reason, true
			}
		}
	}
	if reason := forbiddenInvocation(inv); reason != "" {
		return reason, true
	}
	return "", false
}

// RawCommandRefusal checks only the exact self-replication signature before
// shell operators are discarded by canonical invocation parsing.
func (f Floor) RawCommandRefusal(command string) (string, bool) {
	if !f.enabled {
		return "", false
	}
	normalized := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r':
			return -1
		default:
			return r
		}
	}, command)
	if strings.Contains(normalized, ":(){:|:&};:") {
		return "This self-replicating command is protected by the floor and can never be run by an agent.", true
	}
	return "", false
}

func forbiddenInvocation(inv Invocation) string {
	if !inv.Parsed {
		return ""
	}
	// Fork bombs are checked by RawCommandRefusal before parsing because
	// canonical tokenization discards their shell operators; this path owns
	// only token-preserving command checks.
	for _, words := range inv.Commands {
		if len(words) == 0 {
			continue
		}

		for i, word := range words {
			program := strings.ToLower(filepath.Base(word))
			switch {
			case program == "rm" && removesHome(words[i+1:]):
				return "Deleting a home directory is protected by the floor and can never be requested by an agent."
			case strings.HasPrefix(program, "mkfs") || program == "format":
				return "Formatting a device is protected by the floor and can never be requested by an agent."
			case program == "fdisk" || program == "sfdisk" || program == "cfdisk" || program == "gdisk" || program == "sgdisk" || program == "parted":
				return "Partitioning a device is protected by the floor and can never be requested by an agent."
			case program == "diskutil" && hasDiskDestruction(words[i+1:]):
				return "Formatting or partitioning a device is protected by the floor and can never be requested by an agent."
			case program == "dd" && writesDevice(words[i+1:]):
				return "Overwriting a device is protected by the floor and can never be requested by an agent."
			}
		}
	}
	return ""
}

func removesHome(args []string) bool {
	recursive := false
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			if arg == "--" {
				continue
			}
			if strings.HasPrefix(arg, "--") {
				recursive = recursive || arg == "--recursive"
			} else {
				for _, flag := range arg[1:] {
					if flag == 'r' || flag == 'R' {
						recursive = true
					}
				}
			}
			continue
		}
		if recursive && isHomeTarget(arg) {
			return true
		}
	}
	return false
}

func isHomeTarget(path string) bool {
	return path == "/" || path == "~" || path == "$HOME" || path == "${HOME}"
}

func hasDiskDestruction(args []string) bool {
	for _, arg := range args {
		if arg == "eraseDisk" || arg == "partitionDisk" || arg == "erasevolume" || arg == "partition" {
			return true
		}
	}
	return false
}

func writesDevice(args []string) bool {
	for i, arg := range args {
		if strings.HasPrefix(arg, "of=") && isBlockDevicePath(strings.TrimPrefix(arg, "of=")) {
			return true
		}
		if arg == "of" && i+1 < len(args) && isBlockDevicePath(args[i+1]) {
			return true
		}
	}
	return false
}

func isBlockDevicePath(path string) bool {
	if !strings.HasPrefix(path, "/dev/") {
		return false
	}
	base := filepath.Base(path)
	for _, prefix := range []string{"sd", "hd", "vd", "xvd", "nvme", "mmcblk", "md", "dm-", "loop", "disk", "rdisk"} {
		if strings.HasPrefix(base, prefix) && len(base) > len(prefix) {
			return true
		}
	}
	return strings.HasPrefix(path, "/dev/mapper/") ||
		strings.HasPrefix(path, "/dev/disk/by-")
}

// FloorRefusal evaluates the policy's fixed floor without exposing its
// configuration to policy documents or session overlays.
func (p EffectPolicy) FloorRefusal(inv Invocation, resources []GrantScope) (string, bool) {
	if p.floor == nil {
		return "", false
	}
	return p.floor.Refusal(inv, resources)
}

// FloorRawCommandRefusal evaluates only the fixed floor's raw-command rule.
func (p EffectPolicy) FloorRawCommandRefusal(command string) (string, bool) {
	if p.floor == nil {
		return "", false
	}
	return p.floor.RawCommandRefusal(command)
}
