package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
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
	ID        string `json:"id"`
	Type      string `json:"type"`
	Status    string `json:"status"` // "running", "completed", "failed"
	Progress  int    `json:"progress"`
	Total     int    `json:"total"`
	Message   string `json:"message"`
	StartedAt string `json:"started_at"`
	Error     string `json:"error,omitempty"`
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

	limit := 500
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
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
		Total:     limit,
		Message:   "Demarrage...",
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

	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
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
		Total:     limit,
		Message:   "Demarrage...",
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

	limit := 1000
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
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
		Total:     limit,
		Message:   "Demarrage...",
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

	go tm.runAudit(task)

	writeJSON(w, http.StatusAccepted, task)
}

func (tm *TaskManager) handleStartAuditSensitive(w http.ResponseWriter, r *http.Request) {
	if tm.hasRunning("audit-sensitive") {
		writeError(w, http.StatusConflict, "already_running", "Sensitive audit already running")
		return
	}

	limit := 5000
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}

	task := &TaskStatus{
		ID:        fmt.Sprintf("sens-%d", time.Now().Unix()),
		Type:      "audit-sensitive",
		Status:    "running",
		Total:     limit,
		Message:   "Scan...",
		StartedAt: time.Now().Format(time.RFC3339),
	}
	tm.setTask(task)

	go tm.runAuditSensitive(task, limit)

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

func (tm *TaskManager) runAudit(task *TaskStatus) {
	var anomalyCount int

	// High volume senders.
	rows, err := tm.store.DB().Query(
		`SELECT COUNT(*) FROM (SELECT p.email_address, COUNT(*) as cnt FROM messages m
		 JOIN message_recipients mr ON mr.message_id = m.id AND mr.recipient_type = 'from'
		 JOIN participants p ON p.id = mr.participant_id WHERE p.email_address != ''
		 GROUP BY p.email_address HAVING cnt > 200)`,
	)
	if err == nil {
		defer rows.Close()
		if rows.Next() {
			rows.Scan(&anomalyCount)
		}
	}

	// Duplicate subjects.
	var dupeCount int
	tm.store.DB().QueryRow(
		`SELECT COUNT(*) FROM (SELECT COUNT(*) as cnt FROM messages m
		 JOIN message_recipients mr ON mr.message_id = m.id AND mr.recipient_type = 'from'
		 JOIN participants p ON p.id = mr.participant_id
		 WHERE m.subject IS NOT NULL AND m.subject != '' AND p.email_address != ''
		 GROUP BY p.email_address, m.subject HAVING cnt >= 3)`,
	).Scan(&dupeCount)
	anomalyCount += dupeCount

	task.Status = "completed"
	task.Progress = anomalyCount
	task.Total = anomalyCount
	task.Message = fmt.Sprintf("%d anomalies detectees", anomalyCount)
	tm.setTask(task)
}

func (tm *TaskManager) runAuditSensitive(task *TaskStatus, limit int) {
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
	for rows.Next() {
		var msgID int64
		var body string
		rows.Scan(&msgID, &body)
		scanned++
		// Reuse the same regex patterns from audit_sensitive.go.
		if ibanRE.MatchString(body) || cardRE.MatchString(body) || nirRE.MatchString(body) || passwordRE.MatchString(body) {
			detected++
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
	task.Message = fmt.Sprintf("%d donnees sensibles dans %d messages", detected, scanned)
	tm.setTask(task)
}
