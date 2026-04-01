package importer

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesm/msgvault/internal/store"
	"github.com/wesm/msgvault/internal/testutil/email"
)

// mkEmlFile creates a .eml file with the given MIME bytes.
func mkEmlFile(t *testing.T, dir, name string, raw []byte) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatalf("write eml: %v", err)
	}
}

func TestImportEml_SingleFile(t *testing.T) {
	st, tmp := openTestStore(t)

	raw := email.NewMessage().
		From("Alice <alice@example.com>").
		To("Bob <bob@example.com>").
		Subject("Hello").
		Date("Mon, 01 Jan 2024 12:00:00 +0000").
		Header("Message-ID", "<msg1@example.com>").
		Body("Hi Bob.\n").
		Bytes()

	emlPath := filepath.Join(tmp, "test.eml")
	if err := os.WriteFile(emlPath, raw, 0600); err != nil {
		t.Fatalf("write eml: %v", err)
	}

	summary, err := ImportEml(
		context.Background(), st, emlPath, EmlImportOptions{
			Identifier:         "alice@example.com",
			NoResume:           true,
			CheckpointInterval: 1,
		},
	)
	if err != nil {
		t.Fatalf("ImportEml: %v", err)
	}
	if summary.MessagesAdded != 1 {
		t.Fatalf("MessagesAdded = %d, want 1", summary.MessagesAdded)
	}
	if summary.FilesTotal != 1 {
		t.Fatalf("FilesTotal = %d, want 1", summary.FilesTotal)
	}

	var msgCount int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&msgCount); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if msgCount != 1 {
		t.Fatalf("msgCount = %d, want 1", msgCount)
	}
}

func TestImportEml_Directory(t *testing.T) {
	st, tmp := openTestStore(t)

	root := filepath.Join(tmp, "emails")

	raw1 := email.NewMessage().
		From("Alice <alice@example.com>").
		To("Bob <bob@example.com>").
		Subject("Hello").
		Date("Mon, 01 Jan 2024 12:00:00 +0000").
		Header("Message-ID", "<msg1@example.com>").
		Body("Hi Bob.\n").
		Bytes()

	raw2 := email.NewMessage().
		From("Bob <bob@example.com>").
		To("Alice <alice@example.com>").
		Subject("Re: Hello").
		Date("Mon, 01 Jan 2024 13:00:00 +0000").
		Header("Message-ID", "<msg2@example.com>").
		Header("In-Reply-To", "<msg1@example.com>").
		Body("Reply.\n").
		Bytes()

	raw3 := email.NewMessage().
		From("Charlie <charlie@example.com>").
		To("Alice <alice@example.com>").
		Subject("Other").
		Date("Tue, 02 Jan 2024 10:00:00 +0000").
		Header("Message-ID", "<msg3@example.com>").
		Body("Something else.\n").
		Bytes()

	mkEmlFile(t, root, "msg1.eml", raw1)
	mkEmlFile(t, filepath.Join(root, "subdir"), "msg2.eml", raw2)
	mkEmlFile(t, filepath.Join(root, "subdir", "nested"), "msg3.eml", raw3)

	summary, err := ImportEml(
		context.Background(), st, root, EmlImportOptions{
			Identifier:         "alice@example.com",
			NoResume:           true,
			CheckpointInterval: 1,
		},
	)
	if err != nil {
		t.Fatalf("ImportEml: %v", err)
	}
	if summary.MessagesAdded != 3 {
		t.Fatalf("MessagesAdded = %d, want 3", summary.MessagesAdded)
	}
	if summary.FilesTotal != 3 {
		t.Fatalf("FilesTotal = %d, want 3", summary.FilesTotal)
	}
}

