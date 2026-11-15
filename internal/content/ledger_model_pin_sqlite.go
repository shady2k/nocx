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

// PinConversationModel records admission before the turn can produce an outcome.
// Later admissions cannot replace the conversation's first model.
func (s *sqliteContent) PinConversationModel(ctx context.Context, conversationID, harnessID, model string) error {
	if conversationID == "" || model == "" {
		return nil
	}
	return s.run(ctx, func(ctx context.Context) error {
		_, err := s.conn(ctx).ExecContext(ctx, `INSERT INTO agent_conversations(id,harness,model) VALUES(?,?,?) ON CONFLICT DO NOTHING`, conversationID, harnessID, model)
		return err
	})
}
