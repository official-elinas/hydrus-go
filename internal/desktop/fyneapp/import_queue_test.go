//go:build fyne

package fyneapp

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/storage"
)

func TestDroppedLocalPaths(t *testing.T) {
	t.Run("keeps local file URIs and rejects unsupported drops", func(t *testing.T) {
		firstPath := filepath.Join(t.TempDir(), "first.png")
		secondPath := filepath.Join(t.TempDir(), "second.jpg")

		paths, rejected := droppedLocalPaths([]fyne.URI{
			storage.NewFileURI(firstPath),
			nil,
			storage.NewURI("https://example.test/file.png"),
			storage.NewFileURI(secondPath),
		})

		if len(paths) != 2 {
			t.Fatalf("len(paths) = %d, want 2", len(paths))
		}

		if paths[0] != firstPath || paths[1] != secondPath {
			t.Fatalf("paths = %#v, want [%q %q]", paths, firstPath, secondPath)
		}

		if len(rejected) != 2 {
			t.Fatalf("len(rejected) = %d, want 2", len(rejected))
		}
	})
}

func TestExpandImportSelection(t *testing.T) {
	t.Run("expands files and directories into stable deduplicated paths", func(t *testing.T) {
		root := t.TempDir()
		firstFile := filepath.Join(root, "first.png")
		secondDir := filepath.Join(root, "nested")
		secondFile := filepath.Join(secondDir, "second.jpg")
		thirdDir := filepath.Join(secondDir, "deeper")
		thirdFile := filepath.Join(thirdDir, "third.gif")

		if err := os.WriteFile(firstFile, []byte("first"), 0o644); err != nil {
			t.Fatalf("WriteFile(firstFile) error = %v", err)
		}
		if err := os.MkdirAll(thirdDir, 0o755); err != nil {
			t.Fatalf("MkdirAll(thirdDir) error = %v", err)
		}
		if err := os.WriteFile(secondFile, []byte("second"), 0o644); err != nil {
			t.Fatalf("WriteFile(secondFile) error = %v", err)
		}
		if err := os.WriteFile(thirdFile, []byte("third"), 0o644); err != nil {
			t.Fatalf("WriteFile(thirdFile) error = %v", err)
		}

		resolved, skipped, err := expandImportSelection([]string{firstFile, secondDir, firstFile})
		if err != nil {
			t.Fatalf("expandImportSelection() error = %v", err)
		}

		want := []string{firstFile, secondFile, thirdFile}
		slices.Sort(resolved)
		slices.Sort(want)
		if len(resolved) != len(want) {
			t.Fatalf("len(resolved) = %d, want %d (%#v)", len(resolved), len(want), resolved)
		}

		for index, path := range want {
			if resolved[index] != path {
				t.Fatalf("resolved[%d] = %q, want %q", index, resolved[index], path)
			}
		}

		if len(skipped) != 0 {
			t.Fatalf("skipped = %#v, want none", skipped)
		}
	})

	t.Run("reports empty and missing sources cleanly", func(t *testing.T) {
		emptyDir := filepath.Join(t.TempDir(), "empty")
		if err := os.MkdirAll(emptyDir, 0o755); err != nil {
			t.Fatalf("MkdirAll(emptyDir) error = %v", err)
		}

		resolved, skipped, err := expandImportSelection([]string{" ", emptyDir, filepath.Join(emptyDir, "missing.png")})
		if err != nil {
			t.Fatalf("expandImportSelection() error = %v", err)
		}

		if len(resolved) != 0 {
			t.Fatalf("resolved = %#v, want none", resolved)
		}

		if len(skipped) != 3 {
			t.Fatalf("len(skipped) = %d, want 3 (%#v)", len(skipped), skipped)
		}

		if !strings.Contains(strings.Join(skipped, "\n"), "no importable files were found") {
			t.Fatalf("skipped = %#v, want empty-directory message", skipped)
		}
	})

	t.Run("keeps readable files when a directory contains unreadable children", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("permission-based unreadable directory test is not portable on Windows")
		}

		root := t.TempDir()
		readableFile := filepath.Join(root, "first.png")
		blockedDir := filepath.Join(root, "blocked")
		blockedFile := filepath.Join(blockedDir, "secret.png")

		if err := os.WriteFile(readableFile, []byte("first"), 0o644); err != nil {
			t.Fatalf("WriteFile(readableFile) error = %v", err)
		}
		if err := os.MkdirAll(blockedDir, 0o755); err != nil {
			t.Fatalf("MkdirAll(blockedDir) error = %v", err)
		}
		if err := os.WriteFile(blockedFile, []byte("secret"), 0o644); err != nil {
			t.Fatalf("WriteFile(blockedFile) error = %v", err)
		}
		if err := os.Chmod(blockedDir, 0o000); err != nil {
			t.Fatalf("Chmod(blockedDir) error = %v", err)
		}
		defer func() {
			_ = os.Chmod(blockedDir, 0o755)
		}()

		resolved, skipped, err := expandImportSelection([]string{root})
		if err != nil {
			t.Fatalf("expandImportSelection() error = %v", err)
		}

		if len(resolved) != 1 || resolved[0] != readableFile {
			t.Fatalf("resolved = %#v, want only %q", resolved, readableFile)
		}

		if !strings.Contains(strings.Join(skipped, "\n"), "ignored unreadable path") {
			t.Fatalf("skipped = %#v, want unreadable-path message", skipped)
		}
	})
}

