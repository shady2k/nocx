package ssh

import (
	"context"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// resolvedConfig holds the merged configuration from ~/.ssh/config and explicit options.
type resolvedConfig struct {
	hostName     string
	user         string
	port         int
	identityFile string
	keyAlgos     []string
	cols         uint16
	rows         uint16
	xpixel       uint16
	ypixel       uint16
}

// resolveConfig merges ~/.ssh/config values (via the injected ConfigResolver)
// with explicit ConnectOptions. Precedence: explicit option > config file > default.
func (rc *RealClient) resolveConfig(ctx context.Context, host string, cfg *ConnectConfig) (*resolvedConfig, error) {
	resolvedHost, resolvedPort := host, 22
	hostHasExplicitPort := false
	if h, p, err := net.SplitHostPort(host); err == nil {
		resolvedHost = h
		if port, err := strconv.Atoi(p); err == nil {
			resolvedPort = port
			hostHasExplicitPort = true
		}
	}

	resolved := &resolvedConfig{
		hostName: resolvedHost,
		user:     currentUser(),
		port:     resolvedPort,
		cols:     80,
		rows:     24,
	}

	// Resolve ~/.ssh/config directives via the injected resolver. On error, the
	// resolver has already logged a one-time warning and returned degraded
	// values — do NOT use those degraded values (they would overwrite the
	// explicit host:port).
	hostCfg, rcErr := rc.configResolver.ResolveConfig(ctx, resolvedHost)
	if rcErr == nil && hostCfg != nil {
		if hostCfg.HostName != "" {
			resolved.hostName = hostCfg.HostName
		}
		if hostCfg.User != "" {
			resolved.user = hostCfg.User
		}
		// Only apply config file port when the host string did NOT already
		// carry an explicit port. An explicit host:port always wins over the
		// config file's Port directive (which defaults to 22 for every host).
		if hostCfg.Port > 0 && !hostHasExplicitPort {
			resolved.port = hostCfg.Port
		}
		if hostCfg.IdentityFile != "" {
			resolved.identityFile = hostCfg.IdentityFile
		}
	}

	// Apply explicit ConnectOptions (highest precedence).
	if cfg.User != "" {
		resolved.user = cfg.User
	}
	if cfg.Port > 0 {
		resolved.port = cfg.Port
	}
	if cfg.KeyFile != "" {
		resolved.identityFile = cfg.KeyFile
	}
	if cfg.Cols > 0 {
		resolved.cols = cfg.Cols
	}
	if cfg.Rows > 0 {
		resolved.rows = cfg.Rows
	}
	if cfg.XPixel > 0 {
		resolved.xpixel = cfg.XPixel
	}
	if cfg.YPixel > 0 {
		resolved.ypixel = cfg.YPixel
	}
	if len(cfg.KeyExchanges) > 0 {
		resolved.keyAlgos = cfg.KeyExchanges
	}

	return resolved, nil
}

// currentUser is the login name to use when nothing names one, resolved the way
// OpenSSH resolves it.
//
// ssh takes it from the passwd database — getpwuid(getuid()) — and ssh_config(5)
// documents the default for User as "the name of the user running ssh". This
// read the environment instead, and $USER is set by a shell: an app launched
// from Finder or a desktop session can have no shell in its ancestry and
// therefore no $USER at all.
//
// The old fallback when both variables were empty was the literal string
// "root", which is the worst available guess — the most privileged account on
// the far side, the one an intrusion-detection rule watches first, and the one
// whose failed attempts get an address banned. An empty result is returned
// instead: a connection that cannot name its user should fail saying so, not
// quietly try to be root.
func currentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	// The passwd lookup failing at all is rare (a static build with no entry for
	// the uid). The environment is a better guess than nothing, and still not a
	// guess at root.
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return os.Getenv("LOGNAME")
}

func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

// resolveAuthzEndpoint applies the ConfigResolver's HostName resolution to
// the host portion of an endpoint string. If the endpoint is already a
// resolved value (an IP address or real hostname, not an alias), this is a
// no-op. For an alias, it resolves through the same ConfigResolver that
// resolveConfig uses for the dial target, so both sides of the authorization
// comparison go through the same resolution.
//
// An empty endpoint returns empty — inline auth has no authorization check.
func (rc *RealClient) resolveAuthzEndpoint(ctx context.Context, endpoint string) string {
	if endpoint == "" {
		return ""
	}
	authHost, authPortStr, err := net.SplitHostPort(endpoint)
	if err != nil {
		authHost = endpoint
		authPortStr = ""
	}

	resolvedHost, _ := rc.configResolver.ResolveHost(ctx, authHost)
	if resolvedHost != "" {
		authHost = resolvedHost
	}

	if authPortStr != "" {
		return net.JoinHostPort(authHost, authPortStr)
	}
	return authHost
}

// checkAuthorization enforces that a linked credential is only submitted to
// the endpoint its profile authorizes. The authorized endpoint comes from the
// resolver's effective profile resolution, resolved through ~/.ssh/config
// to the canonical hostname. The check runs AFTER resolveConfig on the dial
// target, comparing the resolved authorized identity against the freshly-
// resolved dial target.
//
// authorizedEndpoint empty => the credential is unlinked (inline auth) —
// the caller must not call this function when Secrets is nil.
// Port is included when the effective profile specifies one, so the port
// check only runs when the format is "host:port".
//
// credID is carried only for the error message; authorization is decided by
// the endpoint the resolver computed from the effective profile.
func checkAuthorization(authorizedEndpoint string, resolved *resolvedConfig, credID string, jump bool) error {
	if authorizedEndpoint == "" {
		return &ErrCredentialAuthorizationFailed{
			CredentialID: credID,
			Expected:     "<none>",
			ResolvedHost: resolved.hostName,
			ResolvedPort: resolved.port,
			Jump:         jump,
		}
	}

	authHost, authPortStr, err := net.SplitHostPort(authorizedEndpoint)
	if err != nil {
		// No port in the endpoint — host-only authorization.
		authHost = authorizedEndpoint
		authPortStr = ""
	}

	// Host comparison: case-insensitive DNS name comparison (crude but correct
	// for ASCII DNS names and IP literals). IP literals are stored canonically
	// by the resolver (via net.JoinHostPort).
	if !strings.EqualFold(authHost, resolved.hostName) {
		return &ErrCredentialAuthorizationFailed{
			CredentialID: credID,
			Expected:     authorizedEndpoint,
			ResolvedHost: resolved.hostName,
			ResolvedPort: resolved.port,
			Jump:         jump,
		}
	}

	// Port comparison when the authorized endpoint includes one.
	if authPortStr != "" {
		authPort, err := strconv.Atoi(authPortStr)
		if err == nil && authPort != resolved.port {
			return &ErrCredentialAuthorizationFailed{
				CredentialID: credID,
				Expected:     authorizedEndpoint,
				ResolvedHost: resolved.hostName,
				ResolvedPort: resolved.port,
				Jump:         jump,
			}
		}
	}

	return nil
}
