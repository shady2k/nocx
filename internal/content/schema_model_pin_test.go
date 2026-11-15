package content

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSchema24ConversationModelMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	rawExec(t, path, releasedSchema(t, 24), `PRAGMA user_version=24`)
	db := openStore(t, path)
	if !aTableExists(t, path, "agent_conversations") {
		t.Fatal("conversation model record missing")
	}
	if got, err := db.Ledger().ConversationModel(context.Background(), "new", "pi"); err != nil || got != "" {
		t.Fatalf("new conversation %q %v", got, err)
	}
}

func TestSchema24ConversationShape(t *testing.T) {
	digest, _, err := shapeOfDDL(context.Background(), releasedSchema(t, 24))
	if err != nil || digest != schemaShapeDigests[24] {
		t.Fatalf("24 shape = %s err %v", digest, err)
	}
}
