package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/wesm/msgvault/internal/ai"
	"github.com/wesm/msgvault/internal/config"
	"github.com/wesm/msgvault/internal/store"
)

// Sensitive data patterns for audit.
var (
	ibanRE     = regexp.MustCompile(`\b[A-Z]{2}\d{2}\s?\d{4}\s?\d{4}\s?\d{4}\s?\d{4}\s?\d{3,4}\b`)
	cardRE     = regexp.MustCompile(`\b\d{4}[\s-]?\d{4}[\s-]?\d{4}[\s-]?\d{4}\b`)
	nirRE      = regexp.MustCompile(`\b[12]\s?\d{2}\s?\d{2}\s?\d{2}\s?\d{3}\s?\d{3}\s?\d{2}\b`)
	passwordRE = regexp.MustCompile(`(?i)(?:mot de passe|password|mdp|pwd)\s*[:=]\s*\S+`)
)

// TaskStatus represents the state of a background task.
type TaskStatus struct {
	ID        string      `json:"id"`
	Type      string      `json:"type"`
	Status    string      `json:"status"` // "running", "completed", "failed"
	Progress  int         `json:"progress"`
	Total     int         `json:"total"`
	Message   string      `json:"message"`
	StartedAt string      `json:"started_at"`
	Error     string      `json:"error,omitempty"`
	Results   interface{} `json:"results,omitempty"` // Task-specific results (audit findings, etc.)
}

// AuditAnomaly represents a finding from the audit.
type AuditAnomaly struct {
	Severity string `json:"severity"` // critique, attention, info
	Category string `json:"category"`
	Message  string `json:"message"`
	Details  string `json:"details,omitempty"`
}

// SensitiveMatch represents a sensitive data finding.
type SensitiveMatch struct {
	MessageID int64  `json:"message_id"`
	Type      string `json:"type"`
	Value     string `json:"value"`
	Context   string `json:"context"`
}

// TaskManager manages background tasks.
type TaskManager struct {
	mu      sync.RWMutex
	tasks   map[string]*TaskStatus
	cancels map[string]context.CancelFunc
	cfg     *config.Config
	store   *store.Store
	log     *slog.Logger
}

// NewTaskManager creates a task manager.
func NewTaskManager(cfg *config.Config, st *store.Store, log *slog.Logger) *TaskManager {
	return &TaskManager{
		tasks:   make(map[string]*TaskStatus),
		cancels: make(map[string]context.CancelFunc),
		cfg:     cfg,
		store:   st,
		log:     log,
	}
}

func (tm *TaskManager) registerCancel(id string, cancel context.CancelFunc) {
	tm.mu.Lock()
	tm.cancels[id] = cancel
	tm.mu.Unlock()
}

func (tm *TaskManager) cancelTask(id string) bool {
	tm.mu.Lock()
	cancel, ok := tm.cancels[id]
	if ok {
		cancel()
		delete(tm.cancels, id)
	}
	tm.mu.Unlock()
	return ok
}

func (tm *TaskManager) getTask(id string) *TaskStatus {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	t := tm.tasks[id]
	if t == nil {
		return nil
	}
	cp := *t
	return &cp
}

func (tm *TaskManager) setTask(t *TaskStatus) {
	tm.mu.Lock()
	tm.tasks[t.ID] = t
	tm.mu.Unlock()
}

func (tm *TaskManager) hasRunning(taskType string) bool {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	for _, t := range tm.tasks {
		if t.Type == taskType && t.Status == "running" {
			return true
		}
	}
	return false
}

func (tm *TaskManager) allTasks() []TaskStatus {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	result := make([]TaskStatus, 0, len(tm.tasks))
	for _, t := range tm.tasks {
		result = append(result, *t)
	}
	return result
}

// RegisterRoutes adds task API endpoints to the router.
func (tm *TaskManager) RegisterRoutes(r chi.Router) {
	r.Get("/tasks", tm.handleListTasks)
	r.Get("/tasks/{id}", tm.handleGetTask)
	r.Delete("/tasks/{id}", tm.handleCancelTask)
	r.Post("/tasks/categorize", tm.handleStartCategorize)
	r.Post("/tasks/extract-entities", tm.handleStartExtractEntities)
	r.Post("/tasks/index", tm.handleStartIndex)
	r.Post("/tasks/audit", tm.handleStartAudit)
	r.Post("/tasks/audit-sensitive", tm.handleStartAuditSensitive)
	r.Post("/tasks/run-all", tm.handleRunAll)
}

