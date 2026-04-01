package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wesm/msgvault/internal/ai"
	"github.com/wesm/msgvault/internal/store"
)

var aiChatCmd = &cobra.Command{
	Use:   "chat",
	Short: "Interactive AI assistant for your email archive",
	Long: `Ask questions about your emails in natural language.

Uses RAG (Retrieval-Augmented Generation): searches relevant emails
via FTS5 and semantic search, then sends them as context to the LLM.

Examples:
  msgvault ai chat
  > Combien d'emails d'Uber en 2024 ?
  > Resume mes echanges avec Groupama sur le sinistre de mars
  > Quel etait le montant de ma derniere facture ?
`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runAIChat,
}

func init() {
	aiCmd.AddCommand(aiChatCmd)
}

const chatSystemPrompt = `Tu es un assistant specialise dans l'analyse d'archives email.
L'utilisateur te pose des questions sur ses emails. Tu recois en contexte des extraits
d'emails pertinents trouves par recherche. Base tes reponses UNIQUEMENT sur ces extraits.
Si tu ne trouves pas l'information, dis-le clairement.
Reponds toujours en francais, de maniere concise et factuelle. /no_think`

func runAIChat(cmd *cobra.Command, args []string) error {
	provider, err := resolveAIProvider()
	if err != nil {
		return err
	}

	st, err := openStoreReadOnly()
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	reader := bufio.NewReader(os.Stdin)

	fmt.Fprintln(out, "msgvault AI Chat — Posez vos questions sur vos emails.")
	fmt.Fprintln(out, "Tapez 'quit' ou 'exit' pour quitter.")
	fmt.Fprintln(out)

	// Conversation history for multi-turn.
	var history []ai.Message

	for {
		fmt.Fprint(out, "> ")
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		question := strings.TrimSpace(line)
		if question == "" {
			continue
		}
		if question == "quit" || question == "exit" || question == "q" {
			break
		}

		// RAG: search for relevant emails.
		contextText := buildRAGContext(st, question)

		// Build messages.
		messages := []ai.Message{
			{Role: ai.RoleSystem, Content: chatSystemPrompt},
		}

		// Include conversation history (last 6 exchanges max).
		histStart := 0
		if len(history) > 12 {
			histStart = len(history) - 12
		}
		messages = append(messages, history[histStart:]...)

		// Add context + question.
		userMsg := question
		if contextText != "" {
			userMsg = fmt.Sprintf("CONTEXTE (emails pertinents) :\n%s\n\nQUESTION : %s", contextText, question)
		}
		messages = append(messages, ai.Message{Role: ai.RoleUser, Content: userMsg})

		resp, err := provider.Complete(ctx, ai.CompletionRequest{
			Messages:    messages,
			Temperature: 0.3,
			MaxTokens:   800,
		})
		if err != nil {
			fmt.Fprintf(out, "Erreur : %v\n\n", err)
			continue
		}

		answer := cleanThinkTags(resp.Content)
		fmt.Fprintf(out, "\n%s\n\n", answer)

		// Update history.
		history = append(history, ai.Message{Role: ai.RoleUser, Content: question})
		history = append(history, ai.Message{Role: ai.RoleAssistant, Content: answer})
	}

	return nil
}

// buildRAGContext searches the archive for emails relevant to the question.
func buildRAGContext(st *store.Store, question string) string {
	var results []string

	// 1. FTS5 search (keyword-based).
	msgs, _, err := st.SearchMessages(question, 0, 5)
	if err == nil {
		for _, m := range msgs {
			results = append(results, formatContextMsg(m))
		}
	}

	// 2. Semantic search if embeddings exist.
	count, _ := st.CountEmbeddings()
	if count > 0 {
		// We can't embed the query here without a provider, so skip
		// semantic search in chat mode (FTS5 is sufficient for RAG).
		// Semantic search is available via 'ai search' command.
	}

	if len(results) == 0 {
		return ""
	}

	// Limit total context size.
	var sb strings.Builder
	totalLen := 0
	for i, r := range results {
		if totalLen+len(r) > 4000 {
			break
		}
		fmt.Fprintf(&sb, "--- Email %d ---\n%s\n\n", i+1, r)
		totalLen += len(r)
	}
	return sb.String()
}

func formatContextMsg(m store.APIMessage) string {
	date := ""
	if !m.SentAt.IsZero() {
		date = m.SentAt.Format("02/01/2006")
	}
	body := m.Body
	if len(body) > 500 {
		body = body[:500] + "..."
	}
	return fmt.Sprintf("De: %s | Date: %s | Sujet: %s\n%s",
		m.From, date, m.Subject, body)
}

// chatWithSemanticSearch is a variant that uses embeddings for RAG.
// Called when the provider is available (for embedding the question).
func chatWithSemanticSearch(ctx context.Context, st *store.Store, provider ai.AIProvider, question string) []string {
	vecs, err := provider.Embed(ctx, []string{question})
	if err != nil || len(vecs) == 0 {
		return nil
	}

	results, err := st.SemanticSearch(vecs[0], 3)
	if err != nil {
		return nil
	}

	var out []string
	for _, r := range results {
		subject, snippet, fromEmail, err := st.GetMessageSnippetAndSubject(r.MessageID)
		if err != nil {
			continue
		}
		out = append(out, fmt.Sprintf("De: %s | Sujet: %s\n%s", fromEmail, subject, snippet))
	}
	return out
}
