package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

// ConfigResolver resolves SSH configuration directives for a host by asking
// ssh -G. The reference implementation is the oracle — there is no subset to
// declare and no conformance gap to chase.
//
// Caching: results are cached by (config file mtime, host). The cache is
// invalidated when the config file's mtime changes. This is correct because
// ssh -G output is a pure function of the config file(s) and the host name.
// Include directives in the config file may reference other files; if those
// change without the main config file changing, the cache is stale until the
// main config file is touched. That is an acceptable limitation for the
// motivating case (forty hosts on one list render) — mtime checking is O(1)
// and avoids spawning a subprocess for every host on every render.
//
// Error behavior: both methods return the degraded fallback value AND the
// typed error. Callers that need to distinguish conditions (binary-not-found,
// timeout, parse-failure) can check errors.Is/As on the returned error.
// Callers that only need a value can use the result and ignore the error.
// Each condition is logged at warn level exactly once, deduplicated by type.
type ConfigResolver interface {
	// ResolveHost resolves a host alias to its canonical hostname via the
	// HostName directive. Returns the original host on error, along with the
	// typed error so the caller can distinguish the condition.
	ResolveHost(ctx context.Context, host string) (string, error)

	// ResolveConfig returns the merged SSH configuration for the given host.
	// Returns a default config (host as-is, current user, port 22) on error,
	// along with the typed error so the caller can distinguish the condition.
	ResolveConfig(ctx context.Context, host string) (*HostConfig, error)
}

// HostConfig holds the SSH configuration directives for a resolved host.
// Fields are populated from ssh -G output.
type HostConfig struct {
	HostName     string
	User         string
	Port         int
	IdentityFile string
}

// Sentinel errors for ssh -G resolution failures. Each is distinguishable
// via errors.Is so callers (and the degradation reporter) can identify and
// surface the specific condition.
var (
	// ErrSSHBinaryNotFound is returned when the ssh binary is not on PATH.
	ErrSSHBinaryNotFound = errors.New("ssh binary not found on PATH")

	// ErrSSHConfigTimeout is returned when ssh -G exceeds the deadline.
	ErrSSHConfigTimeout = errors.New("ssh -G timed out")

	// ErrSSHConfigFailed is returned when ssh -G exits with a non-zero status
	// or produces unparseable output.
	ErrSSHConfigFailed = errors.New("ssh -G failed")
)

// degradationCondition is a reason code for logging the first occurrence of
// a particular degradation.
type degradationCondition int

const (
	degradationNoBinary degradationCondition = iota
	degradationTimeout
	degradationParseFailed
)

// sshConfigResolver implements ConfigResolver by running ssh -G.
type sshConfigResolver struct {
	configPath string
	sshPath    string // empty = look up on PATH
	log        log.Logger

	// Cache: invalidation on config file mtime change.
	mu        sync.RWMutex
	lastMtime time.Time
	cache     map[string]*HostConfig

	// Per-condition one-time reporting via atomic bitmask.
	reported atomic.Uint32
}

// NewSSHConfigResolver creates a resolver that runs ssh -G for configuration.
// sshPath is the path to the ssh binary; if empty, the resolver looks up "ssh"
// on PATH at each call. configPath is the path to the user's ssh_config file.
// Callers MUST provide a logger; the resolver uses it for one-time degradation
// warnings and has no other output path.
func NewSSHConfigResolver(logger log.Logger, configPath, sshPath string) ConfigResolver {
	return &sshConfigResolver{
		configPath: configPath,
		sshPath:    sshPath,
		log:        logger,
		cache:      make(map[string]*HostConfig),
	}
}

// ResolveHost implements ConfigResolver.
func (r *sshConfigResolver) ResolveHost(ctx context.Context, host string) (string, error) {
	cfg, err := r.resolve(ctx, host)
	if err != nil {
		return host, err
	}
	if cfg.HostName != "" {
		return cfg.HostName, nil
	}
	return host, nil
}

// ResolveConfig implements ConfigResolver.
// On error, returns a degraded config with Port 0 (unset) so the caller's
// default port (22 or explicit host:port) is preserved.
func (r *sshConfigResolver) ResolveConfig(ctx context.Context, host string) (*HostConfig, error) {
	cfg, err := r.resolve(ctx, host)
	if err != nil {
		return &HostConfig{
			HostName: host,
			User:     currentUser(),
		}, err
	}
	return cfg, nil
}

// resolve checks the cache and falls back to running ssh -G.
func (r *sshConfigResolver) resolve(ctx context.Context, host string) (*HostConfig, error) {
	r.mu.RLock()
	mtime := r.lastMtime
	hostCfg, ok := r.cache[host]
	r.mu.RUnlock()

	if !ok || r.configChanged(mtime) {
		return r.load(ctx, host)
	}
	return hostCfg, nil
}

// configChanged returns true if the config file's mtime has changed since the
// given mtime. An empty mtime (zero time) always triggers a reload.
func (r *sshConfigResolver) configChanged(since time.Time) bool {
	info, err := os.Stat(r.configPath)
	if err != nil {
		return false
	}
	return !info.ModTime().Equal(since)
}

