package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wesm/msgvault/internal/store"
)

var tagCmd = &cobra.Command{
	Use:   "tag",
	Short: "Manage user tags on messages",
	Long: `Manage user tags independently of Gmail/IMAP labels.

Tags are personal annotations that persist across syncs and cache rebuilds.

Examples:
  msgvault tag add 42 "SENSIBLE"
  msgvault tag add 42 "BetterVTC" --color "#ff6600"
  msgvault tag remove 42 "SENSIBLE"
  msgvault tag list
  msgvault tag search "SENSIBLE"
  msgvault tag delete "OldTag"
`,
}

var tagColor string

var tagAddCmd = &cobra.Command{
	Use:   "add <message_id> <tag>",
	Short: "Add a tag to a message",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		messageID, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid message ID: %w", err)
		}
		tagName := strings.TrimSpace(args[1])

		st, err := openStoreReadWrite()
		if err != nil {
			return err
		}
		defer st.Close()

		// Create tag with color if provided.
		if tagColor != "" {
			if _, err := st.CreateUserTag(tagName, tagColor); err != nil {
				return fmt.Errorf("create tag: %w", err)
			}
		}

		if err := st.TagMessage(messageID, tagName); err != nil {
			return fmt.Errorf("tag message: %w", err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Tag %q added to message %d.\n", tagName, messageID)
		return nil
	},
}

var tagRemoveCmd = &cobra.Command{
	Use:   "remove <message_id> <tag>",
	Short: "Remove a tag from a message",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		messageID, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid message ID: %w", err)
		}
		tagName := strings.TrimSpace(args[1])

		st, err := openStoreReadWrite()
		if err != nil {
			return err
		}
		defer st.Close()

		if err := st.UntagMessage(messageID, tagName); err != nil {
			return fmt.Errorf("untag message: %w", err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Tag %q removed from message %d.\n", tagName, messageID)
		return nil
	},
}

var tagListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all user tags",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		st, err := openStoreReadOnly()
		if err != nil {
			return err
		}
		defer st.Close()

		tags, err := st.ListUserTagsWithCount()
		if err != nil {
			return fmt.Errorf("list tags: %w", err)
		}

		if len(tags) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No user tags.")
			return nil
		}

		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "%-30s  %8s  %s\n", "TAG", "MESSAGES", "COLOR")
		fmt.Fprintf(out, "%-30s  %8s  %s\n", strings.Repeat("-", 30), strings.Repeat("-", 8), strings.Repeat("-", 7))
		for _, t := range tags {
			color := t.Color
			if color == "" {
				color = "-"
			}
			fmt.Fprintf(out, "%-30s  %8d  %s\n", t.Name, t.MessageCount, color)
		}
		return nil
	},
}

var tagSearchCmd = &cobra.Command{
	Use:   "search <tag>",
	Short: "Search messages with a given tag",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		tagName := strings.TrimSpace(args[0])

		st, err := openStoreReadOnly()
		if err != nil {
			return err
		}
		defer st.Close()

		ids, err := st.SearchMessagesByTag(tagName)
		if err != nil {
			return fmt.Errorf("search by tag: %w", err)
		}

		out := cmd.OutOrStdout()
		if len(ids) == 0 {
			fmt.Fprintf(out, "No messages with tag %q.\n", tagName)
			return nil
		}

		fmt.Fprintf(out, "Messages with tag %q (%d):\n", tagName, len(ids))
		for _, id := range ids {
			fmt.Fprintf(out, "  %d\n", id)
		}
		return nil
	},
}

var tagDeleteCmd = &cobra.Command{
	Use:   "delete <tag>",
	Short: "Delete a user tag entirely",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		tagName := strings.TrimSpace(args[0])

		st, err := openStoreReadWrite()
		if err != nil {
			return err
		}
		defer st.Close()

		if err := st.DeleteUserTag(tagName); err != nil {
			return fmt.Errorf("delete tag: %w", err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Tag %q deleted.\n", tagName)
		return nil
	},
}

func openStoreReadWrite() (*store.Store, error) {
	dbPath := cfg.DatabaseDSN()
	st, err := store.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := st.InitSchema(); err != nil {
		st.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return st, nil
}

func openStoreReadOnly() (*store.Store, error) {
	return openStoreReadWrite()
}

func init() {
	rootCmd.AddCommand(tagCmd)
	tagCmd.AddCommand(tagAddCmd)
	tagCmd.AddCommand(tagRemoveCmd)
	tagCmd.AddCommand(tagListCmd)
	tagCmd.AddCommand(tagSearchCmd)
	tagCmd.AddCommand(tagDeleteCmd)

	tagAddCmd.Flags().StringVar(&tagColor, "color", "", "Tag color (hex, e.g. #ff6600)")
}
