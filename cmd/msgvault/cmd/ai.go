package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/wesm/msgvault/internal/ai"
	"github.com/wesm/msgvault/internal/store"
)

var aiCmd = &cobra.Command{
	Use:   "ai",
	Short: "AI-powered email analysis",
	Long: `Commandes IA pour analyser vos emails.

Necessite Ollama (local) ou une cle API cloud (Anthropic/OpenAI).
Configuration dans config.toml section [ai].

Commandes :
  msgvault ai categorize     Classifier les emails par categorie
  msgvault ai extract-entities  Extraire les entites nommees
`,
}

var (
	aiProvider  string
	aiModel     string
	aiBatchSize int
	aiDryRun    bool
	aiMessageID int64
	aiAll       bool
	aiLimit     int
)

func init() {
	rootCmd.AddCommand(aiCmd)

	// Shared flags for all AI subcommands.
	aiCmd.PersistentFlags().StringVar(&aiProvider, "ai", "", "Force AI provider (local, cloud, off)")
	aiCmd.PersistentFlags().StringVar(&aiModel, "model", "", "Override model name")
	aiCmd.PersistentFlags().IntVar(&aiBatchSize, "batch", 20, "Batch size for processing")
}

// resolveAIProvider creates an AI provider based on config and flags.
func resolveAIProvider() (ai.AIProvider, error) {
	providerChoice := cfg.AI.DefaultProvider
	if aiProvider != "" {
		providerChoice = aiProvider
	}

	if providerChoice == "off" {
		return nil, fmt.Errorf("IA desactivee. Configurez [ai] dans config.toml ou utilisez --ai local|cloud")
	}

	switch providerChoice {
	case "local", "":
		endpoint := cfg.AI.Local.Endpoint
		if endpoint == "" {
			endpoint = "http://localhost:11434"
		}
		model := cfg.AI.Local.Model
		if model == "" {
			model = "qwen3:4b"
		}
		if aiModel != "" {
			model = aiModel
		}
		p := ai.NewLocalProvider(endpoint, model, cfg.AI.Local.EmbedModel)
		if !p.Available() {
			return nil, fmt.Errorf("Ollama non disponible sur %s. Lancez 'ollama serve' ou utilisez --ai cloud", endpoint)
		}
		return p, nil

	case "cloud":
		p := ai.NewCloudProvider(
			cfg.AI.Cloud.Endpoint, cfg.AI.Cloud.Model,
			cfg.AI.Cloud.EmbedModel, cfg.AI.Cloud.APIKeyEnv,
		)
		if aiModel != "" {
			p = ai.NewCloudProvider(
				cfg.AI.Cloud.Endpoint, aiModel,
				cfg.AI.Cloud.EmbedModel, cfg.AI.Cloud.APIKeyEnv,
			)
		}
		if !p.Available() {
			return nil, fmt.Errorf("provider cloud non disponible. Configurez %s comme variable d'environnement", cfg.AI.Cloud.APIKeyEnv)
		}
		return p, nil

	default:
		return nil, fmt.Errorf("provider inconnu: %s", providerChoice)
	}
}

// ============================================================================
// ai categorize
// ============================================================================

var aiCategorizeCmd = &cobra.Command{
	Use:   "categorize",
	Short: "Classify emails by category using AI",
	Long: `Classify emails into categories using a local (Ollama) or cloud AI model.

Categories: administratif, commercial, personnel, newsletter, facture,
            litige, notification, spam, professionnel

Examples:
  msgvault ai categorize                    # Categorize uncategorized emails
  msgvault ai categorize --all              # Re-categorize all
  msgvault ai categorize --message 42       # Single message
  msgvault ai categorize --limit 100        # Max 100 messages
  msgvault ai categorize --dry-run          # Preview without saving
  msgvault ai categorize --model qwen3:4b   # Use specific model
`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runAICategorize,
}