func (tm *TaskManager) handleRunAll(w http.ResponseWriter, r *http.Request) {
	// Launch the auto-process pipeline in background.
	go tm.runAutoProcess()
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":  "started",
		"message": "Pipeline IA lance : categorisation → entites → indexation",
	})
}

func (tm *TaskManager) handleListTasks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, tm.allTasks())
}

func (tm *TaskManager) handleGetTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	t := tm.getTask(id)
	if t == nil {
		writeError(w, http.StatusNotFound, "not_found", "Task not found")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (tm *TaskManager) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	t := tm.getTask(id)
	if t == nil {
		writeError(w, http.StatusNotFound, "not_found", "Task not found")
		return
	}
	if t.Status != "running" {
		writeError(w, http.StatusBadRequest, "not_running", "Task is not running")
		return
	}
	if tm.cancelTask(id) {
		t.Status = "failed"
		t.Error = "Annule par l'utilisateur"
		t.Message = fmt.Sprintf("Arrete a %d/%d", t.Progress, t.Total)
		tm.setTask(t)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

func (tm *TaskManager) handleStartCategorize(w http.ResponseWriter, r *http.Request) {
	if tm.hasRunning("categorize") {
		writeError(w, http.StatusConflict, "already_running", "Categorization already running")
		return
	}

	limit := 10000
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}

	// Count real work before launching.
	ids, err := tm.store.ListUncategorizedMessageIDs(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "count_error", err.Error())
		return
	}
	if len(ids) == 0 {
		writeJSON(w, http.StatusOK, map[string]string{"status": "nothing_to_do", "message": "Tous les messages sont deja categorises"})
		return
	}

	provider, err := tm.resolveProvider()
	if err != nil {
		writeError(w, http.StatusBadRequest, "provider_error", err.Error())
		return
	}

	task := &TaskStatus{
		ID:        fmt.Sprintf("cat-%d", time.Now().Unix()),
		Type:      "categorize",
		Status:    "running",
		Total:     len(ids),
		Message:   fmt.Sprintf("0/%d", len(ids)),
		StartedAt: time.Now().Format(time.RFC3339),
	}
	tm.setTask(task)

	ctx, cancel := context.WithCancel(context.Background())
	tm.registerCancel(task.ID, cancel)
	go tm.runCategorize(ctx, task, provider, limit)

	writeJSON(w, http.StatusAccepted, task)
}

func (tm *TaskManager) handleStartExtractEntities(w http.ResponseWriter, r *http.Request) {
	if tm.hasRunning("extract-entities") {
		writeError(w, http.StatusConflict, "already_running", "Entity extraction already running")
		return
	}

	limit := 10000
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}

	ids, err := listMsgsWithoutEntities(tm.store, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "count_error", err.Error())
		return
	}
	if len(ids) == 0 {
		writeJSON(w, http.StatusOK, map[string]string{"status": "nothing_to_do", "message": "Toutes les entites sont deja extraites"})
		return
	}

	provider, err := tm.resolveProvider()
	if err != nil {
		writeError(w, http.StatusBadRequest, "provider_error", err.Error())
		return
	}

	task := &TaskStatus{
		ID:        fmt.Sprintf("ner-%d", time.Now().Unix()),
		Type:      "extract-entities",
		Status:    "running",
		Total:     len(ids),
		Message:   fmt.Sprintf("0/%d", len(ids)),
		StartedAt: time.Now().Format(time.RFC3339),
	}
	tm.setTask(task)

	ctx, cancel := context.WithCancel(context.Background())
	tm.registerCancel(task.ID, cancel)
	go tm.runExtractEntities(ctx, task, provider, limit)

	writeJSON(w, http.StatusAccepted, task)
}

