package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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

	// ResolveArgv resolves SSH configuration for the exact argv a user
	// typed — the complete oracle command line the renderer's plan built,
	// e.g. ["ssh", "-G", "-p", "2222", "pi@host"]. The argv after "ssh" is
	// executed verbatim, so a typed -F/-o/-J/-l/-p reaches the oracle
	// (nocx-c5az); the answer is what the delivery planner's decision and
	// the installed-fact key are built from (ADR-0015 narrowed by the
	// 2026-08-05 delivery-modes design §8). On error, returns a degraded
	// config (host as typed, current user) AND the typed error so the
	// caller can distinguish the condition.
	ResolveArgv(ctx context.Context, argv []string) (*HostConfig, error)
}

// IsOptionLikeHost reports whether ssh would parse host as an option instead
// of the positional destination the caller intended.
func IsOptionLikeHost(host string) bool {
	return host != "" && host[0] == '-'
}

// HostConfig holds the SSH configuration directives for a resolved host.
// Fields are populated from ssh -G output.
type HostConfig struct {
	HostName     string
	User         string
	Port         int
	IdentityFile string

	// RemoteCommand is the command ssh would execute on the remote host,
	// verbatim from the RemoteCommand directive. The empty string means
	// "no remote command configured". ssh -G renders an unset directive
	// as "none" — OpenSSH's own sentinel for "no command" — so that
	// rendering is normalized to the empty string here. "none" would be
	// a legitimate literal command in any other context; only the -G
	// oracle's output is normalized, and only because ssh itself treats
	// the two as the same thing.
	RemoteCommand string

	// RequestTTY is the resolved RequestTTY directive, canonicalized to
	// "yes", "no" or "force". The empty string means the directive is
	// unset. ssh -G renders an unset directive as "auto" (the default:
	// ssh decides, which for a command execution is no TTY), so "auto"
	// is normalized to the empty string — an explicit "auto" and an
	// unset directive behave identically in ssh, so no information a
	// caller could act on is lost. OpenSSH >= 10 serializes the boolean
	// values as true/false while older versions print yes/no; both forms
	// normalize to the canonical yes/no so callers are independent of
	// the ssh version that produced the output.
	RequestTTY string

	// The multiplex directives, as the oracle resolves them for this exact
	// argv — the user's config and their command-line -M/-S/-o together.
	// ADR-0035's typed wrapper reads all three before it proposes a socket
	// of its own, for two different reasons.
	//
	// The first is a REFUSAL: a user who expressed their own multiplex
	// policy is never overridden, so any of these away from its default
	// ends the rewrite before anything happens.
	//
	// The second is the SOCKET PATH, and it is why these are read off the
	// oracle rather than composed here. ControlPath is percent-expanded by
	// ssh, and the %C the wrapper proposes is a hash ssh computes from the
	// local host, the remote host, the port and the remote user. Only ssh
	// knows it — so the wrapper asks ssh, with its own options in the argv,
	// and connects to the answer. Reading the config ourselves would be the
	// second implementation ADR-0015 exists to prevent.
	//
	// Defaults, normalized: ControlMaster is "no", ControlPath is empty
	// (ssh -G prints nothing for the "none" default), ControlPersist is
	// "no". OpenSSH >= 10 serializes ControlMaster's booleans as
	// true/false where older versions print yes/no; both normalize to the
	// canonical yes/no, exactly as RequestTTY does above.
	ControlMaster  string
	ControlPath    string
	ControlPersist string
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

	// Cache for ResolveHost/ResolveConfig: invalidation on config file
	// mtime change, keyed by the typed host alias.
	mu        sync.RWMutex
	lastMtime time.Time
	cache     map[string]*HostConfig

	// argvCache holds ResolveArgv results keyed by THE EXACT ARGV, which is
	// the question that was asked. Never by the typed hostname: with
	// command-line -F/-p/-l/-J/-o the same alias can resolve to different
	// destinations, so the alias is not a key (ADR-0015 narrowed by the
	// 2026-08-05 delivery-modes design §8).
	//
	// AND NEVER BY THE RESOLVED IDENTITY EITHER, which is what it was until
	// 2026-08-21. Keying by identity assumes every argv naming one
	// destination has one answer, and since ADR-0035 that is false by
	// design: the typed wrapper asks this oracle twice about the same
	// destination, once about the user's own line and once about the same
	// line plus our ControlMaster/ControlPath/ControlPersist, and the whole
	// point of the second question is that it answers differently. Sharing
	// one entry meant the second connection to a host got the first
	// question's answer — an empty `controlpath` for the wrapped line — and
	// nocx refused to interpose with `no-control-path`, so every typed ssh
	// after the first came up unintegrated (measured in
	// e2e/nocxify-journey.spec.ts, 2026-08-21).
	//
	// The identity is still computed and still narrows nothing away: it is
	// what the answer resolved TO, not what was asked. Cleared by the same
	// config-mtime purge as `cache`.
	argvCache map[string]*HostConfig

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
		argvCache:  make(map[string]*HostConfig),
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

// ResolveArgv implements ConfigResolver: the typed-argv oracle. ssh -G is
// run with exactly the options and destination the user typed (the argv
// after the leading "ssh"), never with an injected -F — a typed -F on the
// line selects the config file the user chose, and without one ssh reads
// the default ~/.ssh/config, so the oracle answers about the configuration
// the typed line will actually run (nocx-c5az).
//
// Caching: results are cached under the EXACT ARGV, not the typed hostname
// and not the resolved identity — the ADR-0015 narrowing of the 2026-08-05
// delivery-modes design (§8) says the alias is not a key, and ADR-0035 makes
// the destination not a key either: the typed wrapper asks about one
// destination twice, with and without our own mux options, and needs two
// different answers. A repeat of the same argv still skips the ssh -G spawn.
// The same config-mtime purge that invalidates the host-keyed cache clears
// it. A typed -F naming a different config file is a documented
// limitation: only the main config file's mtime is watched, so a change to
// a typed -F file is not observed (eviction stays safe — the cost of a
// miss is one ssh -G spawn, never a wrong answer).
func (r *sshConfigResolver) ResolveArgv(ctx context.Context, argv []string) (*HostConfig, error) {
	if !validOracleArgv(argv) {
		return &HostConfig{
			HostName: oracleHost(argv),
			User:     currentUser(),
		}, fmt.Errorf("%w: malformed oracle argv %v", ErrSSHConfigFailed, argv)
	}
	argvKey := strings.Join(argv, "\x00")

	r.mu.RLock()
	cached, hit := r.argvCache[argvKey]
	mtime := r.lastMtime
	r.mu.RUnlock()
	if hit && !r.configChanged(mtime) {
		return cached, nil
	}

	cfg, err := r.runSSHGArgv(ctx, argv)
	if err != nil {
		// Degraded config AND the error: the planner must refuse the
		// rewrite (nocx-qwhp) — fail-open means the typed bytes go out,
		// never a rewrite built on a guess.
		return &HostConfig{
			HostName: oracleHost(argv),
			User:     currentUser(),
		}, err
	}
	r.mu.Lock()
	if r.configChanged(r.lastMtime) {
		r.purgeCacheLocked()
	}
	r.argvCache[argvKey] = cfg
	// Anchor the mtime like load() does: without it, a repeat of the same
	// argv would always see configChanged(zero time) as "changed" and the
	// argv fast path would never hit in production.
	if info, err := os.Stat(r.configPath); err == nil {
		r.lastMtime = info.ModTime()
	}
	r.mu.Unlock()
	return cfg, nil
}

// validOracleArgv rejects an argv that is not the ssh -G oracle shape the
// renderer's plan builds: ["ssh", "-G", ...options, destination]. The
// resolver is the exec boundary, so it validates rather than trusting the
// caller; a violation is a renderer bug, refused loudly.
func validOracleArgv(argv []string) bool {
	return len(argv) >= 3 && filepath.Base(argv[0]) == "ssh" && argv[1] == "-G"
}

// oracleHost is the destination positional of an oracle argv — the last
// element, which is what the config is resolved for.
func oracleHost(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	return argv[len(argv)-1]
}

// IdentityKey returns the canonical key for a resolved destination: the
// ssh -G answer for the exact argv — user, hostname and port after every
// typed -F/-o/-J/-l/-p and the config file's directives — never the typed
// hostname string. It is the key of the installed-fact store (2026-08-05
// delivery-modes design §5.4): two typed lines that resolve to the same
// destination share one key. Port 0 (unset) normalizes to 22, ssh's default.
//
// It is NOT the key of the argv-oracle cache, and was until 2026-08-21 —
// see argvCache for what that cost. What a destination IS and what was ASKED
// about it are two different things, and only the second decides an answer.
func IdentityKey(cfg *HostConfig) string {
	port := cfg.Port
	if port <= 0 {
		port = 22
	}
	hostport := net.JoinHostPort(cfg.HostName, strconv.Itoa(port))
	if cfg.User == "" {
		return hostport
	}
	return cfg.User + "@" + hostport
}

// resolve checks the cache and falls back to running ssh -G.
func (r *sshConfigResolver) resolve(ctx context.Context, host string) (*HostConfig, error) {
	if IsOptionLikeHost(host) {
		return nil, fmt.Errorf("%w: host must not begin with a dash", ErrSSHConfigFailed)
	}

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
	if r.configChanged(r.lastMtime) {
		r.purgeCacheLocked()
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
	r.purgeCacheLocked()
	r.mu.Unlock()
}

// purgeCacheLocked clears every cache map and the mtime anchor. Caller MUST
// hold r.mu. Both cache families (host-keyed and identity-keyed) belong to
// one config generation: a change to the config file can alter host B's
// resolution, the identity a typed argv resolves to, or both, so a purge
// never leaves the argv family behind.
func (r *sshConfigResolver) purgeCacheLocked() {
	r.cache = make(map[string]*HostConfig)
	r.argvCache = make(map[string]*HostConfig)
	r.lastMtime = time.Time{}
}

// runSSHG executes ssh -F <configPath> -G <host> and parses the output.
// Using -F restricts ssh to the specified config file only, matching the
// existing behavior of the kevinburke/ssh_config library it replaces.
func (r *sshConfigResolver) runSSHG(ctx context.Context, host string) (*HostConfig, error) {
	return r.execSSHG(ctx, []string{"-F", r.configPath, "-G", host}, host)
}

// runSSHGArgv executes ssh -G with the TYPED argv (after the leading
// "ssh"): ssh -G <options> <destination>, exactly as the user wrote them,
// with nothing injected. The typed -F/-o/-J/-l/-p therefore reach the
// oracle, and the answer describes the configuration the typed line will
// actually run (nocx-c5az).
func (r *sshConfigResolver) runSSHGArgv(ctx context.Context, argv []string) (*HostConfig, error) {
	return r.execSSHG(ctx, argv[1:], oracleHost(argv))
}

// execSSHG runs ssh with the given args (after the binary), parses the -G
// output, and reports each failure condition exactly once.
func (r *sshConfigResolver) execSSHG(ctx context.Context, args []string, host string) (*HostConfig, error) {
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

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	// #nosec G204 — sshPath is either explicitly configured by the app or
	// resolved from PATH; the args are either the app's own -F/-G pair or
	// the argv of a hand-typed ssh line the user submitted in their own
	// terminal. ssh -G only prints config and never connects or executes,
	// so this is the intended oracle — the whole point of the package.
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
		case "remotecommand":
			// ssh -G prints "none" when RemoteCommand is unset; "none" is
			// OpenSSH's sentinel for "no command" (the man page and ssh
			// itself special-case it), so it normalizes to the empty
			// string. A literal command of "none" is only reachable as a
			// quoting trick ssh itself would also treat as absent — match
			// the oracle's verdict.
			if value != "none" {
				cfg.RemoteCommand = value
			}
		case "controlmaster":
			// "no" is the default and is what ssh -G prints for an unset
			// directive; it collapses to the empty string so "the user
			// expressed a policy" is a non-empty test on all three
			// fields. true/false are OpenSSH >= 10's spelling of yes/no.
			switch value {
			case "no", "false":
				cfg.ControlMaster = ""
			case "true":
				cfg.ControlMaster = "yes"
			default:
				cfg.ControlMaster = value
			}
		case "controlpath":
			// ssh -G omits the line entirely when ControlPath is unset,
			// and prints "none" when it is set to that sentinel; both mean
			// "no socket", so both collapse to empty.
			if value != "none" {
				cfg.ControlPath = value
			}
		case "controlpersist":
			switch value {
			case "no", "false", "0":
				cfg.ControlPersist = ""
			case "true":
				cfg.ControlPersist = "yes"
			default:
				cfg.ControlPersist = value
			}
		case "requesttty":
			// "auto" is the RequestTTY default; ssh -G prints it when the
			// directive is unset, and "auto" means "ssh decides" — for a
			// command execution that is no TTY, indistinguishable from
			// unset, so it collapses to the empty string. OpenSSH >= 10
			// prints true/false where older versions print yes/no; both
			// normalize to the canonical yes/no. Anything else ("force",
			// future values) passes through verbatim.
			switch value {
			case "auto":
				cfg.RequestTTY = ""
			case "true":
				cfg.RequestTTY = "yes"
			case "false":
				cfg.RequestTTY = "no"
			default:
				cfg.RequestTTY = value
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

	r.reported.Store(r.reported.Load() | mask)

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
