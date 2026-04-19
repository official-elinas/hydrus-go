//go:build fyne

package fyneapp

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
)

type importQueueStatus string

const (
	importQueueStatusPending   importQueueStatus = "Queued"
	importQueueStatusUploading importQueueStatus = "Uploading"
	importQueueStatusImported  importQueueStatus = "Imported"
	importQueueStatusDuplicate importQueueStatus = "Already imported"
	importQueueStatusFailed    importQueueStatus = "Failed"
)

type importQueueEntry struct {
	Path   string
	Source string
	Status importQueueStatus
	Detail string
	FileID int64
}

func droppedLocalPaths(items []fyne.URI) ([]string, []string) {
	paths := []string{}
	rejected := []string{}

	for _, item := range items {
		if item == nil {
			rejected = append(rejected, "ignored an empty dropped item")
			continue
		}

		if strings.TrimSpace(item.Scheme()) != "file" {
			rejected = append(rejected, fmt.Sprintf("ignored non-file drop %q", item.String()))
			continue
		}

		path := filepath.Clean(strings.TrimSpace(item.Path()))
		if path == "." || path == "" {
			rejected = append(rejected, "ignored a dropped item with an empty local path")
			continue
		}

		paths = append(paths, path)
	}

	return paths, rejected
}

func expandImportSelection(paths []string) ([]string, []string, error) {
	resolved := []string{}
	skipped := []string{}
	seen := map[string]struct{}{}

	appendPath := func(path string) {
		normalized := filepath.Clean(path)
		if _, exists := seen[normalized]; exists {
			return
		}

		seen[normalized] = struct{}{}
		resolved = append(resolved, normalized)
	}

	for _, rawPath := range paths {
		path := filepath.Clean(strings.TrimSpace(rawPath))
		if path == "." || path == "" {
			skipped = append(skipped, "ignored an empty import path")
			continue
		}

		info, err := os.Stat(path)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("could not inspect %q: %v", path, err))
			continue
		}

		if info.Mode().IsRegular() {
			appendPath(path)
			continue
		}

		if !info.IsDir() {
			skipped = append(skipped, fmt.Sprintf("ignored non-regular path %q", path))
			continue
		}

		resolvedBeforeDir := len(resolved)
		walkErr := filepath.WalkDir(path, func(candidate string, entry fs.DirEntry, err error) error {
			if err != nil {
				skipped = append(skipped, fmt.Sprintf("ignored unreadable path %q: %v", candidate, err))
				return nil
			}

			if entry.IsDir() {
				return nil
			}

			info, err := entry.Info()
			if err != nil {
				skipped = append(skipped, fmt.Sprintf("ignored unreadable path %q: %v", candidate, err))
				return nil
			}

			if info.Mode().IsRegular() {
				appendPath(candidate)
			}

			return nil
		})
		if walkErr != nil {
			return nil, skipped, fmt.Errorf("walk import directory %q: %w", path, walkErr)
		}

		if len(resolved) == resolvedBeforeDir {
			skipped = append(skipped, fmt.Sprintf("no importable files were found under %q", path))
		}
	}

	return resolved, skipped, nil
}

func formatImportQueueEntry(entry importQueueEntry) string {
	baseName := filepath.Base(entry.Path)
	if strings.TrimSpace(baseName) == "" || baseName == "." || baseName == string(filepath.Separator) {
		baseName = entry.Path
	}

	lines := []string{fmt.Sprintf("%s — %s", entry.Status, baseName)}
	if entry.FileID > 0 {
		lines = append(lines, fmt.Sprintf("file_id %d", entry.FileID))
	}

	if detail := strings.TrimSpace(entry.Detail); detail != "" {
		lines = append(lines, detail)
	}

	if source := strings.TrimSpace(entry.Source); source != "" {
		lines = append(lines, fmt.Sprintf("via %s", source))
	}

	lines = append(lines, entry.Path)
	return strings.Join(lines, "\n")
}

func formatImportQueueSummary(entries []importQueueEntry, running bool) string {
	if len(entries) == 0 {
		return "No imports queued. Add a file, add a folder, or drop files anywhere in the window."
	}

	queued := 0
	uploading := 0
	imported := 0
	duplicates := 0
	failed := 0
	for _, entry := range entries {
		switch entry.Status {
		case importQueueStatusPending:
			queued++
		case importQueueStatusUploading:
			uploading++
		case importQueueStatusImported:
			imported++
		case importQueueStatusDuplicate:
			duplicates++
		case importQueueStatusFailed:
			failed++
		}
	}

	status := "Idle"
	if running || uploading > 0 {
		status = "Running"
	}

	return fmt.Sprintf(
		"%s — queued %d • uploading %d • imported %d • duplicates %d • failed %d",
		status,
		queued,
		uploading,
		imported,
		duplicates,
		failed,
	)
}

