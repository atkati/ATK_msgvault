package importer

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wesm/msgvault/internal/store"
)

// EmlImportOptions configures a .eml file/directory import.
type EmlImportOptions struct {
	// SourceType is the sources.source_type value.
	// Defaults to "eml".
	SourceType string

	// Identifier is the sources.identifier (e.g. "you@gmail.com").
	Identifier string

	// Label is an optional label to apply to all imported messages,
	// in addition to any labels derived from the directory structure.
	Label string

	// NoResume forces a fresh import even if a prior run exists.
	NoResume bool

	// CheckpointInterval controls how often (in messages) to persist
	// progress. Defaults to 200.
	CheckpointInterval int

	// AttachmentsDir controls where attachments are written.
	// Empty means no disk storage.
	AttachmentsDir string

	// MaxMessageBytes limits the maximum .eml file size to read.
	// Defaults to 128 MiB.
	MaxMessageBytes int64

	// IngestFunc overrides message ingestion (for tests). If nil,
	// the default IngestRawMessage is used.
	IngestFunc func(
		ctx context.Context, st *store.Store,
		sourceID int64, identifier, attachmentsDir string,
		labelIDs []int64, sourceMsgID, rawHash string,
		raw []byte, fallbackDate time.Time,
		log *slog.Logger,
	) error

	// Logger is optional; defaults to slog.Default().
	Logger *slog.Logger
}

// EmlImportSummary reports the results of an eml import.
type EmlImportSummary struct {
	WasResumed        bool
	Duration          time.Duration
	FilesTotal        int
	MessagesProcessed int64
	MessagesAdded     int64
	MessagesUpdated   int64
	MessagesSkipped   int64
	Errors            int64
	HardErrors        bool
}

type emlCheckpoint struct {
	RootDir  string `json:"root_dir"`
	LastFile string `json:"last_file"`
}

const defaultMaxEmlBytes int64 = 128 << 20 // 128 MiB