func TestImportEml_DirectoryLabels(t *testing.T) {
	st, tmp := openTestStore(t)

	root := filepath.Join(tmp, "emails")

	raw := email.NewMessage().
		From("Alice <alice@example.com>").
		To("Bob <bob@example.com>").
		Subject("Facture").
		Date("Mon, 01 Jan 2024 12:00:00 +0000").
		Header("Message-ID", "<facture@example.com>").
		Body("Voici la facture.\n").
		Bytes()

	mkEmlFile(t, filepath.Join(root, "Travail", "Uber"), "facture.eml", raw)

	summary, err := ImportEml(
		context.Background(), st, root, EmlImportOptions{
			Identifier:         "alice@example.com",
			NoResume:           true,
			CheckpointInterval: 1,
		},
	)
	if err != nil {
		t.Fatalf("ImportEml: %v", err)
	}
	if summary.MessagesAdded != 1 {
		t.Fatalf("MessagesAdded = %d, want 1", summary.MessagesAdded)
	}

	// Verify labels: "Travail" and "Travail/Uber"
	var labelNames []string
	rows, err := st.DB().Query(`
		SELECT l.name FROM message_labels ml
		JOIN labels l ON l.id = ml.label_id
		JOIN messages m ON m.id = ml.message_id
		ORDER BY l.name
	`)
	if err != nil {
		t.Fatalf("query labels: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan label: %v", err)
		}
		labelNames = append(labelNames, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows error: %v", err)
	}

	if len(labelNames) != 2 {
		t.Fatalf("labels = %v, want 2 labels", labelNames)
	}
	if labelNames[0] != "Travail" || labelNames[1] != "Travail/Uber" {
		t.Fatalf("labels = %v, want [Travail, Travail/Uber]", labelNames)
	}
}

func TestImportEml_Dedup(t *testing.T) {
	st, tmp := openTestStore(t)

	root := filepath.Join(tmp, "emails")

	raw := email.NewMessage().
		From("Alice <alice@example.com>").
		To("Bob <bob@example.com>").
		Subject("Hello").
		Date("Mon, 01 Jan 2024 12:00:00 +0000").
		Header("Message-ID", "<msg1@example.com>").
		Body("Hi Bob.\n").
		Bytes()

	// Same message in two different directories.
	mkEmlFile(t, filepath.Join(root, "Inbox"), "msg.eml", raw)
	mkEmlFile(t, filepath.Join(root, "Archive"), "msg.eml", raw)

	summary, err := ImportEml(
		context.Background(), st, root, EmlImportOptions{
			Identifier:         "alice@example.com",
			NoResume:           true,
			CheckpointInterval: 1,
		},
	)
	if err != nil {
		t.Fatalf("ImportEml: %v", err)
	}

	// Only one message should be created (dedup by content hash).
	if summary.MessagesAdded != 1 {
		t.Fatalf("MessagesAdded = %d, want 1", summary.MessagesAdded)
	}

	var msgCount int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&msgCount); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if msgCount != 1 {
		t.Fatalf("msgCount = %d, want 1", msgCount)
	}

	// Both directory labels should be applied.
	var labelNames []string
	rows, err := st.DB().Query(`
		SELECT l.name FROM message_labels ml
		JOIN labels l ON l.id = ml.label_id
		ORDER BY l.name
	`)
	if err != nil {
		t.Fatalf("query labels: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan label: %v", err)
		}
		labelNames = append(labelNames, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows error: %v", err)
	}

	if len(labelNames) != 2 {
		t.Fatalf("labels = %v, want 2 labels", labelNames)
	}
	if labelNames[0] != "Archive" || labelNames[1] != "Inbox" {
		t.Fatalf("labels = %v, want [Archive, Inbox]", labelNames)
	}
}

func TestImportEml_ZipArchive(t *testing.T) {
	st, tmp := openTestStore(t)

	raw := email.NewMessage().
		From("Alice <alice@example.com>").
		To("Bob <bob@example.com>").
		Subject("From ZIP").
		Date("Mon, 01 Jan 2024 12:00:00 +0000").
		Header("Message-ID", "<zip1@example.com>").
		Body("Extracted from zip.\n").
		Bytes()

	// Create a ZIP file with an .eml inside a subdirectory.
	zipPath := filepath.Join(tmp, "export.zip")
	zf, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	zw := zip.NewWriter(zf)
	w, err := zw.Create("Inbox/msg.eml")
	if err != nil {
		t.Fatalf("zip create entry: %v", err)
	}
	if _, err := w.Write(raw); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close writer: %v", err)
	}
	if err := zf.Close(); err != nil {
		t.Fatalf("zip close file: %v", err)
	}

	summary, err := ImportEml(
		context.Background(), st, zipPath, EmlImportOptions{
			Identifier:         "alice@example.com",
			NoResume:           true,
			CheckpointInterval: 1,
		},
	)
	if err != nil {
		t.Fatalf("ImportEml: %v", err)
	}
	if summary.MessagesAdded != 1 {
		t.Fatalf("MessagesAdded = %d, want 1", summary.MessagesAdded)
	}
}