func (tm *TaskManager) handleStartIndex(w http.ResponseWriter, r *http.Request) {
	if tm.hasRunning("index") {
		writeError(w, http.StatusConflict, "already_running", "Indexing already running")
		return
	}

	limit := 10000
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}

	ids, err := tm.store.ListMessageIDsWithoutEmbedding(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "count_error", err.Error())
		return
	}
	if len(ids) == 0 {
		existing, _ := tm.store.CountEmbeddings()
		writeJSON(w, http.StatusOK, map[string]string{"status": "nothing_to_do", "message": fmt.Sprintf("Tous les messages sont deja indexes (%d embeddings)", existing)})
		return
	}

	provider, err := tm.resolveProvider()
	if err != nil {
		writeError(w, http.StatusBadRequest, "provider_error", err.Error())
		return
	}

	task := &TaskStatus{
		ID:        fmt.Sprintf("idx-%d", time.Now().Unix()),
		Type:      "index",
		Status:    "running",
		Total:     len(ids),
		Message:   fmt.Sprintf("0/%d", len(ids)),
		StartedAt: time.Now().Format(time.RFC3339),
	}
	tm.setTask(task)

	ctx, cancel := context.WithCancel(context.Background())
	tm.registerCancel(task.ID, cancel)
	go tm.runIndex(ctx, task, provider, limit)

	writeJSON(w, http.StatusAccepted, task)
}

func (tm *TaskManager) handleStartAudit(w http.ResponseWriter, r *http.Request) {
	if tm.hasRunning("audit") {
		writeError(w, http.StatusConflict, "already_running", "Audit already running")
		return
	}

	task := &TaskStatus{
		ID:        fmt.Sprintf("aud-%d", time.Now().Unix()),
		Type:      "audit",
		Status:    "running",
		Message:   "Analyse...",
		StartedAt: time.Now().Format(time.RFC3339),
	}
	tm.setTask(task)

	ctx, cancel := context.WithCancel(context.Background())
	tm.registerCancel(task.ID, cancel)
	go tm.runAudit(ctx, task)

	writeJSON(w, http.StatusAccepted, task)
}

func (tm *TaskManager) handleStartAuditSensitive(w http.ResponseWriter, r *http.Request) {
	if tm.hasRunning("audit-sensitive") {
		writeError(w, http.StatusConflict, "already_running", "Sensitive audit already running")
		return
	}

	// Count messages with body text.
	var totalBodies int
	tm.store.DB().QueryRow("SELECT COUNT(*) FROM message_bodies").Scan(&totalBodies)
	if totalBodies == 0 {
		writeJSON(w, http.StatusOK, map[string]string{"status": "nothing_to_do", "message": "Aucun message a scanner"})
		return
	}

	task := &TaskStatus{
		ID:        fmt.Sprintf("sens-%d", time.Now().Unix()),
		Type:      "audit-sensitive",
		Status:    "running",
		Total:     totalBodies,
		Message:   fmt.Sprintf("0/%d", totalBodies),
		StartedAt: time.Now().Format(time.RFC3339),
	}
	tm.setTask(task)

	ctx, cancel := context.WithCancel(context.Background())
	tm.registerCancel(task.ID, cancel)
	go tm.runAuditSensitive(ctx, task, totalBodies)

	writeJSON(w, http.StatusAccepted, task)
}

