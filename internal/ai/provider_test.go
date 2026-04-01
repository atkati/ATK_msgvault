package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockProvider implémente AIProvider pour les tests.
type mockProvider struct {
	name      string
	available bool
	response  CompletionResponse
	embedResp [][]float32
	err       error
}

func (m *mockProvider) Name() string    { return m.name }
func (m *mockProvider) Available() bool { return m.available }
func (m *mockProvider) Complete(_ context.Context, _ CompletionRequest) (CompletionResponse, error) {
	return m.response, m.err
}
func (m *mockProvider) Embed(_ context.Context, _ []string) ([][]float32, error) {
	return m.embedResp, m.err
}

// newCloudProviderForTest crée un CloudProvider avec contrôle du mode Anthropic.
func newCloudProviderForTest(endpoint, model, embedModel, apiKeyEnv string, forceAnthropic bool) *CloudProvider {
	p := NewCloudProvider(endpoint, model, embedModel, apiKeyEnv)
	p.forceAnthropic = forceAnthropic
	return p
}

func TestRouter_ForFeature(t *testing.T) {
	local := &mockProvider{name: "local", available: true}
	cloud := &mockProvider{name: "cloud", available: true}

	tests := []struct {
		name            string
		routing         map[string]string
		defaultProvider string
		feature         string
		localAvail      bool
		cloudAvail      bool
		wantProvider    string // "" = nil
	}{
		{
			name:            "feature routée vers local, local disponible",
			routing:         map[string]string{"categorize": "local"},
			defaultProvider: "off",
			feature:         "categorize",
			localAvail:      true,
			cloudAvail:      true,
			wantProvider:    "local",
		},
		{
			name:            "feature routée vers local, local indisponible, fallback cloud",
			routing:         map[string]string{"categorize": "local"},
			defaultProvider: "off",
			feature:         "categorize",
			localAvail:      false,
			cloudAvail:      true,
			wantProvider:    "cloud",
		},
		{
			name:            "feature routée vers cloud",
			routing:         map[string]string{"summarize": "cloud"},
			defaultProvider: "local",
			feature:         "summarize",
			localAvail:      true,
			cloudAvail:      true,
			wantProvider:    "cloud",
		},
		{
			name:            "feature non mappée, utilise default",
			routing:         map[string]string{},
			defaultProvider: "local",
			feature:         "unknown",
			localAvail:      true,
			cloudAvail:      true,
			wantProvider:    "local",
		},
		{
			name:            "feature off",
			routing:         map[string]string{"categorize": "off"},
			defaultProvider: "local",
			feature:         "categorize",
			localAvail:      true,
			cloudAvail:      true,
			wantProvider:    "",
		},
		{
			name:            "default off, feature non mappée",
			routing:         map[string]string{},
			defaultProvider: "off",
			feature:         "anything",
			localAvail:      true,
			cloudAvail:      true,
			wantProvider:    "",
		},
		{
			name:            "les deux indisponibles",
			routing:         map[string]string{},
			defaultProvider: "local",
			feature:         "test",
			localAvail:      false,
			cloudAvail:      false,
			wantProvider:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local.available = tt.localAvail
			cloud.available = tt.cloudAvail
			r := NewRouter(local, cloud, tt.routing, tt.defaultProvider)

			got := r.ForFeature(tt.feature)

			if tt.wantProvider == "" {
				if got != nil {
					t.Errorf("attendu nil, obtenu %q", got.Name())
				}
				return
			}
			if got == nil {
				t.Fatalf("attendu %q, obtenu nil", tt.wantProvider)
			}
			if got.Name() != tt.wantProvider {
				t.Errorf("attendu %q, obtenu %q", tt.wantProvider, got.Name())
			}
		})
	}
}

func TestRouter_Available(t *testing.T) {
	local := &mockProvider{name: "local", available: false}
	cloud := &mockProvider{name: "cloud", available: false}

	r := NewRouter(local, cloud, nil, "off")
	if r.Available() {
		t.Error("router devrait être indisponible quand default=off")
	}

	r = NewRouter(local, cloud, nil, "local")
	if r.Available() {
		t.Error("router devrait être indisponible quand les deux providers sont down")
	}

	local.available = true
	if !r.Available() {
		t.Error("router devrait être disponible quand local est up")
	}
}