func init() {
	aiCmd.AddCommand(aiCategorizeCmd)
	aiCategorizeCmd.Flags().BoolVar(&aiDryRun, "dry-run", false, "Preview without saving to database")
	aiCategorizeCmd.Flags().Int64Var(&aiMessageID, "message", 0, "Categorize a single message by ID")
	aiCategorizeCmd.Flags().BoolVar(&aiAll, "all", false, "Re-categorize all messages (including already categorized)")
	aiCategorizeCmd.Flags().IntVar(&aiLimit, "limit", 500, "Maximum messages to process")
}

const categorizePrompt = `Tu es un classificateur d'emails. Analyse le sujet et l'extrait du message, puis classe-le dans UNE seule categorie parmi :
- administratif (administrations, impots, CAF, securite sociale, mairie)
- commercial (promotions, offres, pubs, e-commerce)
- personnel (famille, amis, conversations privees)
- newsletter (abonnements, digests, contenus editoriques)
- facture (factures, recus, confirmations de paiement)
- litige (reclamations, contentieux, plaintes, SAV)
- notification (alertes automatiques, confirmations, verifications)
- spam (non sollicite, arnaque, phishing)
- professionnel (travail, collegues, clients, projets)

Reponds UNIQUEMENT avec un JSON : {"category":"...","confidence":0.0-1.0}
Pas d'explication, pas de texte supplementaire. /no_think`

func runAICategorize(cmd *cobra.Command, args []string) error {
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

	if aiMessageID > 0 {
		messageIDs = []int64{aiMessageID}
	} else if aiAll {
		messageIDs, err = listAllMessageIDs(st, aiLimit)
		if err != nil {
			return err
		}
	} else {
		messageIDs, err = st.ListUncategorizedMessageIDs(aiLimit)
		if err != nil {
			return err
		}
	}

	if len(messageIDs) == 0 {
		fmt.Fprintln(out, "Aucun message a categoriser.")
		return nil
	}

	fmt.Fprintf(out, "Categorisation de %d messages avec %s (%s)...\n\n",
		len(messageIDs), provider.Name(), modelName(provider))

	var categorized, errors int
	start := time.Now()

	for i, msgID := range messageIDs {
		if ctx.Err() != nil {
			fmt.Fprintf(out, "\nInterrompu apres %d messages.\n", i)
			break
		}

		subject, snippet, fromEmail, err := st.GetMessageSnippetAndSubject(msgID)
		if err != nil {
			logger.Warn("skip message", "id", msgID, "error", err)
			errors++
			continue
		}

		userMsg := fmt.Sprintf("De: %s\nSujet: %s\nExtrait: %s", fromEmail, subject, snippet)

		resp, err := provider.Complete(ctx, ai.CompletionRequest{
			Messages: []ai.Message{
				{Role: ai.RoleSystem, Content: categorizePrompt},
				{Role: ai.RoleUser, Content: userMsg},
			},
			Temperature: 0.1,
			MaxTokens:   100,
		})
		if err != nil {
			logger.Warn("AI error", "id", msgID, "error", err)
			errors++
			continue
		}

		cat, conf := parseCategoryResponse(resp.Content)
		if cat == "" {
			logger.Warn("unparseable response", "id", msgID, "response", resp.Content)
			errors++
			continue
		}

		if aiDryRun {
			fmt.Fprintf(out, "  #%-6d %-20s → %-15s (%.0f%%)\n", msgID, truncatePurge(subject, 20), cat, conf*100)
		} else {
			if err := st.UpsertAICategory(&store.AICategory{
				MessageID:  msgID,
				Category:   cat,
				Confidence: conf,
				Provider:   provider.Name(),
				Model:      resp.Model,
			}); err != nil {
				logger.Warn("save error", "id", msgID, "error", err)
				errors++
				continue
			}
		}
		categorized++

		// Progress every 10 messages.
		if (i+1)%10 == 0 {
			rate := float64(i+1) / time.Since(start).Seconds()
			remaining := float64(len(messageIDs)-i-1) / rate
			fmt.Fprintf(out, "  %d/%d (%.1f msg/s, ~%.0fs restant)\n",
				i+1, len(messageIDs), rate, remaining)
		}
	}

	elapsed := time.Since(start)
	fmt.Fprintf(out, "\nTermine : %d categorises, %d erreurs, %s\n",
		categorized, errors, elapsed.Round(time.Millisecond))

	return nil
}

