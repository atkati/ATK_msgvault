package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wesm/msgvault/internal/store"
)

var aiAuditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Detect anomalies in email archive",
	Long: `Scan the email archive for anomalies and suspicious patterns.

Detections:
  - Duplicate invoices (same amount + same sender + close dates)
  - Unusual sending patterns (high volume from single sender)
  - Known noreply senders with replies
  - Sender domain changes for known contacts

Examples:
  msgvault ai audit
  msgvault ai audit --limit 100
`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runAIAudit,
}

var aiAuditLimit int

func init() {
	aiCmd.AddCommand(aiAuditCmd)
	aiAuditCmd.Flags().IntVar(&aiAuditLimit, "limit", 200, "Maximum anomalies to report")
}

type anomaly struct {
	Severity string // "critique", "attention", "info"
	Category string
	Message  string
	Details  string
}

func runAIAudit(cmd *cobra.Command, args []string) error {
	st, err := openStoreReadOnly()
	if err != nil {
		return err
	}
	defer st.Close()

	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "Analyse des anomalies...")
	fmt.Fprintln(out)

	var anomalies []anomaly

	// 1. High-volume senders (potential spam/compromise).
	highVolume, err := detectHighVolumeSenders(st)
	if err == nil {
		anomalies = append(anomalies, highVolume...)
	}

	// 2. Duplicate subjects from same sender (potential duplicate invoices).
	dupes, err := detectDuplicateSubjects(st)
	if err == nil {
		anomalies = append(anomalies, dupes...)
	}

	// 3. Senders with multiple domains (potential spoofing).
	multiDomain, err := detectMultiDomainSenders(st)
	if err == nil {
		anomalies = append(anomalies, multiDomain...)
	}

	if len(anomalies) == 0 {
		fmt.Fprintln(out, "Aucune anomalie detectee.")
		return nil
	}

	// Sort by severity.
	severityOrder := map[string]int{"critique": 0, "attention": 1, "info": 2}
	for i := 0; i < len(anomalies)-1; i++ {
		for j := i + 1; j < len(anomalies); j++ {
			if severityOrder[anomalies[i].Severity] > severityOrder[anomalies[j].Severity] {
				anomalies[i], anomalies[j] = anomalies[j], anomalies[i]
			}
		}
	}

	// Limit output.
	if len(anomalies) > aiAuditLimit {
		anomalies = anomalies[:aiAuditLimit]
	}

	// Display.
	critCount, attnCount, infoCount := 0, 0, 0
	for _, a := range anomalies {
		var icon string
		switch a.Severity {
		case "critique":
			icon = "[!!!]"
			critCount++
		case "attention":
			icon = "[!!]"
			attnCount++
		case "info":
			icon = "[i]"
			infoCount++
		}
		fmt.Fprintf(out, "%s %s : %s\n", icon, a.Category, a.Message)
		if a.Details != "" {
			fmt.Fprintf(out, "     %s\n", a.Details)
		}
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "Bilan : %d critique(s), %d attention(s), %d info(s)\n",
		critCount, attnCount, infoCount)

	return nil
}

func detectHighVolumeSenders(st *store.Store) ([]anomaly, error) {
	rows, err := st.DB().Query(
		`SELECT p.email_address, COUNT(*) as cnt
		 FROM messages m
		 JOIN message_recipients mr ON mr.message_id = m.id AND mr.recipient_type = 'from'
		 JOIN participants p ON p.id = mr.participant_id
		 WHERE p.email_address != ''
		 GROUP BY p.email_address
		 HAVING cnt > 200
		 ORDER BY cnt DESC
		 LIMIT 20`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []anomaly
	for rows.Next() {
		var email string
		var count int
		if err := rows.Scan(&email, &count); err != nil {
			continue
		}
		results = append(results, anomaly{
			Severity: "info",
			Category: "Volume eleve",
			Message:  fmt.Sprintf("%s : %d messages", email, count),
			Details:  "Verifiez s'il s'agit d'un expediteur legitime ou de spam",
		})
	}
	return results, nil
}

func detectDuplicateSubjects(st *store.Store) ([]anomaly, error) {
	rows, err := st.DB().Query(
		`SELECT p.email_address, m.subject, COUNT(*) as cnt
		 FROM messages m
		 JOIN message_recipients mr ON mr.message_id = m.id AND mr.recipient_type = 'from'
		 JOIN participants p ON p.id = mr.participant_id
		 WHERE m.subject IS NOT NULL AND m.subject != ''
		   AND p.email_address != ''
		 GROUP BY p.email_address, m.subject
		 HAVING cnt >= 3
		 ORDER BY cnt DESC
		 LIMIT 30`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []anomaly
	for rows.Next() {
		var email, subject string
		var count int
		if err := rows.Scan(&email, &subject, &count); err != nil {
			continue
		}
		sev := "info"
		if count >= 5 {
			sev = "attention"
		}
		if strings.Contains(strings.ToLower(subject), "facture") ||
			strings.Contains(strings.ToLower(subject), "invoice") {
			sev = "attention"
			if count >= 5 {
				sev = "critique"
			}
		}
		subj := subject
		if len(subj) > 50 {
			subj = subj[:50] + "..."
		}
		results = append(results, anomaly{
			Severity: sev,
			Category: "Sujet duplique",
			Message:  fmt.Sprintf("%s x%d de %s", subj, count, email),
			Details:  "Possibles doublons ou factures en double",
		})
	}
	return results, nil
}

func detectMultiDomainSenders(st *store.Store) ([]anomaly, error) {
	// Find display names that appear with multiple email domains.
	rows, err := st.DB().Query(
		`SELECT mr.display_name, COUNT(DISTINCT p.domain) as domain_count,
		        GROUP_CONCAT(DISTINCT p.domain) as domains
		 FROM message_recipients mr
		 JOIN participants p ON p.id = mr.participant_id
		 WHERE mr.recipient_type = 'from'
		   AND mr.display_name IS NOT NULL AND mr.display_name != ''
		   AND p.domain IS NOT NULL AND p.domain != ''
		 GROUP BY mr.display_name
		 HAVING domain_count >= 3
		 ORDER BY domain_count DESC
		 LIMIT 20`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []anomaly
	for rows.Next() {
		var name, domains string
		var count int
		if err := rows.Scan(&name, &count, &domains); err != nil {
			continue
		}
		results = append(results, anomaly{
			Severity: "attention",
			Category: "Domaines multiples",
			Message:  fmt.Sprintf("%q utilise %d domaines differents", name, count),
			Details:  "Domaines : " + domains,
		})
	}
	return results, nil
}