func compactImportQueueTitle(entry importQueueEntry) string {
	baseName := filepath.Base(entry.Path)
	if strings.TrimSpace(baseName) == "" || baseName == "." || baseName == string(filepath.Separator) {
		baseName = entry.Path
	}

	return fmt.Sprintf("%s • %s", entry.Status, baseName)
}

func compactImportQueueSubtitle(entry importQueueEntry) string {
	parts := []string{}
	if detail := strings.TrimSpace(entry.Detail); detail != "" {
		parts = append(parts, detail)
	}
	if source := strings.TrimSpace(entry.Source); source != "" {
		parts = append(parts, fmt.Sprintf("via %s", source))
	}

	return strings.Join(parts, " • ")
}

func defaultSelectedQueueText() string {
	return "Select a queued import to review its full path, source, and current state."
}

func formatSelectedImportQueueEntry(entry importQueueEntry) string {
	lines := []string{
		fmt.Sprintf("status: %s", entry.Status),
		fmt.Sprintf("path: %s", entry.Path),
	}

	if entry.FileID > 0 {
		lines = append(lines, fmt.Sprintf("file_id: %d", entry.FileID))
	}
	if source := strings.TrimSpace(entry.Source); source != "" {
		lines = append(lines, fmt.Sprintf("source: %s", source))
	}
	if detail := strings.TrimSpace(entry.Detail); detail != "" {
		lines = append(lines, fmt.Sprintf("detail: %s", detail))
	}

	return strings.Join(lines, "\n")
}

func canRetryImportQueueEntry(entry importQueueEntry) bool {
	return entry.Status == importQueueStatusFailed
}

func canRemoveImportQueueEntry(entry importQueueEntry) bool {
	return entry.Status != importQueueStatusUploading
}

func isFinishedImportQueueEntry(entry importQueueEntry) bool {
	switch entry.Status {
	case importQueueStatusImported, importQueueStatusDuplicate, importQueueStatusFailed:
		return true
	default:
		return false
	}
}

func retryImportQueueEntry(entry *importQueueEntry, detail string) bool {
	if entry == nil || !canRetryImportQueueEntry(*entry) {
		return false
	}

	entry.Status = importQueueStatusPending
	entry.FileID = 0
	entry.Detail = strings.TrimSpace(detail)
	return true
}

func retryFailedImportQueueEntries(entries []importQueueEntry, detail string) int {
	retried := 0
	for index := range entries {
		if retryImportQueueEntry(&entries[index], detail) {
			retried++
		}
	}

	return retried
}

func clearFinishedImportQueueEntries(entries []importQueueEntry) ([]importQueueEntry, int) {
	kept := make([]importQueueEntry, 0, len(entries))
	removed := 0
	for _, entry := range entries {
		if isFinishedImportQueueEntry(entry) {
			removed++
			continue
		}

		kept = append(kept, entry)
	}

	return kept, removed
}

func removeImportQueueEntry(entries []importQueueEntry, index int) ([]importQueueEntry, bool) {
	if index < 0 || index >= len(entries) {
		return entries, false
	}

	if !canRemoveImportQueueEntry(entries[index]) {
		return entries, false
	}

	updated := make([]importQueueEntry, 0, len(entries)-1)
	updated = append(updated, entries[:index]...)
	updated = append(updated, entries[index+1:]...)
	return updated, true
}

func findImportQueueEntryByPath(entries []importQueueEntry, path string) int {
	normalized := strings.TrimSpace(path)
	if normalized == "" {
		return -1
	}

	for index, entry := range entries {
		if entry.Path == normalized {
			return index
		}
	}

	return -1
}

func normalizeImportQueueSelection(selected int, length int) int {
	if length <= 0 {
		return -1
	}

	if selected < 0 {
		return -1
	}

	if selected >= length {
		return length - 1
	}

	return selected
}

func formatRejectedImportItems(items []string) string {
	if len(items) == 0 {
		return ""
	}

	const maxItems = 8
	if len(items) <= maxItems {
		return strings.Join(items, "\n")
	}

	message := append([]string{}, items[:maxItems]...)
	message = append(message, fmt.Sprintf("...and %d more", len(items)-maxItems))
	return strings.Join(message, "\n")
}
