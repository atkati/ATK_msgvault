package store

import (
	"database/sql"
	"fmt"
	"time"
)

func parseSQLTime(s string) time.Time {
	for _, fmt := range []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05",
		time.RFC3339,
	} {
		if t, err := time.Parse(fmt, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// AuditReport represents a persisted audit report.
type AuditReport struct {
	ID          int64
	AuditType   string // "anomalies", "sensitive"
	ResultsJSON string // JSON array
	ResultCount int
	Summary     string
	CreatedAt   time.Time
}

// SaveAuditReport persists an audit report.
func (s *Store) SaveAuditReport(auditType, resultsJSON, summary string, count int) (int64, error) {
	result, err := s.db.Exec(
		`INSERT INTO audit_reports (audit_type, results_json, result_count, summary)
		 VALUES (?, ?, ?, ?)`,
		auditType, resultsJSON, count, summary,
	)
	if err != nil {
		return 0, fmt.Errorf("save audit report: %w", err)
	}
	return result.LastInsertId()
}

// ListAuditReports returns all audit reports, newest first.
func (s *Store) ListAuditReports(limit int) ([]AuditReport, error) {
	rows, err := s.db.Query(
		`SELECT id, audit_type, results_json, result_count, summary, created_at
		 FROM audit_reports ORDER BY created_at DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reports []AuditReport
	for rows.Next() {
		var r AuditReport
		var ts sql.NullString
		if err := rows.Scan(&r.ID, &r.AuditType, &r.ResultsJSON, &r.ResultCount, &r.Summary, &ts); err != nil {
			return nil, err
		}
		if ts.Valid {
			r.CreatedAt = parseSQLTime(ts.String)
		}
		reports = append(reports, r)
	}
	return reports, rows.Err()
}

// GetAuditReport returns a single audit report by ID.
func (s *Store) GetAuditReport(id int64) (*AuditReport, error) {
	var r AuditReport
	var ts sql.NullString
	err := s.db.QueryRow(
		`SELECT id, audit_type, results_json, result_count, summary, created_at
		 FROM audit_reports WHERE id = ?`, id,
	).Scan(&r.ID, &r.AuditType, &r.ResultsJSON, &r.ResultCount, &r.Summary, &ts)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if ts.Valid {
		r.CreatedAt = parseSQLTime(ts.String)
	}
	return &r, nil
}
