package ssh

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrConfigNotReadable is returned when the SSH config file cannot be opened
// or read. Callers should surface this as a degraded-configuration condition.
var ErrConfigNotReadable = errors.New("ssh config file not readable")

// AliasEntry represents one SSH host alias from ~/.ssh/config, resolved
// through the ConfigResolver (ssh -G for values). The alias name comes from
// the config file's Host directive; the resolved values come from ssh -G.
//
// This is the split the brief warns about keeping explicit:
//
//	Enumeration (reading Host lines) is a narrow file scan — the config
//	file is where the list of names lives.
//	Resolution (HostName, User, Port) goes through ConfigResolver, which
//	is backed by ssh -G and is the sole authority on values.
//
// These are intentionally separate: ssh -G answers *for a host you name*
// but does not enumerate the names themselves. The config file is the only
// source of the alias list. Nothing in this package re-parses the config
// file for values — that would re-create the kevinburke/ssh_config defect.

// AliasEntry represents one SSH host alias from ~/.ssh/config.
// Alias is the Host pattern as written by the user — this is the identity
// that duplicate-suppression compares on. HostName is the resolved value
// (may equal alias when the config sets no HostName). User and Port are
// omitted when the config sets none — do not invent defaults here.
type AliasEntry struct {
	Alias    string `json:"alias"`
	HostName string `json:"hostName"`
	User     string `json:"user,omitempty"`
	Port     int    `json:"port,omitempty"`
}

// UnavailableInfo describes why the SSH config could not be read.
// Reason is one of: "no-ssh-binary", "timeout", "parse-failure".
type UnavailableInfo struct {
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

// AliasesResponse is the response from sshConfig.aliases.
// Unavailable is null when the read succeeded. An empty aliases list with
// unavailable=null is valid (no aliases in config). An empty list with
// unavailable set means the config could not be read — these are
// distinguishable.
type AliasesResponse struct {
	Aliases     []AliasEntry     `json:"aliases"`
	Unavailable *UnavailableInfo `json:"unavailable"`
}

// EnumerateHostPatterns reads ~/.ssh/config and extracts the `Host` directive
// patterns. It excludes wildcard patterns (containing *, ?, !) and returns
// only concrete host aliases — each in the form they appeared in the config
// file.
//
// This is a NARROW scan: it reads only the `Host` keyword lines and splits
// on whitespace. It does NOT parse values, evaluate Match blocks, resolve
// Include directives, or interpret any other SSH config keyword. The
// ConfigResolver is the sole authority on values.
//
// Multiple patterns on one Host line are returned as separate entries.
func EnumerateHostPatterns(configPath string) ([]string, error) {
	// #nosec G304 -- configPath is built at the composition root from
	// os.UserHomeDir() + ".ssh/config" (app.go) and is never renderer input;
	// the renderer cannot name a file here. Same justification as the
	// resolver's own open in ssh_config.go.
	f, err := os.Open(configPath)
	if err != nil {
		// File not existing is not a degradation — no config means no aliases.
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: %w", ErrConfigNotReadable, err)
	}
	defer func() { _ = f.Close() }()

	var patterns []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// Trim leading whitespace, skip empty/comment lines.
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Check for Host keyword. The keyword is case-insensitive in SSH
		// config, but in practice it is almost always capitalized. We check
		// both to be safe without doing a full token parse.
		var rest string
		switch {
		case strings.HasPrefix(trimmed, "Host ") || trimmed == "Host":
			rest = strings.TrimSpace(trimmed[4:])
		case strings.HasPrefix(trimmed, "host ") || trimmed == "host":
			rest = strings.TrimSpace(trimmed[4:])
		default:
			continue
		}

		if rest == "" {
			continue
		}

		// Split on whitespace and collect non-wildcard patterns.
		for _, pat := range strings.Fields(rest) {
			if containsWildcard(pat) {
				continue
			}
			patterns = append(patterns, pat)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfigNotReadable, err)
	}

	return patterns, nil
}

// containsWildcard returns true if pat contains SSH wildcard characters
// (*, ?, !), indicating it is a pattern rather than a concrete host alias.
func containsWildcard(pat string) bool {
	return strings.ContainsAny(pat, "*?!")
}

// ResolveAliases resolves a list of host patterns via the ConfigResolver and
// returns the AliasesResponse. Each pattern is resolved through ssh -G for
// its HostName, User, and Port values. Patterns where resolution fails are
// included with hostName equal to the alias (the fallback the resolver
// returns), and the unavailable field conveys the degradation.
//
// The context should carry a reasonable deadline — resolving N aliases runs
// ssh -G up to N times, subject to caching.
func ResolveAliases(ctx context.Context, resolver ConfigResolver, patterns []string) *AliasesResponse {
	resp := &AliasesResponse{
		Aliases: make([]AliasEntry, 0, len(patterns)),
	}

	for _, alias := range patterns {
		cfg, err := resolver.ResolveConfig(ctx, alias)
		entry := AliasEntry{
			Alias:    alias,
			HostName: alias,
		}
		if cfg != nil {
			if cfg.HostName != "" {
				entry.HostName = cfg.HostName
			}
			if cfg.User != "" {
				entry.User = cfg.User
			}
			if cfg.Port > 0 {
				entry.Port = cfg.Port
			}
		}
		resp.Aliases = append(resp.Aliases, entry)

		// Record the first degradation encountered.
		if err != nil && resp.Unavailable == nil {
			resp.Unavailable = classifyError(err)
		}
	}

	return resp
}

// classifyError maps a ConfigResolver error to an UnavailableInfo.
func classifyError(err error) *UnavailableInfo {
	if errors.Is(err, ErrSSHBinaryNotFound) {
		return &UnavailableInfo{Reason: "no-ssh-binary", Detail: err.Error()}
	}
	if errors.Is(err, ErrSSHConfigTimeout) {
		return &UnavailableInfo{Reason: "timeout", Detail: err.Error()}
	}
	return &UnavailableInfo{Reason: "parse-failure", Detail: err.Error()}
}
