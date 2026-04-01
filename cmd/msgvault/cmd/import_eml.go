package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/wesm/msgvault/internal/importer"
	"github.com/wesm/msgvault/internal/store"
)

var (
	importEmlSourceType         string
	importEmlLabel              string
	importEmlNoResume           bool
	importEmlCheckpointInterval int
	importEmlNoAttachments      bool
	importEmlAttachmentsDir     string
)

var importEmlCmd = &cobra.Command{
	Use:   "import-eml <identifier> <path>",
	Short: "Import .eml files into msgvault",
	Long: `Import .eml files (RFC 5322) into msgvault.

The path may be:
  - a single .eml file
  - a directory (scanned recursively for .eml files)
  - a .zip archive containing .eml files

For directory imports, the directory structure becomes labels.
Example: a file at Work/Uber/bills/msg.eml produces labels
"Work", "Work/Uber", "Work/Uber/bills".

Examples:
  msgvault import-eml you@gmail.com /path/to/takeout/Mail/
  msgvault import-eml you@gmail.com /path/to/export.zip
  msgvault import-eml you@gmail.com /path/to/single-message.eml
  msgvault import-eml you@gmail.com ./emails --label google-takeout
`,
	Args:         cobra.ExactArgs(2),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		identifier := args[0]
		emlPath := args[1]

		// Handle Ctrl+C gracefully (save checkpoint and exit cleanly).
		ctx, cancel := context.WithCancel(cmd.Context())
		defer cancel()
		sigChan := make(chan os.Signal, 2)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		done := make(chan struct{})
		defer func() {
			close(done)
			signal.Stop(sigChan)
			for {
				select {
				case <-sigChan:
				default:
					return
				}
			}
		}()
		go func() {
			signals := 0
			for {
				select {
				case <-done:
					return
				case <-sigChan:
					select {
					case <-done:
						return
					default:
					}
					signals++
					if signals == 1 {
						_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "\nInterrupted. Saving checkpoint...")
						cancel()
						continue
					}
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Interrupted again. Exiting immediately.")
					os.Exit(130)
				}
			}
		}()

		dbPath := cfg.DatabaseDSN()
		st, err := store.Open(dbPath)
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer func() { _ = st.Close() }()

		if err := st.InitSchema(); err != nil {
			return fmt.Errorf("init schema: %w", err)
		}

		attachmentsDir := cfg.AttachmentsDir()
		if importEmlNoAttachments {
			attachmentsDir = ""
		}
		if importEmlAttachmentsDir != "" {
			attachmentsDir = importEmlAttachmentsDir
		}

		summary, err := importer.ImportEml(ctx, st, emlPath, importer.EmlImportOptions{
			SourceType:         importEmlSourceType,
			Identifier:         identifier,
			Label:              importEmlLabel,
			NoResume:           importEmlNoResume,
			CheckpointInterval: importEmlCheckpointInterval,
			AttachmentsDir:     attachmentsDir,
			Logger:             logger,
		})
		if err != nil {
			return err
		}

		out := cmd.OutOrStdout()
		if ctx.Err() != nil {
			_, _ = fmt.Fprintln(out, "Import interrupted. Run again to resume.")
		} else if summary.Errors > 0 {
			_, _ = fmt.Fprintln(out, "Import complete (with errors).")
		} else {
			_, _ = fmt.Fprintln(out, "Import complete.")
		}
		_, _ = fmt.Fprintf(out, "  Files:          %d\n", summary.FilesTotal)
		_, _ = fmt.Fprintf(out, "  Processed:      %d messages\n", summary.MessagesProcessed)
		_, _ = fmt.Fprintf(out, "  Added:          %d messages\n", summary.MessagesAdded)
		_, _ = fmt.Fprintf(out, "  Updated:        %d messages\n", summary.MessagesUpdated)
		_, _ = fmt.Fprintf(out, "  Skipped:        %d messages\n", summary.MessagesSkipped)
		_, _ = fmt.Fprintf(out, "  Errors:         %d\n", summary.Errors)
		_, _ = fmt.Fprintf(out, "  Duration:       %s\n", summary.Duration.Round(time.Millisecond))

		if ctx.Err() == nil && summary.HardErrors {
			return fmt.Errorf("import completed with %d errors", summary.Errors)
		}
		if ctx.Err() != nil {
			return context.Canceled
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(importEmlCmd)

	importEmlCmd.Flags().StringVar(&importEmlSourceType, "source-type", "eml", "Source type to record in the database")
	importEmlCmd.Flags().StringVar(&importEmlLabel, "label", "", "Label to apply to all imported messages (in addition to path-derived labels)")
	importEmlCmd.Flags().BoolVar(&importEmlNoResume, "no-resume", false, "Do not resume from an interrupted import")
	importEmlCmd.Flags().IntVar(&importEmlCheckpointInterval, "checkpoint-interval", 200, "Save progress every N messages")
	importEmlCmd.Flags().BoolVar(&importEmlNoAttachments, "no-attachments", false, "Do not store attachments")
	importEmlCmd.Flags().StringVar(&importEmlAttachmentsDir, "attachments-dir", "", "Custom attachments directory")
}
