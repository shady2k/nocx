package transport

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/backup"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/storage"
)

func newBackupContractWSServer(t *testing.T) (*WSServer, func()) {
	t.Helper()
	dir := t.TempDir()
	profiles := profile.NewJSONStore(filepath.Join(dir, "profiles.json"))
	profileService := profile.NewProfileService(profiles)
	doc := storage.NewDocumentStore(dir)
	registry := settings.New(doc, &fakeSecretStore{})
	backupService := backup.NewService(profiles, registry, doc, nil, nil)
	ws := NewWSServer(
		log.NewSlogAdapter(nil),
		newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(profiles),
		WithGroupRepository(profiles),
		WithProfileService(profileService),
		WithSettingsRegistry(registry),
		WithBackupService(backupService),
		WithBackupFileSaver(func(fileName, contents string) (*backup.SaveResult, error) {
			return &backup.SaveResult{Path: filepath.Join(dir, fileName)}, nil
		}),
	)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return ws, func() { _ = ws.Stop(ctx) }
}

func backupContractCall(t *testing.T, conn *websocket.Conn, method string, params any) rpcEnvelope {
	t.Helper()
	raw := jsonrpcCall(t, conn, method, params)
	var env rpcEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal %s response: %v", method, err)
	}
	return env
}

func TestBackup_OverTheWireResultsConformToContracts(t *testing.T) {
	createSchema := loadSchema(t, "backup.create.schema.json")
	previewSchema := loadSchema(t, "backup.preview.schema.json")
	restoreSchema := loadSchema(t, "backup.restore.schema.json")
	saveSchema := loadSchema(t, "backup.saveToFile.schema.json")

	ws, stop := newBackupContractWSServer(t)
	defer stop()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	create := backupContractCall(t, conn, "backup.create", map[string]any{})
	if create.Error != nil {
		t.Fatalf("backup.create: %+v", create.Error)
	}
	validateJSON(t, createSchema, create.Result, "backup.create result")
	var created struct {
		Contents string `json:"contents"`
	}
	if err := json.Unmarshal(create.Result, &created); err != nil {
		t.Fatalf("decode backup.create: %v", err)
	}

	preview := backupContractCall(t, conn, "backup.preview", map[string]any{
		"contents": created.Contents,
		"strategy": "merge",
	})
	if preview.Error != nil {
		t.Fatalf("backup.preview: %+v", preview.Error)
	}
	validateJSON(t, previewSchema, preview.Result, "backup.preview result")
	var previewData struct {
		PreviewToken string `json:"previewToken"`
	}
	if err := json.Unmarshal(preview.Result, &previewData); err != nil {
		t.Fatalf("decode backup.preview: %v", err)
	}

	restore := backupContractCall(t, conn, "backup.restore", map[string]any{
		"contents":     created.Contents,
		"strategy":     "merge",
		"previewToken": previewData.PreviewToken,
	})
	if restore.Error != nil {
		t.Fatalf("backup.restore: %+v", restore.Error)
	}
	validateJSON(t, restoreSchema, restore.Result, "backup.restore result")

	saved := backupContractCall(t, conn, "backup.saveToFile", map[string]any{
		"fileName": "backup.json",
		"contents": created.Contents,
	})
	if saved.Error != nil {
		t.Fatalf("backup.saveToFile: %+v", saved.Error)
	}
	validateJSON(t, saveSchema, saved.Result, "backup.saveToFile result")
}

func TestBackup_SaveToFileCancel_ConformsToContract(t *testing.T) {
	saveSchema := loadSchema(t, "backup.saveToFile.schema.json")

	dir := t.TempDir()
	profiles := profile.NewJSONStore(filepath.Join(dir, "profiles.json"))
	profileService := profile.NewProfileService(profiles)
	doc := storage.NewDocumentStore(dir)
	registry := settings.New(doc, &fakeSecretStore{})
	backupService := backup.NewService(profiles, registry, doc, nil, nil)
	// Saver returns (nil, nil) to simulate user cancelling the save dialog.
	ws := NewWSServer(
		log.NewSlogAdapter(nil),
		newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(profiles),
		WithGroupRepository(profiles),
		WithProfileService(profileService),
		WithSettingsRegistry(registry),
		WithBackupService(backupService),
		WithBackupFileSaver(func(fileName, contents string) (*backup.SaveResult, error) {
			return nil, nil
		}),
	)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	saved := backupContractCall(t, conn, "backup.saveToFile", map[string]any{
		"fileName": "cancelled.json",
		"contents": `{"version":1}`,
	})
	if saved.Result == nil {
		t.Fatal("expected JSON null result for cancel, got nil bytes")
	}
	validateJSON(t, saveSchema, saved.Result, "backup.saveToFile cancel result")
}