// resolveProvider creates an AI provider from config.
func (tm *TaskManager) resolveProvider() (ai.AIProvider, error) {
	pref := tm.cfg.AI.DefaultProvider
	if pref == "off" || pref == "" {
		pref = "local"
	}

	switch pref {
	case "local":
		endpoint := tm.cfg.AI.Local.Endpoint
		if endpoint == "" {
			endpoint = "http://localhost:11434"
		}
		model := tm.cfg.AI.Local.Model
		if model == "" {
			model = "llama3.2"
		}
		embedModel := tm.cfg.AI.Local.EmbedModel
		if embedModel == "" {
			embedModel = model
		}
		// Try the configured model first; if it 404s, fall back to llama3.2.
		p := ai.NewLocalProvider(endpoint, model, embedModel)
		if !p.Available() {
			return nil, fmt.Errorf("Ollama non disponible sur %s", endpoint)
		}
		// Quick test: try a minimal completion to verify the model exists.
		_, testErr := p.Complete(context.Background(), ai.CompletionRequest{
			Messages:  []ai.Message{{Role: ai.RoleUser, Content: "test"}},
			MaxTokens: 1,
		})
		if testErr != nil && model != "llama3.2" {
			tm.log.Warn("model not available, falling back", "model", model, "fallback", "llama3.2", "error", testErr)
			model = "llama3.2"
			if embedModel == tm.cfg.AI.Local.Model || embedModel == "" {
				embedModel = model
			}
			p = ai.NewLocalProvider(endpoint, model, embedModel)
		}
		return p, nil

	case "cloud":
		p := ai.NewCloudProvider(
			tm.cfg.AI.Cloud.Endpoint, tm.cfg.AI.Cloud.Model,
			tm.cfg.AI.Cloud.EmbedModel, tm.cfg.AI.Cloud.APIKeyEnv,
		)
		if !p.Available() {
			return nil, fmt.Errorf("provider cloud non disponible")
		}
		return p, nil
	}

	return nil, fmt.Errorf("provider inconnu: %s", pref)
}

// Task runners (background goroutines).

func (tm *TaskManager) runCategorize(ctx context.Context, task *TaskStatus, provider ai.AIProvider, limit int) {

	ids, err := tm.store.ListUncategorizedMessageIDs(limit)
	if err != nil {
		task.Status = "failed"
		task.Error = err.Error()
		tm.setTask(task)
		return
	}

	task.Total = len(ids)
	task.Message = fmt.Sprintf("0/%d", len(ids))
	tm.setTask(task)

	prompt := `Tu es un classificateur d'emails. Classe dans UNE categorie parmi : administratif, commercial, personnel, newsletter, facture, litige, notification, spam, professionnel. Reponds UNIQUEMENT avec un JSON : {"category":"...","confidence":0.0-1.0} /no_think`

	var errors int
	for i, msgID := range ids {
		if ctx.Err() != nil {
			task.Status = "failed"
			task.Error = "Annule"
			task.Message = fmt.Sprintf("Arrete a %d/%d", i, len(ids))
			tm.setTask(task)
			return
		}

		subject, snippet, fromEmail, err := tm.store.GetMessageSnippetAndSubject(msgID)
		if err != nil {
			errors++
			continue
		}

		resp, err := provider.Complete(ctx, ai.CompletionRequest{
			Messages: []ai.Message{
				{Role: ai.RoleSystem, Content: prompt},
				{Role: ai.RoleUser, Content: fmt.Sprintf("De: %s\nSujet: %s\nExtrait: %s", fromEmail, subject, snippet)},
			},
			Temperature: 0.1,
			MaxTokens:   100,
		})
		if err != nil {
			errors++
			if errors <= 3 {
				tm.log.Warn("AI categorize error", "id", msgID, "error", err)
			}
			// If too many consecutive errors, likely a model/connection issue. Abort.
			if errors > 10 && task.Progress == 0 {
				task.Status = "failed"
				task.Error = fmt.Sprintf("Trop d'erreurs (%d). Verifiez Ollama et le modele. Derniere erreur : %s", errors, err)
				task.Message = task.Error
				tm.setTask(task)
				return
			}
			continue
		}

		cat, conf := parseCategoryJSON(resp.Content)
		if cat != "" {
			tm.store.UpsertAICategory(&store.AICategory{
				MessageID:  msgID,
				Category:   cat,
				Confidence: conf,
				Provider:   provider.Name(),
				Model:      resp.Model,
			})
		}

		task.Progress = i + 1
		task.Message = fmt.Sprintf("%d/%d categorises", i+1, len(ids))
		tm.setTask(task)
	}

	task.Status = "completed"
	task.Message = fmt.Sprintf("Termine : %d categorises, %d erreurs", task.Progress, errors)
	tm.setTask(task)
}

func parseCategoryJSON(content string) (string, float64) {
	content = cleanThinkContent(content)
	var result struct {
		Category   string  `json:"category"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(content), &result); err == nil && result.Category != "" {
		return result.Category, result.Confidence
	}
	return "", 0
}

func cleanThinkContent(s string) string {
	if idx := findIndex(s, "</think>"); idx >= 0 {
		s = s[idx+8:]
	}
	s = trimSpace(s)
	s = trimPrefix(s, "```json")
	s = trimPrefix(s, "```")
	s = trimSuffix(s, "```")
	return trimSpace(s)
}

func findIndex(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\n' || s[start] == '\r' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\n' || s[end-1] == '\r' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

func trimPrefix(s, prefix string) string {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):]
	}
	return s
}