// ImportEml imports .eml files from a file, directory, or ZIP archive.
//
// For directories, files are scanned recursively. Labels are derived from
// the directory structure: a file at Work/Uber/bills/msg.eml produces
// labels ["Work", "Work/Uber", "Work/Uber/bills"].
//
// Messages are deduplicated by content hash (sha256 of raw MIME).
// When the same message appears in multiple directories, the first
// occurrence is fully ingested; subsequent occurrences add their
// directory labels to the existing message.
func ImportEml(
	ctx context.Context, st *store.Store,
	emlPath string, opts EmlImportOptions,
) (*EmlImportSummary, error) {
	if opts.SourceType == "" {
		opts.SourceType = "eml"
	}
	if opts.Identifier == "" {
		return nil, fmt.Errorf("identifier is required")
	}
	if opts.CheckpointInterval <= 0 {
		opts.CheckpointInterval = 200
	}
	if opts.MaxMessageBytes <= 0 {
		opts.MaxMessageBytes = defaultMaxEmlBytes
	}
	ingestFn := opts.IngestFunc
	if ingestFn == nil {
		ingestFn = IngestRawMessage
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}

	start := time.Now()
	summary := &EmlImportSummary{}

	absPath, err := filepath.Abs(emlPath)
	if err != nil {
		return nil, fmt.Errorf("abs path: %w", err)
	}

	fi, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("stat path: %w", err)
	}

	// Discover .eml files.
	var files []string
	var rootDir string
	var tmpDir string

	switch {
	case fi.IsDir():
		rootDir = absPath
		files, err = discoverEmlFiles(absPath)
		if err != nil {
			return nil, fmt.Errorf("discover eml files: %w", err)
		}

	case isZipFile(absPath):
		tmpDir, err = os.MkdirTemp("", "eml-import-*")
		if err != nil {
			return nil, fmt.Errorf("create temp dir: %w", err)
		}
		defer os.RemoveAll(tmpDir)

		files, err = extractEmlFromZip(absPath, tmpDir, opts.MaxMessageBytes, log)
		if err != nil {
			return nil, fmt.Errorf("extract zip: %w", err)
		}
		rootDir = tmpDir

	default:
		// Single file.
		ext := strings.ToLower(filepath.Ext(absPath))
		if ext != ".eml" {
			return nil, fmt.Errorf("file %q does not have .eml extension", absPath)
		}
		rootDir = filepath.Dir(absPath)
		files = []string{absPath}
	}

	summary.FilesTotal = len(files)
	if len(files) == 0 {
		summary.Duration = time.Since(start)
		return summary, nil
	}

	src, srcErr := st.GetOrCreateSource(opts.SourceType, opts.Identifier)
	if srcErr != nil {
		return nil, fmt.Errorf("get/create source: %w", srcErr)
	}

	// Resume support.
	var (
		syncID     int64
		cp         store.Checkpoint
		startAfter string
	)

	if !opts.NoResume {
		active, err := st.GetActiveSync(src.ID)
		if err != nil {
			return nil, fmt.Errorf("check active sync: %w", err)
		}
		if active != nil && active.CursorBefore.Valid && active.CursorBefore.String != "" {
			var ecp emlCheckpoint
			if err := json.Unmarshal([]byte(active.CursorBefore.String), &ecp); err == nil {
				if ecp.RootDir != rootDir {
					return nil, fmt.Errorf(
						"active eml import is for a different directory (%q), not %q; rerun with --no-resume to start fresh",
						ecp.RootDir, rootDir,
					)
				}
				syncID = active.ID
				cp.MessagesProcessed = active.MessagesProcessed
				cp.MessagesAdded = active.MessagesAdded
				cp.MessagesUpdated = active.MessagesUpdated
				cp.ErrorsCount = active.ErrorsCount
				startAfter = ecp.LastFile
				summary.WasResumed = true
				log.Info("resuming eml import",
					"root", rootDir,
					"last_file", startAfter,
					"processed", cp.MessagesProcessed,
				)
			}
		}
	}

	if syncID == 0 {
		syncID, err = st.StartSync(src.ID, "import-eml")
		if err != nil {
			return nil, fmt.Errorf("start sync: %w", err)
		}
	}

	// Ensure global label if specified.
	var globalLabelID int64
	if opts.Label != "" {
		var labelErr error
		globalLabelID, labelErr = st.EnsureLabel(src.ID, opts.Label, opts.Label, "user")
		if labelErr != nil {
			return nil, fmt.Errorf("ensure label %q: %w", opts.Label, labelErr)
		}
	}

	hardErrors := false

	type pendingEmlMsg struct {
		Raw       []byte
		RawHash   string
		SourceMsg string
		LabelIDs  []int64
		Fallback  time.Time
		FileName  string
	}

	const (
		batchSize  = 200
		batchBytes = 32 << 20 // 32 MiB
	)

	var pending []pendingEmlMsg
	var pendingBytes int64
	pendingIdx := make(map[string]int) // SourceMsg → index in pending
	lastCpFile := startAfter
	checkpointBlocked := false

	// Save initial checkpoint.
	if err := saveEmlCheckpoint(st, syncID, rootDir, startAfter, &cp); err != nil {
		cp.ErrorsCount++
		summary.Errors++
		log.Warn("failed to save initial checkpoint", "error", err)
	}

	flushPending := func() (bool, error) {
		if len(pending) == 0 {
			return false, nil
		}

		ids := make([]string, len(pending))
		for i, p := range pending {
			ids[i] = p.SourceMsg
		}

		existingWithRaw, err := st.MessageExistsWithRawBatch(src.ID, ids)
		batchOK := err == nil
		if err != nil {
			cp.ErrorsCount++
			summary.Errors++
			log.Warn("existence check failed", "error", err)
		}

		existingAny, err := st.MessageExistsBatch(src.ID, ids)
		anyOK := err == nil
		if err != nil {
			cp.ErrorsCount++
			summary.Errors++
			log.Warn("existence check failed (any)", "error", err)
		}

		for _, p := range pending {
			if err := ctx.Err(); err != nil {
				summary.Duration = time.Since(start)
				if err := saveEmlCheckpoint(st, syncID, rootDir, lastCpFile, &cp); err != nil {
					log.Warn("checkpoint save failed", "error", err)
				}
				return true, nil
			}

			cp.MessagesProcessed++
			summary.MessagesProcessed++

			// Check if fully exists (with raw).
			exists := false
			if batchOK {
				msgID, ok := existingWithRaw[p.SourceMsg]
				if ok {
					exists = true
					if len(p.LabelIDs) > 0 {
						if err := st.AddMessageLabels(msgID, p.LabelIDs); err != nil {
							log.Warn("failed to add labels to existing message",
								"message_id", msgID, "error", err,
							)
						}
					}
				}
			} else {
				one, err := st.MessageExistsWithRawBatch(src.ID, []string{p.SourceMsg})
				if err != nil {
					cp.ErrorsCount++
					summary.Errors++
				} else if msgID, ok := one[p.SourceMsg]; ok {
					exists = true
					if len(p.LabelIDs) > 0 {
						if err := st.AddMessageLabels(msgID, p.LabelIDs); err != nil {
							log.Warn("failed to add labels",
								"message_id", msgID, "error", err,
							)
						}
					}
				}
			}

			if exists {
				summary.MessagesSkipped++
				if !checkpointBlocked {
					lastCpFile = p.FileName
					checkpointEmlIfDue(
						&cp, summary, opts.CheckpointInterval,
						st, syncID, rootDir, lastCpFile, log,
					)
				}
				continue
			}

			alreadyExists := false
			if anyOK {
				_, alreadyExists = existingAny[p.SourceMsg]
			}

			if err := ingestFn(
				ctx, st, src.ID, opts.Identifier,
				opts.AttachmentsDir, p.LabelIDs,
				p.SourceMsg, p.RawHash,
				p.Raw, p.Fallback, log,
			); err != nil {
				cp.ErrorsCount++
				summary.Errors++
				log.Warn("failed to ingest message",
					"source_msg", p.SourceMsg,
					"file", p.FileName,
					"error", err,
				)
				checkpointBlocked = true
				hardErrors = true
				continue
			}

			if alreadyExists {
				cp.MessagesUpdated++
				summary.MessagesUpdated++
			} else {
				cp.MessagesAdded++
				summary.MessagesAdded++
			}

			if !checkpointBlocked {
				lastCpFile = p.FileName
				checkpointEmlIfDue(
					&cp, summary, opts.CheckpointInterval,
					st, syncID, rootDir, lastCpFile, log,
				)
			}
		}

		clear(pending)
		pending = pending[:0]
		pendingBytes = 0
		clear(pendingIdx)
		return false, nil
	}

	for _, filePath := range files {
		if ctx.Err() != nil {
			break
		}

		// Resume: skip files already processed.
		if startAfter != "" && filePath <= startAfter {
			continue
		}

		// Check file size before reading.
		fi, statErr := os.Stat(filePath)
		if statErr != nil {
			cp.ErrorsCount++
			summary.Errors++
			log.Warn("failed to stat .eml",
				"file", filePath, "error", statErr,
			)
			continue
		}
		if fi.Size() > opts.MaxMessageBytes {
			cp.ErrorsCount++
			summary.Errors++
			log.Warn("file exceeds size limit",
				"file", filePath,
				"size", fi.Size(),
				"limit", opts.MaxMessageBytes,
			)
			continue
		}

		raw, readErr := os.ReadFile(filePath)
		if readErr != nil {
			cp.ErrorsCount++
			summary.Errors++
			log.Warn("failed to read .eml",
				"file", filePath, "error", readErr,
			)
			continue
		}

		sum := sha256.Sum256(raw)
		rawHash := hex.EncodeToString(sum[:])
		sourceMsgID := "eml-" + rawHash

		// Fallback date: file modification time.
		fallbackDate := fi.ModTime()

		// Labels from directory structure.
		labelIDs := labelsFromPath(st, src.ID, rootDir, filePath, log)
		if globalLabelID != 0 {
			labelIDs = append(labelIDs, globalLabelID)
		}

		// In-batch dedup: merge labels if same content hash already pending.
		if idx, dup := pendingIdx[sourceMsgID]; dup {
			existing := pending[idx].LabelIDs
			for _, lid := range labelIDs {
				found := false
				for _, eid := range existing {
					if eid == lid {
						found = true
						break
					}
				}
				if !found {
					existing = append(existing, lid)
				}
			}
			pending[idx].LabelIDs = existing
		} else {
			pendingIdx[sourceMsgID] = len(pending)
			pending = append(pending, pendingEmlMsg{
				Raw:       raw,
				RawHash:   rawHash,
				SourceMsg: sourceMsgID,
				LabelIDs:  labelIDs,
				Fallback:  fallbackDate,
				FileName:  filePath,
			})
			pendingBytes += int64(len(raw))
		}

		if len(pending) >= batchSize || pendingBytes >= batchBytes {
			stop, err := flushPending()
			if err != nil {
				return summary, err
			}
			if stop {
				return summary, nil
			}
		}
	}

	// Flush remaining.
	if stop, err := flushPending(); err != nil {
		return summary, err
	} else if stop {
		return summary, nil
	}

	summary.Duration = time.Since(start)
	summary.HardErrors = hardErrors

	// Final checkpoint.
	if err := saveEmlCheckpoint(st, syncID, rootDir, lastCpFile, &cp); err != nil {
		cp.ErrorsCount++
		summary.Errors++
		log.Warn("failed to save final checkpoint", "error", err)
	}

	// If cancelled, leave the sync run as "running" so resume works.
	if ctx.Err() != nil {
		return summary, nil
	}

	if hardErrors {
		if err := st.FailSync(syncID, fmt.Sprintf(
			"completed with %d errors", cp.ErrorsCount,
		)); err != nil {
			return summary, fmt.Errorf("fail sync: %w", err)
		}
		return summary, nil
	}

	finalMsg := fmt.Sprintf(
		"files:%d messages:%d",
		summary.FilesTotal, summary.MessagesAdded,
	)
	if cp.ErrorsCount > 0 {
		finalMsg = fmt.Sprintf(
			"files:%d messages:%d errors:%d",
			summary.FilesTotal, summary.MessagesAdded, cp.ErrorsCount,
		)
	}
	if err := st.CompleteSync(syncID, finalMsg); err != nil {
		return summary, fmt.Errorf("complete sync: %w", err)
	}

	return summary, nil
}