func parseCategoryResponse(content string) (category string, confidence float64) {
	content = strings.TrimSpace(content)

	// Strip <think>...</think> blocks (qwen3 reasoning mode).
	if idx := strings.Index(content, "</think>"); idx >= 0 {
		content = strings.TrimSpace(content[idx+len("</think>"):])
	}

	// Strip markdown code fences if present.
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	// Try JSON parse.
	var result struct {
		Category   string  `json:"category"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(content), &result); err == nil && result.Category != "" {
		return strings.ToLower(result.Category), result.Confidence
	}

	// Fallback: extract from raw text.
	validCats := []string{
		"administratif", "commercial", "personnel", "newsletter",
		"facture", "litige", "notification", "spam", "professionnel",
	}
	lower := strings.ToLower(content)
	for _, cat := range validCats {
		if strings.Contains(lower, cat) {
			return cat, 0.5
		}
	}
	return "", 0
}

func modelName(p ai.AIProvider) string {
	switch p.Name() {
	case "ollama":
		if aiModel != "" {
			return aiModel
		}
		if cfg.AI.Local.Model != "" {
			return cfg.AI.Local.Model
		}
		return "qwen3:4b"
	case "anthropic", "openai":
		if aiModel != "" {
			return aiModel
		}
		return cfg.AI.Cloud.Model
	}
	return "?"
}

func listAllMessageIDs(st *store.Store, limit int) ([]int64, error) {
	rows, err := st.DB().Query("SELECT id FROM messages ORDER BY id LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ============================================================================
// ai extract-entities
// ============================================================================

var aiEntitiesCmd = &cobra.Command{
	Use:   "extract-entities",
	Short: "Extract named entities from emails using AI",
	Long: `Extract named entities (amounts, IBAN, dates, phone numbers, company names)
from email content using AI.

Entity types: montant, iban, date, telephone, personne, entreprise, contrat, adresse

Examples:
  msgvault ai extract-entities                  # Process uncategorized messages
  msgvault ai extract-entities --message 42     # Single message
  msgvault ai extract-entities --limit 50       # Max 50 messages
  msgvault ai extract-entities --dry-run        # Preview
`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runAIEntities,
}

func init() {
	aiCmd.AddCommand(aiEntitiesCmd)
	aiEntitiesCmd.Flags().BoolVar(&aiDryRun, "dry-run", false, "Preview without saving")
	aiEntitiesCmd.Flags().Int64Var(&aiMessageID, "message", 0, "Process a single message")
	aiEntitiesCmd.Flags().IntVar(&aiLimit, "limit", 200, "Maximum messages to process")
}

const entitiesPrompt = `Tu es un extracteur d'entites nommees pour les emails en francais. Analyse le contenu et extrais les entites suivantes si presentes :
- montant : sommes d'argent (ex: "45,50 EUR", "1 200,00 €")
- iban : numeros IBAN (ex: "FR76 3000 1007 ...")
- date : dates importantes mentionnees (pas la date d'envoi)
- telephone : numeros de telephone
- personne : noms de personnes
- entreprise : noms d'entreprises ou organisations
- contrat : numeros de contrat, dossier, reference, commande
- adresse : adresses postales

Reponds UNIQUEMENT avec un JSON : {"entities":[{"type":"...","value":"..."}]}
Si aucune entite, reponds : {"entities":[]}
Pas d'explication. /no_think`

func runAIEntities(cmd *cobra.Command, args []string) error {
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

	if aiMessageID > 0 {
		messageIDs = []int64{aiMessageID}
	} else {
		// Process messages that have been categorized but not yet entity-extracted.
		messageIDs, err = listMessagesWithoutEntities(st, aiLimit)
		if err != nil {
			return err
		}
	}

	if len(messageIDs) == 0 {
		fmt.Fprintln(out, "Aucun message a analyser.")
		return nil
	}

	fmt.Fprintf(out, "Extraction d'entites sur %d messages avec %s...\n\n",
		len(messageIDs), provider.Name())

	var processed, totalEntities, errors int
	start := time.Now()

	for i, msgID := range messageIDs {
		if ctx.Err() != nil {
			fmt.Fprintf(out, "\nInterrompu apres %d messages.\n", i)
			break
		}

		subject, snippet, fromEmail, err := st.GetMessageSnippetAndSubject(msgID)
		if err != nil {
			errors++
			continue
		}

		userMsg := fmt.Sprintf("De: %s\nSujet: %s\nContenu: %s", fromEmail, subject, snippet)

		resp, err := provider.Complete(ctx, ai.CompletionRequest{
			Messages: []ai.Message{
				{Role: ai.RoleSystem, Content: entitiesPrompt},
				{Role: ai.RoleUser, Content: userMsg},
			},
			Temperature: 0.1,
			MaxTokens:   500,
		})
		if err != nil {
			logger.Warn("AI error", "id", msgID, "error", err)
			errors++
			continue
		}

		entities := parseEntitiesResponse(resp.Content)

		if aiDryRun && len(entities) > 0 {
			fmt.Fprintf(out, "  #%-6d %s\n", msgID, truncatePurge(subject, 40))
			for _, e := range entities {
				fmt.Fprintf(out, "           [%s] %s\n", e.EntityType, e.Value)
			}
		}

		if !aiDryRun {
			for _, e := range entities {
				e.MessageID = msgID
				e.Provider = provider.Name()
				e.Model = resp.Model
				if _, err := st.InsertAIEntity(&e); err != nil {
					logger.Warn("save entity error", "id", msgID, "error", err)
				}
			}
		}

		totalEntities += len(entities)
		processed++

		if (i+1)%10 == 0 {
			rate := float64(i+1) / time.Since(start).Seconds()
			fmt.Fprintf(out, "  %d/%d (%.1f msg/s)\n", i+1, len(messageIDs), rate)
		}
	}

	elapsed := time.Since(start)
	fmt.Fprintf(out, "\nTermine : %d messages, %d entites extraites, %d erreurs, %s\n",
		processed, totalEntities, errors, elapsed.Round(time.Millisecond))

	return nil
}

type rawEntity struct {
	EntityType string `json:"type"`
	Value      string `json:"value"`
}

func parseEntitiesResponse(content string) []store.AIEntity {
	content = strings.TrimSpace(content)

	// Strip <think>...</think> blocks (qwen3 reasoning mode).
	if idx := strings.Index(content, "</think>"); idx >= 0 {
		content = strings.TrimSpace(content[idx+len("</think>"):])
	}

	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var result struct {
		Entities []rawEntity `json:"entities"`
	}
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil
	}

	var entities []store.AIEntity
	for _, e := range result.Entities {
		if e.Value == "" || e.EntityType == "" {
			continue
		}
		entities = append(entities, store.AIEntity{
			EntityType: strings.ToLower(e.EntityType),
			Value:      e.Value,
			Confidence: 0.8,
		})
	}
	return entities
}

func listMessagesWithoutEntities(st *store.Store, limit int) ([]int64, error) {
	rows, err := st.DB().Query(
		`SELECT m.id FROM messages m
		 WHERE NOT EXISTS (SELECT 1 FROM ai_entities ae WHERE ae.message_id = m.id)
		 ORDER BY m.id
		 LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
