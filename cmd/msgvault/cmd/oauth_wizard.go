package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var oauthWizardCmd = &cobra.Command{
	Use:   "oauth-wizard [email]",
	Short: "Guided setup for Google OAuth credentials",
	Long: `Assistant interactif pour configurer Google OAuth pas a pas.

Ouvre automatiquement les bonnes pages dans votre navigateur et detecte
le fichier client_secret.json dans vos telechargements.

Exemple :
  msgvault oauth-wizard you@gmail.com
  msgvault oauth-wizard
`,
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE:         runOAuthWizard,
}

func init() {
	rootCmd.AddCommand(oauthWizardCmd)
}

func runOAuthWizard(cmd *cobra.Command, args []string) error {
	reader := bufio.NewReader(os.Stdin)
	out := cmd.OutOrStdout()

	fmt.Fprintln(out)
	fmt.Fprintln(out, "=============================================================")
	fmt.Fprintln(out, "  msgvault — Assistant de configuration Google OAuth")
	fmt.Fprintln(out, "=============================================================")
	fmt.Fprintln(out)

	// Check if already configured.
	if cfg.OAuth.ClientSecrets != "" {
		if _, err := os.Stat(cfg.OAuth.ClientSecrets); err == nil {
			fmt.Fprintf(out, "OAuth deja configure : %s\n\n", cfg.OAuth.ClientSecrets)
			fmt.Fprint(out, "Reconfigurer ? [o/N] : ")
			resp, _ := reader.ReadString('\n')
			resp = strings.ToLower(strings.TrimSpace(resp))
			if resp != "o" && resp != "oui" && resp != "y" && resp != "yes" {
				fmt.Fprintln(out, "Configuration conservee.")
				return nil
			}
			fmt.Fprintln(out)
		}
	}

	// Get email if not provided.
	var email string
	if len(args) > 0 {
		email = args[0]
	} else {
		fmt.Fprint(out, "Votre adresse Gmail : ")
		email, _ = reader.ReadString('\n')
		email = strings.TrimSpace(email)
	}
	if email == "" {
		return fmt.Errorf("adresse email requise")
	}
	fmt.Fprintln(out)

	// ================================================================
	// STEP 1: Create GCP project
	// ================================================================
	fmt.Fprintln(out, "ETAPE 1/4 — Creer un projet Google Cloud")
	fmt.Fprintln(out, strings.Repeat("-", 50))
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  Je vais ouvrir Google Cloud Console.")
	fmt.Fprintln(out, "  Cliquez sur \"Nouveau projet\" et nommez-le \"msgvault\".")
	fmt.Fprintln(out, "  Puis selectionnez-le comme projet actif.")
	fmt.Fprintln(out)

	openBrowser("https://console.cloud.google.com/projectcreate")
	waitForUser(reader, out)

	// ================================================================
	// STEP 2: Enable Gmail API
	// ================================================================
	fmt.Fprintln(out, "ETAPE 2/4 — Activer l'API Gmail")
	fmt.Fprintln(out, strings.Repeat("-", 50))
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  Je vais ouvrir la page de l'API Gmail.")
	fmt.Fprintln(out, "  Cliquez sur le bouton bleu \"ACTIVER\".")
	fmt.Fprintln(out)

	openBrowser("https://console.cloud.google.com/apis/library/gmail.googleapis.com")
	waitForUser(reader, out)

	// ================================================================
	// STEP 3: OAuth consent screen
	// ================================================================
	fmt.Fprintln(out, "ETAPE 3/4 — Configurer l'ecran de consentement")
	fmt.Fprintln(out, strings.Repeat("-", 50))
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  Je vais ouvrir la configuration du consentement OAuth.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  Suivez ces etapes :")
	fmt.Fprintln(out, "    1. User Type : selectionnez \"Externe\" → Creer")
	fmt.Fprintln(out, "    2. Nom de l'application : \"msgvault\"")
	fmt.Fprintln(out, "    3. Email d'assistance : votre adresse email")
	fmt.Fprintln(out, "    4. Coordonnees du developpeur : votre adresse email")
	fmt.Fprintln(out, "    5. Enregistrer et continuer")
	fmt.Fprintln(out, "    6. Scopes : Ajouter → filtrer \"gmail\" → cocher")
	fmt.Fprintln(out, "       \"Gmail API .../auth/gmail.modify\"")
	fmt.Fprintln(out, "       → Mettre a jour → Enregistrer et continuer")
	fmt.Fprintf(out, "    7. Utilisateurs test : Ajouter → %s → Enregistrer\n", email)
	fmt.Fprintln(out)

	openBrowser("https://console.cloud.google.com/apis/credentials/consent")
	waitForUser(reader, out)

	// ================================================================
	// STEP 4: Create credentials + auto-detect download
	// ================================================================
	fmt.Fprintln(out, "ETAPE 4/4 — Creer les identifiants OAuth")
	fmt.Fprintln(out, strings.Repeat("-", 50))
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  Je vais ouvrir la page de creation des identifiants.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  Suivez ces etapes :")
	fmt.Fprintln(out, "    1. Cliquez \"+ CREER DES IDENTIFIANTS\"")
	fmt.Fprintln(out, "       → \"ID client OAuth\"")
	fmt.Fprintln(out, "    2. Type d'application : \"Application de bureau\"")
	fmt.Fprintln(out, "    3. Nom : \"msgvault\" → Creer")
	fmt.Fprintln(out, "    4. Cliquez \"TELECHARGER LE FICHIER JSON\"")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  Je surveille votre dossier Telechargements...")
	fmt.Fprintln(out)

	openBrowser("https://console.cloud.google.com/apis/credentials")

	// Auto-detect the downloaded file.
	secretsPath, err := detectClientSecret(reader, out)
	if err != nil {
		return err
	}

	// ================================================================
	// Validate and install
	// ================================================================
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Validation du fichier...")

	if err := validateClientSecretJSON(secretsPath); err != nil {
		return fmt.Errorf("fichier invalide : %w", err)
	}
	fmt.Fprintln(out, "  Fichier valide !")

	// Copy to msgvault home directory.
	if err := cfg.EnsureHomeDir(); err != nil {
		return fmt.Errorf("creation repertoire: %w", err)
	}

	destPath := filepath.Join(cfg.HomeDir, "client_secret.json")
	if err := copyFile(secretsPath, destPath); err != nil {
		return fmt.Errorf("copie fichier: %w", err)
	}
	fmt.Fprintf(out, "  Copie vers : %s\n", destPath)

	// Update config.
	cfg.OAuth.ClientSecrets = destPath
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("sauvegarde config: %w", err)
	}
	fmt.Fprintf(out, "  Config mise a jour : %s\n", cfg.ConfigFilePath())

	// ================================================================
	// Auto-launch add-account
	// ================================================================
	fmt.Fprintln(out)
	fmt.Fprintln(out, "=============================================================")
	fmt.Fprintln(out, "  Configuration terminee !")
	fmt.Fprintln(out, "=============================================================")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  Prochaine etape — autoriser l'acces a %s :\n\n", email)
	fmt.Fprintf(out, "    msgvault add-account %s\n\n", email)

	fmt.Fprint(out, "Lancer maintenant ? [O/n] : ")
	resp, _ := reader.ReadString('\n')
	resp = strings.ToLower(strings.TrimSpace(resp))
	if resp == "" || resp == "o" || resp == "oui" || resp == "y" || resp == "yes" {
		fmt.Fprintln(out)
		// Execute add-account in the same process.
		addCmd := rootCmd
		addCmd.SetArgs([]string{"add-account", email})
		return addCmd.Execute()
	}

	return nil
}