// discoverEmlFiles walks a directory recursively and returns all .eml files
// sorted lexicographically for deterministic checkpoint ordering.
func discoverEmlFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Skip symlinks.
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if isEmlFile(path) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// isEmlFile checks if a file path has a .eml extension (case-insensitive).
func isEmlFile(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".eml")
}

// isZipFile checks if a file path has a .zip extension (case-insensitive).
func isZipFile(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".zip")
}

// labelsFromPath derives hierarchical labels from the directory structure.
// A file at root/Work/Uber/bills/msg.eml produces labels:
// ["Work", "Work/Uber", "Work/Uber/bills"]
func labelsFromPath(
	st *store.Store, sourceID int64,
	rootDir, filePath string,
	log *slog.Logger,
) []int64 {
	relDir, err := filepath.Rel(rootDir, filepath.Dir(filePath))
	if err != nil || relDir == "." {
		return nil
	}

	// Normalize to forward slashes for consistent label names across platforms.
	relDir = filepath.ToSlash(relDir)
	parts := strings.Split(relDir, "/")

	var labelIDs []int64
	var prefix string
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if prefix == "" {
			prefix = part
		} else {
			prefix = prefix + "/" + part
		}

		labelID, err := st.EnsureLabel(sourceID, prefix, prefix, "user")
		if err != nil {
			log.Warn("failed to ensure label from path",
				"label", prefix, "error", err,
			)
			continue
		}
		labelIDs = append(labelIDs, labelID)
	}
	return labelIDs
}

