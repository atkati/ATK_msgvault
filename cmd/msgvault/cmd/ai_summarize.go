package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/wesm/msgvault/internal/ai"
	"github.com/wesm/msgvault/internal/store"
)

var aiSummarizeCmd = &cobra.Command{
	Use:   "summarize",
	Short: "Generate summaries of email threads",
	Long: `Summarize email threads using AI.

Generates a short summary (3-4 lines) for quick overview, plus an optional
structured summary (context, key points, actions, commitments).

Examples:
  msgvault ai summarize --thread 5479          # Summarize a specific thread
  msgvault ai summarize --message 42           # Summarize a single message
  msgvault ai summarize --all-threads --min-messages 5  # Batch: threads with 5+ messages
  msgvault ai summarize --thread 5479 --dry-run        # Preview
`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runAISummarize,
}

var (
	aiSumThread     int64
	aiSumAllThreads bool
	aiSumMinMsgs    int
)

func init() {
	aiCmd.AddCommand(aiSummarizeCmd)
	aiSummarizeCmd.Flags().Int64Var(&aiSumThread, "thread", 0, "Summarize a specific thread/conversation ID")
	aiSummarizeCmd.Flags().Int64Var(&aiMessageID, "message", 0, "Summarize a single message")
	aiSummarizeCmd.Flags().BoolVar(&aiSumAllThreads, "all-threads", false, "Summarize all large threads")
	aiSummarizeCmd.Flags().IntVar(&aiSumMinMsgs, "min-messages", 5, "Minimum messages for --all-threads")
	aiSummarizeCmd.Flags().BoolVar(&aiDryRun, "dry-run", false, "Preview without saving")
	aiSummarizeCmd.Flags().IntVar(&aiLimit, "limit", 50, "Maximum threads to summarize")
}

const threadSummarizePrompt = `Tu es un assistant qui resume des fils de conversation email en francais.

Genere un resume structure au format suivant :

RESUME : [2-3 phrases de synthese]

POINTS CLES :
- [point 1]
- [point 2]
- ...

ACTIONS EN ATTENTE :
- [action 1, si applicable]

ENGAGEMENTS :
- [engagement pris par une partie, si applicable]

Si aucune action ou engagement, omets ces sections.
Sois concis et factuel. /no_think`

const messageSummarizePrompt = `Resume ce message email en 2-3 phrases en francais. Sois concis et factuel. /no_think`

func runAISummarize(cmd *cobra.Command, args []string) error {
	provider, err := resolveAIProvider()
	if err != nil {
		return err
	}

	st, err := openStoreReadWrite()
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	// Single message mode.
	if aiMessageID > 0 {
		return summarizeSingleMessage(ctx, out, st, provider)
	}

	// Single thread mode.
	if aiSumThread > 0 {
		return summarizeThread(ctx, out, st, provider, aiSumThread)
	}

	// Batch mode: all large threads.
	if aiSumAllThreads {
		return summarizeAllThreads(ctx, out, st, provider)
	}

	return fmt.Errorf("specifiez --thread <id>, --message <id>, ou --all-threads")
}

func summarizeSingleMessage(ctx context.Context, out io.Writer, st *store.Store, provider ai.AIProvider) error {
	subject, snippet, fromEmail, err := st.GetMessageSnippetAndSubject(aiMessageID)
	if err != nil {
		return fmt.Errorf("message %d: %w", aiMessageID, err)
	}

	userMsg := fmt.Sprintf("De: %s\nSujet: %s\nContenu: %s", fromEmail, subject, snippet)

	fmt.Fprintf(out, "Resume du message #%d...\n\n", aiMessageID)

	resp, err := provider.Complete(ctx, ai.CompletionRequest{
		Messages: []ai.Message{
			{Role: ai.RoleSystem, Content: messageSummarizePrompt},
			{Role: ai.RoleUser, Content: userMsg},
		},
		Temperature: 0.3,
		MaxTokens:   300,
	})
	if err != nil {
		return fmt.Errorf("AI: %w", err)
	}

	summary := cleanThinkTags(resp.Content)
	fmt.Fprintf(out, "Sujet: %s\nDe: %s\n\n%s\n", subject, fromEmail, summary)

	if !aiDryRun {
		st.UpsertAISummary(&store.AISummary{
			MessageID:    aiMessageID,
			SummaryShort: summary,
			Provider:     provider.Name(),
			Model:        resp.Model,
		})
	}

	return nil
}

