package store

import (
	"database/sql"
	"fmt"
	"time"
)

// AICategory represents a message's AI-generated category.
type AICategory struct {
	MessageID   int64
	Category    string
	Subcategory string
	Confidence  float64
	Provider    string
	Model       string
	CreatedAt   time.Time
}

// AIEntity represents an AI-extracted named entity.
type AIEntity struct {
	ID         int64
	MessageID  int64
	EntityType string
	Value      string
	Context    string
	Confidence float64
	Provider   string
	Model      string
	CreatedAt  time.Time
}

// UpsertAICategory inserts or replaces the AI category for a message.
func (s *Store) UpsertAICategory(cat *AICategory) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO ai_categories (message_id, category, subcategory, confidence, provider, model)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		cat.MessageID, cat.Category, cat.Subcategory, cat.Confidence, cat.Provider, cat.Model,
	)
	if err != nil {
		return fmt.Errorf("upsert ai category: %w", err)
	}
	return nil
}

// GetAICategory returns the AI category for a message.
func (s *Store) GetAICategory(messageID int64) (*AICategory, error) {
	var cat AICategory
	var sub sql.NullString
	err := s.db.QueryRow(
		`SELECT message_id, category, subcategory, confidence, provider, COALESCE(model,''), created_at
		 FROM ai_categories WHERE message_id = ?`, messageID,
	).Scan(&cat.MessageID, &cat.Category, &sub, &cat.Confidence, &cat.Provider, &cat.Model, &cat.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if sub.Valid {
		cat.Subcategory = sub.String
	}
	return &cat, nil
}

// ListUncategorizedMessageIDs returns IDs of messages without an AI category.
func (s *Store) ListUncategorizedMessageIDs(limit int) ([]int64, error) {
	rows, err := s.db.Query(
		`SELECT m.id FROM messages m
		 WHERE NOT EXISTS (SELECT 1 FROM ai_categories ac WHERE ac.message_id = m.id)
		 ORDER BY m.id
		 LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// CountUncategorized returns the number of messages without AI categories.
func (s *Store) CountUncategorized() (int64, error) {
	var count int64
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM messages m
		 WHERE NOT EXISTS (SELECT 1 FROM ai_categories ac WHERE ac.message_id = m.id)`,
	).Scan(&count)
	return count, err
}

// InsertAIEntity inserts a new AI entity.
func (s *Store) InsertAIEntity(ent *AIEntity) (int64, error) {
	result, err := s.db.Exec(
		`INSERT INTO ai_entities (message_id, entity_type, value, context, confidence, provider, model)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ent.MessageID, ent.EntityType, ent.Value, ent.Context, ent.Confidence, ent.Provider, ent.Model,
	)
	if err != nil {
		return 0, fmt.Errorf("insert ai entity: %w", err)
	}
	return result.LastInsertId()
}

// ListAIEntities returns entities for a message.
func (s *Store) ListAIEntities(messageID int64) ([]AIEntity, error) {
	rows, err := s.db.Query(
		`SELECT id, message_id, entity_type, value, COALESCE(context,''), confidence, provider, COALESCE(model,''), created_at
		 FROM ai_entities WHERE message_id = ? ORDER BY entity_type, value`, messageID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ents []AIEntity
	for rows.Next() {
		var e AIEntity
		if err := rows.Scan(&e.ID, &e.MessageID, &e.EntityType, &e.Value, &e.Context, &e.Confidence, &e.Provider, &e.Model, &e.CreatedAt); err != nil {
			return nil, err
		}
		ents = append(ents, e)
	}
	return ents, rows.Err()
}

// SearchAIEntities searches entities by type and optional value filter.
func (s *Store) SearchAIEntities(entityType, valueFilter string, limit int) ([]AIEntity, error) {
	var rows *sql.Rows
	var err error
	if valueFilter != "" {
		rows, err = s.db.Query(
			`SELECT id, message_id, entity_type, value, COALESCE(context,''), confidence, provider, COALESCE(model,''), created_at
			 FROM ai_entities WHERE entity_type = ? AND value LIKE ? ORDER BY message_id DESC LIMIT ?`,
			entityType, "%"+valueFilter+"%", limit,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT id, message_id, entity_type, value, COALESCE(context,''), confidence, provider, COALESCE(model,''), created_at
			 FROM ai_entities WHERE entity_type = ? ORDER BY message_id DESC LIMIT ?`,
			entityType, limit,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ents []AIEntity
	for rows.Next() {
		var e AIEntity
		if err := rows.Scan(&e.ID, &e.MessageID, &e.EntityType, &e.Value, &e.Context, &e.Confidence, &e.Provider, &e.Model, &e.CreatedAt); err != nil {
			return nil, err
		}
		ents = append(ents, e)
	}
	return ents, rows.Err()
}

// AISummary represents a thread or message summary.
type AISummary struct {
	ID             int64
	ConversationID int64
	MessageID      int64
	SummaryShort   string
	SummaryFull    string
	Provider       string
	Model          string
	CreatedAt      time.Time
}

// UpsertAISummary inserts or updates a summary.
func (s *Store) UpsertAISummary(sum *AISummary) (int64, error) {
	// Check if summary exists for this conversation or message.
	var existingID int64
	var err error
	if sum.ConversationID > 0 {
		err = s.db.QueryRow(
			"SELECT id FROM ai_summaries WHERE conversation_id = ?", sum.ConversationID,
		).Scan(&existingID)
	} else if sum.MessageID > 0 {
		err = s.db.QueryRow(
			"SELECT id FROM ai_summaries WHERE message_id = ?", sum.MessageID,
		).Scan(&existingID)
	}

	if err == nil && existingID > 0 {
		_, err := s.db.Exec(
			`UPDATE ai_summaries SET summary_short=?, summary_full=?, provider=?, model=?, created_at=CURRENT_TIMESTAMP
			 WHERE id=?`,
			sum.SummaryShort, sum.SummaryFull, sum.Provider, sum.Model, existingID,
		)
		return existingID, err
	}

	result, err := s.db.Exec(
		`INSERT INTO ai_summaries (conversation_id, message_id, summary_short, summary_full, provider, model)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		nilIfZero(sum.ConversationID), nilIfZero(sum.MessageID),
		sum.SummaryShort, sum.SummaryFull, sum.Provider, sum.Model,
	)
	if err != nil {
		return 0, fmt.Errorf("insert summary: %w", err)
	}
	return result.LastInsertId()
}

func nilIfZero(v int64) interface{} {
	if v == 0 {
		return nil
	}
	return v
}

// GetSummaryByConversation returns the summary for a thread.
func (s *Store) GetSummaryByConversation(convID int64) (*AISummary, error) {
	var sum AISummary
	var convNull, msgNull sql.NullInt64
	var full sql.NullString
	err := s.db.QueryRow(
		`SELECT id, conversation_id, message_id, summary_short, summary_full, provider, COALESCE(model,''), created_at
		 FROM ai_summaries WHERE conversation_id = ? ORDER BY created_at DESC LIMIT 1`, convID,
	).Scan(&sum.ID, &convNull, &msgNull, &sum.SummaryShort, &full, &sum.Provider, &sum.Model, &sum.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if convNull.Valid {
		sum.ConversationID = convNull.Int64
	}
	if msgNull.Valid {
		sum.MessageID = msgNull.Int64
	}
	if full.Valid {
		sum.SummaryFull = full.String
	}
	return &sum, nil
}

// ThreadMessage represents a message in a thread for summarization.
type ThreadMessage struct {
	ID        int64
	Subject   string
	Snippet   string
	FromEmail string
	SentAt    time.Time
}

// GetThreadMessages returns messages for a conversation, ordered chronologically.
func (s *Store) GetThreadMessages(convID int64) ([]ThreadMessage, error) {
	rows, err := s.db.Query(
		`SELECT m.id, COALESCE(m.subject,''), COALESCE(m.snippet,''),
		        COALESCE(p.email_address,''),
		        COALESCE(m.sent_at, m.received_at, m.internal_date, '') as sent
		 FROM messages m
		 LEFT JOIN message_recipients mr ON mr.message_id = m.id AND mr.recipient_type = 'from'
		 LEFT JOIN participants p ON p.id = mr.participant_id
		 WHERE m.conversation_id = ?
		 ORDER BY m.sent_at ASC`, convID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []ThreadMessage
	for rows.Next() {
		var m ThreadMessage
		var sentStr string
		if err := rows.Scan(&m.ID, &m.Subject, &m.Snippet, &m.FromEmail, &sentStr); err != nil {
			return nil, err
		}
		if sentStr != "" {
			m.SentAt, _ = time.Parse("2006-01-02 15:04:05", sentStr)
			if m.SentAt.IsZero() {
				m.SentAt, _ = time.Parse(time.RFC3339, sentStr)
			}
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// LargeThread represents a conversation with many messages.
type LargeThread struct {
	ConversationID int64
	Title          string
	MessageCount   int
}

// ListLargeThreads returns conversation IDs with at least minMessages messages.
func (s *Store) ListLargeThreads(minMessages, limit int) ([]LargeThread, error) {
	rows, err := s.db.Query(
		`SELECT c.id, COALESCE(c.title,''), COUNT(m.id) as cnt
		 FROM conversations c
		 JOIN messages m ON m.conversation_id = c.id
		 GROUP BY c.id
		 HAVING cnt >= ?
		 ORDER BY cnt DESC
		 LIMIT ?`, minMessages, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []LargeThread
	for rows.Next() {
		var r LargeThread
		if err := rows.Scan(&r.ConversationID, &r.Title, &r.MessageCount); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// GetMessageSnippetAndSubject returns subject and snippet for a message (lightweight query for AI batch).
func (s *Store) GetMessageSnippetAndSubject(messageID int64) (subject, snippet, fromEmail string, err error) {
	err = s.db.QueryRow(
		`SELECT COALESCE(m.subject,''), COALESCE(m.snippet,''),
		        COALESCE(p.email_address,'')
		 FROM messages m
		 LEFT JOIN message_recipients mr ON mr.message_id = m.id AND mr.recipient_type = 'from'
		 LEFT JOIN participants p ON p.id = mr.participant_id
		 WHERE m.id = ?`, messageID,
	).Scan(&subject, &snippet, &fromEmail)
	return
}
