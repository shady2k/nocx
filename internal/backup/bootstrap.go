package backup

import (
	"fmt"

	"github.com/shady2k/nocx/internal/storage"
)

// RecoverPrerequisites restores the settings/connection inputs needed to open
// ContentDB before its workspace profile store exists. It leaves the journal
// intact: Service.Recover must finish all sections after the stores are wired.
// A crash between these phases safely repeats the same prepared rollback.
func RecoverPrerequisites(connections ConnectionSnapshotStore, settings SettingsSnapshotStore, doc storage.DocumentStore) error {
	js, err := readJournal(doc)
	if err != nil {
		return err
	}
	if js.state != "prepared" {
		return nil
	}
	if err = connections.ReplaceConnectionSnapshot(*js.connections); err != nil {
		return fmt.Errorf("%w: rollback prerequisite connections: %w", ErrRecoveryRequired, err)
	}
	pending, err := settings.ReplaceNonSecretOverrides(*js.settings)
	if err != nil {
		return fmt.Errorf("%w: rollback prerequisite settings: %w", ErrRecoveryRequired, err)
	}
	settings.Publish(pending)
	return nil
}
