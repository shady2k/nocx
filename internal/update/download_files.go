package update

// Small pieces the resuming download needs: byte counts, an entity tag kept
// beside the staged file (so a resume can be validated across attempts and
// across processes), and a whole-file hash.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
)

// downloadAttempts bounds the number of bodies one download may take: the
// first, plus two resumptions. Not a retry of a failure — each attempt carries
// the bytes the last one delivered.
const downloadAttempts = 3

func fileSize(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	return fi.Size(), nil
}

// etagPath is the staged file's sidecar. Beside the download, never inside it:
// the artefact's size and hash are the manifest's facts, and this file is only
// the server's promise about the bytes they describe.
func etagPath(destPath string) string { return destPath + ".etag" }

func readETag(destPath string) string {
	data, err := os.ReadFile(etagPath(destPath)) //nolint:gosec // sibling of the staged download
	if err != nil {
		return ""
	}
	return string(data)
}

func writeETag(destPath, etag string) {
	_ = os.WriteFile(etagPath(destPath), []byte(etag), 0o600)
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // staged download path
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
