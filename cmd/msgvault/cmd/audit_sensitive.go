package cmd

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

var auditSensitiveCmd = &cobra.Command{
	Use:   "audit-sensitive",
	Short: "Scan emails for sensitive data (IBAN, passwords, card numbers, etc.)",
	Long: `Scan the email archive for sensitive personal data using regex patterns.

Detects:
  - IBAN numbers (FR76...)
  - Credit card numbers (Luhn validation)
  - French social security numbers (NIR)
  - SIRET/SIREN numbers
  - Passwords in clear text ("mot de passe :", "password:", etc.)
  - Phone numbers

Examples:
  msgvault audit-sensitive
  msgvault audit-sensitive --tag     # Auto-tag messages with "SENSIBLE"
  msgvault audit-sensitive --limit 5000
`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runAuditSensitive,
}

var (
	auditSensTag   bool
	auditSensLimit int
)

func init() {
	rootCmd.AddCommand(auditSensitiveCmd)
	auditSensitiveCmd.Flags().BoolVar(&auditSensTag, "tag", false, "Auto-tag detected messages with 'SENSIBLE'")
	auditSensitiveCmd.Flags().IntVar(&auditSensLimit, "limit", 10000, "Maximum messages to scan")
}

type sensitiveMatch struct {
	MessageID int64
	Type      string
	Value     string
	Context   string
}

// Patterns.
var (
	ibanRE     = regexp.MustCompile(`\b[A-Z]{2}\d{2}\s?\d{4}\s?\d{4}\s?\d{4}\s?\d{4}\s?\d{3,4}\b`)
	cardRE     = regexp.MustCompile(`\b\d{4}[\s-]?\d{4}[\s-]?\d{4}[\s-]?\d{4}\b`)
	nirRE      = regexp.MustCompile(`\b[12]\s?\d{2}\s?\d{2}\s?\d{2}\s?\d{3}\s?\d{3}\s?\d{2}\b`)
	siretRE    = regexp.MustCompile(`\b\d{3}\s?\d{3}\s?\d{3}\s?\d{5}\b`)
	sirenRE    = regexp.MustCompile(`\b\d{3}\s?\d{3}\s?\d{3}\b`)
	phoneRE    = regexp.MustCompile(`\b(?:(?:\+33|0033|0)\s?[1-9])(?:[\s.-]?\d{2}){4}\b`)
	passwordRE = regexp.MustCompile(`(?i)(?:mot de passe|password|mdp|pwd)\s*[:=]\s*\S+`)
)

func runAuditSensitive(cmd *cobra.Command, args []string) error {
	st, err := openStoreReadWrite()
	if err != nil {
		return err
	}
	defer st.Close()

	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "Scan des donnees sensibles...")

	// Query messages with body text (direct PK lookup per message is too slow;
	// scan message_bodies in batches).
	rows, err := st.DB().Query(
		`SELECT mb.message_id, COALESCE(mb.body_text,'')
		 FROM message_bodies mb
		 ORDER BY mb.message_id
		 LIMIT ?`, auditSensLimit,
	)
	if err != nil {
		return fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	var matches []sensitiveMatch
	var scanned int

	for rows.Next() {
		var msgID int64
		var body string
		if err := rows.Scan(&msgID, &body); err != nil {
			continue
		}
		scanned++

		// Scan body text only (subject is too short for sensitive data,
		// and querying it separately per row is too slow).
		found := scanSensitiveText(msgID, body)
		matches = append(matches, found...)

		if scanned%1000 == 0 {
			fmt.Fprintf(out, "  %d messages scannes...\n", scanned)
		}
	}

	if len(matches) == 0 {
		fmt.Fprintf(out, "\nAucune donnee sensible detectee dans %d messages.\n", scanned)
		return nil
	}

	// Display results.
	fmt.Fprintf(out, "\n%d donnees sensibles detectees dans %d messages :\n\n", len(matches), scanned)
	fmt.Fprintf(out, "%-8s  %-15s  %-30s  %s\n", "MSG ID", "TYPE", "VALEUR", "CONTEXTE")
	fmt.Fprintf(out, "%-8s  %-15s  %-30s  %s\n",
		strings.Repeat("-", 8), strings.Repeat("-", 15), strings.Repeat("-", 30), strings.Repeat("-", 30))

	taggedMsgs := make(map[int64]bool)
	for _, m := range matches {
		val := m.Value
		// Mask sensitive values for display.
		if m.Type == "IBAN" || m.Type == "Carte bancaire" || m.Type == "NIR" {
			val = maskValue(val)
		}
		ctx := m.Context
		if len(ctx) > 30 {
			ctx = ctx[:30] + "..."
		}
		fmt.Fprintf(out, "%-8d  %-15s  %-30s  %s\n", m.MessageID, m.Type, val, ctx)
		taggedMsgs[m.MessageID] = true
	}

	// Auto-tag if requested.
	if auditSensTag && len(taggedMsgs) > 0 {
		fmt.Fprintf(out, "\nApplication du tag 'SENSIBLE' a %d messages...\n", len(taggedMsgs))
		tagged := 0
		for msgID := range taggedMsgs {
			if err := st.TagMessage(msgID, "SENSIBLE"); err != nil {
				logger.Warn("tag failed", "message_id", msgID, "error", err)
				continue
			}
			tagged++
		}
		fmt.Fprintf(out, "%d messages tagges.\n", tagged)
	}

	return nil
}

