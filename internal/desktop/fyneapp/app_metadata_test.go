//go:build fyne

package fyneapp

import (
	"testing"
	"time"

	coreptrsync "github.com/official-elinas/hydrus-go/internal/core/ptrsync"
	"github.com/official-elinas/hydrus-go/internal/desktop/daemonclient"
)

func TestFormatMetadata(t *testing.T) {
	t.Run("renders base metadata without tags", func(t *testing.T) {
		metadata := daemonclient.FileMetadata{
			FileID:    7,
			Hash:      "abc123",
			MIME:      "image/png",
			Size:      123,
			IsLocal:   true,
			IsTrashed: false,
			IsDeleted: false,
		}

		got := formatMetadata(metadata)
		want := "file_id: 7\n" +
			"hash: abc123\n" +
			"mime: image/png\n" +
			"size: 123 bytes\n" +
			"dimensions: unknown\n" +
			"local: true\n" +
			"trashed: false\n" +
			"deleted: false"

		if got != want {
			t.Fatalf("formatMetadata() = %q, want %q", got, want)
		}
	})

	t.Run("renders daemon-served tags with per-status display preference and storage fallback", func(t *testing.T) {
		width := int64(640)
		height := int64(480)
		metadata := daemonclient.FileMetadata{
			FileID:    42,
			Hash:      "def456",
			MIME:      "image/jpeg",
			Size:      456,
			Width:     &width,
			Height:    &height,
			IsLocal:   true,
			IsTrashed: false,
			IsDeleted: false,
			Tags: map[string]daemonclient.FileMetadataTagService{
				"downloader": {
					Name:       "downloader tags",
					TypePretty: "local tag domain",
					StorageTags: map[string][]string{
						"1": {"pending:review"},
					},
				},
				"local": {
					Name:       "my tags",
					TypePretty: "local tag domain",
					StorageTags: map[string][]string{
						"0": {"creator:alice", "series:zeta"},
						"2": {"old:tag"},
					},
					DisplayTags: map[string][]string{
						"0": {"creator:alice", "series:zeta"},
					},
				},
				"empty": {
					Name:       "unused tags",
					TypePretty: "local tag domain",
				},
			},
		}

		got := formatMetadata(metadata)
		want := "file_id: 42\n" +
			"hash: def456\n" +
			"mime: image/jpeg\n" +
			"size: 456 bytes\n" +
			"dimensions: 640x480\n" +
			"local: true\n" +
			"trashed: false\n" +
			"deleted: false\n\n" +
			"tags:\n" +
			"- downloader tags (local tag domain)\n" +
			"  pending: pending:review\n" +
			"- my tags (local tag domain)\n" +
			"  current: creator:alice, series:zeta\n" +
			"  deleted: old:tag"

		if got != want {
			t.Fatalf("formatMetadata() = %q, want %q", got, want)
		}
	})
}

