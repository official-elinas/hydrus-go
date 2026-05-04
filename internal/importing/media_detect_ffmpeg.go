package importing

import (
	"context"
	"strings"
	"time"

	"github.com/official-elinas/hydrus-go/internal/media/ffmpegutil"
)

func detectImportMIMEWithFFmpeg(path string) (int, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	formatName, majorBrand, compatibleBrands, codecName, codecType, err := ffmpegutil.ProbePrimaryStream(ctx, path)
	if err != nil {
		return 0, false
	}

	formatName = strings.ToLower(strings.TrimSpace(formatName))
	majorBrand = strings.ToLower(strings.TrimSpace(majorBrand))
	compatibleBrands = strings.ToLower(strings.TrimSpace(compatibleBrands))
	codecName = strings.ToLower(strings.TrimSpace(codecName))
	codecType = strings.ToLower(strings.TrimSpace(codecType))

	switch {
	case codecType == "video" && strings.Contains(codecName, "av1") && (strings.Contains(formatName, "avif") || strings.Contains(majorBrand, "avif") || strings.Contains(compatibleBrands, "avif")):
		return 65, true
	case codecType == "video" && strings.Contains(codecName, "hevc") && (strings.Contains(formatName, "heic") || strings.Contains(majorBrand, "heic") || strings.Contains(compatibleBrands, "heic")):
		return 63, true
	case codecType == "video" && strings.Contains(codecName, "hevc") && (strings.Contains(formatName, "heif") || strings.Contains(majorBrand, "heif") || strings.Contains(compatibleBrands, "heif") || strings.Contains(majorBrand, "mif1") || strings.Contains(compatibleBrands, "mif1")):
		return 61, true
	case strings.Contains(formatName, "matroska") || strings.Contains(formatName, "webm"):
		if strings.Contains(formatName, "webm") {
			return 21, true
		}
		return 20, true
	case strings.Contains(formatName, "flv"):
		return 9, true
	case strings.Contains(formatName, "asf") || strings.Contains(formatName, "wmv"):
		return 18, true
	case strings.Contains(formatName, "3gp") || strings.Contains(formatName, "3g2"):
		return 14, true
	case strings.Contains(formatName, "mpegts") || strings.Contains(formatName, "mpeg ts"):
		return 25, true
	case strings.Contains(formatName, "mov") || strings.Contains(formatName, "mp4") || strings.Contains(formatName, "m4a"):
		if codecType == "audio" {
			return 36, true
		}
		return 14, true
	case strings.Contains(formatName, "avi"):
		return 27, true
	case strings.Contains(formatName, "mpeg"):
		return 25, true
	case strings.Contains(formatName, "ogg") && codecType == "video":
		return 47, true
	case strings.Contains(formatName, "gif"):
		return 3, true
	case codecType == "video" && codecName == "mjpeg":
		return 1, true
	case codecType == "video" && codecName == "png":
		return 2, true
	case codecType == "video" && codecName == "bmp":
		return 4, true
	case codecType == "video" && codecName == "tiff":
		return 34, true
	case codecType == "video" && codecName == "webp":
		return 33, true
	case codecType == "video" && codecName == "qoi":
		return 70, true
	case codecType == "video" && codecName == "jpegxl":
		return 85, true
	case codecType == "video":
		return 42, true
	case codecType == "audio":
		return 40, true
	default:
		return 0, false
	}
}
