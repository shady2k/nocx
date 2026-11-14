package content

import (
	"context"
	"database/sql"
	"errors"
)

// ConversationModel reads the conversation record independently of retained runs.
func (s *sqliteContent) ConversationModel(ctx context.Context, conversationID, harnessID string) (string, error) {
	var model string
	err := s.conn(ctx).QueryRowContext(ctx, `SELECT model FROM agent_conversations WHERE id = ? AND harness = ?`, conversationID, harnessID).Scan(&model)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return model, err
}

func pinConversationModel(ctx context.Context, tx *sql.Tx, facts RunFacts) error {
	if facts.ConversationID == "" || facts.Model == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO agent_conversations(id,harness,model) VALUES(?,?,?) ON CONFLICT DO NOTHING`, facts.ConversationID, facts.Harness, facts.Model)
	return err
}