func scanSensitiveText(msgID int64, text string) []sensitiveMatch {
	var matches []sensitiveMatch

	for _, m := range ibanRE.FindAllStringIndex(text, -1) {
		val := text[m[0]:m[1]]
		matches = append(matches, sensitiveMatch{
			MessageID: msgID, Type: "IBAN", Value: val,
			Context: extractContext(text, m[0], m[1]),
		})
	}

	for _, m := range cardRE.FindAllStringIndex(text, -1) {
		val := strings.ReplaceAll(text[m[0]:m[1]], " ", "")
		val = strings.ReplaceAll(val, "-", "")
		if len(val) == 16 && luhnCheck(val) {
			matches = append(matches, sensitiveMatch{
				MessageID: msgID, Type: "Carte bancaire", Value: text[m[0]:m[1]],
				Context: extractContext(text, m[0], m[1]),
			})
		}
	}

	for _, m := range nirRE.FindAllStringIndex(text, -1) {
		matches = append(matches, sensitiveMatch{
			MessageID: msgID, Type: "NIR", Value: text[m[0]:m[1]],
			Context: extractContext(text, m[0], m[1]),
		})
	}

	for _, m := range siretRE.FindAllStringIndex(text, -1) {
		matches = append(matches, sensitiveMatch{
			MessageID: msgID, Type: "SIRET", Value: text[m[0]:m[1]],
			Context: extractContext(text, m[0], m[1]),
		})
	}

	for _, m := range phoneRE.FindAllStringIndex(text, -1) {
		matches = append(matches, sensitiveMatch{
			MessageID: msgID, Type: "Telephone", Value: text[m[0]:m[1]],
			Context: extractContext(text, m[0], m[1]),
		})
	}

	for _, m := range passwordRE.FindAllStringIndex(text, -1) {
		matches = append(matches, sensitiveMatch{
			MessageID: msgID, Type: "Mot de passe", Value: text[m[0]:m[1]],
			Context: extractContext(text, m[0], m[1]),
		})
	}

	return matches
}

func extractContext(text string, start, end int) string {
	ctxStart := start - 30
	if ctxStart < 0 {
		ctxStart = 0
	}
	ctxEnd := end + 30
	if ctxEnd > len(text) {
		ctxEnd = len(text)
	}
	ctx := strings.ReplaceAll(text[ctxStart:ctxEnd], "\n", " ")
	return strings.TrimSpace(ctx)
}

func maskValue(val string) string {
	clean := strings.ReplaceAll(val, " ", "")
	if len(clean) <= 6 {
		return val
	}
	return clean[:4] + strings.Repeat("*", len(clean)-6) + clean[len(clean)-2:]
}

// luhnCheck validates a number string using the Luhn algorithm.
func luhnCheck(number string) bool {
	sum := 0
	nDigits := len(number)
	parity := nDigits % 2
	for i := 0; i < nDigits; i++ {
		d := int(number[i] - '0')
		if d < 0 || d > 9 {
			return false
		}
		if i%2 == parity {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return sum%10 == 0
}