// detectClientSecret watches the Downloads folder for a client_secret*.json file,
// with fallback to manual path input.
func detectClientSecret(reader *bufio.Reader, out io.Writer) (string, error) {
	downloadsDir := getDownloadsDir()
	if downloadsDir == "" {
		return promptForSecretPath(reader, out)
	}

	// Snapshot existing client_secret files before the user downloads.
	existingFiles := listClientSecretFiles(downloadsDir)
	existingSet := make(map[string]bool)
	for _, f := range existingFiles {
		existingSet[f] = true
	}

	fmt.Fprintf(out, "  (Surveillance de %s)\n\n", downloadsDir)

	// Poll for new file (max 5 minutes).
	timeout := time.After(5 * time.Minute)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Also listen for manual input in background.
	manualCh := make(chan string, 1)
	go func() {
		fmt.Fprint(out, "  Ou collez le chemin du fichier ici : ")
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		// Strip surrounding quotes (drag & drop on Windows adds them).
		line = strings.Trim(line, `"'`)
		if line != "" {
			manualCh <- line
		}
	}()

	for {
		select {
		case path := <-manualCh:
			if _, err := os.Stat(path); err == nil {
				fmt.Fprintf(out, "\n  Fichier recu : %s\n", filepath.Base(path))
				return path, nil
			}
			return "", fmt.Errorf("fichier introuvable : %s", path)

		case <-ticker.C:
			currentFiles := listClientSecretFiles(downloadsDir)
			for _, f := range currentFiles {
				if !existingSet[f] {
					fmt.Fprintf(out, "\n  Fichier detecte : %s\n", filepath.Base(f))
					return f, nil
				}
			}

		case <-timeout:
			return "", fmt.Errorf("delai depasse (5 min). Relancez la commande et telechargez le fichier JSON")
		}
	}
}

