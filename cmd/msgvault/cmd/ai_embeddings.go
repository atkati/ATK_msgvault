package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/wesm/msgvault/internal/ai"
)

// ============================================================================
// ai index — generate embeddings
// ============================================================================

var aiIndexCmd = &cobra.Command{
	Use:   "index",
	Short: "Generate embeddings for semantic search",
	Long: `Generate vector embeddings for email messages using a local or cloud model.

Embeddings are stored locally in SQLite and used for semantic search.
Requires an embedding model (e.g., nomic-embed-text on Ollama).

If no dedicated embedding model is available, the chat model is used
via Ollama's /api/embed endpoint (slower but works with any model).

Examples:
  msgvault ai index                           # Index unindexed messages
  msgvault ai index --limit 1000              # Max 1000 messages
  msgvault ai index --model nomic-embed-text  # Use specific embed model
  msgvault ai index --all                     # Re-index everything
`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runAIIndex,
}

var aiIndexAll bool

func init() {
	aiCmd.AddCommand(aiIndexCmd)
	aiIndexCmd.Flags().IntVar(&aiLimit, "limit", 1000, "Maximum messages to index")
	aiIndexCmd.Flags().BoolVar(&aiIndexAll, "all", false, "Re-index all messages")
}

func runAIIndex(cmd *cobra.Command, args []string) error {
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

	var messageIDs []int64
	if aiIndexAll {
		messageIDs, err = listAllMessageIDs(st, aiLimit)
	} else {
		messageIDs, err = st.ListMessageIDsWithoutEmbedding(aiLimit)
	}
	if err != nil {
		return err
	}

	if len(messageIDs) == 0 {
		existing, _ := st.CountEmbeddings()
		fmt.Fprintf(out, "Tous les messages sont deja indexes (%d embeddings).\n", existing)
		return nil
	}

	// Determine embed model name for display.
	embedModel := cfg.AI.Local.EmbedModel
	if aiModel != "" {
		embedModel = aiModel
	}
	if embedModel == "" {
		embedModel = cfg.AI.Local.Model
		if embedModel == "" {
			embedModel = "llama3.2"
		}
	}

	fmt.Fprintf(out, "Indexation de %d messages avec %s (%s)...\n\n",
		len(messageIDs), provider.Name(), embedModel)

	var indexed, errors int
	start := time.Now()

	// Process in batches for embedding API efficiency.
	batchSize := 10
	for i := 0; i < len(messageIDs); i += batchSize {
		if ctx.Err() != nil {
			fmt.Fprintf(out, "\nInterrompu apres %d messages.\n", indexed)
			break
		}

		end := i + batchSize
		if end > len(messageIDs) {
			end = len(messageIDs)
		}
		batch := messageIDs[i:end]

		// Build text for each message in the batch.
		texts := make([]string, 0, len(batch))
		validIDs := make([]int64, 0, len(batch))
		for _, msgID := range batch {
			subject, snippet, fromEmail, err := st.GetMessageSnippetAndSubject(msgID)
			if err != nil {
				errors++
				continue
			}
			text := fmt.Sprintf("De: %s | Sujet: %s | %s", fromEmail, subject, snippet)
			texts = append(texts, text)
			validIDs = append(validIDs, msgID)
		}

		if len(texts) == 0 {
			continue
		}

		// Call embedding API.
		embeddings, err := provider.Embed(ctx, texts)
		if err != nil {
			// If embed fails (e.g., Anthropic doesn't support it), try one-by-one with a workaround.
			logger.Warn("batch embed failed, trying individually", "error", err)
			for j, text := range texts {
				vec, err := embedSingle(ctx, provider, text)
				if err != nil {
					errors++
					continue
				}
				if err := st.UpsertEmbedding(validIDs[j], vec, embedModel); err != nil {
					errors++
					continue
				}
				indexed++
			}
		} else {
			for j, vec := range embeddings {
				if j >= len(validIDs) {
					break
				}
				if err := st.UpsertEmbedding(validIDs[j], vec, embedModel); err != nil {
					errors++
					continue
				}
				indexed++
			}
		}

		// Progress.
		total := i + len(batch)
		if total%50 == 0 || total == len(messageIDs) {
			rate := float64(total) / time.Since(start).Seconds()
			remaining := float64(len(messageIDs)-total) / rate
			fmt.Fprintf(out, "  %d/%d (%.1f msg/s, ~%.0fs restant)\n",
				total, len(messageIDs), rate, remaining)
		}
	}

	elapsed := time.Since(start)
	fmt.Fprintf(out, "\nTermine : %d indexes, %d erreurs, %s\n",
		indexed, errors, elapsed.Round(time.Millisecond))

	return nil
}

// embedSingle generates embedding for a single text.
func embedSingle(ctx context.Context, provider ai.AIProvider, text string) ([]float32, error) {
	vecs, err := provider.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("no embedding returned")
	}
	return vecs[0], nil
}