func TestImportEml_Resume(t *testing.T) {
	st, tmp := openTestStore(t)

	root := filepath.Join(tmp, "emails")

	raw1 := email.NewMessage().
		From("Alice <alice@example.com>").
		Subject("One").
		Body("first\n").
		Bytes()
	raw2 := email.NewMessage().
		From("Alice <alice@example.com>").
		Subject("Two").
		Body("second\n").
		Bytes()

	mkEmlFile(t, root, "a.eml", raw1)
	mkEmlFile(t, root, "b.eml", raw2)

	// First import: run to completion.
	_, err := ImportEml(
		context.Background(), st, root, EmlImportOptions{
			Identifier:         "alice@example.com",
			NoResume:           true,
			CheckpointInterval: 1,
		},
	)
	if err != nil {
		t.Fatalf("ImportEml (first): %v", err)
	}

	// Second import with resume: should skip already-imported.
	summary2, err := ImportEml(
		context.Background(), st, root, EmlImportOptions{
			Identifier:         "alice@example.com",
			NoResume:           false,
			CheckpointInterval: 1,
		},
	)
	if err != nil {
		t.Fatalf("ImportEml (resume): %v", err)
	}
	if summary2.MessagesAdded != 0 {
		t.Fatalf("MessagesAdded (resume) = %d, want 0", summary2.MessagesAdded)
	}

	var msgCount int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&msgCount); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if msgCount != 2 {
		t.Fatalf("msgCount = %d, want 2", msgCount)
	}
}

func TestImportEml_CorruptedSkipped(t *testing.T) {
	st, tmp := openTestStore(t)

	root := filepath.Join(tmp, "emails")

	raw := email.NewMessage().
		From("Alice <alice@example.com>").
		Subject("Good").
		Body("ok\n").
		Bytes()

	mkEmlFile(t, root, "good.eml", raw)
	// "Corrupted" file — IngestRawMessage handles this gracefully
	// (MIME parse error is not fatal), but the message still gets ingested.
	// To test actual read errors, we'd need a permission-denied file,
	// which is platform-dependent.
	mkEmlFile(t, root, "bad.eml", []byte("not valid mime at all"))

	summary, err := ImportEml(
		context.Background(), st, root, EmlImportOptions{
			Identifier:         "alice@example.com",
			NoResume:           true,
			CheckpointInterval: 1,
		},
	)
	if err != nil {
		t.Fatalf("ImportEml: %v", err)
	}

	// Both messages get ingested (IngestRawMessage preserves raw even on parse error).
	if summary.MessagesAdded != 2 {
		t.Fatalf("MessagesAdded = %d, want 2", summary.MessagesAdded)
	}
}

func TestImportEml_OversizedFileSkipped(t *testing.T) {
	st, tmp := openTestStore(t)

	root := filepath.Join(tmp, "emails")

	raw := email.NewMessage().
		From("Alice <alice@example.com>").
		Subject("Hello").
		Body("hi\n").
		Bytes()

	mkEmlFile(t, root, "msg.eml", raw)

	summary, err := ImportEml(
		context.Background(), st, root, EmlImportOptions{
			Identifier:         "alice@example.com",
			NoResume:           true,
			CheckpointInterval: 1,
			MaxMessageBytes:    10, // Tiny limit.
		},
	)
	if err != nil {
		t.Fatalf("ImportEml: %v", err)
	}
	if summary.MessagesAdded != 0 {
		t.Fatalf("MessagesAdded = %d, want 0", summary.MessagesAdded)
	}
	if summary.Errors == 0 {
		t.Fatalf("expected errors > 0 for oversized file")
	}
}

func TestImportEml_CaseInsensitiveExtension(t *testing.T) {
	st, tmp := openTestStore(t)

	root := filepath.Join(tmp, "emails")

	mkMsg := func(subject string) []byte {
		return email.NewMessage().
			From("Alice <alice@example.com>").
			Subject(subject).
			Body("hi\n").
			Bytes()
	}

	mkEmlFile(t, root, "lower.eml", mkMsg("lower"))
	mkEmlFile(t, root, "upper.EML", mkMsg("upper"))
	mkEmlFile(t, root, "mixed.Eml", mkMsg("mixed"))

	summary, err := ImportEml(
		context.Background(), st, root, EmlImportOptions{
			Identifier:         "alice@example.com",
			NoResume:           true,
			CheckpointInterval: 1,
		},
	)
	if err != nil {
		t.Fatalf("ImportEml: %v", err)
	}
	if summary.MessagesAdded != 3 {
		t.Fatalf("MessagesAdded = %d, want 3", summary.MessagesAdded)
	}
}

