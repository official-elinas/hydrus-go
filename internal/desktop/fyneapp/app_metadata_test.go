//go:build fyne

package fyneapp

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
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
	if desktopWindowTitle != "hydrus-go curation cockpit" {
		t.Fatalf("desktopWindowTitle = %q, want curation cockpit title", desktopWindowTitle)
	}
}

func TestPrototypeShellText(t *testing.T) {
	if desktopHeaderTitle != "Hydrus Go" {
		t.Fatalf("desktopHeaderTitle = %q, want Hydrus Go", desktopHeaderTitle)
	}
	if desktopHeaderSubtitle != "Daemon-backed browse, import, search, and PTR" {
		t.Fatalf("desktopHeaderSubtitle = %q, want production subtitle", desktopHeaderSubtitle)
	}
	if defaultStatusText != "Ready. Connect to hydrusd to start validation." {
		t.Fatalf("defaultStatusText = %q, want production status text", defaultStatusText)
	}
	if defaultPreviewText != "Select an image or video to preview." {
		t.Fatalf("defaultPreviewText = %q, want compact preview text", defaultPreviewText)
	}
	if defaultMetadataText != "Select a file from the grid to inspect daemon-backed metadata." {
		t.Fatalf("defaultMetadataText = %q, want compact metadata text", defaultMetadataText)
	}
}

func TestSelectedPreviewLabelDoesNotWrap(t *testing.T) {
	label := newSelectedPreviewLabel(defaultPreviewText)

	if label.Wrapping != fyne.TextWrapOff {
		t.Fatalf("label.Wrapping = %v, want %v", label.Wrapping, fyne.TextWrapOff)
	}
	if label.Alignment != fyne.TextAlignCenter {
		t.Fatalf("label.Alignment = %v, want %v", label.Alignment, fyne.TextAlignCenter)
	}
}

func TestFormatPTRBytesPerSecond(t *testing.T) {
	if got := formatPTRBytesPerSecond(0); got != "0.00 MB/s" {
		t.Fatalf("formatPTRBytesPerSecond(0) = %q, want %q", got, "0.00 MB/s")
	}

	oneMiBPerSecond := int64(1024 * 1024)
	if got := formatPTRBytesPerSecond(oneMiBPerSecond); got != "1.00 MB/s" {
		t.Fatalf("formatPTRBytesPerSecond(1MiB/s) = %q, want %q", got, "1.00 MB/s")
	}
}

func TestBuildContentOmitsDebugChrome(t *testing.T) {
	p := &prototype{
		connectionLabel:   widget.NewLabel("status"),
		queueSummaryLabel: widget.NewLabel("queue"),
		queueDetailLabel:  widget.NewLabel("detail"),
		leftTagsRichText:  widget.NewRichText(),
		searchHintLabel:   widget.NewLabel("hint"),
		previewImage:      canvas.NewImageFromImage(nil),
		previewLabel:      widget.NewLabel(defaultPreviewText),
		metadataLabel:     widget.NewLabel(defaultMetadataText),
		tagsRichText:      widget.NewRichText(),
		activityLabel:     widget.NewLabel("activity"),
		statusBarLabel:    widget.NewLabel(defaultStatusText),
		ptrStatusLabel:    widget.NewLabel("ptr"),
		ptrHeadlineLabel:  widget.NewLabel("headline"),
		ptrPendingLabel:   widget.NewLabel("pending"),
		ptrProgressBar:    widget.NewProgressBarInfinite(),
		gridHost:          container.NewStack(),
		searchEntry:       widget.NewEntry(),
		gallerySortSelect: widget.NewSelect(gallerySortModes, nil),
		searchSuggestionsList: widget.NewList(
			func() int { return 0 },
			func() fyne.CanvasObject { return widget.NewLabel("") },
			func(widget.ListItemID, fyne.CanvasObject) {},
		),
		queueList: widget.NewList(
			func() int { return 0 },
			func() fyne.CanvasObject { return widget.NewLabel("") },
			func(widget.ListItemID, fyne.CanvasObject) {},
		),
		retrySelectedButton:  widget.NewButton("Retry Selected", nil),
		removeSelectedButton: widget.NewButton("Remove Selected", nil),
		retryFailedButton:    widget.NewButton("Retry Failed", nil),
		clearFinishedButton:  widget.NewButton("Clear Finished", nil),
		clearQueueButton:     widget.NewButton("Clear Queue", nil),
		editTagsButton:       widget.NewButton("Edit Tags", nil),
		ptrRefreshButton:     widget.NewButton("Refresh PTR Status", nil),
		ptrSyncButton:        widget.NewButton("Manual Sync", nil),
	}
	content := p.buildContent()
	texts := collectCanvasObjectTexts(content)
	joined := strings.Join(texts, "\n")

	if strings.Contains(joined, "NEW WINDOWS TEST BUILD") {
		t.Fatal("build content still includes debug header marker")
	}
	if strings.Contains(joined, "rebuilt artifact marker for Windows smoke testing") {
		t.Fatal("build content still includes build banner text")
	}
	if strings.Contains(joined, "older build") {
		t.Fatal("build content still includes older build warning")
	}
	if !strings.Contains(joined, desktopHeaderTitle) {
		t.Fatalf("build content texts = %q, want header title %q", joined, desktopHeaderTitle)
	}
	if !strings.Contains(joined, desktopHeaderSubtitle) {
		t.Fatalf("build content texts = %q, want header subtitle %q", joined, desktopHeaderSubtitle)
	}
}

