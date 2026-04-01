package store

import (
	"encoding/binary"
	"fmt"
	"math"
)

// UpsertEmbedding stores or replaces an embedding for a message.
func (s *Store) UpsertEmbedding(messageID int64, vector []float32, model string) error {
	blob := float32sToBlob(vector)
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO ai_embeddings (message_id, vector, dimensions, model)
		 VALUES (?, ?, ?, ?)`,
		messageID, blob, len(vector), model,
	)
	if err != nil {
		return fmt.Errorf("upsert embedding: %w", err)
	}
	return nil
}

// CountEmbeddings returns the total number of stored embeddings.
func (s *Store) CountEmbeddings() (int64, error) {
	var count int64
	err := s.db.QueryRow("SELECT COUNT(*) FROM ai_embeddings").Scan(&count)
	return count, err
}

// ListMessageIDsWithoutEmbedding returns message IDs that don't have embeddings yet.
func (s *Store) ListMessageIDsWithoutEmbedding(limit int) ([]int64, error) {
	rows, err := s.db.Query(
		`SELECT m.id FROM messages m
		 WHERE NOT EXISTS (SELECT 1 FROM ai_embeddings ae WHERE ae.message_id = m.id)
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

// SemanticSearchResult holds a message ID and its similarity score.
type SemanticSearchResult struct {
	MessageID  int64
	Similarity float64
}

// SemanticSearch finds the top-K most similar messages to the query vector.
// Uses cosine similarity computed in Go (vectors loaded from SQLite).
func (s *Store) SemanticSearch(queryVec []float32, topK int) ([]SemanticSearchResult, error) {
	rows, err := s.db.Query(
		`SELECT message_id, vector FROM ai_embeddings`,
	)
	if err != nil {
		return nil, fmt.Errorf("query embeddings: %w", err)
	}
	defer rows.Close()

	// Min-heap emulation with a simple slice (topK is small).
	type scored struct {
		id  int64
		sim float64
	}
	var results []scored

	queryNorm := vecNorm(queryVec)
	if queryNorm == 0 {
		return nil, nil
	}

	for rows.Next() {
		var msgID int64
		var blob []byte
		if err := rows.Scan(&msgID, &blob); err != nil {
			continue
		}
		vec := blobToFloat32s(blob)
		if len(vec) != len(queryVec) {
			continue
		}

		sim := cosineSimilarity(queryVec, vec, queryNorm)

		// Insert into top-K.
		if len(results) < topK {
			results = append(results, scored{msgID, sim})
			// Bubble down to maintain min at end.
			for i := len(results) - 1; i > 0 && results[i].sim > results[i-1].sim; i-- {
				results[i], results[i-1] = results[i-1], results[i]
			}
		} else if sim > results[len(results)-1].sim {
			results[len(results)-1] = scored{msgID, sim}
			for i := len(results) - 1; i > 0 && results[i].sim > results[i-1].sim; i-- {
				results[i], results[i-1] = results[i-1], results[i]
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]SemanticSearchResult, len(results))
	for i, r := range results {
		out[i] = SemanticSearchResult{MessageID: r.id, Similarity: r.sim}
	}
	return out, nil
}

// cosineSimilarity computes cos(a, b) given precomputed norm of a.
func cosineSimilarity(a, b []float32, aNorm float64) float64 {
	var dot float64
	var bNormSq float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		bNormSq += float64(b[i]) * float64(b[i])
	}
	bNorm := math.Sqrt(bNormSq)
	if bNorm == 0 {
		return 0
	}
	return dot / (aNorm * bNorm)
}

func vecNorm(v []float32) float64 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	return math.Sqrt(sum)
}

func float32sToBlob(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

func blobToFloat32s(b []byte) []float32 {
	n := len(b) / 4
	v := make([]float32, n)
	for i := range n {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}
