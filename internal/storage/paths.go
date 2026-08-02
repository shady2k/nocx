// Package storage provides shared storage path resolution and the atomic
// JSON DocumentStore capability (ADR-0011). Platform-aware path resolution
// distinguishes three OS roles — configuration documents, application data,
// and disposable caches — with no fallback: failure to resolve is explicit.
package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Paths resolves the OS locations nocx persists into, distinguishing roles.
// There is deliberately no SecretsDir: secrets live in the OS keychain, which
// is not a path we own (ADR-0011 §1).
type Paths interface {
	// ConfigDir is where human-recoverable configuration documents live.
	ConfigDir() string
	// DataDir is where content.db lives.
	DataDir() string
	// CacheDir is where disposable indexes live.
	CacheDir() string
}

type osPaths struct {
	configDir string
	dataDir   string
	cacheDir  string
}

func (p *osPaths) ConfigDir() string { return p.configDir }
func (p *osPaths) DataDir() string   { return p.dataDir }
func (p *osPaths) CacheDir() string  { return p.cacheDir }

// NewAppPaths resolves the three roles for the profile THIS build owns:
// AppDirName, which the build tag decides (appdir.go). It is the only way to
// reach the application's paths from outside this package — the app name is
// deliberately not a parameter, so no caller can name a profile and no
// composition root can name the wrong one.
//
// That is the whole guarantee. A parameter would have to be got right in the
// app, in cmd/devharness, and in whatever launches the e2e backend next, which
// is the arrangement that produced nocx-ti8w in the first place.
func NewAppPaths() (Paths, error) {
	return newOSPaths(AppDirName)
}

// newOSPaths resolves the three roles for appName on the current platform.
// It returns an error when any role cannot be resolved; there is no fallback
// (ADR-0011: failure to resolve is explicit, never a silent /tmp write).
//
// Unexported on purpose: see NewAppPaths. It stays a parameterised function
// because the tests need to resolve a profile other than this build's in order
// to assert the two are disjoint.
func newOSPaths(appName string) (Paths, error) {
	switch runtime.GOOS {
	case "linux":
		return newLinuxPaths(appName)
	case "darwin":
		return newDarwinPaths(appName)
	default:
		return nil, fmt.Errorf("storage: unsupported platform %q", runtime.GOOS)
	}
}

func newLinuxPaths(appName string) (Paths, error) {
	configDir, err := resolveConfigDir(appName)
	if err != nil {
		return nil, fmt.Errorf("storage: resolve config dir: %w", err)
	}

	dataDir, err := resolveDataDir(appName)
	if err != nil {
		return nil, fmt.Errorf("storage: resolve data dir: %w", err)
	}

	cacheDir, err := resolveCacheDir(appName)
	if err != nil {
		return nil, fmt.Errorf("storage: resolve cache dir: %w", err)
	}

	return &osPaths{
		configDir: configDir,
		dataDir:   dataDir,
		cacheDir:  cacheDir,
	}, nil
}

func newDarwinPaths(appName string) (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("storage: resolve home dir: %w", err)
	}

	appSupport := filepath.Join(home, "Library", "Application Support", appName)
	caches := filepath.Join(home, "Library", "Caches", appName)

	return &osPaths{
		configDir: appSupport,
		dataDir:   appSupport,
		cacheDir:  caches,
	}, nil
}

// resolveConfigDir returns the platform config directory with appName appended.
func resolveConfigDir(appName string) (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, appName), nil
}

// resolveDataDir returns the platform data directory with appName appended.
// There is no os.UserDataDir; we resolve from XDG_DATA_HOME or ~/.local/share.
func resolveDataDir(appName string) (string, error) {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, appName), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no XDG_DATA_HOME and cannot resolve home: %w", err)
	}
	return filepath.Join(home, ".local", "share", appName), nil
}

// resolveCacheDir returns the platform cache directory with appName appended.
func resolveCacheDir(appName string) (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, appName), nil
}