func TestFormatPTRStatus(t *testing.T) {
	t.Run("renders basic idle status", func(t *testing.T) {
		status := coreptrsync.Status{
			Enabled:                  true,
			ServiceName:              "public tag repository",
			Host:                     "ptr.hydrus.network",
			Port:                     45871,
			AccountMode:              coreptrsync.AccountModeSharedReadOnly,
			Phase:                    "idle",
			IsRunning:                false,
			MetadataSlice:            7,
			ProcessedDefinitionCount: 100,
			ProcessedContentCount:    200,
			DownloadedUpdateCount:    5,
		}

		got := formatPTRStatus(status)
		want := "Service: public tag repository\n" +
			"Endpoint: ptr.hydrus.network:45871\n" +
			"Account: shared-read-only\n" +
			"Phase: idle\n" +
			"Status: Idle\n" +
			"Metadata Slice: 7\n" +
			"Processed Definitions: 100\n" +
			"Processed Content: 200\n" +
			"Downloaded Update Files: 5"

		if got != want {
			t.Fatalf("formatPTRStatus() = %q, want %q", got, want)
		}
	})

	t.Run("renders error status", func(t *testing.T) {
		status := coreptrsync.Status{
			Enabled:                  true,
			ServiceName:              "public tag repository",
			Phase:                    "syncing",
			IsRunning:                true,
			MetadataSlice:            12,
			LastError:                "connection reset by peer",
			ProcessedDefinitionCount: 0,
			ProcessedContentCount:    0,
			DownloadedUpdateCount:    0,
		}

		got := formatPTRStatus(status)
		want := "Service: public tag repository\n" +
			"Phase: syncing\n" +
			"Status: Sync is currently running\n" +
			"Metadata Slice: 12\n" +
			"Last error: connection reset by peer\n" +
			"Processed Definitions: 0\n" +
			"Processed Content: 0\n" +
			"Downloaded Update Files: 0"

		if got != want {
			t.Fatalf("formatPTRStatus() = %q, want %q", got, want)
		}
	})

	t.Run("renders retry countdown status", func(t *testing.T) {
		retryAtMS := time.Now().Add(119 * time.Second).UnixMilli()
		status := coreptrsync.Status{
			Enabled:               true,
			ServiceName:           "public tag repository",
			Phase:                 coreptrsync.PhaseRetrying,
			RetryAtMS:             retryAtMS,
			RetryAttempt:          2,
			DownloadedUpdateCount: 7,
		}

		got := formatPTRStatus(status)
		want := "Service: public tag repository\n" +
			"Phase: retrying\n" +
			"Status: Remote PTR busy; retrying in 2m\n" +
			"Metadata Slice: 0\n" +
			"Processed Definitions: 0\n" +
			"Processed Content: 0\n" +
			"Downloaded Update Files: 7"

		if got != want {
			t.Fatalf("formatPTRStatus() = %q, want %q", got, want)
		}
	})

	t.Run("renders explicit complete status", func(t *testing.T) {
		status := coreptrsync.Status{
			Enabled:                  true,
			ServiceName:              "public tag repository",
			Phase:                    coreptrsync.PhaseIdle,
			IsComplete:               true,
			MetadataSlice:            7,
			ProcessedDefinitionCount: 2,
			ProcessedContentCount:    3,
			DownloadedUpdateCount:    5,
		}

		got := formatPTRStatus(status)
		want := "Service: public tag repository\n" +
			"Phase: idle\n" +
			"Status: Complete\n" +
			"Metadata Slice: 7\n" +
			"Processed Definitions: 2\n" +
			"Processed Content: 3\n" +
			"Downloaded Update Files: 5"

		if got != want {
			t.Fatalf("formatPTRStatus() = %q, want %q", got, want)
		}
	})
}

func TestPTRStatusSummaryText(t *testing.T) {
	t.Run("headline prefers retrying over generic failure wording", func(t *testing.T) {
		retryAtMS := time.Now().Add(45 * time.Second).UnixMilli()
		status := coreptrsync.Status{
			Enabled:      true,
			Phase:        coreptrsync.PhaseRetrying,
			RetryAtMS:    retryAtMS,
			RetryAttempt: 1,
			LastError:    "old error",
		}

		got := ptrHeadlineText(status)
		if got != "PTR sync: retrying" {
			t.Fatalf("ptrHeadlineText() = %q, want %q", got, "PTR sync: retrying")
		}
	})

	t.Run("completion text includes retry countdown", func(t *testing.T) {
		retryAtMS := time.Now().Add(45 * time.Second).UnixMilli()
		status := coreptrsync.Status{
			Enabled:      true,
			Phase:        coreptrsync.PhaseRetrying,
			RetryAtMS:    retryAtMS,
			RetryAttempt: 1,
		}

		got := ptrCompletionStatusText(status)
		want := "PTR server is busy. Retrying in 45s."
		if got != want {
			t.Fatalf("ptrCompletionStatusText() = %q, want %q", got, want)
		}
	})

	t.Run("headline only marks complete when daemon says complete", func(t *testing.T) {
		status := coreptrsync.Status{
			Enabled:               true,
			Phase:                 coreptrsync.PhaseIdle,
			MetadataSlice:         7,
			DownloadedUpdateCount: 5,
		}

		if got := ptrHeadlineText(status); got != "PTR sync: idle" {
			t.Fatalf("ptrHeadlineText() = %q, want %q", got, "PTR sync: idle")
		}

		status.IsComplete = true
		if got := ptrHeadlineText(status); got != "PTR sync: ✓ complete" {
			t.Fatalf("ptrHeadlineText() = %q, want %q", got, "PTR sync: ✓ complete")
		}
	})
}