func summarizeThread(ctx context.Context, out io.Writer, st *store.Store, provider ai.AIProvider, convID int64) error {
	msgs, err := st.GetThreadMessages(convID)
	if err != nil {
		return fmt.Errorf("thread %d: %w", convID, err)
	}
	if len(msgs) == 0 {
		fmt.Fprintf(out, "Thread %d : aucun message.\n", convID)
		return nil
	}

	fmt.Fprintf(out, "Resume du thread #%d (%d messages)...\n\n", convID, len(msgs))

	// Build conversation text for the AI.
	var sb strings.Builder
	for _, m := range msgs {
		date := ""
		if !m.SentAt.IsZero() {
			date = m.SentAt.Format("02/01/2006 15:04")
		}
		fmt.Fprintf(&sb, "[%s] De: %s\nSujet: %s\n%s\n\n", date, m.FromEmail, m.Subject, m.Snippet)
	}

	// Truncate if too long for the model context.
	text := sb.String()
	if len(text) > 6000 {
		text = text[:6000] + "\n[... tronque ...]"
	}

	resp, err := provider.Complete(ctx, ai.CompletionRequest{
		Messages: []ai.Message{
			{Role: ai.RoleSystem, Content: threadSummarizePrompt},
			{Role: ai.RoleUser, Content: text},
		},
		Temperature: 0.3,
		MaxTokens:   800,
	})
	if err != nil {
		return fmt.Errorf("AI: %w", err)
	}

	summary := cleanThinkTags(resp.Content)
	fmt.Fprintf(out, "Thread: %s\nParticipants: %d messages\n\n%s\n",
		msgs[0].Subject, len(msgs), summary)

	if !aiDryRun {
		st.UpsertAISummary(&store.AISummary{
			ConversationID: convID,
			SummaryShort:   extractFirstParagraph(summary),
			SummaryFull:    summary,
			Provider:       provider.Name(),
			Model:          resp.Model,
		})
	}

	return nil
}

func summarizeAllThreads(ctx context.Context, out io.Writer, st *store.Store, provider ai.AIProvider) error {
	threads, err := st.ListLargeThreads(aiSumMinMsgs, aiLimit)
	if err != nil {
		return err
	}

	if len(threads) == 0 {
		fmt.Fprintf(out, "Aucun thread avec %d+ messages.\n", aiSumMinMsgs)
		return nil
	}

	fmt.Fprintf(out, "Resume de %d threads (%d+ messages chacun)...\n\n", len(threads), aiSumMinMsgs)

	var summarized, errors int
	start := time.Now()

	for i, t := range threads {
		if ctx.Err() != nil {
			fmt.Fprintf(out, "\nInterrompu apres %d threads.\n", i)
			break
		}

		// Skip already summarized.
		existing, _ := st.GetSummaryByConversation(t.ConversationID)
		if existing != nil && !aiDryRun {
			continue
		}

		err := summarizeThread(ctx, out, st, provider, t.ConversationID)
		if err != nil {
			logger.Warn("summarize failed", "thread", t.ConversationID, "error", err)
			errors++
			continue
		}
		summarized++

		fmt.Fprintln(out, "---")

		if (i+1)%5 == 0 {
			rate := float64(i+1) / time.Since(start).Seconds()
			fmt.Fprintf(out, "  Progression: %d/%d (%.1f threads/min)\n\n", i+1, len(threads), rate*60)
		}
	}

	elapsed := time.Since(start)
	fmt.Fprintf(out, "\nTermine : %d resumes, %d erreurs, %s\n",
		summarized, errors, elapsed.Round(time.Millisecond))

	return nil
}

func cleanThinkTags(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.Index(s, "</think>"); idx >= 0 {
		s = strings.TrimSpace(s[idx+len("</think>"):])
	}
	return s
}

func extractFirstParagraph(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.Index(s, "\n\n"); idx > 0 {
		return s[:idx]
	}
	if len(s) > 300 {
		return s[:300]
	}
	return s
}
