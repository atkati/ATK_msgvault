# ATK_msgvault

[![Go 1.25+](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

> Fork de [wesm/msgvault](https://github.com/wesm/msgvault) par Pascal PORTIER — archiveur email offline avec IA hybride.

Archivez vos emails pour toujours. Recherche, analyse et IA 100% offline.

## Pourquoi ce fork ?

msgvault original est un excellent outil d'archivage email. Ce fork l'enrichit avec des fonctionnalités absentes du projet upstream, en respectant les principes de **souveraineté numérique** et de fonctionnement **100% offline-first** :

- Import Google Takeout (fichiers `.eml`)
- IA hybride local/cloud (Ollama ou API Anthropic/OpenAI)
- Interface web locale embarquée
- Outils d'analyse et d'audit des archives

## Nouvelles fonctionnalités

### Import EML (Google Takeout)

Import de fichiers `.eml` depuis Google Takeout ou toute autre source.

```bash
msgvault import-eml you@gmail.com /chemin/vers/Takeout/Mail/
msgvault import-eml you@gmail.com /chemin/vers/export.zip
msgvault import-eml you@gmail.com message.eml --label google-takeout
```

- Scan recursif de repertoires, support ZIP
- Labels hierarchiques depuis l'arborescence des dossiers
- Deduplication SHA-256, reprise sur interruption

### Interface web locale

Interface web embarquee dans le binaire, zero dependance externe.

```bash
msgvault serve    # puis ouvrir http://localhost:8080/
```

- Tableau de bord avec statistiques
- Navigation par labels et expediteurs (sidebar cliquable)
- Recherche full-text avec rendu HTML des emails
- Mode sombre/clair, design responsive

### IA hybride (Ollama / Cloud)

Couche d'abstraction IA avec double moteur local (Ollama) et cloud (Anthropic/OpenAI).

```bash
# Categorisation automatique
msgvault ai categorize --ai local --model llama3.2 --limit 500

# Extraction d'entites (montants, IBAN, noms, dates...)
msgvault ai extract-entities --ai local --limit 200

# Recherche semantique par embeddings
msgvault ai index --ai local --limit 1000
msgvault ai search "probleme remboursement assurance"

# Resume de fil de conversation
msgvault ai summarize --thread 5479

# Assistant conversationnel (RAG)
msgvault ai chat
> Combien d'emails d'Uber en 2024 ?
> Resume mes echanges avec Groupama
```

Configuration dans `config.toml` :

```toml
[ai]
default_provider = "local"    # "local", "cloud", "off"

[ai.local]
endpoint = "http://localhost:11434"
model = "llama3.2"

[ai.routing]
categorize = "local"          # masse → local
summarize = "cloud"           # synthese fine → cloud
```

### Tags personnalises

Systeme de tags independant des labels Gmail.

```bash
msgvault tag add 42 "SENSIBLE" --color "#ff0000"
msgvault tag list
msgvault tag search "SENSIBLE"
```

### Export analytique

Rapports CSV des echanges avec un domaine ou expediteur.

```bash
msgvault export-report --domain uber.com --output rapport-uber.csv
msgvault export-report --sender support@groupama.fr --after 2023-01-01
```

### Purge assistee

Identification des candidats a la suppression (newsletters, notifications).

```bash
msgvault suggest-purge
msgvault suggest-purge --min-count 50
```

### Audit

Detection d'anomalies et de donnees sensibles dans l'archive.

```bash
# Anomalies : doublons, spoofing, volumes suspects
msgvault ai audit

# Donnees sensibles : IBAN, cartes bancaires, mots de passe, NIR
msgvault audit-sensitive
msgvault audit-sensitive --tag    # auto-tag "SENSIBLE"
```

### Interface web

Interface web complete embarquee dans le binaire (zero dependance).

```bash
msgvault serve    # ouvrir http://localhost:8080/
```

- **Tableau de bord** : statistiques, messages recents
- **Navigation** : sidebar avec labels et expediteurs cliquables
- **Recherche** : texte libre, filtres par label/expediteur/domaine
- **Visualisation** : rendu HTML des emails, pieces jointes
- **Actions IA** : lancer categorisation, NER, indexation, audits depuis le navigateur
- **Parametres** : choix du modele Ollama, provider, endpoint — tout configurable
- **Sync Gmail** : bouton Synchroniser avec suivi de progression
- **Auto-process** : apres chaque sync, categorisation + NER + indexation automatiques
- **Controle** : barres de progression temps reel, ETA, bouton Arreter pour chaque tache
- **Tout lancer** : pipeline complet (categoriser + entites + indexer) en un clic
- **Historique des audits** : rapports persistes en SQLite, consultables a tout moment
- **Barre de notification** : progression des taches visible depuis n'importe quelle page

### OAuth Wizard

Assistant interactif pour configurer Google OAuth.

```bash
msgvault oauth-wizard you@gmail.com
```

Ouvre automatiquement les bonnes pages Google Cloud Console, detecte le fichier `client_secret.json` dans vos telechargements, configure tout automatiquement.

## Installation

Necessite **Go 1.25+** et un compilateur C (pour CGO/SQLite/DuckDB).

```bash
git clone https://github.com/atkati/ATK_msgvault.git
cd ATK_msgvault
make install
```

Sur Windows avec MSYS2 :

```powershell
$env:PATH = "C:\msys64\ucrt64\bin;C:\Program Files\Go\bin;$env:PATH"
$env:CGO_ENABLED = "1"
go build -tags fts5 -o msgvault.exe ./cmd/msgvault/
```

## Demarrage rapide

```bash
msgvault init-db
msgvault oauth-wizard you@gmail.com     # guide OAuth interactif
msgvault sync-full you@gmail.com        # synchroniser vos emails
msgvault serve                          # lancer l'interface web
```

## Commandes ajoutees par ce fork

| Commande | Description |
|----------|-------------|
| `import-eml` | Import de fichiers .eml (Google Takeout, etc.) |
| `oauth-wizard` | Assistant OAuth interactif |
| `tag add/remove/list/search/delete` | Tags personnalises |
| `export-report` | Export CSV par domaine/expediteur |
| `suggest-purge` | Detection newsletters/notifications |
| `ai categorize` | Classification IA des emails |
| `ai extract-entities` | Extraction d'entites nommees (NER) |
| `ai index` | Generation d'embeddings |
| `ai search` | Recherche semantique |
| `ai summarize` | Resume de threads |
| `ai chat` | Assistant conversationnel RAG |
| `ai audit` | Detection d'anomalies |
| `ai find-entity` | Recherche dans les entites extraites |
| `audit-sensitive` | Scan de donnees sensibles |

## Architecture

Binaire unique Go. Pas de dependance externe (sauf Ollama pour l'IA locale).

```
internal/
├── ai/          # Interface AIProvider + implementations Ollama/Cloud
├── api/         # Serveur HTTP REST (chi), TaskManager, parametres
├── importer/    # Import EML, MBOX, Apple Mail
├── store/       # SQLite (messages, tags, categories, entites, embeddings)
├── web/         # Interface web embarquee (HTML/CSS/JS via embed.FS)
├── query/       # DuckDB/Parquet analytics
└── ...          # OAuth, sync, TUI, MCP, etc.
```

## API REST

En plus de l'API upstream (messages, search, aggregates), le fork ajoute :

| Endpoint | Description |
|----------|-------------|
| `GET /api/v1/settings` | Lire la configuration |
| `PUT /api/v1/settings` | Modifier la config IA (provider, modele) |
| `GET /api/v1/ollama/models` | Lister les modeles Ollama disponibles |
| `POST /api/v1/sync-web` | Lancer une sync Gmail |
| `GET /api/v1/tasks` | Lister les taches en cours |
| `GET /api/v1/tasks/{id}` | Statut d'une tache |
| `POST /api/v1/tasks/{type}` | Lancer une tache IA |
| `DELETE /api/v1/tasks/{id}` | Arreter une tache en cours |
| `POST /api/v1/tasks/run-all` | Lancer le pipeline complet (cat+ner+idx) |
| `GET /api/v1/audit-reports` | Historique des rapports d'audit |
| `GET /api/v1/audit-reports/{id}` | Detail d'un rapport d'audit |

Types de taches : `categorize`, `extract-entities`, `index`, `audit`, `audit-sensitive`

Voir [docs/api.md](docs/api.md) pour l'API complete.

## Configuration locale FR

```toml
[display]
locale = "fr"
date_format = "02/01/2006 15:04"
```

Recherche accent-insensitive : "resume" trouve aussi "resume" (FTS5 unicode61).

## Upstream

Ce fork est base sur [msgvault](https://github.com/wesm/msgvault) v0.11.0 par Wes McKinney.
Toutes les fonctionnalites upstream (Gmail sync, IMAP, TUI, MCP, DuckDB) sont preservees.

## Licence

MIT. Voir [LICENSE](LICENSE).
