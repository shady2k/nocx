package pi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/shady2k/nocx/internal/harness"
)

type turnFiles struct {
	dir, configPath, extensionPath, sessionDir string
	lock                                       *os.File
	once                                       sync.Once
}

func (f *turnFiles) cleanup() {
	f.once.Do(func() {
		_ = os.RemoveAll(filepath.Dir(f.configPath))
		_ = f.lock.Close()
	})
}

// The persistent guard serializes directory creation with sweeping. Individual
// turn locks survive until cleanup; the kernel releases them on backend death.
func (c *Connector) turnGuard() (*os.File, error) {
	root := filepath.Join(c.dataDir, "pi", "turns")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	guard, err := os.OpenFile(filepath.Join(root, ".lock"), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // nocx-owned state path
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(guard.Fd()), syscall.LOCK_EX); err != nil {
		_ = guard.Close()
		return nil, err
	}
	return guard, nil
}

func (c *Connector) sweepTurnFiles() error {
	guard, err := c.turnGuard()
	if err != nil {
		return err
	}
	defer func() { _ = guard.Close() }()
	return c.sweepLocked()
}

func (c *Connector) sweepLocked() error {
	root := filepath.Join(c.dataDir, "pi", "turns")
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "turn-") {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // nocx-owned turn directory
		if errors.Is(err, os.ErrNotExist) {
			continue // A completed turn can clean up while the sweep enumerates.
		}
		if err != nil {
			return err
		}
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			err = os.RemoveAll(dir)
		} else if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			err = nil
		}
		_ = lock.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *Connector) prepareTurn(spec harness.TurnSpec) (*turnFiles, error) {
	if c.dataDir == "" {
		return nil, errors.New("pi needs nocx's data directory")
	}
	guard, err := c.turnGuard()
	if err != nil {
		return nil, err
	}
	defer func() { _ = guard.Close() }()
	if err = c.sweepLocked(); err != nil {
		return nil, err
	}
	root := filepath.Join(c.dataDir, "pi")
	sum := sha256.Sum256([]byte(spec.ConversationID))
	workspace := filepath.Join(root, "workspaces", hex.EncodeToString(sum[:]))
	if err = os.MkdirAll(workspace, 0o700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(filepath.Join(root, "turns"), "turn-")
	if err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // nocx-owned turn directory
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	f := &turnFiles{dir: workspace, configPath: filepath.Join(dir, "config.json"), extensionPath: filepath.Join(dir, "extension.mjs"), sessionDir: filepath.Join(root, "sessions"), lock: lock}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.cleanup()
		return nil, err
	}
	bridgeEnv := map[string]string{}
	for _, v := range spec.Tools.Env {
		key, value, ok := strings.Cut(v, "=")
		if !ok {
			f.cleanup()
			return nil, errors.New("invalid bridge environment variable " + key)
		}
		bridgeEnv[key] = value
	}
	raw, err := json.Marshal(struct {
		Command      []string          `json:"command"`
		Env          map[string]string `json:"env"`
		SystemPrompt string            `json:"systemPrompt"`
	}{spec.Tools.Command, bridgeEnv, spec.SystemPrompt})
	if err == nil {
		err = os.WriteFile(f.configPath, raw, 0o600)
	}
	if err == nil {
		err = os.WriteFile(f.extensionPath, extension, 0o600)
	}
	if err != nil {
		f.cleanup()
		return nil, err
	}
	return f, nil
}