func TestLocalProvider_Complete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}

		resp := ollamaChatResponse{
			Model: "mistral:7b",
			Message: struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			}{
				Role:    "assistant",
				Content: "Bonjour !",
			},
			PromptEvalCount: 10,
			EvalCount:       5,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := NewLocalProvider(srv.URL, "mistral:7b", "nomic-embed-text")
	resp, err := p.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: RoleUser, Content: "Salut"}},
	})
	if err != nil {
		t.Fatalf("erreur inattendue: %v", err)
	}

	if resp.Content != "Bonjour !" {
		t.Errorf("contenu attendu %q, obtenu %q", "Bonjour !", resp.Content)
	}
	if resp.Usage.PromptTokens != 10 {
		t.Errorf("prompt tokens attendu 10, obtenu %d", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 5 {
		t.Errorf("completion tokens attendu 5, obtenu %d", resp.Usage.CompletionTokens)
	}
}

func TestLocalProvider_Embed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			http.NotFound(w, r)
			return
		}

		resp := struct {
			Embeddings [][]float32 `json:"embeddings"`
		}{
			Embeddings: [][]float32{{0.1, 0.2, 0.3}, {0.4, 0.5, 0.6}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := NewLocalProvider(srv.URL, "mistral:7b", "nomic-embed-text")
	embeddings, err := p.Embed(context.Background(), []string{"texte1", "texte2"})
	if err != nil {
		t.Fatalf("erreur inattendue: %v", err)
	}

	if len(embeddings) != 2 {
		t.Fatalf("attendu 2 embeddings, obtenu %d", len(embeddings))
	}
	if len(embeddings[0]) != 3 {
		t.Errorf("attendu dimension 3, obtenu %d", len(embeddings[0]))
	}
}

func TestLocalProvider_Available(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"models":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	p := NewLocalProvider(srv.URL, "test", "test")
	if !p.Available() {
		t.Error("devrait être disponible quand le serveur répond")
	}

	p2 := NewLocalProvider("http://localhost:1", "test", "test")
	if p2.Available() {
		t.Error("ne devrait pas être disponible quand le serveur est injoignable")
	}
}

func TestCloudProvider_Complete_Anthropic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Le serveur mock accepte toutes les requêtes POST pour /v1/messages.
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("x-api-key") == "" {
			http.Error(w, "missing api key", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("anthropic-version") == "" {
			http.Error(w, "missing version", http.StatusBadRequest)
			return
		}

		resp := anthropicResponse{
			Content: []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{
				{Type: "text", Text: "Réponse Anthropic"},
			},
			Model: "claude-sonnet-4-20250514",
		}
		resp.Usage.InputTokens = 15
		resp.Usage.OutputTokens = 8
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	// L'endpoint doit contenir "anthropic" pour déclencher le mode Anthropic.
	// On utilise srv.URL comme base, et on ajoute un suffixe contenant "anthropic"
	// que le CloudProvider va stopper avec TrimRight("/").
	t.Setenv("TEST_ANTHROPIC_KEY", "test-key-123")
	// Astuce : on met l'URL du mock comme endpoint, mais on triche
	// en injectant "anthropic" dans le nom pour que isAnthropic() retourne true.
	// Le doWithRetry concatène endpoint + path, donc on utilise directement srv.URL.
	p := newCloudProviderForTest(srv.URL, "claude-sonnet-4-20250514", "", "TEST_ANTHROPIC_KEY", true)

	resp, err := p.Complete(context.Background(), CompletionRequest{
		Messages: []Message{
			{Role: RoleSystem, Content: "Tu es un assistant."},
			{Role: RoleUser, Content: "Bonjour"},
		},
		MaxTokens: 100,
	})
	if err != nil {
		t.Fatalf("erreur inattendue: %v", err)
	}

	if resp.Content != "Réponse Anthropic" {
		t.Errorf("contenu attendu %q, obtenu %q", "Réponse Anthropic", resp.Content)
	}
	if resp.Usage.PromptTokens != 15 {
		t.Errorf("prompt tokens attendu 15, obtenu %d", resp.Usage.PromptTokens)
	}
}

func TestCloudProvider_Complete_OpenAI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}

		resp := openaiChatResponse{
			Model: "gpt-4",
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{
				{Message: struct {
					Content string `json:"content"`
				}{Content: "Réponse OpenAI"}},
			},
		}
		resp.Usage.PromptTokens = 12
		resp.Usage.CompletionTokens = 6
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	t.Setenv("TEST_OPENAI_KEY", "test-key-456")
	p := NewCloudProvider(srv.URL, "gpt-4", "", "TEST_OPENAI_KEY")

	resp, err := p.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: RoleUser, Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("erreur inattendue: %v", err)
	}

	if resp.Content != "Réponse OpenAI" {
		t.Errorf("contenu attendu %q, obtenu %q", "Réponse OpenAI", resp.Content)
	}
}

func TestCloudProvider_Available(t *testing.T) {
	p := NewCloudProvider("https://api.anthropic.com", "model", "", "NONEXISTENT_VAR_12345")
	if p.Available() {
		t.Error("ne devrait pas être disponible sans clé API")
	}

	t.Setenv("TEST_AVAIL_KEY", "some-key")
	p2 := NewCloudProvider("https://api.anthropic.com", "model", "", "TEST_AVAIL_KEY")
	if !p2.Available() {
		t.Error("devrait être disponible avec une clé API configurée")
	}
}

func TestCloudProvider_Embed_AnthropicNotSupported(t *testing.T) {
	t.Setenv("TEST_KEY", "key")
	p := NewCloudProvider("https://api.anthropic.com", "model", "", "TEST_KEY")
	_, err := p.Embed(context.Background(), []string{"test"})
	if err == nil {
		t.Fatal("attendu une erreur pour embeddings Anthropic")
	}
}
