package shellintegration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	gossh "golang.org/x/crypto/ssh"

	"github.com/shady2k/nocx/internal/log"
)

// randReader is the io.Reader used to generate session IDs. Swappable
// in tests so we can verify the fail-closed path (nocx-4ff.13).
var randReader io.Reader = rand.Reader

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
	// rc gate activates the integration. When enhanced is true, it also
	// emits NOCX_PROMPT_MODE=marker-only and a unique NOCX_SESSION_ID.
	ActivationEnv(enhanced bool) []string

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
	// #nosec G304 — path is built from validated home + fixed dir/version constants.
	if data, err := os.ReadFile(vf); err == nil && strings.TrimSpace(string(data)) == version {
		s.log.Debug("shellintegration: already installed, version matches", "version", version)
		return nil
	}

	// Create directory.
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("shellintegration: create dir %s: %w", dir, err)
	}

	// Write scripts.
	for name, content := range scripts {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return fmt.Errorf("shellintegration: write script %s: %w", path, err)
		}
	}

	// Append gate lines to rc files.
	gatesOK := true
	for rcFile, gate := range rcGate {
		rcPath := filepath.Join(home, rcFile)
		if err := appendGate(rcPath, gate); err != nil {
			s.log.Warn("shellintegration: failed to append gate to rc file",
				"path", rcPath, "error", err)
			// Non-fatal: scripts are installed, user can source manually.
			gatesOK = false
		}
	}

	// Write the version marker LAST, and only once the gates are in place. A
	// matching version short-circuits every future run, so recording success
	// before a gate is appended would strand the integration forever if the
	// append failed — the next launch must retry rather than skip.
	if gatesOK {
		if err := os.WriteFile(vf, []byte(version+"\n"), 0o600); err != nil {
			return fmt.Errorf("shellintegration: write version: %w", err)
		}
	}

	s.log.Info("shellintegration: installed", "dir", dir, "version", version, "gatesOK", gatesOK)
	return nil
}

// appendGate appends gateLine to filePath if not already present.
// Writes atomically via a temporary file + rename to avoid truncating the
// rc file if the process is interrupted.
func appendGate(filePath, gateLine string) error {
	// #nosec G304 — path is built from validated home + fixed rc filename constants.
	data, err := os.ReadFile(filePath)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		// File doesn't exist — create it with just the gate.
		return writeFileAtomic(filePath, []byte(gateLine+"\n"))
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
	return writeFileAtomic(filePath, []byte(content))
}

// writeFileAtomic writes data to path by creating a temporary file in the
// same directory and renaming it over path.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".nocx-gate-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}

func (s *Impl) ActivationEnv(enhanced bool) []string {
	env := []string{activationEnvVar + "=1"}
	if enhanced {
		sid, ok := newSessionID()
		if !ok {
			s.log.Warn("shellintegration: session id unavailable; disabling enhanced prompt (fail-closed)")
			return env
		}
		env = append(
			env,
			promptModeEnvVar+"="+promptModeMarkerOnly,
			sessionIDEnvVar+"="+sid,
		)
	}
	return env
}

func newSessionID() (string, bool) {
	var b [16]byte
	if _, err := io.ReadFull(randReader, b[:]); err != nil {
		return "", false
	}
	return hex.EncodeToString(b[:]), true
}

func (s *Impl) RemoteStartCommand() string {
	// Quote ${SHELL:-/bin/sh} so paths with spaces are handled as a single
	// argument to exec. The expansion still happens on the remote host.
	return activationEnvVar + `=1 exec "${SHELL:-/bin/sh}" -l`
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
