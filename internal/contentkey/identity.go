package contentkey

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// errNoMachineID reports that this host exposes no identifier of its own.
// Every platform's readMachineID returns it rather than a bespoke error, so
// the fallback below can recognise the case without matching on strings.
var errNoMachineID = errors.New("this host exposes no machine identifier")

// machineIDOrMinted returns the host's own identifier, or — when the host
// has none — one we mint once and keep beside the salt.
//
// Not every machine has an identity to borrow. A container usually has no
// /etc/machine-id at all, and our own `go test -race` runs in one, which is
// how this was found: the acceptance test passed on the developer's host and
// failed in the gate, reporting that history simply does not work there.
// That is not a test-environment quirk to wave away — containers, minimal
// images and several BSDs are machines a user runs a terminal on, and
// failing closed would silently cost them the feature.
//
// What is lost when we mint it ourselves is worth naming precisely, because
// it is less than it appears. The salt already carries the guarantee that
// matters: it lives in the CONFIG directory, so a copy of the data directory
// — content.db and its WAL — carries nothing that opens it. The machine id
// adds only that a copy of the whole home directory fails to open on a
// DIFFERENT machine. With a minted id that property weakens: the id travels
// in the same directory as the salt. Migration is backup-and-restore's job
// anyway (nocx-u0rv), so this trades away the weaker half of the design to
// keep the feature working, rather than the reverse.
func machineIDOrMinted(configDir string) (string, error) {
	id, err := readMachineID()
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, errNoMachineID) {
		return "", err
	}
	return loadOrMintMachineID(filepath.Join(configDir, "machine.id"))
}

// loadOrMintMachineID reads the id we minted on a previous start, or mints
// one. Same discipline as the salt: 0600 inside a 0700 directory, O_EXCL so
// two racing starts cannot each believe they created it — the loser re-reads
// the winner's file rather than overwriting it, because an id that changes
// between starts would strand the database exactly as a lost salt does.
func loadOrMintMachineID(path string) (string, error) {
	//nolint:gosec // The path is the app-owned machine-id location.
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 { // #nosec G304 -- app-owned config path, never caller input
		return string(b), nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create machine-id dir: %w", err)
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate machine-id: %w", err)
	}
	id := hex.EncodeToString(raw[:])
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec -- app-owned config path, never caller input
	if err != nil {
		// Lost the race, or the file appeared between the read and now.
		//nolint:gosec // The path is the same app-owned machine-id location.
		if b, readErr := os.ReadFile(path); readErr == nil && len(b) > 0 { // #nosec G304 -- same app-owned path
			return string(b), nil
		}
		return "", fmt.Errorf("create machine-id: %w", err)
	}
	if _, err := f.WriteString(id); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("write machine-id: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close machine-id: %w", err)
	}
	return id, nil
}
