//go:build fyne

package fyneapp

import (
	"testing"

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
