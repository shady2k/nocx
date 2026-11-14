package pi

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// validate permits an append after a crash, but never a replacement or rollback
// of the prefix already acknowledged by nocx.
func (m resumeMarker) validate() error {
	if m.Bytes <= 0 || m.SHA256 == "" {
		return errors.New("invalid native session checkpoint")
	}
	file, err := os.Open(m.SessionFile) //nolint:gosec // pi's native session path from its RPC state
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.CopyN(hash, file, m.Bytes); err != nil {
		return err
	}
	if fmt.Sprintf("%x", hash.Sum(nil)) != m.SHA256 {
		return errors.New("native session checkpoint differs")
	}
	return nil
}

func (c *codec) checkpoint() error {
	if c.markerPath == "" || c.sessionFile == "" {
		return nil
	}
	raw, err := os.ReadFile(c.sessionFile) //nolint:gosec // pi's native session path from its RPC state
	if errors.Is(err, os.ErrNotExist) && c.expected.SessionFile == "" {
		return nil
	}
	if err != nil {
		return err
	}
	// pi does not persist a new session until it has messages to resume.
	hasMessages := false
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var entry struct {
			Type string `json:"type"`
		}
		if err = json.Unmarshal(line, &entry); err != nil {
			return err
		}
		if entry.Type == "message" {
			hasMessages = true
		}
	}
	if !hasMessages {
		return nil
	}
	marker := resumeMarker{SessionFile: c.sessionFile, Bytes: int64(len(raw)), SHA256: fmt.Sprintf("%x", sha256.Sum256(raw))}
	encoded, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	temp, err := prepareResumeRecord(c.markerPath, encoded)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp) }()
	if err := os.Rename(temp, c.markerPath); err != nil {
		return err
	}
	c.expected = marker
	return nil
}

// prepareResumeRecord leaves the old record untouched until the caller commits
// the closed, synced sibling with rename. A crash leaves only a private orphan.
func prepareResumeRecord(path string, raw []byte) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".resume-*.json")
	if err != nil {
		return "", err
	}
	name := file.Name()
	_, writeErr := file.Write(raw)
	syncErr := file.Sync()
	if err := errors.Join(writeErr, syncErr, file.Close()); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}
