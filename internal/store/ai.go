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
