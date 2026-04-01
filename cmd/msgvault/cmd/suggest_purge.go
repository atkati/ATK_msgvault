package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wesm/msgvault/internal/query"
	"github.com/wesm/msgvault/internal/store"
)

var (
	suggestPurgeMinCount int
	suggestPurgeAccount  string
	suggestPurgeLimit    int
)

var suggestPurgeCmd = &cobra.Command{
	Use:   "suggest-purge",
	Short: "Identify emails that are candidates for deletion",
	Long: `Analyze the archive and suggest messages for deletion.

Identifies:
  - Newsletters (high-volume senders, noreply addresses)
  - Automated notifications (noreply@, notification@, alerts@, etc.)
  - High-volume senders (more than N messages from same sender)

Results are sorted by message count (highest first).

Examples:
  msgvault suggest-purge
  msgvault suggest-purge --min-count 50
  msgvault suggest-purge --account you@gmail.com
`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runSuggestPurge,
}

func init() {
	rootCmd.AddCommand(suggestPurgeCmd)

	suggestPurgeCmd.Flags().IntVar(&suggestPurgeMinCount, "min-count", 20, "Minimum message count to flag a sender")
	suggestPurgeCmd.Flags().StringVar(&suggestPurgeAccount, "account", "", "Filter by account email")
	suggestPurgeCmd.Flags().IntVar(&suggestPurgeLimit, "limit", 50, "Maximum number of suggestions")
}

// Known noreply/automated patterns.
var noReplyPrefixes = []string{
	"noreply", "no-reply", "ne-pas-repondre", "nepasrepondre",
	"notification", "notifications", "alert", "alerts",
	"mailer-daemon", "postmaster",
	"newsletter", "news", "info", "contact",
	"support", "service", "donotreply",
}

// Known newsletter/bulk sender domains.
var bulkSenderDomains = []string{
	"mailchimp.com", "sendgrid.net", "amazonses.com",
	"mailjet.com", "sendinblue.com", "brevo.com",
	"mailgun.org", "constantcontact.com", "hubspot.com",
	"intercom.io", "intercom-mail.com",
	"createsend.com", "campaignmonitor.com",
	"mandrillapp.com", "postmarkapp.com",
}

type purgeSuggestion struct {
	SenderEmail string
	Category    string
	Count       int64
	TotalSize   int64
}

func runSuggestPurge(cmd *cobra.Command, args []string) error {
	dbPath := cfg.DatabaseDSN()
	st, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer st.Close()

	if err := st.InitSchema(); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}

	engine := query.NewSQLiteEngine(st.DB())
	defer engine.Close()

	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	fmt.Fprintln(out, "Analyse de l'archive...")
	fmt.Fprintln(out)

	// Get all senders with high message counts.
	opts := query.AggregateOptions{
		SortField:     query.SortByCount,
		SortDirection: query.SortDesc,
		Limit:         500, // Scan top 500 senders
	}
	if suggestPurgeAccount != "" {
		srcID, err := resolveAccountSourceID(ctx, engine, suggestPurgeAccount)
		if err != nil {
			return err
		}
		opts.SourceID = &srcID
	}

	senders, err := engine.Aggregate(ctx, query.ViewSenders, opts)
	if err != nil {
		return fmt.Errorf("aggregate senders: %w", err)
	}

	// Also get domain aggregation for bulk sender detection.
	domains, err := engine.Aggregate(ctx, query.ViewDomains, opts)
	if err != nil {
		return fmt.Errorf("aggregate domains: %w", err)
	}

	bulkDomainSet := make(map[string]bool)
	for _, d := range domains {
		if isBulkSenderDomain(d.Key) {
			bulkDomainSet[d.Key] = true
		}
	}

	var suggestions []purgeSuggestion

	for _, s := range senders {
		if s.Count < int64(suggestPurgeMinCount) {
			continue
		}

		category := classifySender(s.Key, bulkDomainSet)
		if category == "" {
			continue
		}

		suggestions = append(suggestions, purgeSuggestion{
			SenderEmail: s.Key,
			Category:    category,
			Count:       s.Count,
			TotalSize:   s.TotalSize,
		})

		if len(suggestions) >= suggestPurgeLimit {
			break
		}
	}

	if len(suggestions) == 0 {
		fmt.Fprintln(out, "Aucune suggestion de purge trouvee.")
		fmt.Fprintf(out, "  (seuil: %d messages par expediteur)\n", suggestPurgeMinCount)
		return nil
	}

	// Print results.
	fmt.Fprintf(out, "%-40s  %-20s  %8s  %10s\n", "EXPEDITEUR", "CATEGORIE", "MESSAGES", "TAILLE")
	fmt.Fprintf(out, "%-40s  %-20s  %8s  %10s\n",
		strings.Repeat("-", 40), strings.Repeat("-", 20),
		strings.Repeat("-", 8), strings.Repeat("-", 10))

	var totalCount int64
	var totalSize int64
	for _, s := range suggestions {
		fmt.Fprintf(out, "%-40s  %-20s  %8d  %10s\n",
			truncatePurge(s.SenderEmail, 40),
			s.Category,
			s.Count,
			formatSizeCSV(s.TotalSize),
		)
		totalCount += s.Count
		totalSize += s.TotalSize
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "Total: %d expediteurs, %d messages, %s\n",
		len(suggestions), totalCount, formatSizeCSV(totalSize))
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Pour supprimer les messages d'un expediteur :")
	fmt.Fprintln(out, "  1. Verifiez dans le TUI : msgvault tui")
	fmt.Fprintln(out, "  2. Utilisez la selection par lot (Space/A/d) pour stager la suppression")

	return nil
}

func classifySender(email string, bulkDomainSet map[string]bool) string {
	lower := strings.ToLower(email)

	// Check noreply patterns in local part.
	localPart := lower
	if atIdx := strings.Index(lower, "@"); atIdx > 0 {
		localPart = lower[:atIdx]
	}
	for _, prefix := range noReplyPrefixes {
		if strings.HasPrefix(localPart, prefix) {
			return "notification-auto"
		}
	}

	// Check bulk sender domain.
	domain := ""
	if atIdx := strings.Index(lower, "@"); atIdx > 0 {
		domain = lower[atIdx+1:]
	}
	if bulkDomainSet[domain] {
		return "newsletter"
	}
	if isBulkSenderDomain(domain) {
		return "newsletter"
	}

	return ""
}

func isBulkSenderDomain(domain string) bool {
	lower := strings.ToLower(domain)
	for _, d := range bulkSenderDomains {
		if lower == d {
			return true
		}
		// Also match subdomains (e.g. bounce.mailchimp.com).
		if strings.HasSuffix(lower, "."+d) {
			return true
		}
	}
	return false
}

func truncatePurge(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
