package importing

import (
	"context"
	"strings"
	"time"

	"github.com/official-elinas/hydrus-go/internal/core/mimes"
	"github.com/official-elinas/hydrus-go/internal/media/ffmpegutil"
)

type videoImportMetadata struct {
	Duration  *int64
	NumFrames *int64
	HasAudio  *bool
}

func detectVideoImportMetadata(path string, mimeEnum int) videoImportMetadata {
	mimeType := strings.ToLower(strings.TrimSpace(mimes.Lookup(mimeEnum).Mimetype))
	if !strings.HasPrefix(mimeType, "video/") {
		return videoImportMetadata{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	durationMS, numFrames, hasAudio, err := ffmpegutil.ProbeMediaMetadata(ctx, path)
	if err != nil {
		return videoImportMetadata{}
	}

	return videoImportMetadata{
		Duration:  durationMS,
		NumFrames: numFrames,
		HasAudio:  hasAudio,
	}
}
