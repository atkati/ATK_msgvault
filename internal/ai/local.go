package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// LocalProvider implémente AIProvider via l'API REST Ollama.
type LocalProvider struct {
	endpoint   string
	model      string
	embedModel string
	client     *http.Client

	mu            sync.Mutex
	availCache    bool
	availCacheAt  time.Time
	availCacheTTL time.Duration
}

// NewLocalProvider crée un provider Ollama connecté à l'endpoint donné.
func NewLocalProvider(endpoint, model, embedModel string) *LocalProvider {
	return &LocalProvider{
		endpoint:      endpoint,
		model:         model,
		embedModel:    embedModel,
		client:        &http.Client{Timeout: 120 * time.Second},
		availCacheTTL: 30 * time.Second,
	}
}

func (p *LocalProvider) Name() string { return "ollama" }

// Available vérifie si Ollama est joignable (résultat caché 30s).
func (p *LocalProvider) Available() bool {
	p.mu.Lock()
	if time.Since(p.availCacheAt) < p.availCacheTTL {
		cached := p.availCache
		p.mu.Unlock()
		return cached
	}
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint+"/api/tags", nil)
	if err != nil {
		p.setAvailCache(false)
		return false
	}

	resp, err := p.client.Do(req)
	if err != nil {
		p.setAvailCache(false)
		return false
	}
	resp.Body.Close()

	avail := resp.StatusCode == http.StatusOK
	p.setAvailCache(avail)
	return avail
}

func (p *LocalProvider) setAvailCache(v bool) {
	p.mu.Lock()
	p.availCache = v
	p.availCacheAt = time.Now()
	p.mu.Unlock()
}

// Complete envoie une requête de complétion à Ollama /api/chat.
func (p *LocalProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}

	type ollamaMsg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	msgs := make([]ollamaMsg, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = ollamaMsg{Role: string(m.Role), Content: m.Content}
	}

	body := struct {
		Model    string      `json:"model"`
		Messages []ollamaMsg `json:"messages"`
		Stream   bool        `json:"stream"`
		Options  *ollamaOpts `json:"options,omitempty"`
	}{
		Model:    model,
		Messages: msgs,
		Stream:   false,
	}
	if req.Temperature > 0 || req.MaxTokens > 0 {
		opts := &ollamaOpts{}
		if req.Temperature > 0 {
			opts.Temperature = &req.Temperature
		}
		if req.MaxTokens > 0 {
			opts.NumPredict = &req.MaxTokens
		}
		body.Options = opts
	}

	respBody, err := p.doJSON(ctx, "/api/chat", body)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("ollama: complétion: %w", err)
	}
	defer respBody.Close()

	var result ollamaChatResponse
	if err := json.NewDecoder(respBody).Decode(&result); err != nil {
		return CompletionResponse{}, fmt.Errorf("ollama: décodage réponse: %w", err)
	}

	return CompletionResponse{
		Content: result.Message.Content,
		Model:   result.Model,
		Usage: Usage{
			PromptTokens:     result.PromptEvalCount,
			CompletionTokens: result.EvalCount,
		},
	}, nil
}

// Embed génère des embeddings via Ollama /api/embed.
func (p *LocalProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	model := p.embedModel
	if model == "" {
		model = p.model
	}

	body := struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}{
		Model: model,
		Input: texts,
	}

	respBody, err := p.doJSON(ctx, "/api/embed", body)
	if err != nil {
		return nil, fmt.Errorf("ollama: embeddings: %w", err)
	}
	defer respBody.Close()

	var result struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(respBody).Decode(&result); err != nil {
		return nil, fmt.Errorf("ollama: décodage embeddings: %w", err)
	}

	return result.Embeddings, nil
}

func (p *LocalProvider) doJSON(ctx context.Context, path string, payload any) (io.ReadCloser, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encodage requête: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	return resp.Body, nil
}

type ollamaOpts struct {
	Temperature *float64 `json:"temperature,omitempty"`
	NumPredict  *int     `json:"num_predict,omitempty"`
}

type ollamaChatResponse struct {
	Model   string `json:"model"`
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	PromptEvalCount int `json:"prompt_eval_count"`
	EvalCount       int `json:"eval_count"`
}
