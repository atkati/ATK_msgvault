package store

import "database/sql"

// UpdateSourceOAuthApp updates the OAuth app binding for a source.
// Pass a null NullString to clear the binding (use default app).
func (s *Store) UpdateSourceOAuthApp(sourceID int64, oauthApp sql.NullString) error {
	_, err := s.db.Exec(`
		UPDATE sources
		SET oauth_app = ?, updated_at = datetime('now')
		WHERE id = ?
	`, oauthApp, sourceID)
	return err
}
