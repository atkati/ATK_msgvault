package cmd

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/wesm/msgvault/internal/query"
	"github.com/wesm/msgvault/internal/store"
)

var (
	exportReportDomain  string
	exportReportSender  string
	exportReportAfter   string
	exportReportBefore  string
	exportReportOutput  string
	exportReportFormat  string
	exportReportAccount string
	exportReportLimit   int
)

var exportReportCmd = &cobra.Command{
	Use:   "export-report",
	Short: "Export an analytical report by sender or domain",
	Long: `Generate a CSV report of all exchanges with a given sender or domain.

Useful for reconstituting the history of exchanges with a company,
administration, insurance, or contractor.

Examples:
  msgvault export-report --domain uber.com
  msgvault export-report --sender support@groupama.fr --after 2023-01-01
  msgvault export-report --domain amazon.fr --output amazon-report.csv
  msgvault export-report --domain uber.com --account you@gmail.com
`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runExportReport,
}

func init() {
	rootCmd.AddCommand(exportReportCmd)

	exportReportCmd.Flags().StringVar(&exportReportDomain, "domain", "", "Filter by sender domain (e.g. uber.com)")
	exportReportCmd.Flags().StringVar(&exportReportSender, "sender", "", "Filter by sender email (e.g. support@example.com)")
	exportReportCmd.Flags().StringVar(&exportReportAfter, "after", "", "Only messages after this date (YYYY-MM-DD)")
	exportReportCmd.Flags().StringVar(&exportReportBefore, "before", "", "Only messages before this date (YYYY-MM-DD)")
	exportReportCmd.Flags().StringVar(&exportReportOutput, "output", "", "Output file (default: stdout)")
	exportReportCmd.Flags().StringVar(&exportReportFormat, "format", "csv", "Output format (csv)")
	exportReportCmd.Flags().StringVar(&exportReportAccount, "account", "", "Filter by account email")
	exportReportCmd.Flags().IntVar(&exportReportLimit, "limit", 10000, "Maximum number of messages")
}

func runExportReport(cmd *cobra.Command, args []string) error {
	if exportReportDomain == "" && exportReportSender == "" {
		return fmt.Errorf("specify --domain or --sender")
	}

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

	// Build filter.
	filter := query.MessageFilter{
		Domain: exportReportDomain,
		Sender: exportReportSender,
		Pagination: query.Pagination{
			Limit:  exportReportLimit,
			Offset: 0,
		},
		Sorting: query.MessageSorting{
			Field:     query.MessageSortByDate,
			Direction: query.SortDesc,
		},
	}

	if exportReportAfter != "" {
		t, err := time.Parse("2006-01-02", exportReportAfter)
		if err != nil {
			return fmt.Errorf("invalid --after date: %w", err)
		}
		filter.After = &t
	}
	if exportReportBefore != "" {
		t, err := time.Parse("2006-01-02", exportReportBefore)
		if err != nil {
			return fmt.Errorf("invalid --before date: %w", err)
		}
		filter.Before = &t
	}
	if exportReportAccount != "" {
		srcID, err := resolveAccountSourceID(ctx, engine, exportReportAccount)
		if err != nil {
			return err
		}
		filter.SourceID = &srcID
	}

	messages, err := engine.ListMessages(ctx, filter)
	if err != nil {
		return fmt.Errorf("query messages: %w", err)
	}

	if len(messages) == 0 {
		filterDesc := exportReportDomain
		if filterDesc == "" {
			filterDesc = exportReportSender
		}
		fmt.Fprintf(cmd.OutOrStdout(), "No messages found for %s.\n", filterDesc)
		return nil
	}

	// Write CSV.
	var w *csv.Writer
	if exportReportOutput != "" {
		f, err := os.Create(exportReportOutput)
		if err != nil {
			return fmt.Errorf("create output file: %w", err)
		}
		defer f.Close()
		// BOM for Excel UTF-8 detection.
		f.Write([]byte{0xEF, 0xBB, 0xBF})
		w = csv.NewWriter(f)
		w.Comma = ';' // European CSV standard
	} else {
		w = csv.NewWriter(cmd.OutOrStdout())
	}
	defer w.Flush()

	// Header.
	w.Write([]string{
		"Date", "De", "Sujet", "Direction", "Taille",
		"Pieces jointes", "Labels",
	})

	for _, msg := range messages {
		direction := "recu"
		if exportReportAccount != "" {
			if msg.FromEmail == exportReportAccount {
				direction = "envoye"
			}
		}

		date := ""
		if !msg.SentAt.IsZero() {
			date = msg.SentAt.Format("02/01/2006 15:04")
		}

		labels := ""
		for i, l := range msg.Labels {
			if i > 0 {
				labels += ", "
			}
			labels += l
		}

		attachments := "non"
		if msg.HasAttachments {
			attachments = fmt.Sprintf("oui (%d)", msg.AttachmentCount)
		}

		w.Write([]string{
			date,
			msg.FromEmail,
			msg.Subject,
			direction,
			formatSizeCSV(msg.SizeEstimate),
			attachments,
			labels,
		})
	}

	if exportReportOutput != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Report exported: %s (%d messages)\n", exportReportOutput, len(messages))
	}

	return nil
}

func formatSizeCSV(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	if bytes < 1048576 {
		return fmt.Sprintf("%.1f Ko", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1f Mo", float64(bytes)/1048576)
}

func resolveAccountSourceID(ctx context.Context, engine query.Engine, email string) (int64, error) {
	accounts, err := engine.ListAccounts(ctx)
	if err != nil {
		return 0, fmt.Errorf("list accounts: %w", err)
	}
	for _, acc := range accounts {
		if acc.Identifier == email {
			return acc.ID, nil
		}
	}
	return 0, fmt.Errorf("account %q not found", email)
}