func TestFormatPTRStatus(t *testing.T) {
	t.Run("renders basic idle status", func(t *testing.T) {
		mappingCount := int64(321)
		status := coreptrsync.Status{
			Enabled:                         true,
			ServiceName:                     "public tag repository",
			Host:                            "ptr.hydrus.network",
			Port:                            45871,
			AccountMode:                     coreptrsync.AccountModeSharedReadOnly,
			Phase:                           "idle",
			IsRunning:                       false,
			MetadataSlice:                   7,
			ProcessedDefinitionCount:        100,
			ProcessedContentCount:           200,
			DownloadedUpdateCount:           5,
			DownloadedUpdateBytes:           4096,
			CurrentRunDownloadedBytes:       1024,
			CurrentRunDownloadMS:            250,
			CurrentRunBytesPerSecond:        4096,
			CurrentRunNetworkFetchedBytes:   1024,
			CurrentRunNetworkFetchMS:        125,
			CurrentRunNetworkBytesPerSecond: 8192,
			PendingDownloadCount:            2,
			PendingProcessCount:             3,
			LastSyncMappingCount:            &mappingCount,
		}

		got := formatPTRStatus(status)
		want := "Service: public tag repository\n" +
			"Endpoint: ptr.hydrus.network:45871\n" +
			"Account: shared-read-only\n" +
			"Phase: idle\n" +
			"Status: Idle\n" +
			"Remote Metadata Slice: 7\n" +
			"Pending Download Bundles: 2\n" +
			"Pending Process Bundles: 3\n" +
			"Bundle Download Progress: 5/7 (71%)\n" +
			"Bundle Apply Progress: 300/303 (99%)\n" +
			"Next Update Due: unknown\n" +
			"Applied Definition Bundles: 100\n" +
			"Applied Content Bundles: 200\n" +
			"Stored Repository Update Bundles: 5\n" +
			"Stored Repository Update Bytes: 4096\n" +
			"Current Run Network Fetched Bytes: 1024\n" +
			"Current Run Effective Progress Window MS: 250\n" +
			"Current Run Effective Progress Rate: 0.00 MB/s\n" +
			"Current Run Raw Network Fetch MS: 125\n" +
			"Current Run Raw Network Fetch Rate: 0.01 MB/s\n" +
			"Verified Current PTR Mappings: 321\n" +
			"Storage: SQLite repository update rows with raw update bodies; client_files reserved for imported media only"

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
			"Pending Download Bundles: 0\n" +
			"Pending Process Bundles: 0\n" +
			"Bundle Download Progress: 0/0\n" +
			"Bundle Apply Progress: 0/0\n" +
			"Next Update Due: unknown\n" +
			"Last error: connection reset by peer\n" +
			"Applied Definition Bundles: 0\n" +
			"Applied Content Bundles: 0\n" +
			"Stored Repository Update Bundles: 0\n" +
			"Stored Repository Update Bytes: 0\n" +
			"Current Run Network Fetched Bytes: 0\n" +
			"Current Run Effective Progress Window MS: 0\n" +
			"Current Run Effective Progress Rate: 0.00 MB/s\n" +
			"Current Run Raw Network Fetch MS: 0\n" +
			"Current Run Raw Network Fetch Rate: 0.00 MB/s\n" +
			"Storage: SQLite repository update rows with raw update bodies; client_files reserved for imported media only"

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
			"Status: Waiting to retry in 2m\n" +
			"Remote Metadata Slice: 0\n" +
			"Pending Download Bundles: 0\n" +
			"Pending Process Bundles: 0\n" +
			"Bundle Download Progress: 7/7 (100%)\n" +
			"Bundle Apply Progress: 0/0\n" +
			"Next Update Due: unknown\n" +
			"Applied Definition Bundles: 0\n" +
			"Applied Content Bundles: 0\n" +
			"Stored Repository Update Bundles: 7\n" +
			"Stored Repository Update Bytes: 0\n" +
			"Current Run Network Fetched Bytes: 0\n" +
			"Current Run Effective Progress Window MS: 0\n" +
			"Current Run Effective Progress Rate: 0.00 MB/s\n" +
			"Current Run Raw Network Fetch MS: 0\n" +
			"Current Run Raw Network Fetch Rate: 0.00 MB/s\n" +
			"Storage: SQLite repository update rows with raw update bodies; client_files reserved for imported media only"

		if got != want {
			t.Fatalf("formatPTRStatus() = %q, want %q", got, want)
		}
	})

	t.Run("renders explicit complete status", func(t *testing.T) {
		mappingCount := int64(5)
		nextUpdateDue := time.Now().Add(119 * time.Second).Unix()
		status := coreptrsync.Status{
			Enabled:                  true,
			ServiceName:              "public tag repository",
			Phase:                    coreptrsync.PhaseIdle,
			IsComplete:               true,
			IsUpToDate:               true,
			MetadataSlice:            7,
			ProcessedDefinitionCount: 2,
			ProcessedContentCount:    3,
			DownloadedUpdateCount:    5,
			NextUpdateDue:            nextUpdateDue,
			LastSyncMappingCount:     &mappingCount,
		}

		got := formatPTRStatus(status)
		want := "Service: public tag repository\n" +
			"Phase: idle\n" +
			"Status: Up to date\n" +
			"Remote Metadata Slice: 7\n" +
			"Pending Download Bundles: 0\n" +
			"Pending Process Bundles: 0\n" +
			"Bundle Download Progress: 5/5 (100%)\n" +
			"Bundle Apply Progress: 5/5 (100%)\n" +
			"Next Update Due: in 2m\n" +
			"Applied Definition Bundles: 2\n" +
			"Applied Content Bundles: 3\n" +
			"Stored Repository Update Bundles: 5\n" +
			"Stored Repository Update Bytes: 0\n" +
			"Current Run Network Fetched Bytes: 0\n" +
			"Current Run Effective Progress Window MS: 0\n" +
			"Current Run Effective Progress Rate: 0.00 MB/s\n" +
			"Current Run Raw Network Fetch MS: 0\n" +
			"Current Run Raw Network Fetch Rate: 0.00 MB/s\n" +
			"Verified Current PTR Mappings: 5\n" +
			"Storage: SQLite repository update rows with raw update bodies; client_files reserved for imported media only"

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
		want := "PTR sync is waiting to retry in 45s."
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
		if got := ptrHeadlineText(status); got != "PTR sync: ✓ caught up locally" {
			t.Fatalf("ptrHeadlineText() = %q, want %q", got, "PTR sync: ✓ caught up locally")
		}

		status.IsUpToDate = true
		if got := ptrHeadlineText(status); got != "PTR sync: ✓ up to date" {
			t.Fatalf("ptrHeadlineText() = %q, want %q", got, "PTR sync: ✓ up to date")
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

	t.Run("supports ffmpeg-backed still images without fallback", func(t *testing.T) {
		if got := nativeWatcherFallbackMessage("image/avif"); got != "" {
			t.Fatalf("nativeWatcherFallbackMessage(image/avif) = %q, want empty string", got)
		}
	})

	t.Run("explains native video limitation in app", func(t *testing.T) {
		got := nativeWatcherFallbackMessage("video/mp4")
		if supportsNativeVideoPlayback() {
			if got != "" {
				t.Fatalf("nativeWatcherFallbackMessage(video/mp4) = %q, want empty string when playback is available", got)
			}
			return
		}

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

func TestBuildSelectedPreviewResource_FFmpegFallbacks(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is required for preview fallback tests")
	}

	t.Run("renders avif still image into a previewable PNG resource", func(t *testing.T) {
		payload := writeFFmpegPreviewFixture(t, ".avif")
		resource, err := buildSelectedPreviewResource(context.Background(), payload, "image/avif", 7)
		if err != nil {
			t.Fatalf("buildSelectedPreviewResource(avif) error = %v", err)
		}

		decoded, _, err := image.Decode(bytes.NewReader(resource.Content()))
		if err != nil {
			t.Fatalf("image.Decode(resource avif) error = %v", err)
		}
		if decoded.Bounds().Dx() != 4 || decoded.Bounds().Dy() != 4 {
			t.Fatalf("decoded avif preview size = %dx%d, want 4x4", decoded.Bounds().Dx(), decoded.Bounds().Dy())
		}
	})

	tests := []struct {
		name string
		ext  string
		mime string
	}{
		{name: "mp4", ext: ".mp4", mime: "video/mp4"},
		{name: "webm", ext: ".webm", mime: "video/webm"},
		{name: "mkv", ext: ".mkv", mime: "video/x-matroska"},
		{name: "mov", ext: ".mov", mime: "video/quicktime"},
		{name: "avi", ext: ".avi", mime: "video/x-msvideo"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run("renders "+tt.name+" poster frame for selected preview", func(t *testing.T) {
			payload := writeFFmpegVideoPreviewFixture(t, tt.ext)
			resource, err := buildSelectedPreviewResource(context.Background(), payload, tt.mime, 8)
			if err != nil {
				t.Fatalf("buildSelectedPreviewResource(%s) error = %v", tt.mime, err)
			}

			decoded, _, err := image.Decode(bytes.NewReader(resource.Content()))
			if err != nil {
				t.Fatalf("image.Decode(resource %s poster) error = %v", tt.mime, err)
			}
			if decoded.Bounds().Dx() != 4 || decoded.Bounds().Dy() != 4 {
				t.Fatalf("decoded %s poster size = %dx%d, want 4x4", tt.mime, decoded.Bounds().Dx(), decoded.Bounds().Dy())
			}
		})

		t.Run("streams at least one frame for "+tt.name+" playback", func(t *testing.T) {
			path := writeFFmpegVideoFixturePath(t, tt.ext)
			frameCount := 0
			err := streamVideoFrames(context.Background(), path, watcherVideoMaxDimension, false, func(img image.Image) {
				if img != nil {
					frameCount++
				}
			})
			if err != nil {
				t.Fatalf("streamVideoFrames(%s) error = %v", tt.ext, err)
			}
			if frameCount == 0 {
				t.Fatalf("frameCount = 0, want at least one frame for %s", tt.ext)
			}
		})
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
		want := "Type a tag prefix to load autocomplete suggestions from hydrusd. Search uses daemon-backed tags and supported system predicates."
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

	t.Run("suggestions invite daemon-backed search", func(t *testing.T) {
		got := searchSuggestionsHint("creator:a", true, []string{"creator:alice"}, nil)
		want := "Click a suggestion to search the daemon-backed library by that tag."
		if got != want {
			t.Fatalf("searchSuggestionsHint() = %q, want %q", got, want)
		}
	})
}

func TestPrototypeSplitGallerySearchQuery(t *testing.T) {
	t.Run("routes explicit tag terms to daemon search and keeps local overlay terms separate", func(t *testing.T) {
		p := &prototype{}

		remoteTags, daemonSystem, overlayTerms := p.splitGallerySearchQuery("creator:alice system:size>=2048 SYSTEM:local SYSTEM:WIDTH<800 alice")
		if !slices.Equal(remoteTags, []string{"creator:alice"}) {
			t.Fatalf("remoteTags = %v, want [creator:alice]", remoteTags)
		}
		if !slices.Equal(daemonSystem, []string{"size>=2048", "width<800"}) {
			t.Fatalf("daemonSystem = %v, want [size>=2048 width<800]", daemonSystem)
		}
		if !slices.Equal(overlayTerms, []string{"SYSTEM:local", "alice"}) {
			t.Fatalf("overlayTerms = %v, want [SYSTEM:local alice]", overlayTerms)
		}
	})

	t.Run("promotes exact suggestion matches into daemon search even without a namespace", func(t *testing.T) {
		p := &prototype{searchSuggestions: []string{"landscape", "creator:alice"}}

		remoteTags, daemonSystem, overlayTerms := p.splitGallerySearchQuery("landscape")
		if !slices.Equal(remoteTags, []string{"landscape"}) {
			t.Fatalf("remoteTags = %v, want [landscape]", remoteTags)
		}
		if len(daemonSystem) != 0 {
			t.Fatalf("daemonSystem = %v, want empty slice", daemonSystem)
		}
		if len(overlayTerms) != 0 {
			t.Fatalf("overlayTerms = %v, want empty slice", overlayTerms)
		}
	})

	t.Run("keeps free-text fallback local when no daemon tag hint is available", func(t *testing.T) {
		p := &prototype{}

		remoteTags, daemonSystem, overlayTerms := p.splitGallerySearchQuery("landscape")
		if len(remoteTags) != 0 {
			t.Fatalf("remoteTags = %v, want empty slice", remoteTags)
		}
		if len(daemonSystem) != 0 {
			t.Fatalf("daemonSystem = %v, want empty slice", daemonSystem)
		}
		if !slices.Equal(overlayTerms, []string{"landscape"}) {
			t.Fatalf("overlayTerms = %v, want [landscape]", overlayTerms)
		}
	})

	t.Run("routes system:favorite to daemon", func(t *testing.T) {
		p := &prototype{}

		remoteTags, daemonSystem, overlayTerms := p.splitGallerySearchQuery("system:favorite")
		if len(remoteTags) != 0 {
			t.Fatalf("remoteTags = %v, want empty", remoteTags)
		}
		if !slices.Equal(daemonSystem, []string{"favorite"}) {
			t.Fatalf("daemonSystem = %v, want [favorite]", daemonSystem)
		}
		if len(overlayTerms) != 0 {
			t.Fatalf("overlayTerms = %v, want empty", overlayTerms)
		}
	})

	t.Run("routes system:favourite=false to daemon", func(t *testing.T) {
		p := &prototype{}

		remoteTags, daemonSystem, overlayTerms := p.splitGallerySearchQuery("system:favourite=false")
		if len(remoteTags) != 0 {
			t.Fatalf("remoteTags = %v, want empty", remoteTags)
		}
		if !slices.Equal(daemonSystem, []string{"favourite=false"}) {
			t.Fatalf("daemonSystem = %v, want [favourite=false]", daemonSystem)
		}
		if len(overlayTerms) != 0 {
			t.Fatalf("overlayTerms = %v, want empty", overlayTerms)
		}
	})

	t.Run("routes system:resolution>=WxH to daemon", func(t *testing.T) {
		p := &prototype{}

		remoteTags, daemonSystem, overlayTerms := p.splitGallerySearchQuery("system:resolution>=1280x720")
		if len(remoteTags) != 0 {
			t.Fatalf("remoteTags = %v, want empty", remoteTags)
		}
		if !slices.Equal(daemonSystem, []string{"resolution>=1280x720"}) {
			t.Fatalf("daemonSystem = %v, want [resolution>=1280x720]", daemonSystem)
		}
		if len(overlayTerms) != 0 {
			t.Fatalf("overlayTerms = %v, want empty", overlayTerms)
		}
	})

	t.Run("mixes favorite and resolution with tag in daemon search", func(t *testing.T) {
		p := &prototype{}

		remoteTags, daemonSystem, overlayTerms := p.splitGallerySearchQuery("creator:alice system:favorite system:resolution>=1920x1080")
		if !slices.Equal(remoteTags, []string{"creator:alice"}) {
			t.Fatalf("remoteTags = %v, want [creator:alice]", remoteTags)
		}
		if !slices.Equal(daemonSystem, []string{"favorite", "resolution>=1920x1080"}) {
			t.Fatalf("daemonSystem = %v, want [favorite resolution>=1920x1080]", daemonSystem)
		}
		if len(overlayTerms) != 0 {
			t.Fatalf("overlayTerms = %v, want empty", overlayTerms)
		}
	})
}

func TestMapDaemonSort(t *testing.T) {
	if got := mapDaemonSort(gallerySortNewest); got != "" {
		t.Errorf("mapDaemonSort(gallerySortNewest) = %q, want \"\"", got)
	}
	if got := mapDaemonSort(gallerySortOldest); got != "import_oldest" {
		t.Errorf("mapDaemonSort(gallerySortOldest) = %q, want \"import_oldest\"", got)
	}
	if got := mapDaemonSort(gallerySortSizeDesc); got != "size_desc" {
		t.Errorf("mapDaemonSort(gallerySortSizeDesc) = %q, want \"size_desc\"", got)
	}
	if got := mapDaemonSort(gallerySortSizeAsc); got != "size_asc" {
		t.Errorf("mapDaemonSort(gallerySortSizeAsc) = %q, want \"size_asc\"", got)
	}
	if got := mapDaemonSort(gallerySortNameAZ); got != "" {
		t.Errorf("mapDaemonSort(gallerySortNameAZ) = %q, want \"\"", got)
	}
}

func TestPrototypeGalleryUsesDaemonSearch(t *testing.T) {
	p := &prototype{
		connected: true,
		client:    &daemonclient.Client{},
	}

	tests := []struct {
		name     string
		query    string
		sortMode string
		wantUse  bool
	}{
		{
			name:     "pure local free text with default sort stays local",
			query:    "some local terms",
			sortMode: gallerySortNewest,
			wantUse:  false,
		},
		{
			name:     "remote tags trigger daemon search",
			query:    "creator:alice",
			sortMode: gallerySortNewest,
			wantUse:  true,
		},
		{
			name:     "daemon-capable system predicate triggers daemon search",
			query:    "system:size>=2048",
			sortMode: gallerySortNewest,
			wantUse:  true,
		},
		{
			name:     "unsupported system predicate stays local",
			query:    "system:local",
			sortMode: gallerySortNewest,
			wantUse:  false,
		},
		{
			name:     "daemon-capable sort (non-default) triggers daemon search",
			query:    "",
			sortMode: gallerySortSizeDesc,
			wantUse:  true,
		},
		{
			name:     "local sort with no tags stays local",
			query:    "",
			sortMode: gallerySortNameAZ,
			wantUse:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p.galleryFilterQuery = tc.query
			p.gallerySortMode = tc.sortMode
			if got := p.galleryUsesDaemonSearch(); got != tc.wantUse {
				t.Errorf("galleryUsesDaemonSearch() = %v, want %v", got, tc.wantUse)
			}
		})
	}

	t.Run("completion text includes verified mapping count when available", func(t *testing.T) {
		mappingCount := int64(42)
		status := coreptrsync.Status{
			Enabled:                  true,
			Phase:                    coreptrsync.PhaseIdle,
			IsComplete:               true,
			ProcessedDefinitionCount: 2,
			ProcessedContentCount:    3,
			DownloadedUpdateCount:    5,
			LastSyncMappingCount:     &mappingCount,
		}

		got := ptrCompletionStatusText(status)
		want := "PTR sync has no local backlog in hydrusd. Applied definition bundles 2 • applied content bundles 3 • stored repository update bundles 5 • verified current mappings 42."
		if got != want {
			t.Fatalf("ptrCompletionStatusText() = %q, want %q", got, want)
		}
	})
}

func TestPrototypeFilteredRecentItems(t *testing.T) {
	importedNewest := int64(300)
	importedOldest := int64(100)
	p := &prototype{
		recent: []daemonclient.RecentItem{
			{FileID: 1, Hash: "hash-one-abcdef", ImportedAtMS: &importedOldest},
			{FileID: 2, Hash: "hash-two-abcdef", ImportedAtMS: &importedNewest},
		},
		galleryFilterQuery: "alice system:local",
		gallerySortMode:    gallerySortNewest,
	}

	filtered := p.filteredRecentItems()
	assertRecentItemOrder(t, filtered, []int64{2, 1})
}

func TestUpdateActionStateEnablesPTRSyncWhenDisabledButAvailable(t *testing.T) {
	p := &prototype{
		connected:            true,
		ptrStatusLoaded:      true,
		ptrStatus:            coreptrsync.Status{Phase: coreptrsync.PhaseDisabled, Enabled: false},
		addButton:            widget.NewButton("Add File", nil),
		addFolderButton:      widget.NewButton("Add Folder", nil),
		refreshButton:        widget.NewButton("Refresh", nil),
		ptrRefreshButton:     widget.NewButton("Refresh PTR Status", nil),
		clearQueueButton:     widget.NewButton("Clear Queue", nil),
		retryFailedButton:    widget.NewButton("Retry Failed", nil),
		clearFinishedButton:  widget.NewButton("Clear Finished", nil),
		retrySelectedButton:  widget.NewButton("Retry Selected", nil),
		removeSelectedButton: widget.NewButton("Remove Selected", nil),
		trashButton:          widget.NewButton("Trash", nil),
		editTagsButton:       widget.NewButton("Edit Tags", nil),
		ptrSyncButton:        widget.NewButton("Manual Sync", nil),
		connectButton:        widget.NewButton("Connect", nil),
	}

	p.updateActionState()

	if p.ptrSyncButton.Disabled() {
		t.Fatal("ptrSyncButton.Disabled() = true, want false when PTR is disabled but available")
	}
}

func TestShouldPrefetchTileMetadata(t *testing.T) {
	t.Run("skips selected file", func(t *testing.T) {
		p := &prototype{selectedFileID: 7}
		if p.shouldPrefetchTileMetadata(7) {
			t.Fatal("shouldPrefetchTileMetadata(selected) = true, want false")
		}
	})

	t.Run("skips prefetch while ptr sync is running", func(t *testing.T) {
		p := &prototype{
			ptrStatusLoaded: true,
			ptrStatus: coreptrsync.Status{
				Phase:     coreptrsync.PhaseSyncing,
				IsRunning: true,
			},
		}
		if p.shouldPrefetchTileMetadata(9) {
			t.Fatal("shouldPrefetchTileMetadata(ptr running) = true, want false")
		}
	})

	t.Run("allows non-selected prefetch when ptr is idle", func(t *testing.T) {
		p := &prototype{
			selectedFileID:  7,
			ptrStatusLoaded: true,
			ptrStatus: coreptrsync.Status{
				Phase:     coreptrsync.PhaseIdle,
				IsRunning: false,
			},
		}
		if !p.shouldPrefetchTileMetadata(9) {
			t.Fatal("shouldPrefetchTileMetadata(idle) = false, want true")
		}
	})

	t.Run("allows prefetch when ptr status has not loaded yet", func(t *testing.T) {
		p := &prototype{selectedFileID: 7}
		if !p.shouldPrefetchTileMetadata(9) {
			t.Fatal("shouldPrefetchTileMetadata(unloaded status) = false, want true")
		}
	})
}

func TestEnsureTileMetadataSkipsSelectedAndPTRRunning(t *testing.T) {
	item := daemonclient.RecentItem{FileID: 11}

	t.Run("selected file is never queued for tile prefetch", func(t *testing.T) {
		p := &prototype{
			client:            daemonclient.New(),
			connected:         true,
			selectedFileID:    item.FileID,
			tileMetadataCache: map[int64]daemonclient.FileMetadata{},
			tileMetadataLoads: map[int64]struct{}{},
		}

		p.ensureTileMetadata(item)

		if len(p.tileMetadataLoads) != 0 {
			t.Fatalf("len(tileMetadataLoads) = %d, want 0", len(p.tileMetadataLoads))
		}
	})

	t.Run("ptr sync blocks tile prefetch queueing", func(t *testing.T) {
		p := &prototype{
			client:            daemonclient.New(),
			connected:         true,
			ptrStatusLoaded:   true,
			ptrStatus:         coreptrsync.Status{Phase: coreptrsync.PhaseSyncing, IsRunning: true},
			tileMetadataCache: map[int64]daemonclient.FileMetadata{},
			tileMetadataLoads: map[int64]struct{}{},
		}

		p.ensureTileMetadata(item)

		if len(p.tileMetadataLoads) != 0 {
			t.Fatalf("len(tileMetadataLoads) = %d, want 0", len(p.tileMetadataLoads))
		}
	})
}

func TestIsDaemonCapableSort(t *testing.T) {
	if !isDaemonCapableSort(gallerySortNewest) {
		t.Error("expected gallerySortNewest to be daemon capable")
	}
	if isDaemonCapableSort(gallerySortNameAZ) {
		t.Error("expected gallerySortNameAZ NOT to be daemon capable")
	}
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

func collectCanvasObjectTexts(object fyne.CanvasObject) []string {
	switch value := object.(type) {
	case *fyne.Container:
		texts := []string{}
		for _, child := range value.Objects {
			texts = append(texts, collectCanvasObjectTexts(child)...)
		}
		return texts
	case *widget.Label:
		return []string{value.Text}
	case *widget.Button:
		return []string{value.Text}
	case *canvas.Text:
		return []string{value.Text}
	case *widget.RichText:
		return []string{flattenTextSegmentsForCollect(value.Segments)}
	default:
		return nil
	}
}

func flattenTextSegmentsForCollect(segments []widget.RichTextSegment) string {
	var builder strings.Builder
	for _, segment := range segments {
		textSegment, ok := segment.(*widget.TextSegment)
		if !ok {
			continue
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

func writeFFmpegPreviewFixture(t *testing.T, ext string) []byte {
	t.Helper()

	dir := t.TempDir()
	inputPath := filepath.Join(dir, "input.png")
	outputPath := filepath.Join(dir, "output"+ext)

	file, err := os.Create(inputPath)
	if err != nil {
		t.Fatalf("Create(input.png) error = %v", err)
	}
	imageData := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			imageData.Set(x, y, color.RGBA{R: 25, G: 50, B: 75, A: 255})
		}
	}
	if err := png.Encode(file, imageData); err != nil {
		file.Close()
		t.Fatalf("png.Encode(input) error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close(input.png) error = %v", err)
	}

	cmd := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-y", "-i", inputPath, outputPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg convert %q error = %v\n%s", ext, err, string(output))
	}

	payload, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile(output%s) error = %v", ext, err)
	}

	return payload
}

func writeFFmpegVideoPreviewFixture(t *testing.T, ext string) []byte {
	t.Helper()
	path := writeFFmpegVideoFixturePath(t, ext)
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(video output%s) error = %v", ext, err)
	}

	return payload
}

func writeFFmpegVideoFixturePath(t *testing.T, ext string) string {
	t.Helper()

	dir := t.TempDir()
	outputPath := filepath.Join(dir, "output"+ext)
	args := []string{
		"ffmpeg",
		"-nostdin",
		"-v", "error",
		"-y",
		"-f", "lavfi",
		"-i", "color=c=#336699:s=4x4:d=1",
	}
	switch ext {
	case ".webm":
		args = append(args, "-c:v", "libvpx-vp9", "-pix_fmt", "yuv420p")
	case ".avi":
		args = append(args, "-c:v", "mpeg4", "-pix_fmt", "yuv420p")
	default:
		args = append(args, "-c:v", "libx264", "-pix_fmt", "yuv420p")
	}
	args = append(args, outputPath)
	cmd := exec.Command(args[0], args[1:]...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg video fixture %q error = %v\n%s", ext, err, string(output))
	}

	return outputPath
}
