package shellintegration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	gossh "golang.org/x/crypto/ssh"

	"github.com/shady2k/nocx/internal/log"
)

// CwdInfo carries the validated cwd payload defined by AD-5.
type CwdInfo struct {
	Host string `json:"host"`
	Path string `json:"path"`
}

// ShellIntegration is the OSC 7/133 substrate contract (Tier A shell hooks
// now; Tier B remote-helper seam later). Per AD-6 the backend never parses
// OSC — the VT frontend surfaces OSC events and the backend only validates
// the results.
type ShellIntegration interface {
	// ValidateCwd validates a frontend-supplied host and path from an
	// OSC 7 event.
	ValidateCwd(host string, path string) (CwdInfo, error)

	// EnsureInstalled writes integration scripts to home/.nocx/ and appends
	// gate lines to rc files (if not already present). Idempotent: skips if
	// VERSION matches. Best-effort: errors are logged, not fatal.
	EnsureInstalled(home string) error

	// EnsureInstalledRemote does the same on a remote host via SFTP.
	// Best-effort: errors are logged, not fatal.
	EnsureInstalledRemote(ctx context.Context, sshClient *gossh.Client, remoteHome string) error

	// ActivationEnv returns env vars to set when starting a shell so the
	// rc gate activates the integration.
	ActivationEnv() []string

	// RemoteStartCommand returns the command to use for SSH session.Start()
	// that sets the activation env var and execs the user's shell.
	RemoteStartCommand() string
}

// Impl is the production implementation.
type Impl struct {
	log    log.Logger
	isHost func(host string) bool
}

// New returns a production ShellIntegration implementation.
func New(logger log.Logger) *Impl {
	return &Impl{
		log:    logger,
		isHost: isLocalHost,
	}
}

func (s *Impl) ValidateCwd(host string, path string) (CwdInfo, error) {
	if !s.isHost(host) {
		return CwdInfo{}, fmt.Errorf("shellintegration: host %q is not localhost", host)
	}
	if path == "" {
		return CwdInfo{}, fmt.Errorf("shellintegration: path must not be empty")
	}
	if !strings.HasPrefix(path, "/") && path != "~" {
		return CwdInfo{}, fmt.Errorf("shellintegration: path %q is not absolute", path)
	}
	s.log.Debug("shellintegration: cwd validated", "host", host, "path", path)
	return CwdInfo{Host: host, Path: path}, nil
}

func (s *Impl) EnsureInstalled(home string) error {
	if home == "" {
		return fmt.Errorf("shellintegration: home directory is empty")
	}

	dir := filepath.Join(home, dirName)
	vf := filepath.Join(dir, versionFile)

	// Check version — skip if already installed and up to date.
	if data, err := os.ReadFile(vf); err == nil && strings.TrimSpace(string(data)) == version {
		s.log.Debug("shellintegration: already installed, version matches", "version", version)
		return nil
	}

	// Create directory.
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("shellintegration: create dir %s: %w", dir, err)
	}

	// Write scripts.
	for name, content := range scripts {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return fmt.Errorf("shellintegration: write script %s: %w", path, err)
		}
	}

	// Write version.
	if err := os.WriteFile(vf, []byte(version+"\n"), 0644); err != nil {
		return fmt.Errorf("shellintegration: write version: %w", err)
	}

	// Append gate lines to rc files.
	for rcFile, gate := range rcGate {
		rcPath := filepath.Join(home, rcFile)
		if err := appendGate(rcPath, gate); err != nil {
			s.log.Warn("shellintegration: failed to append gate to rc file",
				"path", rcPath, "error", err)
			// Non-fatal: scripts are installed, user can source manually.
		}
	}

	s.log.Info("shellintegration: installed", "dir", dir, "version", version)
	return nil
}

// appendGate appends gateLine to filePath if not already present.
func appendGate(filePath, gateLine string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		// File doesn't exist — create it with just the gate.
		return os.WriteFile(filePath, []byte(gateLine+"\n"), 0644)
	}

	content := string(data)
	if strings.Contains(content, gateLine) {
		return nil // Already present.
	}

	// Append with a newline separator if file doesn't end with one.
	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += gateLine + "\n"
	return os.WriteFile(filePath, []byte(content), 0644)
}

func (s *Impl) ActivationEnv() []string {
	return []string{activationEnvVar + "=1"}
}

func (s *Impl) RemoteStartCommand() string {
	return activationEnvVar + "=1 exec ${SHELL:-/bin/sh} -l"
}

func isLocalHost(host string) bool {
	if host == "" || host == "localhost" {
		return true
	}
	hn, err := os.Hostname()
	if err != nil {
		return false
	}
	return host == hn
}