func TestFormatImportQueueSummary(t *testing.T) {
	entries := []importQueueEntry{
		{Status: importQueueStatusPending},
		{Status: importQueueStatusUploading},
		{Status: importQueueStatusImported},
		{Status: importQueueStatusDuplicate},
		{Status: importQueueStatusFailed},
	}

	summary := formatImportQueueSummary(entries, true)
	for _, fragment := range []string{"Running", "queued 1", "uploading 1", "imported 1", "duplicates 1", "failed 1"} {
		if !strings.Contains(summary, fragment) {
			t.Fatalf("summary = %q, want fragment %q", summary, fragment)
		}
	}
}

func TestPrototypeAppendImportQueueEntries(t *testing.T) {
	p := &prototype{}

	firstPath := filepath.Join(t.TempDir(), "first.png")
	secondPath := filepath.Join(t.TempDir(), "second.png")
	added, duplicates := p.appendImportQueueEntries([]string{firstPath, secondPath}, "test")
	if added != 2 || duplicates != 0 {
		t.Fatalf("first append = (%d, %d), want (2, 0)", added, duplicates)
	}

	added, duplicates = p.appendImportQueueEntries([]string{firstPath}, "test")
	if added != 0 || duplicates != 1 {
		t.Fatalf("second append = (%d, %d), want (0, 1)", added, duplicates)
	}

	p.importQueue[0].Status = importQueueStatusFailed
	p.importQueue[0].Detail = "temporary failure"
	added, duplicates = p.appendImportQueueEntries([]string{firstPath}, "retry")
	if added != 1 || duplicates != 0 {
		t.Fatalf("retry append = (%d, %d), want (1, 0)", added, duplicates)
	}

	if p.importQueue[0].Status != importQueueStatusPending {
		t.Fatalf("retry status = %q, want %q", p.importQueue[0].Status, importQueueStatusPending)
	}

	if len(p.importQueue) != 2 {
		t.Fatalf("len(importQueue) = %d, want 2", len(p.importQueue))
	}
}

func TestImportQueueHelpers(t *testing.T) {
	t.Run("retry failed entries resets only failed rows", func(t *testing.T) {
		entries := []importQueueEntry{
			{Path: "queued.png", Status: importQueueStatusPending, Detail: "waiting"},
			{Path: "failed.png", Status: importQueueStatusFailed, Detail: "boom", FileID: 99},
		}

		retried := retryFailedImportQueueEntries(entries, "Retrying after review.")
		if retried != 1 {
			t.Fatalf("retried = %d, want 1", retried)
		}

		if entries[0].Status != importQueueStatusPending {
			t.Fatalf("entries[0].Status = %q, want queued", entries[0].Status)
		}

		if entries[1].Status != importQueueStatusPending || entries[1].FileID != 0 {
			t.Fatalf("entries[1] = %#v, want pending with cleared file ID", entries[1])
		}
	})

	t.Run("clear finished keeps queued and uploading rows", func(t *testing.T) {
		entries := []importQueueEntry{
			{Path: "queued.png", Status: importQueueStatusPending},
			{Path: "uploading.png", Status: importQueueStatusUploading},
			{Path: "imported.png", Status: importQueueStatusImported},
			{Path: "duplicate.png", Status: importQueueStatusDuplicate},
			{Path: "failed.png", Status: importQueueStatusFailed},
		}

		kept, removed := clearFinishedImportQueueEntries(entries)
		if removed != 3 {
			t.Fatalf("removed = %d, want 3", removed)
		}

		if len(kept) != 2 {
			t.Fatalf("len(kept) = %d, want 2", len(kept))
		}

		if kept[0].Path != "queued.png" || kept[1].Path != "uploading.png" {
			t.Fatalf("kept = %#v, want queued/uploading only", kept)
		}
	})

	t.Run("remove entry refuses uploading rows", func(t *testing.T) {
		entries := []importQueueEntry{{Path: "uploading.png", Status: importQueueStatusUploading}}
		updated, removed := removeImportQueueEntry(entries, 0)
		if removed {
			t.Fatal("removed = true, want false for uploading row")
		}

		if len(updated) != 1 {
			t.Fatalf("len(updated) = %d, want 1", len(updated))
		}
	})

	t.Run("selected index follows surviving path after clearing finished rows", func(t *testing.T) {
		entries := []importQueueEntry{
			{Path: "finished.png", Status: importQueueStatusImported},
			{Path: "keep.png", Status: importQueueStatusPending},
		}

		kept, _ := clearFinishedImportQueueEntries(entries)
		selected := selectedImportQueueIndex(kept, "keep.png", 1)
		if selected != 0 {
			t.Fatalf("selected = %d, want 0", selected)
		}
	})
}