func trimSuffix(s, suffix string) string {
	if len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix {
		return s[:len(s)-len(suffix)]
	}
	return s
}

func (tm *TaskManager) runExtractEntities(ctx context.Context, task *TaskStatus, provider ai.AIProvider, limit int) {

	ids, err := listMsgsWithoutEntities(tm.store, limit)
	if err != nil {
		task.Status = "failed"
		task.Error = err.Error()
		tm.setTask(task)
		return
	}

	task.Total = len(ids)
	tm.setTask(task)

	prompt := `Extrais les entites nommees : montant, iban, date, telephone, personne, entreprise, contrat, adresse. Reponds UNIQUEMENT avec un JSON : {"entities":[{"type":"...","value":"..."}]} /no_think`

	for i, msgID := range ids {
		if ctx.Err() != nil {
			task.Status = "failed"
			task.Error = "Annule"
			task.Message = fmt.Sprintf("Arrete a %d/%d", i, len(ids))
			tm.setTask(task)
			return
		}

		subject, snippet, fromEmail, err := tm.store.GetMessageSnippetAndSubject(msgID)
		if err != nil {
			continue
		}

		resp, err := provider.Complete(ctx, ai.CompletionRequest{
			Messages: []ai.Message{
				{Role: ai.RoleSystem, Content: prompt},
				{Role: ai.RoleUser, Content: fmt.Sprintf("De: %s\nSujet: %s\nContenu: %s", fromEmail, subject, snippet)},
			},
			Temperature: 0.1,
			MaxTokens:   500,
		})
		if err != nil {
			continue
		}

		entities := parseEntitiesJSON(resp.Content)
		for _, e := range entities {
			tm.store.InsertAIEntity(&store.AIEntity{
				MessageID:  msgID,
				EntityType: e.Type,
				Value:      e.Value,
				Confidence: 0.8,
				Provider:   provider.Name(),
				Model:      resp.Model,
			})
		}

		task.Progress = i + 1
		task.Message = fmt.Sprintf("%d/%d", i+1, len(ids))
		tm.setTask(task)
	}

	task.Status = "completed"
	task.Message = fmt.Sprintf("Termine : %d messages", task.Progress)
	tm.setTask(task)
}

type entityResult struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

func parseEntitiesJSON(content string) []entityResult {
	content = cleanThinkContent(content)
	var result struct {
		Entities []entityResult `json:"entities"`
	}
	json.Unmarshal([]byte(content), &result)
	return result.Entities
}

