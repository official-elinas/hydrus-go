//go:build fyne

package fyneapp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"

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

func TestPrototypeWindowTitle(t *testing.T) {
	if desktopWindowTitle != "hydrus-go curation cockpit — UI ENHANCE BUILD 2026-04-26" {
		t.Fatalf("desktopWindowTitle = %q, want curation cockpit title", desktopWindowTitle)
	}
}

func TestDesktopBuildMarkerText(t *testing.T) {
	if !strings.Contains(desktopHeaderSubtitle, desktopBuildMarker) {
		t.Fatalf("desktopHeaderSubtitle = %q, want build marker %q", desktopHeaderSubtitle, desktopBuildMarker)
	}
	if !strings.Contains(defaultStatusText, desktopBuildMarker) {
		t.Fatalf("defaultStatusText = %q, want build marker %q", defaultStatusText, desktopBuildMarker)
	}
	if !strings.Contains(desktopIntroText, "older build") {
		t.Fatalf("desktopIntroText = %q, want older-build warning", desktopIntroText)
	}
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
			"Remote Metadata Slice: 7\n" +
			"Applied Definition Updates: 100\n" +
			"Applied Content Updates: 200\n" +
			"Stored Repository Update Files: 5\n" +
			"Storage: <db_dir>/repository_updates/<hash-prefix>/<hash>; registered in the repository updates local file domain"

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
			"Remote Metadata Slice: 12\n" +
			"Last error: connection reset by peer\n" +
			"Applied Definition Updates: 0\n" +
			"Applied Content Updates: 0\n" +
			"Stored Repository Update Files: 0\n" +
			"Storage: <db_dir>/repository_updates/<hash-prefix>/<hash>; registered in the repository updates local file domain"

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
			"Remote Metadata Slice: 0\n" +
			"Applied Definition Updates: 0\n" +
			"Applied Content Updates: 0\n" +
			"Stored Repository Update Files: 7\n" +
			"Storage: <db_dir>/repository_updates/<hash-prefix>/<hash>; registered in the repository updates local file domain"

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
			"Remote Metadata Slice: 7\n" +
			"Applied Definition Updates: 2\n" +
			"Applied Content Updates: 3\n" +
			"Stored Repository Update Files: 5\n" +
			"Storage: <db_dir>/repository_updates/<hash-prefix>/<hash>; registered in the repository updates local file domain"

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

func TestPTRPollingRetryHelpers(t *testing.T) {
	t.Run("continues polling before the error limit", func(t *testing.T) {
		if !shouldContinuePTRPollingAfterError(1) {
			t.Fatal("shouldContinuePTRPollingAfterError(1) = false, want true")
		}

		if !shouldContinuePTRPollingAfterError(ptrPollErrorLimit - 1) {
			t.Fatalf("shouldContinuePTRPollingAfterError(%d) = false, want true", ptrPollErrorLimit-1)
		}
	})

	t.Run("stops polling at the error limit", func(t *testing.T) {
		if shouldContinuePTRPollingAfterError(ptrPollErrorLimit) {
			t.Fatalf("shouldContinuePTRPollingAfterError(%d) = true, want false", ptrPollErrorLimit)
		}
	})

	t.Run("formats transient error status with retry counters", func(t *testing.T) {
		got := ptrPollingErrorStatusText(errors.New("temporary network failure"), 2)
		want := "PTR status refresh hit a transient error (2/3): temporary network failure"
		if got != want {
			t.Fatalf("ptrPollingErrorStatusText() = %q, want %q", got, want)
		}
	})
}

func TestNativeWatcherFallbackMessage(t *testing.T) {
	t.Run("supports still images without fallback", func(t *testing.T) {
		if got := nativeWatcherFallbackMessage("image/png"); got != "" {
			t.Fatalf("nativeWatcherFallbackMessage(image/png) = %q, want empty string", got)
		}
	})

	t.Run("explains native video limitation in app", func(t *testing.T) {
		got := nativeWatcherFallbackMessage("video/mp4")
		if got == "" || !strings.Contains(got, "Native video playback") {
			t.Fatalf("nativeWatcherFallbackMessage(video/mp4) = %q, want native video explanation", got)
		}
	})

	t.Run("reports generic unsupported types", func(t *testing.T) {
		got := nativeWatcherFallbackMessage("application/pdf")
		if got != "Viewer not available for application/pdf." {
			t.Fatalf("nativeWatcherFallbackMessage(application/pdf) = %q, want generic unsupported message", got)
		}
	})
}

func TestResetConnectionAttemptState(t *testing.T) {
	previewCtx, previewCancel := context.WithCancel(context.Background())
	watcherCtx, watcherCancel := context.WithCancel(context.Background())

	p := &prototype{
		thumbnailCache: map[int64]fyne.Resource{
			1: nil,
		},
		thumbnailLoads: map[int64]struct{}{
			1: {},
		},
		tileMetadataCache: map[int64]daemonclient.FileMetadata{
			7: {FileID: 7},
		},
		tileMetadataLoads: map[int64]struct{}{
			7: {},
		},
		previewRequestID: 4,
		previewCancel:    previewCancel,
		watcherRequestID: 9,
		watcherCancel:    watcherCancel,
		ptrStatusBusy:    true,
		ptrStatusLoaded:  true,
		ptrStatusRequest: 12,
		thumbnailGen:     2,
		tileMetadataGen:  3,
	}

	p.resetConnectionAttemptState()

	if p.previewRequestID != 5 {
		t.Fatalf("previewRequestID = %d, want 5", p.previewRequestID)
	}

	if p.previewCancel != nil {
		t.Fatal("previewCancel = non-nil, want nil")
	}

	select {
	case <-previewCtx.Done():
	default:
		t.Fatal("preview context was not canceled")
	}

	if p.watcherRequestID != 10 {
		t.Fatalf("watcherRequestID = %d, want 10", p.watcherRequestID)
	}

	if p.watcherCancel != nil {
		t.Fatal("watcherCancel = non-nil, want nil")
	}

	select {
	case <-watcherCtx.Done():
	default:
		t.Fatal("watcher context was not canceled")
	}

	if p.ptrStatusBusy {
		t.Fatal("ptrStatusBusy = true, want false")
	}

	if p.ptrStatusLoaded {
		t.Fatal("ptrStatusLoaded = true, want false")
	}

	if p.ptrStatusRequest != 13 {
		t.Fatalf("ptrStatusRequest = %d, want 13", p.ptrStatusRequest)
	}

	if p.thumbnailGen != 3 {
		t.Fatalf("thumbnailGen = %d, want 3", p.thumbnailGen)
	}

	if len(p.thumbnailLoads) != 0 {
		t.Fatalf("len(thumbnailLoads) = %d, want 0", len(p.thumbnailLoads))
	}

	if len(p.thumbnailCache) != 1 {
		t.Fatalf("len(thumbnailCache) = %d, want 1", len(p.thumbnailCache))
	}

	if p.tileMetadataGen != 4 {
		t.Fatalf("tileMetadataGen = %d, want 4", p.tileMetadataGen)
	}

	if len(p.tileMetadataLoads) != 0 {
		t.Fatalf("len(tileMetadataLoads) = %d, want 0", len(p.tileMetadataLoads))
	}

	if len(p.tileMetadataCache) != 1 {
		t.Fatalf("len(tileMetadataCache) = %d, want 1", len(p.tileMetadataCache))
	}
}

func TestSelectedPreviewCacheHelpers(t *testing.T) {
	t.Run("prefers normalized hash as cache key", func(t *testing.T) {
		key := selectedPreviewCacheKey(daemonclient.RecentItem{FileID: 7, Hash: "  ABC123  "})
		if key != "abc123" {
			t.Fatalf("selectedPreviewCacheKey() = %q, want %q", key, "abc123")
		}
	})

	t.Run("falls back to file id when hash is unavailable", func(t *testing.T) {
		key := selectedPreviewCacheKey(daemonclient.RecentItem{FileID: 42})
		if key != "file-id:42" {
			t.Fatalf("selectedPreviewCacheKey() = %q, want %q", key, "file-id:42")
		}
	})

	t.Run("stores and loads cached selected previews", func(t *testing.T) {
		resource := fyne.NewStaticResource("preview.png", []byte("png-bytes"))
		p := &prototype{selectedPreviewCache: map[string]fyne.Resource{}}
		item := daemonclient.RecentItem{FileID: 9, Hash: "def456"}

		p.storeSelectedPreview(item, resource)

		got, ok := p.lookupSelectedPreview(item)
		if !ok {
			t.Fatal("lookupSelectedPreview() = not found, want cached preview")
		}

		if got != resource {
			t.Fatalf("lookupSelectedPreview() returned unexpected resource %v", got)
		}
	})

	t.Run("ignores empty cache keys and nil resources", func(t *testing.T) {
		p := &prototype{selectedPreviewCache: map[string]fyne.Resource{}}

		p.storeSelectedPreview(daemonclient.RecentItem{}, nil)

		if len(p.selectedPreviewCache) != 0 {
			t.Fatalf("len(selectedPreviewCache) = %d, want 0", len(p.selectedPreviewCache))
		}
	})
}

func TestRightTagRenderingHelpers(t *testing.T) {
	metadata := daemonclient.FileMetadata{
		Tags: map[string]daemonclient.FileMetadataTagService{
			"local": {
				Name:       "my tags",
				TypePretty: "local tag domain",
				DisplayTags: map[string][]string{
					"0": {"creator:alice", "series:zeta"},
				},
			},
		},
	}

	t.Run("sets plain fallback tag text as rich-text segments", func(t *testing.T) {
		p := &prototype{tagsRichText: widget.NewRichText()}
		p.setRightTagsText(defaultTagsText)

		got := flattenTextSegments(t, p.tagsRichText.Segments)
		if got != defaultTagsText {
			t.Fatalf("flattened segments = %q, want %q", got, defaultTagsText)
		}
	})

	t.Run("reuses colored metadata segments in the right pane", func(t *testing.T) {
		p := &prototype{tagsRichText: widget.NewRichText()}
		p.setRightTagsMetadata(metadata)

		got := flattenTextSegments(t, p.tagsRichText.Segments)
		want := "my tags (local tag domain)\ncurrent: creator:alice, series:zeta\n"
		if got != want {
			t.Fatalf("flattened segments = %q, want %q", got, want)
		}

		creatorSegment := findTextSegment(t, p.tagsRichText.Segments, "creator:alice")
		if creatorSegment.Style.ColorName != hydrusTagColorCreator {
			t.Fatalf("creator segment color = %q, want %q", creatorSegment.Style.ColorName, hydrusTagColorCreator)
		}
	})
}

func TestParseTagEditorInput(t *testing.T) {
	t.Run("splits on commas and newlines while trimming blanks", func(t *testing.T) {
		got := parseTagEditorInput(" creator:alice,\nseries:zeta\n\tcharacter:bob  ")
		want := []string{"creator:alice", "series:zeta", "character:bob"}

		if len(got) != len(want) {
			t.Fatalf("len(parseTagEditorInput()) = %d, want %d (%v)", len(got), len(want), got)
		}

		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("parseTagEditorInput()[%d] = %q, want %q", index, got[index], want[index])
			}
		}
	})

	t.Run("deduplicates repeated tags while preserving first-seen order", func(t *testing.T) {
		got := parseTagEditorInput("creator:alice, creator:alice\nseries:zeta,creator:alice")
		want := []string{"creator:alice", "series:zeta"}

		if len(got) != len(want) {
			t.Fatalf("len(parseTagEditorInput()) = %d, want %d (%v)", len(got), len(want), got)
		}

		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("parseTagEditorInput()[%d] = %q, want %q", index, got[index], want[index])
			}
		}
	})
}

