package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// SettingsResponse represents the editable settings exposed to the web UI.
type SettingsResponse struct {
	AI       AISettingsResponse        `json:"ai"`
	Accounts []AccountSettingsResponse `json:"accounts"`
}

type AISettingsResponse struct {
	DefaultProvider string `json:"default_provider"`
	LocalEndpoint   string `json:"local_endpoint"`
	LocalModel      string `json:"local_model"`
	LocalEmbedModel string `json:"local_embed_model"`
	CloudEndpoint   string `json:"cloud_endpoint"`
	CloudModel      string `json:"cloud_model"`
	CloudAPIKeyEnv  string `json:"cloud_api_key_env"`
	CloudAPIKeySet  bool   `json:"cloud_api_key_set"` // true if env var is non-empty
}

type AccountSettingsResponse struct {
	Email    string `json:"email"`
	Schedule string `json:"schedule"`
	Enabled  bool   `json:"enabled"`
}

// AISettingsUpdate represents a partial update to AI settings.
type AISettingsUpdate struct {
	DefaultProvider *string `json:"default_provider,omitempty"`
	LocalModel      *string `json:"local_model,omitempty"`
	LocalEmbedModel *string `json:"local_embed_model,omitempty"`
	LocalEndpoint   *string `json:"local_endpoint,omitempty"`
}

// OllamaModel represents a model available on Ollama.
type OllamaModel struct {
	Name          string `json:"name"`
	Size          int64  `json:"size"`
	ParameterSize string `json:"parameter_size"`
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()

	resp := SettingsResponse{
		AI: AISettingsResponse{
			DefaultProvider: s.cfg.AI.DefaultProvider,
			LocalEndpoint:   s.cfg.AI.Local.Endpoint,
			LocalModel:      s.cfg.AI.Local.Model,
			LocalEmbedModel: s.cfg.AI.Local.EmbedModel,
			CloudEndpoint:   s.cfg.AI.Cloud.Endpoint,
			CloudModel:      s.cfg.AI.Cloud.Model,
			CloudAPIKeyEnv:  s.cfg.AI.Cloud.APIKeyEnv,
		},
	}

	// Check if cloud API key env var is set.
	if s.cfg.AI.Cloud.APIKeyEnv != "" {
		resp.AI.CloudAPIKeySet = os.Getenv(s.cfg.AI.Cloud.APIKeyEnv) != ""
	}

	// Load accounts from config.
	for _, acc := range s.cfg.Accounts {
		resp.Accounts = append(resp.Accounts, AccountSettingsResponse{
			Email:    acc.Email,
			Schedule: acc.Schedule,
			Enabled:  acc.Enabled,
		})
	}

	// Also load accounts from database (sources table) if not already listed.
	if s.engine != nil {
		dbAccounts, err := s.engine.ListAccounts(r.Context())
		if err == nil {
			existing := make(map[string]bool)
			for _, a := range resp.Accounts {
				existing[a.Email] = true
			}
			for _, a := range dbAccounts {
				if !existing[a.Identifier] {
					resp.Accounts = append(resp.Accounts, AccountSettingsResponse{
						Email: a.Identifier,
					})
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var update AISettingsUpdate
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read_error", "Failed to read request body")
		return
	}
	if err := json.Unmarshal(body, &update); err != nil {
		writeError(w, http.StatusBadRequest, "parse_error", "Invalid JSON")
		return
	}

	s.cfgMu.Lock()
	if update.DefaultProvider != nil {
		s.cfg.AI.DefaultProvider = *update.DefaultProvider
	}
	if update.LocalModel != nil {
		s.cfg.AI.Local.Model = *update.LocalModel
	}
	if update.LocalEmbedModel != nil {
		s.cfg.AI.Local.EmbedModel = *update.LocalEmbedModel
	}
	if update.LocalEndpoint != nil {
		s.cfg.AI.Local.Endpoint = *update.LocalEndpoint
	}
	s.cfgMu.Unlock()

	// Persist to disk.
	if err := s.cfg.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, "save_error", "Failed to save config: "+err.Error())
		return
	}

	// Reload task manager provider on next task.
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (s *Server) handleListOllamaModels(w http.ResponseWriter, r *http.Request) {
	endpoint := s.cfg.AI.Local.Endpoint
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(endpoint + "/api/tags")
	if err != nil {
		writeJSON(w, http.StatusOK, []OllamaModel{}) // Ollama not running, return empty.
		return
	}
	defer resp.Body.Close()

	var result struct {
		Models []struct {
			Name    string `json:"name"`
			Size    int64  `json:"size"`
			Details struct {
				ParameterSize string `json:"parameter_size"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		writeJSON(w, http.StatusOK, []OllamaModel{})
		return
	}

	models := make([]OllamaModel, len(result.Models))
	for i, m := range result.Models {
		models[i] = OllamaModel{
			Name:          m.Name,
			Size:          m.Size,
			ParameterSize: m.Details.ParameterSize,
		}
	}
	writeJSON(w, http.StatusOK, models)
}

func (s *Server) handleTriggerSyncWeb(w http.ResponseWriter, r *http.Request) {
	if s.taskManager == nil {
		writeError(w, http.StatusServiceUnavailable, "no_task_manager", "Task manager not available")
		return
	}

	var req struct {
		Account string `json:"account"`
	}
	body, _ := io.ReadAll(r.Body)
	json.Unmarshal(body, &req)

	if req.Account == "" {
		writeError(w, http.StatusBadRequest, "missing_account", "Account email required")
		return
	}

	// Use the existing scheduler sync trigger if available.
	if s.scheduler != nil {
		// If the account is not scheduled, add it first.
		if !s.scheduler.IsScheduled(req.Account) {
			if err := s.scheduler.AddAccount(req.Account, ""); err != nil {
				// Not fatal — try TriggerSync anyway.
				_ = err
			}
		}

		if err := s.scheduler.TriggerSync(req.Account); err != nil {
			writeError(w, http.StatusInternalServerError, "sync_error",
				fmt.Sprintf("Erreur sync : %s. Assurez-vous que le compte est autorise (msgvault add-account %s)", err.Error(), req.Account))
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{
			"status":  "started",
			"account": req.Account,
			"message": fmt.Sprintf("Sync lancee pour %s", req.Account),
		})
		return
	}

	writeError(w, http.StatusServiceUnavailable, "no_scheduler", "Scheduler non disponible")
}
