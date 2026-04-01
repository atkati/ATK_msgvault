// Package ai fournit une couche d'abstraction pour les providers IA (local/cloud).
package ai

import (
	"context"
	"errors"
)

// Erreurs sentinelles.
var (
	ErrNotSupported = errors.New("ai: opération non supportée par ce provider")
	ErrUnavailable  = errors.New("ai: provider indisponible")
)

// Role représente le rôle d'un participant dans une conversation IA.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message représente un message dans une conversation IA.
type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}

// CompletionRequest contient les paramètres d'une requête de complétion.
type CompletionRequest struct {
	Model       string    `json:"model,omitempty"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
}

// CompletionResponse contient la réponse d'une complétion.
type CompletionResponse struct {
	Content string `json:"content"`
	Model   string `json:"model"`
	Usage   Usage  `json:"usage"`
}

// Usage contient les statistiques de tokens consommés.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// AIProvider définit l'interface commune pour tous les providers IA.
type AIProvider interface {
	// Complete envoie une requête de complétion et retourne la réponse.
	Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)

	// Embed génère des vecteurs d'embeddings pour les textes donnés.
	Embed(ctx context.Context, texts []string) ([][]float32, error)

	// Available retourne true si le provider est joignable et opérationnel.
	Available() bool

	// Name retourne le nom du provider (ex: "ollama", "anthropic").
	Name() string
}