func TestAppendTagEditorInput(t *testing.T) {
	t.Run("appends new unique tag on its own line", func(t *testing.T) {
		got := appendTagEditorInput("creator:alice", "series:zeta")
		want := "creator:alice\nseries:zeta"
		if got != want {
			t.Fatalf("appendTagEditorInput() = %q, want %q", got, want)
		}
	})

	t.Run("ignores duplicate appended tags", func(t *testing.T) {
		got := appendTagEditorInput("creator:alice\nseries:zeta", " creator:alice ")
		want := "creator:alice\nseries:zeta"
		if got != want {
			t.Fatalf("appendTagEditorInput() = %q, want %q", got, want)
		}
	})
}

func TestCurrentTagEditorPrefix(t *testing.T) {
	t.Run("uses final comma-or-newline-delimited token", func(t *testing.T) {
		got := currentTagEditorPrefix("creator:alice\nseries:ze")
		if got != "series:ze" {
			t.Fatalf("currentTagEditorPrefix() = %q, want %q", got, "series:ze")
		}
	})

	t.Run("trims trailing separators and whitespace", func(t *testing.T) {
		got := currentTagEditorPrefix("creator:alice,   ")
		if got != "" {
			t.Fatalf("currentTagEditorPrefix() = %q, want empty string", got)
		}
	})
}

