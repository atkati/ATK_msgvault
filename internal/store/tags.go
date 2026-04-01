package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// UserTag represents a user-created tag (independent of Gmail labels).
type UserTag struct {
	ID    int64
	Name  string
	Color string
}

// CreateUserTag creates a user tag. Returns the tag ID.
// User tags have source_id = NULL and label_type = 'user-tag'.
func (s *Store) CreateUserTag(name, color string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, fmt.Errorf("tag name cannot be empty")
	}

	// Check for existing tag with same name (NULL source_id).
	var existing int64
	err := s.db.QueryRow(
		`SELECT id FROM labels WHERE source_id IS NULL AND label_type = 'user-tag' AND name = ?`,
		name,
	).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if err != sql.ErrNoRows {
		return 0, fmt.Errorf("check existing tag: %w", err)
	}

	result, err := s.db.Exec(
		`INSERT INTO labels (source_id, source_label_id, name, label_type, color)
		 VALUES (NULL, NULL, ?, 'user-tag', ?)`,
		name, color,
	)
	if err != nil {
		return 0, fmt.Errorf("create tag: %w", err)
	}
	return result.LastInsertId()
}

// GetUserTag retrieves a user tag by name.
func (s *Store) GetUserTag(name string) (*UserTag, error) {
	var tag UserTag
	err := s.db.QueryRow(
		`SELECT id, name, COALESCE(color, '') FROM labels
		 WHERE source_id IS NULL AND label_type = 'user-tag' AND name = ?`,
		name,
	).Scan(&tag.ID, &tag.Name, &tag.Color)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get tag: %w", err)
	}
	return &tag, nil
}

// ListUserTags returns all user-created tags.
func (s *Store) ListUserTags() ([]UserTag, error) {
	rows, err := s.db.Query(
		`SELECT l.id, l.name, COALESCE(l.color, ''),
		        (SELECT COUNT(*) FROM message_labels ml WHERE ml.label_id = l.id)
		 FROM labels l
		 WHERE l.source_id IS NULL AND l.label_type = 'user-tag'
		 ORDER BY l.name`,
	)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()

	var tags []UserTag
	for rows.Next() {
		var tag UserTag
		var count int
		if err := rows.Scan(&tag.ID, &tag.Name, &tag.Color, &count); err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		tags = append(tags, tag)
	}
	return tags, rows.Err()
}

// ListUserTagsWithCount returns all user tags with their message count.
type UserTagWithCount struct {
	UserTag
	MessageCount int
}

func (s *Store) ListUserTagsWithCount() ([]UserTagWithCount, error) {
	rows, err := s.db.Query(
		`SELECT l.id, l.name, COALESCE(l.color, ''),
		        (SELECT COUNT(*) FROM message_labels ml WHERE ml.label_id = l.id)
		 FROM labels l
		 WHERE l.source_id IS NULL AND l.label_type = 'user-tag'
		 ORDER BY l.name`,
	)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()

	var tags []UserTagWithCount
	for rows.Next() {
		var t UserTagWithCount
		if err := rows.Scan(&t.ID, &t.Name, &t.Color, &t.MessageCount); err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

// DeleteUserTag removes a user tag and all its message associations.
func (s *Store) DeleteUserTag(name string) error {
	result, err := s.db.Exec(
		`DELETE FROM labels WHERE source_id IS NULL AND label_type = 'user-tag' AND name = ?`,
		name,
	)
	if err != nil {
		return fmt.Errorf("delete tag: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("tag %q not found", name)
	}
	return nil
}

// TagMessage adds a user tag to a message.
func (s *Store) TagMessage(messageID int64, tagName string) error {
	tagID, err := s.CreateUserTag(tagName, "")
	if err != nil {
		return err
	}
	return s.AddMessageLabels(messageID, []int64{tagID})
}

// UntagMessage removes a user tag from a message.
func (s *Store) UntagMessage(messageID int64, tagName string) error {
	tag, err := s.GetUserTag(tagName)
	if err != nil {
		return err
	}
	if tag == nil {
		return fmt.Errorf("tag %q not found", tagName)
	}
	return s.RemoveMessageLabels(messageID, []int64{tag.ID})
}

// SearchMessagesByTag returns message IDs with the given tag.
func (s *Store) SearchMessagesByTag(tagName string) ([]int64, error) {
	rows, err := s.db.Query(
		`SELECT ml.message_id FROM message_labels ml
		 JOIN labels l ON l.id = ml.label_id
		 WHERE l.source_id IS NULL AND l.label_type = 'user-tag' AND l.name = ?
		 ORDER BY ml.message_id DESC`,
		tagName,
	)
	if err != nil {
		return nil, fmt.Errorf("search by tag: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan message id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