func TestImportEml_ExplicitLabel(t *testing.T) {
	st, tmp := openTestStore(t)

	root := filepath.Join(tmp, "emails")

	raw := email.NewMessage().
		From("Alice <alice@example.com>").
		To("Bob <bob@example.com>").
		Subject("Tagged").
		Date("Mon, 01 Jan 2024 12:00:00 +0000").
		Header("Message-ID", "<tagged@example.com>").
		Body("With label.\n").
		Bytes()

	mkEmlFile(t, filepath.Join(root, "subdir"), "msg.eml", raw)

	summary, err := ImportEml(
		context.Background(), st, root, EmlImportOptions{
			Identifier:         "alice@example.com",
			Label:              "google-takeout",
			NoResume:           true,
			CheckpointInterval: 1,
		},
	)
	if err != nil {
		t.Fatalf("ImportEml: %v", err)
	}
	if summary.MessagesAdded != 1 {
		t.Fatalf("MessagesAdded = %d, want 1", summary.MessagesAdded)
	}

	// Should have both the path-derived label ("subdir") and the explicit label.
	var labelNames []string
	rows, err := st.DB().Query(`
		SELECT l.name FROM message_labels ml
		JOIN labels l ON l.id = ml.label_id
		ORDER BY l.name
	`)
	if err != nil {
		t.Fatalf("query labels: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan label: %v", err)
		}
		labelNames = append(labelNames, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows error: %v", err)
	}

	if len(labelNames) != 2 {
		t.Fatalf("labels = %v, want 2 labels", labelNames)
	}
	if labelNames[0] != "google-takeout" || labelNames[1] != "subdir" {
		t.Fatalf("labels = %v, want [google-takeout, subdir]", labelNames)
	}
}

func TestImportEml_CancelledLeavesRunning(t *testing.T) {
	st, tmp := openTestStore(t)

	root := filepath.Join(tmp, "emails")

	raw := email.NewMessage().
		From("Alice <alice@example.com>").
		Subject("Hello").
		Body("hi\n").
		Bytes()
	mkEmlFile(t, root, "msg.eml", raw)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ImportEml(ctx, st, root, EmlImportOptions{
		Identifier:         "alice@example.com",
		NoResume:           true,
		CheckpointInterval: 1,
	})
	if err != nil {
		t.Fatalf("ImportEml: %v", err)
	}

	var status string
	if err := st.DB().QueryRow(
		`SELECT status FROM sync_runs ORDER BY started_at DESC LIMIT 1`,
	).Scan(&status); err != nil {
		t.Fatalf("select sync: %v", err)
	}
	if status != store.SyncStatusRunning {
		t.Fatalf("status = %q, want %q", status, store.SyncStatusRunning)
	}
}

func TestImportEml_Idempotent(t *testing.T) {
	st, tmp := openTestStore(t)

	root := filepath.Join(tmp, "emails")

	raw := email.NewMessage().
		From("Alice <alice@example.com>").
		Subject("Hello").
		Body("hi\n").
		Bytes()
	mkEmlFile(t, root, "msg.eml", raw)

	_, err := ImportEml(
		context.Background(), st, root, EmlImportOptions{
			Identifier: "alice@example.com",
			NoResume:   true,
		},
	)
	if err != nil {
		t.Fatalf("ImportEml (first): %v", err)
	}

	summary, err := ImportEml(
		context.Background(), st, root, EmlImportOptions{
			Identifier: "alice@example.com",
			NoResume:   true,
		},
	)
	if err != nil {
		t.Fatalf("ImportEml (second): %v", err)
	}

	if summary.MessagesAdded != 0 {
		t.Fatalf("MessagesAdded = %d, want 0", summary.MessagesAdded)
	}

	var msgCount int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&msgCount); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if msgCount != 1 {
		t.Fatalf("msgCount = %d, want 1", msgCount)
	}
}

