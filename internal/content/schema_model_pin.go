package content

import (
	"context"
	"database/sql"
)

const agentConversationsDDL = `CREATE TABLE IF NOT EXISTS agent_conversations (
  id TEXT NOT NULL,
  harness TEXT NOT NULL,
  model TEXT NOT NULL,
  PRIMARY KEY (id, harness)
) STRICT;`

func migrateAgentConversations24to25(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, agentConversationsDDL)
	return err
}