func TestFilterTagSuggestions(t *testing.T) {
	got := filterTagSuggestions(
		[]string{"creator:alice", "series:zeta", "creator:alina"},
		"creator:a",
	)
	want := []string{"creator:alice", "creator:alina"}

	if len(got) != len(want) {
		t.Fatalf("len(filterTagSuggestions()) = %d, want %d (%v)", len(got), len(want), got)
	}

	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("filterTagSuggestions()[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}

func TestMergeTagSuggestions(t *testing.T) {
	got := mergeTagSuggestions(
		[]string{"creator:alice", "series:zeta"},
		[]string{"creator:alice", "creator:alina"},
	)
	want := []string{"creator:alice", "series:zeta", "creator:alina"}

	if len(got) != len(want) {
		t.Fatalf("len(mergeTagSuggestions()) = %d, want %d (%v)", len(got), len(want), got)
	}

	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("mergeTagSuggestions()[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}

func TestCollectTagEditorSuggestions(t *testing.T) {
	metadata := daemonclient.FileMetadata{
		Tags: map[string]daemonclient.FileMetadataTagService{
			"local": {
				DisplayTags: map[string][]string{
					"0": {"series:zeta", "creator:alice"},
					"1": {"pending:review"},
				},
			},
			"downloader": {
				StorageTags: map[string][]string{
					"0": {"creator:alice", "character:bob"},
				},
			},
		},
	}

	got := collectTagEditorSuggestions(metadata)
	want := []string{"character:bob", "creator:alice", "pending:review", "series:zeta"}

	if len(got) != len(want) {
		t.Fatalf("len(collectTagEditorSuggestions()) = %d, want %d (%v)", len(got), len(want), got)
	}

	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("collectTagEditorSuggestions()[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}

func TestPrototypeCollectLoadedSearchSuggestions(t *testing.T) {
	p := &prototype{
		tileMetadataCache: map[int64]daemonclient.FileMetadata{
			1: {
				Tags: map[string]daemonclient.FileMetadataTagService{
					"local": {
						DisplayTags: map[string][]string{
							"0": {"series:zeta", "creator:alice"},
						},
					},
				},
			},
			2: {
				Tags: map[string]daemonclient.FileMetadataTagService{
					"downloader": {
						StorageTags: map[string][]string{
							"0": {"creator:alice", "character:bob"},
						},
					},
				},
			},
		},
	}

	got := p.collectLoadedSearchSuggestions()
	want := []string{"character:bob", "creator:alice", "series:zeta"}

	if len(got) != len(want) {
		t.Fatalf("len(collectLoadedSearchSuggestions()) = %d, want %d (%v)", len(got), len(want), got)
	}

	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("collectLoadedSearchSuggestions()[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}

func TestSearchSuggestionsHint(t *testing.T) {
	t.Run("empty prefix while connected", func(t *testing.T) {
		got := searchSuggestionsHint("", true, nil, nil)
		want := "Type a tag prefix to load autocomplete suggestions from hydrusd."
		if got != want {
			t.Fatalf("searchSuggestionsHint() = %q, want %q", got, want)
		}
	})

	t.Run("shows fallback message on remote error", func(t *testing.T) {
		got := searchSuggestionsHint("creator:a", true, []string{"creator:alice"}, context.DeadlineExceeded)
		want := "Showing loaded-tag suggestions while hydrusd autocomplete is unavailable."
		if got != want {
			t.Fatalf("searchSuggestionsHint() = %q, want %q", got, want)
		}
	})

	t.Run("no local matches while disconnected", func(t *testing.T) {
		got := searchSuggestionsHint("creator:a", false, nil, nil)
		want := "No matching loaded tags. Connect to hydrusd for daemon-backed autocomplete."
		if got != want {
			t.Fatalf("searchSuggestionsHint() = %q, want %q", got, want)
		}
	})
}

func TestFormatTagMetadataSegments(t *testing.T) {
	metadata := daemonclient.FileMetadata{
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
		},
	}

	segments := formatTagMetadataSegments(metadata)
	if len(segments) == 0 {
		t.Fatal("formatTagMetadataSegments() returned no segments")
	}

	firstSegment, ok := segments[0].(*widget.TextSegment)
	if !ok {
		t.Fatalf("first segment type = %T, want *widget.TextSegment", segments[0])
	}

	if firstSegment.Style != widget.RichTextStyleStrong {
		t.Fatalf("first segment style = %#v, want RichTextStyleStrong", firstSegment.Style)
	}

	got := flattenTextSegments(t, segments)
	want := "downloader tags (local tag domain)\n" +
		"pending: pending:review\n\n" +
		"my tags (local tag domain)\n" +
		"current: creator:alice, series:zeta\n" +
		"deleted: old:tag\n"
	if got != want {
		t.Fatalf("flattened segments = %q, want %q", got, want)
	}

	creatorSegment := findTextSegment(t, segments, "creator:alice")
	if creatorSegment.Style.ColorName != hydrusTagColorCreator {
		t.Fatalf("creator segment color = %q, want %q", creatorSegment.Style.ColorName, hydrusTagColorCreator)
	}

	seriesSegment := findTextSegment(t, segments, "series:zeta")
	if seriesSegment.Style.ColorName != hydrusTagColorSeries {
		t.Fatalf("series segment color = %q, want %q", seriesSegment.Style.ColorName, hydrusTagColorSeries)
	}
}

func TestFormatTagMetadataSegments_NoTags(t *testing.T) {
	segments := formatTagMetadataSegments(daemonclient.FileMetadata{})
	got := flattenTextSegments(t, segments)
	want := "No tag metadata is available for the selected file."
	if got != want {
		t.Fatalf("flattened segments = %q, want %q", got, want)
	}
}

func TestMetadataTagColorName(t *testing.T) {
	tests := []struct {
		name string
		tag  string
		want fyne.ThemeColorName
	}{
		{name: "creator namespace", tag: "creator:alice", want: hydrusTagColorCreator},
		{name: "series namespace", tag: "series:zeta", want: hydrusTagColorSeries},
		{name: "character namespace", tag: "character:bob", want: hydrusTagColorCharacter},
		{name: "unnamespaced tag", tag: "tag_without_namespace", want: hydrusTagColorUnnamespaced},
		{name: "unknown namespace", tag: "studio:kemono", want: hydrusTagColorNamespacedFallback},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := metadataTagColorName(tt.tag)
			if got != tt.want {
				t.Fatalf("metadataTagColorName(%q) = %q, want %q", tt.tag, got, tt.want)
			}
		})
	}
}

func TestSortRecentItemsForDisplay(t *testing.T) {
	importedNewest := int64(300)
	importedMiddle := int64(200)
	importedOldest := int64(100)

	items := []daemonclient.RecentItem{
		{FileID: 1, Hash: "hash-one-abcdef", ImportedAtMS: &importedMiddle},
		{FileID: 2, Hash: "hash-two-abcdef", ImportedAtMS: &importedNewest},
		{FileID: 3, Hash: "hash-three-abcdef", ImportedAtMS: &importedOldest},
	}

	metadataByID := map[int64]daemonclient.FileMetadata{
		1: {FileID: 1, Size: 500, Tags: map[string]daemonclient.FileMetadataTagService{"local": {DisplayTags: map[string][]string{"0": {"creator:alice"}}}}},
		2: {FileID: 2, Size: 100, Tags: map[string]daemonclient.FileMetadataTagService{"local": {DisplayTags: map[string][]string{"0": {"creator:zeta"}}}}},
		3: {FileID: 3, Size: 900, Tags: map[string]daemonclient.FileMetadataTagService{"local": {DisplayTags: map[string][]string{"0": {"creator:bob"}}}}},
	}

	lookup := func(fileID int64) (daemonclient.FileMetadata, bool) {
		metadata, ok := metadataByID[fileID]
		return metadata, ok
	}

	t.Run("sorts by newest imported first", func(t *testing.T) {
		sorted := append([]daemonclient.RecentItem(nil), items...)
		sortRecentItemsForDisplay(sorted, gallerySortNewest, lookup)
		assertRecentItemOrder(t, sorted, []int64{2, 1, 3})
	})

	t.Run("sorts by oldest imported first", func(t *testing.T) {
		sorted := append([]daemonclient.RecentItem(nil), items...)
		sortRecentItemsForDisplay(sorted, gallerySortOldest, lookup)
		assertRecentItemOrder(t, sorted, []int64{3, 1, 2})
	})

	t.Run("sorts by display label ascending", func(t *testing.T) {
		sorted := append([]daemonclient.RecentItem(nil), items...)
		sortRecentItemsForDisplay(sorted, gallerySortNameAZ, lookup)
		assertRecentItemOrder(t, sorted, []int64{1, 3, 2})
	})

	t.Run("sorts by size descending", func(t *testing.T) {
		sorted := append([]daemonclient.RecentItem(nil), items...)
		sortRecentItemsForDisplay(sorted, gallerySortSizeDesc, lookup)
		assertRecentItemOrder(t, sorted, []int64{3, 1, 2})
	})

	t.Run("sorts unknown sizes after known metadata", func(t *testing.T) {
		sorted := []daemonclient.RecentItem{
			{FileID: 4, Hash: "hash-four-abcdef", ImportedAtMS: &importedNewest},
			items[0],
			items[1],
		}
		sortRecentItemsForDisplay(sorted, gallerySortSizeAsc, lookup)
		assertRecentItemOrder(t, sorted, []int64{2, 1, 4})
	})
}

func TestGallerySortRequiresMetadata(t *testing.T) {
	if gallerySortRequiresMetadata(gallerySortNewest) {
		t.Fatal("gallerySortRequiresMetadata(newest) = true, want false")
	}

	if !gallerySortRequiresMetadata(gallerySortNameAZ) {
		t.Fatal("gallerySortRequiresMetadata(name A-Z) = false, want true")
	}

	if !gallerySortRequiresMetadata(gallerySortSizeDesc) {
		t.Fatal("gallerySortRequiresMetadata(size descending) = false, want true")
	}
}

func TestRecentItemMatchesSystemPredicate(t *testing.T) {
	width := int64(1920)
	height := int64(1080)
	metadata := daemonclient.FileMetadata{
		Size:      2048,
		Width:     &width,
		Height:    &height,
		IsLocal:   true,
		IsTrashed: false,
		IsDeleted: false,
		Ratings: map[string]any{
			"favorites-service": true,
		},
	}

	tests := []struct {
		name      string
		predicate string
		want      bool
	}{
		{name: "size minimum", predicate: "size>=1024", want: true},
		{name: "size exact fail", predicate: "size=1024", want: false},
		{name: "width minimum", predicate: "width>=1920", want: true},
		{name: "height maximum", predicate: "height<=1080", want: true},
		{name: "resolution minimum", predicate: "resolution>=1280x720", want: true},
		{name: "resolution exact fail", predicate: "resolution=1280x720", want: false},
		{name: "local shorthand", predicate: "local", want: true},
		{name: "favorite shorthand", predicate: "favorite", want: true},
		{name: "favourite shorthand", predicate: "favourite", want: true},
		{name: "trashed shorthand", predicate: "trashed", want: false},
		{name: "local explicit true", predicate: "local=true", want: true},
		{name: "favorite explicit true", predicate: "favorite=true", want: true},
		{name: "favorite explicit false", predicate: "favorite=false", want: false},
		{name: "deleted explicit false", predicate: "deleted=false", want: true},
		{name: "unknown predicate", predicate: "favorites=true", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := recentItemMatchesSystemPredicate(tt.predicate, metadata, true)
			if got != tt.want {
				t.Fatalf("recentItemMatchesSystemPredicate(%q) = %t, want %t", tt.predicate, got, tt.want)
			}
		})
	}
}

func TestRecentItemMatchesQuery_WithPredicatesAndText(t *testing.T) {
	width := int64(1920)
	height := int64(1080)
	metadata := daemonclient.FileMetadata{
		Hash:    "abcdef1234567890",
		MIME:    "image/png",
		Size:    4096,
		Width:   &width,
		Height:  &height,
		IsLocal: true,
		Ratings: map[string]any{
			"favourites": true,
		},
		Tags: map[string]daemonclient.FileMetadataTagService{
			"local": {
				DisplayTags: map[string][]string{
					"0": {"creator:alice", "series:zeta"},
				},
			},
		},
	}
	item := daemonclient.RecentItem{Hash: "abcdef1234567890", MIME: "image/png"}

	if !recentItemMatchesQuery("alice system:size>=2048 system:resolution>=1280x720", item, metadata, true) {
		t.Fatal("recentItemMatchesQuery() = false, want true for mixed text and system predicates")
	}

	if recentItemMatchesQuery("alice system:size>9000", item, metadata, true) {
		t.Fatal("recentItemMatchesQuery() = true, want false for failing system predicate")
	}

	if !recentItemMatchesQuery("system:favorite=true alice", item, metadata, true) {
		t.Fatal("recentItemMatchesQuery() = false, want true for favorite predicate")
	}

	if recentItemMatchesQuery("system:size>=2048", item, daemonclient.FileMetadata{}, false) {
		t.Fatal("recentItemMatchesQuery() = true, want false when metadata is unavailable for system predicate")
	}
}

func TestMetadataHasFavorite(t *testing.T) {
	if !metadataHasFavorite(daemonclient.FileMetadata{Ratings: map[string]any{"favourites": true}}) {
		t.Fatal("metadataHasFavorite() = false, want true")
	}

	if metadataHasFavorite(daemonclient.FileMetadata{Ratings: map[string]any{"stars": 5.0, "favourites": false}}) {
		t.Fatal("metadataHasFavorite() = true, want false when no boolean-like rating is true")
	}
}

func TestFormatRecentTileText(t *testing.T) {
	t.Run("prefers creator tag for title and series tag for subtitle", func(t *testing.T) {
		width := int64(640)
		height := int64(480)
		item := daemonclient.RecentItem{
			Hash:   "abcdef1234567890",
			MIME:   "image/png",
			Width:  &width,
			Height: &height,
		}
		metadata := daemonclient.FileMetadata{
			Tags: map[string]daemonclient.FileMetadataTagService{
				"local": {
					DisplayTags: map[string][]string{
						"0": {"creator:alice", "series:zeta"},
					},
				},
			},
		}

		title, subtitle := formatRecentTileText(item, metadata, true)
		if title != "alice" {
			t.Fatalf("title = %q, want alice", title)
		}

		if subtitle != "zeta • image/png • 640x480" {
			t.Fatalf("subtitle = %q, want %q", subtitle, "zeta • image/png • 640x480")
		}
	})

	t.Run("falls back to short hash without metadata", func(t *testing.T) {
		item := daemonclient.RecentItem{Hash: "abcdef1234567890", MIME: "image/jpeg"}

		title, subtitle := formatRecentTileText(item, daemonclient.FileMetadata{}, false)
		if title != "abcdef123456" {
			t.Fatalf("title = %q, want abcdef123456", title)
		}

		if subtitle != "image/jpeg" {
			t.Fatalf("subtitle = %q, want image/jpeg", subtitle)
		}
	})
}

func TestRecentItemMatchesQuery(t *testing.T) {
	metadata := daemonclient.FileMetadata{
		Hash: "abcdef1234567890",
		MIME: "image/png",
		Tags: map[string]daemonclient.FileMetadataTagService{
			"local": {
				DisplayTags: map[string][]string{
					"0": {"creator:alice", "series:zeta"},
				},
			},
		},
	}
	item := daemonclient.RecentItem{Hash: "abcdef1234567890", MIME: "image/png"}

	if !recentItemMatchesQuery("alice", item, metadata, true) {
		t.Fatal("recentItemMatchesQuery(alice) = false, want true")
	}

	if !recentItemMatchesQuery("abcdef123456", item, daemonclient.FileMetadata{}, false) {
		t.Fatal("recentItemMatchesQuery(hash) = false, want true")
	}

	if recentItemMatchesQuery("missing-tag", item, metadata, true) {
		t.Fatal("recentItemMatchesQuery(missing-tag) = true, want false")
	}
}

func flattenTextSegments(t *testing.T, segments []widget.RichTextSegment) string {
	t.Helper()

	var builder strings.Builder
	for _, segment := range segments {
		textSegment, ok := segment.(*widget.TextSegment)
		if !ok {
			t.Fatalf("segment type = %T, want *widget.TextSegment", segment)
		}

		builder.WriteString(textSegment.Text)
	}

	return builder.String()
}

func findTextSegment(t *testing.T, segments []widget.RichTextSegment, text string) *widget.TextSegment {
	t.Helper()

	for _, segment := range segments {
		textSegment, ok := segment.(*widget.TextSegment)
		if !ok {
			continue
		}

		if textSegment.Text == text {
			return textSegment
		}
	}

	t.Fatalf("did not find text segment %q", text)
	return nil
}

func assertRecentItemOrder(t *testing.T, items []daemonclient.RecentItem, want []int64) {
	t.Helper()

	if len(items) != len(want) {
		t.Fatalf("len(items) = %d, want %d", len(items), len(want))
	}

	for index, fileID := range want {
		if items[index].FileID != fileID {
			t.Fatalf("items[%d].FileID = %d, want %d", index, items[index].FileID, fileID)
		}
	}
}