func TestImportEml_CheckpointBlockedOnIngestFailure(t *testing.T) {
	st, tmp := openTestStore(t)

	root := filepath.Join(tmp, "emails")

	mkMsg := func(subject string) []byte {
		return email.NewMessage().
			From("Alice <alice@example.com>").
			To("Bob <bob@example.com>").
			Subject(subject).
			Date("Mon, 01 Jan 2024 12:00:00 +0000").
			Header("Message-ID", fmt.Sprintf("<%s@example.com>", subject)).
			Body("Hi.\n").
			Bytes()
	}

	mkEmlFile(t, root, "a.eml", mkMsg("msg1"))
	mkEmlFile(t, root, "b.eml", mkMsg("msg2"))
	mkEmlFile(t, root, "c.eml", mkMsg("msg3"))

	// Inject failure on the second message.
	calls := 0
	injectFn := func(
		ctx context.Context, s *store.Store,
		sourceID int64, identifier, attachmentsDir string,
		labelIDs []int64, sourceMsgID, rawHash string,
		raw []byte, fallbackDate time.Time,
		log *slog.Logger,
	) error {
		calls++
		if calls == 2 {
			return fmt.Errorf("injected failure")
		}
		return IngestRawMessage(
			ctx, s, sourceID, identifier, attachmentsDir,
			labelIDs, sourceMsgID, rawHash,
			raw, fallbackDate, log,
		)
	}

	summary, err := ImportEml(
		context.Background(), st, root, EmlImportOptions{
			Identifier:         "alice@example.com",
			NoResume:           true,
			CheckpointInterval: 1,
			IngestFunc:         injectFn,
		},
	)
	if err != nil {
		t.Fatalf("ImportEml: %v", err)
	}
	if !summary.HardErrors {
		t.Fatalf("expected HardErrors=true")
	}
	if summary.MessagesAdded != 2 {
		t.Fatalf("MessagesAdded = %d, want 2", summary.MessagesAdded)
	}

	// Verify the checkpoint did not advance past the failed message.
	var cursor string
	if err := st.DB().QueryRow(
		`SELECT cursor_before FROM sync_runs ORDER BY started_at DESC LIMIT 1`,
	).Scan(&cursor); err != nil {
		t.Fatalf("select cursor: %v", err)
	}
	var cp emlCheckpoint
	if err := json.Unmarshal([]byte(cursor), &cp); err != nil {
		t.Fatalf("unmarshal checkpoint: %v", err)
	}
	// Checkpoint should be at the first file (a.eml), not past b.eml.
	if !strings.HasSuffix(cp.LastFile, "a.eml") {
		t.Fatalf(
			"checkpoint LastFile = %q, expected suffix 'a.eml' (should not advance past failed msg)",
			cp.LastFile,
		)
	}
}

func TestImportEml_RootMismatchRejectsResume(t *testing.T) {
	st, tmp := openTestStore(t)

	raw := email.NewMessage().
		From("Alice <alice@example.com>").
		Subject("Hello").
		Body("hi\n").
		Bytes()

	// Seed checkpoint for root A.
	src, err := st.GetOrCreateSource("eml", "alice@example.com")
	if err != nil {
		t.Fatalf("get/create source: %v", err)
	}
	syncID, err := st.StartSync(src.ID, "import-eml")
	if err != nil {
		t.Fatalf("start sync: %v", err)
	}
	absRootA, err := filepath.Abs(filepath.Join(tmp, "emailsA"))
	if err != nil {
		t.Fatalf("abs root A: %v", err)
	}
	if err := saveEmlCheckpoint(st, syncID, absRootA, "", &store.Checkpoint{}); err != nil {
		t.Fatalf("save checkpoint: %v", err)
	}

	// Create files at root B.
	rootB := filepath.Join(tmp, "emailsB")
	mkEmlFile(t, rootB, "msg.eml", raw)

	_, err = ImportEml(
		context.Background(), st, rootB, EmlImportOptions{
			Identifier:         "alice@example.com",
			NoResume:           false,
			CheckpointInterval: 1,
		},
	)
	if err == nil {
		t.Fatalf("expected error for root mismatch")
	}
	if !strings.Contains(err.Error(), "--no-resume") {
		t.Fatalf("error should mention --no-resume, got: %v", err)
	}
}

// Suppress unused import warnings.
var (
	_ = slog.Default
	_ = time.Now
)