// load runs ssh -G, parses output, caches, and returns.
// On ssh -G failure with a valid cache entry, the cached config AND the error
// are both returned so callers can distinguish the degradation condition.
//
// When the config file mtime has changed since the last successful load, the
// entire cache is purged before inserting the new entry. This ensures all
// stale entries from the previous config generation are discarded — a change
// that alters host B's resolution is reflected when host A triggers the
// invalidation, not only when host B is accessed.
func (r *sshConfigResolver) load(ctx context.Context, host string) (*HostConfig, error) {
	cfg, err := r.runSSHG(ctx, host)
	if err != nil {
		r.mu.RLock()
		hostCfg, cached := r.cache[host]
		mtime := r.lastMtime
		r.mu.RUnlock()
		if cached {
			if r.configChanged(mtime) {
				r.purgeCache()
			}
			return hostCfg, err
		}
		return nil, err
	}

	// On successful load, check if the config file changed since the last
	// generation. If so, purge all stale entries so no host from the old
	// config survives — a change to host B is visible when host A reloads.
	r.mu.Lock()
	if !r.lastMtime.IsZero() {
		if info, errStat := os.Stat(r.configPath); errStat == nil && !info.ModTime().Equal(r.lastMtime) {
			r.cache = make(map[string]*HostConfig)
		}
	}
	r.cache[host] = cfg
	if info, err := os.Stat(r.configPath); err == nil {
		r.lastMtime = info.ModTime()
	}
	r.mu.Unlock()

	return cfg, nil
}

// purgeCache clears all cached entries. Caller must NOT hold r.mu.
func (r *sshConfigResolver) purgeCache() {
	r.mu.Lock()
	r.cache = make(map[string]*HostConfig)
	r.lastMtime = time.Time{}
	r.mu.Unlock()
}

// runSSHG executes ssh -F <configPath> -G <host> and parses the output.
// Using -F restricts ssh to the specified config file only, matching the
// existing behavior of the kevinburke/ssh_config library it replaces.
func (r *sshConfigResolver) runSSHG(ctx context.Context, host string) (*HostConfig, error) {
	sshPath := r.sshPath
	if sshPath == "" {
		var err error
		sshPath, err = exec.LookPath("ssh")
		if err != nil {
			r.reportOnce(degradationNoBinary)
			return nil, fmt.Errorf("%w: %v", ErrSSHBinaryNotFound, err)
		}
	} else if _, err := os.Stat(sshPath); err != nil {
		r.reportOnce(degradationNoBinary)
		return nil, fmt.Errorf("%w: ssh path %s: %v", ErrSSHBinaryNotFound, sshPath, err)
	}

	// Use -F to read only the specified config file. This keeps the behavior
	// consistent with the replaced kevinburke/ssh_config library and ensures
	// cache invalidation based on configPath mtime is correct.
	args := []string{"-F", r.configPath, "-G", host}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	// #nosec G204 — sshPath is either explicitly configured by the app or
	// resolved from PATH; host comes from user's ~/.ssh/config alias lookup.
	// This is the intended oracle — the whole point of the package.
	cmd := exec.CommandContext(ctx, sshPath, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			r.reportOnce(degradationTimeout)
			return nil, fmt.Errorf("%w: ssh -G %s: %v", ErrSSHConfigTimeout, host, ctx.Err())
		}
		r.reportOnce(degradationParseFailed)
		return nil, fmt.Errorf("%w: ssh -G %s: %v\nstderr: %s",
			ErrSSHConfigFailed, host, err, strings.TrimSpace(stderr.String()))
	}

	cfg, err := parseSSHGOutput(stdout.String(), host)
	if err != nil {
		r.reportOnce(degradationParseFailed)
		return nil, fmt.Errorf("%w: parse ssh -G output for %s: %v", ErrSSHConfigFailed, host, err)
	}

	return cfg, nil
}

// parseSSHGOutput parses the output of ssh -G into a HostConfig.
func parseSSHGOutput(output, host string) (*HostConfig, error) {
	cfg := &HostConfig{
		HostName: host,
		User:     currentUser(),
	}

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		key, value, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		key = strings.ToLower(key)
		value = strings.TrimSpace(value)

		switch key {
		case "hostname":
			if value != "" {
				cfg.HostName = value
			}
		case "user":
			cfg.User = value
		case "port":
			if p, err := strconv.Atoi(value); err == nil && p > 0 {
				cfg.Port = p
			}
		case "identityfile":
			if cfg.IdentityFile == "" && value != "" {
				cfg.IdentityFile = expandPath(value)
			}
		}
	}

	return cfg, nil
}

// reportOnce logs a warning for the given degradation condition, at most once
// per condition.
func (r *sshConfigResolver) reportOnce(cond degradationCondition) {
	mask := uint32(1 << cond)
	if r.reported.Load()&mask != 0 {
		return
	}
	if r.reported.Add(mask)&mask == 0 {
		return // another goroutine already set it
	}

	switch cond {
	case degradationNoBinary:
		r.log.Warn("ssh binary not found on PATH — hostname aliases will not be resolved",
			"condition", "ssh_binary_not_found")
	case degradationTimeout:
		r.log.Warn("ssh -G timed out — hostname aliases will not be resolved",
			"condition", "ssh_config_timeout")
	case degradationParseFailed:
		r.log.Warn("ssh -G failed or produced unparseable output — hostname aliases will not be resolved",
			"condition", "ssh_config_parse_failed")
	}
}

// compile-time interface check
var _ ConfigResolver = (*sshConfigResolver)(nil)