// ============================================================================
// ai search — semantic search
// ============================================================================

var aiSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Semantic search across your emails",
	Long: `Search emails by meaning using vector embeddings.

Unlike full-text search (FTS5), semantic search finds messages that are
conceptually similar to your query, even if they don't contain the exact words.

Requires embeddings to be generated first with 'ai index'.

Examples:
  msgvault ai search "probleme remboursement assurance"
  msgvault ai search "facture impayee" --limit 20
`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runAISearch,
}

var aiSearchLimit int

func init() {
	aiCmd.AddCommand(aiSearchCmd)
	aiSearchCmd.Flags().IntVar(&aiSearchLimit, "limit", 10, "Number of results")
}

func runAISearch(cmd *cobra.Command, args []string) error {
	queryText := args[0]

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

	// Check if we have any embeddings.
	count, err := st.CountEmbeddings()
	if err != nil {
		return err
	}
	if count == 0 {
		fmt.Fprintln(out, "Aucun embedding. Lancez d'abord : msgvault ai index")
		return nil
	}

	fmt.Fprintf(out, "Recherche semantique dans %d messages...\n\n", count)

	// Generate embedding for the query.
	vecs, err := provider.Embed(ctx, []string{queryText})
	if err != nil {
		return fmt.Errorf("embedding de la requete: %w", err)
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return fmt.Errorf("embedding vide")
	}

	queryVec := vecs[0]

	// Search.
	results, err := st.SemanticSearch(queryVec, aiSearchLimit)
	if err != nil {
		return err
	}

	if len(results) == 0 {
		fmt.Fprintln(out, "Aucun resultat.")
		return nil
	}

	// Display results.
	for i, r := range results {
		subject, snippet, fromEmail, err := st.GetMessageSnippetAndSubject(r.MessageID)
		if err != nil {
			continue
		}
		similarity := r.Similarity * 100

		fmt.Fprintf(out, "%2d. [%.0f%%] #%d\n", i+1, similarity, r.MessageID)
		fmt.Fprintf(out, "    De: %s\n", fromEmail)
		fmt.Fprintf(out, "    Sujet: %s\n", subject)
		if snippet != "" {
			snip := snippet
			if len(snip) > 100 {
				snip = snip[:100] + "..."
			}
			fmt.Fprintf(out, "    %s\n", snip)
		}
		fmt.Fprintln(out)
	}

	return nil
}

// ============================================================================
// ai find-entity — search entities
// ============================================================================

var aiFindEntityCmd = &cobra.Command{
	Use:   "find-entity",
	Short: "Search extracted entities",
	Long: `Search through AI-extracted entities (amounts, IBAN, phone numbers, etc.)

Examples:
  msgvault ai find-entity --type montant
  msgvault ai find-entity --type montant --value ">500"
  msgvault ai find-entity --type iban
  msgvault ai find-entity --type entreprise --value "Groupama"
`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runAIFindEntity,
}

var (
	aiFindType  string
	aiFindValue string
)

func init() {
	aiCmd.AddCommand(aiFindEntityCmd)
	aiFindEntityCmd.Flags().StringVar(&aiFindType, "type", "", "Entity type (montant, iban, date, telephone, personne, entreprise, contrat, adresse)")
	aiFindEntityCmd.Flags().StringVar(&aiFindValue, "value", "", "Filter by value (contains)")
	aiFindEntityCmd.Flags().IntVar(&aiLimit, "limit", 50, "Maximum results")
	aiFindEntityCmd.MarkFlagRequired("type")
}

func runAIFindEntity(cmd *cobra.Command, args []string) error {
	st, err := openStoreReadOnly()
	if err != nil {
		return err
	}
	defer st.Close()

	out := cmd.OutOrStdout()

	entities, err := st.SearchAIEntities(strings.ToLower(aiFindType), aiFindValue, aiLimit)
	if err != nil {
		return err
	}

	if len(entities) == 0 {
		fmt.Fprintf(out, "Aucune entite de type %q trouvee.\n", aiFindType)
		return nil
	}

	fmt.Fprintf(out, "Entites de type %q (%d resultats) :\n\n", aiFindType, len(entities))
	fmt.Fprintf(out, "%-8s  %-40s  %s\n", "MSG ID", "VALEUR", "CONTEXTE")
	fmt.Fprintf(out, "%-8s  %-40s  %s\n",
		strings.Repeat("-", 8), strings.Repeat("-", 40), strings.Repeat("-", 30))

	for _, e := range entities {
		ctx := e.Context
		if len(ctx) > 30 {
			ctx = ctx[:30] + "..."
		}
		fmt.Fprintf(out, "%-8d  %-40s  %s\n", e.MessageID, truncatePurge(e.Value, 40), ctx)
	}

	return nil
}