// extractEmlFromZip extracts .eml files from a ZIP archive into destDir,
// preserving directory structure for label derivation.
func extractEmlFromZip(
	zipPath, destDir string, maxFileBytes int64, log *slog.Logger,
) ([]string, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()

	var files []string
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if !isEmlFile(f.Name) {
			continue
		}

		// Security: prevent path traversal.
		cleanName := filepath.Clean(f.Name)
		if strings.HasPrefix(cleanName, "..") {
			log.Warn("skipping zip entry with path traversal", "name", f.Name)
			continue
		}

		if f.UncompressedSize64 > uint64(maxFileBytes) {
			log.Warn("skipping oversized zip entry",
				"name", f.Name,
				"size", f.UncompressedSize64,
				"limit", maxFileBytes,
			)
			continue
		}

		destPath := filepath.Join(destDir, cleanName)

		// Ensure parent directory exists.
		if err := os.MkdirAll(filepath.Dir(destPath), 0700); err != nil {
			return nil, fmt.Errorf("create dir for %q: %w", f.Name, err)
		}

		rc, err := f.Open()
		if err != nil {
			log.Warn("failed to open zip entry", "name", f.Name, "error", err)
			continue
		}

		out, err := os.Create(destPath)
		if err != nil {
			rc.Close()
			return nil, fmt.Errorf("create %q: %w", destPath, err)
		}

		_, copyErr := io.Copy(out, io.LimitReader(rc, maxFileBytes+1))
		out.Close()
		rc.Close()

		if copyErr != nil {
			log.Warn("failed to extract zip entry", "name", f.Name, "error", copyErr)
			continue
		}

		files = append(files, destPath)
	}

	sort.Strings(files)
	return files, nil
}

func saveEmlCheckpoint(
	st *store.Store, syncID int64,
	rootDir, lastFile string, cp *store.Checkpoint,
) error {
	b, err := json.Marshal(emlCheckpoint{
		RootDir:  rootDir,
		LastFile: lastFile,
	})
	if err != nil {
		return fmt.Errorf("marshal checkpoint: %w", err)
	}
	cp.PageToken = string(b)
	return st.UpdateSyncCheckpoint(syncID, cp)
}

func checkpointEmlIfDue(
	cp *store.Checkpoint, summary *EmlImportSummary,
	interval int,
	st *store.Store, syncID int64,
	rootDir, lastFile string, log *slog.Logger,
) {
	if cp.MessagesProcessed%int64(interval) != 0 {
		return
	}
	if err := saveEmlCheckpoint(st, syncID, rootDir, lastFile, cp); err != nil {
		cp.ErrorsCount++
		summary.Errors++
		log.Warn("failed to save checkpoint", "error", err)
	}
}