// listClientSecretFiles returns paths to client_secret*.json files in a directory.
func listClientSecretFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if strings.HasPrefix(name, "client_secret") && strings.HasSuffix(name, ".json") {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files)
	return files
}

// getDownloadsDir returns the user's Downloads directory.
func getDownloadsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, "Downloads")
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		return dir
	}
	// French Windows.
	dir = filepath.Join(home, "Téléchargements")
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		return dir
	}
	return ""
}

func promptForSecretPath(reader *bufio.Reader, out io.Writer) (string, error) {
	fmt.Fprint(out, "  Chemin vers le fichier client_secret.json : ")
	path, _ := reader.ReadString('\n')
	path = strings.TrimSpace(path)
	path = strings.Trim(path, `"'`)
	if path == "" {
		return "", fmt.Errorf("chemin requis")
	}
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, path[2:])
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", fmt.Errorf("fichier introuvable : %s", path)
	}
	return path, nil
}

// validateClientSecretJSON checks that the file is a valid Google OAuth client secrets file.
func validateClientSecretJSON(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("lecture fichier: %w", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("JSON invalide: %w", err)
	}

	// Must have "installed" or "web" key.
	var clientData json.RawMessage
	var ok bool
	if clientData, ok = raw["installed"]; !ok {
		if clientData, ok = raw["web"]; !ok {
			return fmt.Errorf("le fichier doit contenir une cle \"installed\" ou \"web\"")
		}
	}

	var fields map[string]interface{}
	if err := json.Unmarshal(clientData, &fields); err != nil {
		return fmt.Errorf("structure invalide: %w", err)
	}

	// Check required fields.
	for _, field := range []string{"client_id", "client_secret"} {
		if v, exists := fields[field]; !exists || v == "" {
			return fmt.Errorf("champ manquant: %s", field)
		}
	}

	return nil
}

// waitForUser prints a prompt and waits for Enter.
func waitForUser(reader *bufio.Reader, out io.Writer) {
	fmt.Fprint(out, "  Appuyez sur Entree quand c'est fait...")
	reader.ReadString('\n')
	fmt.Fprintln(out)
}

// openBrowser opens a URL in the default browser.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