func listMsgsWithoutEntities(st *store.Store, limit int) ([]int64, error) {
	rows, err := st.DB().Query(
		`SELECT m.id FROM messages m WHERE NOT EXISTS (SELECT 1 FROM ai_entities ae WHERE ae.message_id = m.id) ORDER BY m.id LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids, nil
}

func (tm *TaskManager) runIndex(ctx context.Context, task *TaskStatus, provider ai.AIProvider, limit int) {

	ids, err := tm.store.ListMessageIDsWithoutEmbedding(limit)
	if err != nil {
		task.Status = "failed"
		task.Error = err.Error()
		tm.setTask(task)
		return
	}

	task.Total = len(ids)
	tm.setTask(task)

	for i := 0; i < len(ids); i += 10 {
		if ctx.Err() != nil {
			task.Status = "failed"
			task.Error = "Annule"
			task.Message = fmt.Sprintf("Arrete a %d/%d", i, len(ids))
			tm.setTask(task)
			return
		}

		end := i + 10
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[i:end]

		texts := make([]string, 0, len(batch))
		validIDs := make([]int64, 0, len(batch))
		for _, msgID := range batch {
			subject, snippet, fromEmail, err := tm.store.GetMessageSnippetAndSubject(msgID)
			if err != nil {
				continue
			}
			texts = append(texts, fmt.Sprintf("De: %s | Sujet: %s | %s", fromEmail, subject, snippet))
			validIDs = append(validIDs, msgID)
		}

		if len(texts) == 0 {
			continue
		}

		embeddings, err := provider.Embed(ctx, texts)
		if err != nil {
			continue
		}

		for j, vec := range embeddings {
			if j < len(validIDs) {
				tm.store.UpsertEmbedding(validIDs[j], vec, "web")
			}
		}

		task.Progress = end
		task.Message = fmt.Sprintf("%d/%d", end, len(ids))
		tm.setTask(task)
	}

	task.Status = "completed"
	task.Message = fmt.Sprintf("Termine : %d embeddings", task.Progress)
	tm.setTask(task)
}

func (tm *TaskManager) runAudit(ctx context.Context, task *TaskStatus) {
	var anomalies []AuditAnomaly

	// High volume senders.
	hvRows, err := tm.store.DB().Query(
		`SELECT p.email_address, COUNT(*) as cnt FROM messages m
		 JOIN message_recipients mr ON mr.message_id = m.id AND mr.recipient_type = 'from'
		 JOIN participants p ON p.id = mr.participant_id WHERE p.email_address != ''
		 GROUP BY p.email_address HAVING cnt > 200 ORDER BY cnt DESC LIMIT 20`,
	)
	if err == nil {
		defer hvRows.Close()
		for hvRows.Next() {
			var email string
			var count int
			hvRows.Scan(&email, &count)
			anomalies = append(anomalies, AuditAnomaly{
				Severity: "info",
				Category: "Volume eleve",
				Message:  fmt.Sprintf("%s : %d messages", email, count),
			})
		}
	}

	// Duplicate subjects.
	dupeRows, err := tm.store.DB().Query(
		`SELECT p.email_address, m.subject, COUNT(*) as cnt FROM messages m
		 JOIN message_recipients mr ON mr.message_id = m.id AND mr.recipient_type = 'from'
		 JOIN participants p ON p.id = mr.participant_id
		 WHERE m.subject IS NOT NULL AND m.subject != '' AND p.email_address != ''
		 GROUP BY p.email_address, m.subject HAVING cnt >= 3 ORDER BY cnt DESC LIMIT 30`,
	)
	if err == nil {
		defer dupeRows.Close()
		for dupeRows.Next() {
			var email, subject string
			var count int
			dupeRows.Scan(&email, &subject, &count)
			sev := "attention"
			if count >= 5 {
				sev = "critique"
			}
			subj := subject
			if len(subj) > 50 {
				subj = subj[:50] + "..."
			}
			anomalies = append(anomalies, AuditAnomaly{
				Severity: sev,
				Category: "Sujet duplique",
				Message:  fmt.Sprintf("%s x%d de %s", subj, count, email),
			})
		}
	}

	task.Status = "completed"
	task.Progress = len(anomalies)
	task.Total = len(anomalies)
	task.Results = anomalies
	task.Message = fmt.Sprintf("%d anomalies detectees", len(anomalies))
	tm.setTask(task)
}

func (tm *TaskManager) runAuditSensitive(ctx context.Context, task *TaskStatus, limit int) {
	rows, err := tm.store.DB().Query(
		`SELECT mb.message_id, COALESCE(mb.body_text,'') FROM message_bodies mb ORDER BY mb.message_id LIMIT ?`, limit,
	)
	if err != nil {
		task.Status = "failed"
		task.Error = err.Error()
		tm.setTask(task)
		return
	}
	defer rows.Close()

	var scanned, detected int
	var findings []SensitiveMatch
	for rows.Next() {
		if ctx.Err() != nil {
			task.Status = "failed"
			task.Error = "Annule"
			task.Message = fmt.Sprintf("Arrete a %d/%d (%d detectes)", scanned, task.Total, detected)
			tm.setTask(task)
			return
		}
		var msgID int64
		var body string
		rows.Scan(&msgID, &body)
		scanned++

		// Check each pattern and record matches.
		for _, check := range []struct {
			re   *regexp.Regexp
			name string
		}{
			{ibanRE, "IBAN"},
			{cardRE, "Carte bancaire"},
			{nirRE, "NIR"},
			{passwordRE, "Mot de passe"},
		} {
			matches := check.re.FindAllString(body, 3)
			for _, val := range matches {
				detected++
				if len(findings) < 500 { // Cap results to avoid memory issues.
					v := val
					if check.name == "IBAN" || check.name == "Carte bancaire" || check.name == "NIR" {
						v = maskSensitiveValue(v)
					}
					findings = append(findings, SensitiveMatch{
						MessageID: msgID,
						Type:      check.name,
						Value:     v,
					})
				}
			}
		}

		if scanned%500 == 0 {
			task.Progress = scanned
			task.Message = fmt.Sprintf("%d scannes, %d detectes", scanned, detected)
			tm.setTask(task)
		}
	}

	task.Status = "completed"
	task.Progress = scanned
	task.Total = scanned
	task.Results = findings
	task.Message = fmt.Sprintf("%d donnees sensibles dans %d messages", detected, scanned)
	tm.setTask(task)
}

func maskSensitiveValue(val string) string {
	clean := strings.ReplaceAll(val, " ", "")
	if len(clean) <= 6 {
		return val
	}
	return clean[:4] + strings.Repeat("*", len(clean)-6) + clean[len(clean)-2:]
}

// runAutoProcess chains AI tasks after a sync: categorize → extract entities → index.
// Each step only runs if there are new messages to process. Skips steps where
// the provider is unavailable.
func (tm *TaskManager) runAutoProcess() {
	provider, err := tm.resolveProvider()
	if err != nil {
		tm.log.Info("auto-process: IA non disponible, etape IA ignoree", "error", err)
		return
	}

	// Step 1: Categorize new messages.
	uncatIDs, _ := tm.store.ListUncategorizedMessageIDs(10000)
	if len(uncatIDs) > 0 {
		tm.log.Info("auto-process: categorisation", "messages", len(uncatIDs))
		task := &TaskStatus{
			ID:        fmt.Sprintf("auto-cat-%d", time.Now().Unix()),
			Type:      "categorize",
			Status:    "running",
			Total:     len(uncatIDs),
			Message:   fmt.Sprintf("Auto: 0/%d", len(uncatIDs)),
			StartedAt: time.Now().Format(time.RFC3339),
		}
		tm.setTask(task)
		ctx, cancel := context.WithCancel(context.Background())
		tm.registerCancel(task.ID, cancel)
		tm.runCategorize(ctx, task, provider, 10000) // Blocking — wait for completion.
	}

	// Step 2: Extract entities from new messages.
	nerIDs, _ := listMsgsWithoutEntities(tm.store, 10000)
	if len(nerIDs) > 0 {
		tm.log.Info("auto-process: extraction entites", "messages", len(nerIDs))
		task := &TaskStatus{
			ID:        fmt.Sprintf("auto-ner-%d", time.Now().Unix()),
			Type:      "extract-entities",
			Status:    "running",
			Total:     len(nerIDs),
			Message:   fmt.Sprintf("Auto: 0/%d", len(nerIDs)),
			StartedAt: time.Now().Format(time.RFC3339),
		}
		tm.setTask(task)
		ctx, cancel := context.WithCancel(context.Background())
		tm.registerCancel(task.ID, cancel)
		tm.runExtractEntities(ctx, task, provider, 10000)
	}

	// Step 3: Index new messages (embeddings).
	idxIDs, _ := tm.store.ListMessageIDsWithoutEmbedding(10000)
	if len(idxIDs) > 0 {
		tm.log.Info("auto-process: indexation embeddings", "messages", len(idxIDs))
		task := &TaskStatus{
			ID:        fmt.Sprintf("auto-idx-%d", time.Now().Unix()),
			Type:      "index",
			Status:    "running",
			Total:     len(idxIDs),
			Message:   fmt.Sprintf("Auto: 0/%d", len(idxIDs)),
			StartedAt: time.Now().Format(time.RFC3339),
		}
		tm.setTask(task)
		ctx, cancel := context.WithCancel(context.Background())
		tm.registerCancel(task.ID, cancel)
		tm.runIndex(ctx, task, provider, 10000)
	}

	tm.log.Info("auto-process: termine")
}
