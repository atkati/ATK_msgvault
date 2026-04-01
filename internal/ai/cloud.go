package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"strings"
	"time"
)

// CloudProvider implémente AIProvider pour les API cloud (Anthropic, OpenAI-style).
type CloudProvider struct {
	endpoint       string
	model          string
	embedModel     string
	apiKeyEnv      string
	client         *http.Client
	forceAnthropic bool // pour les tests uniquement
}

// NewCloudProvider crée un provider cloud.
// apiKeyEnv est le nom de la variable d'environnement contenant la clé API.
func NewCloudProvider(endpoint, model, embedModel, apiKeyEnv string) *CloudProvider {
	return &CloudProvider{
		endpoint:   strings.TrimRight(endpoint, "/"),
		model:      model,
		embedModel: embedModel,
		apiKeyEnv:  apiKeyEnv,
		client:     &http.Client{Timeout: 120 * time.Second},
	}
}

func (p *CloudProvider) Name() string {
	if p.isAnthropic() {
		return "anthropic"
	}
	return "openai"
}

// Available retourne true si la clé API est configurée dans l'environnement.
func (p *CloudProvider) Available() bool {
	return os.Getenv(p.apiKeyEnv) != ""
}

func (p *CloudProvider) isAnthropic() bool {
	return p.forceAnthropic || strings.Contains(p.endpoint, "anthropic")
}

func (p *CloudProvider) apiKey() string {
	return os.Getenv(p.apiKeyEnv)
}

// Complete envoie une requête de complétion au provider cloud.
func (p *CloudProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}

	if p.isAnthropic() {
		return p.completeAnthropic(ctx, model, req)
	}
	return p.completeOpenAI(ctx, model, req)
}

func (p *CloudProvider) completeAnthropic(ctx context.Context, model string, req CompletionRequest) (CompletionResponse, error) {
	// Séparer le message system des messages utilisateur/assistant.
	var systemText string
	var messages []anthropicMsg
	for _, m := range req.Messages {
		if m.Role == RoleSystem {
			systemText = m.Content
			continue
		}
		messages = append(messages, anthropicMsg{
			Role:    string(m.Role),
			Content: m.Content,
		})
	}

	body := anthropicRequest{
		Model:     model,
		Messages:  messages,
		MaxTokens: req.MaxTokens,
	}
	if systemText != "" {
		body.System = systemText
	}
	if req.Temperature > 0 {
		body.Temperature = &req.Temperature
	}
	if body.MaxTokens == 0 {
		body.MaxTokens = 1024
	}

	respBody, err := p.doWithRetry(ctx, "/v1/messages", body, true)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("anthropic: complétion: %w", err)
	}
	defer respBody.Close()

	var result anthropicResponse
	if err := json.NewDecoder(respBody).Decode(&result); err != nil {
		return CompletionResponse{}, fmt.Errorf("anthropic: décodage réponse: %w", err)
	}

	var content string
	for _, block := range result.Content {
		if block.Type == "text" {
			content += block.Text
		}
	}

	return CompletionResponse{
		Content: content,
		Model:   result.Model,
		Usage: Usage{
			PromptTokens:     result.Usage.InputTokens,
			CompletionTokens: result.Usage.OutputTokens,
		},
	}, nil
}

func (p *CloudProvider) completeOpenAI(ctx context.Context, model string, req CompletionRequest) (CompletionResponse, error) {
	msgs := make([]openaiMsg, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = openaiMsg{Role: string(m.Role), Content: m.Content}
	}

	body := openaiChatRequest{
		Model:    model,
		Messages: msgs,
	}
	if req.Temperature > 0 {
		body.Temperature = &req.Temperature
	}
	if req.MaxTokens > 0 {
		body.MaxTokens = &req.MaxTokens
	}

	respBody, err := p.doWithRetry(ctx, "/v1/chat/completions", body, false)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("openai: complétion: %w", err)
	}
	defer respBody.Close()

	var result openaiChatResponse
	if err := json.NewDecoder(respBody).Decode(&result); err != nil {
		return CompletionResponse{}, fmt.Errorf("openai: décodage réponse: %w", err)
	}

	var content string
	if len(result.Choices) > 0 {
		content = result.Choices[0].Message.Content
	}

	return CompletionResponse{
		Content: content,
		Model:   result.Model,
		Usage: Usage{
			PromptTokens:     result.Usage.PromptTokens,
			CompletionTokens: result.Usage.CompletionTokens,
		},
	}, nil
}

// Embed génère des embeddings via l'API cloud.
func (p *CloudProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if p.isAnthropic() {
		return nil, fmt.Errorf("%w: anthropic ne supporte pas les embeddings natifs", ErrNotSupported)
	}

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

	respBody, err := p.doWithRetry(ctx, "/v1/embeddings", body, false)
	if err != nil {
		return nil, fmt.Errorf("openai: embeddings: %w", err)
	}
	defer respBody.Close()

	var result struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(respBody).Decode(&result); err != nil {
		return nil, fmt.Errorf("openai: décodage embeddings: %w", err)
	}

	embeddings := make([][]float32, len(result.Data))
	for i, d := range result.Data {
		embeddings[i] = d.Embedding
	}
	return embeddings, nil
}

// doWithRetry exécute une requête HTTP POST avec retry sur erreurs transitoires.
func (p *CloudProvider) doWithRetry(ctx context.Context, path string, payload any, isAnthropic bool) (io.ReadCloser, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encodage requête: %w", err)
	}

	const maxRetries = 3
	for attempt := range maxRetries {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint+path, bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")

		key := p.apiKey()
		if isAnthropic {
			req.Header.Set("x-api-key", key)
			req.Header.Set("anthropic-version", "2023-06-01")
		} else {
			req.Header.Set("Authorization", "Bearer "+key)
		}

		resp, err := p.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if attempt < maxRetries-1 {
				p.backoff(ctx, attempt)
				continue
			}
			return nil, err
		}

		if resp.StatusCode == http.StatusOK {
			return resp.Body, nil
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		// Retry uniquement sur 429 (rate limit) et 5xx (erreurs serveur).
		if (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) && attempt < maxRetries-1 {
			p.backoff(ctx, attempt)
			continue
		}

		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	return nil, fmt.Errorf("nombre maximal de tentatives atteint")
}

// backoff attend avec un délai exponentiel + jitter.
func (p *CloudProvider) backoff(ctx context.Context, attempt int) {
	base := time.Duration(math.Pow(2, float64(attempt))) * time.Second
	jitter := time.Duration(rand.Int64N(int64(base / 2)))
	delay := base + jitter

	select {
	case <-time.After(delay):
	case <-ctx.Done():
	}
}

// Types pour l'API Anthropic.

type anthropicMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicRequest struct {
	Model       string         `json:"model"`
	Messages    []anthropicMsg `json:"messages"`
	System      string         `json:"system,omitempty"`
	MaxTokens   int            `json:"max_tokens"`
	Temperature *float64       `json:"temperature,omitempty"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Model string `json:"model"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Types pour l'API OpenAI-compatible.

type openaiMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openaiChatRequest struct {
	Model       string      `json:"model"`
	Messages    []openaiMsg `json:"messages"`
	Temperature *float64    `json:"temperature,omitempty"`
	MaxTokens   *int        `json:"max_tokens,omitempty"`
}

type openaiChatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}
