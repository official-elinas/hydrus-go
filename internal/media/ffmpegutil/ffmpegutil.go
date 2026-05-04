package ffmpegutil

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

type probeInfo struct {
	Format struct {
		FormatName string `json:"format_name"`
		Tags       struct {
			MajorBrand       string `json:"major_brand"`
			CompatibleBrands string `json:"compatible_brands"`
		} `json:"tags"`
	} `json:"format"`
	Streams []struct {
		CodecName string `json:"codec_name"`
		CodecType string `json:"codec_type"`
	} `json:"streams"`
}

// ProbeDimensions asks ffprobe for the first video/image stream dimensions.
func ProbeDimensions(ctx context.Context, path string) (*int64, *int64, error) {
	cmd := exec.CommandContext(
		ctx,
		"ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height",
		"-of", "csv=s=x:p=0",
		path,
	)
	output, err := cmd.Output()
	if err != nil {
		return nil, nil, fmt.Errorf("run ffprobe dimensions: %w", err)
	}

	parts := strings.Split(strings.TrimSpace(string(output)), "x")
	if len(parts) != 2 {
		return nil, nil, fmt.Errorf("unexpected ffprobe dimensions output %q", strings.TrimSpace(string(output)))
	}

	width, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	if err != nil {
		return nil, nil, fmt.Errorf("parse ffprobe width: %w", err)
	}
	height, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil {
		return nil, nil, fmt.Errorf("parse ffprobe height: %w", err)
	}
	if width <= 0 || height <= 0 {
		return nil, nil, fmt.Errorf("ffprobe returned invalid dimensions %dx%d", width, height)
	}

	return &width, &height, nil
}

// ProbePrimaryStream returns the ffprobe format name, major brand, and first stream codec/type.
func ProbePrimaryStream(ctx context.Context, path string) (formatName string, majorBrand string, compatibleBrands string, codecName string, codecType string, err error) {
	cmd := exec.CommandContext(
		ctx,
		"ffprobe",
		"-v", "error",
		"-show_entries", "format=format_name:format_tags=major_brand,compatible_brands:stream=codec_name,codec_type",
		"-of", "json",
		path,
	)
	output, err := cmd.Output()
	if err != nil {
		return "", "", "", "", "", fmt.Errorf("run ffprobe stream probe: %w", err)
	}

	var info probeInfo
	if err := json.Unmarshal(output, &info); err != nil {
		return "", "", "", "", "", fmt.Errorf("decode ffprobe stream probe: %w", err)
	}

	formatName = strings.TrimSpace(info.Format.FormatName)
	majorBrand = strings.TrimSpace(info.Format.Tags.MajorBrand)
	compatibleBrands = strings.TrimSpace(info.Format.Tags.CompatibleBrands)
	for _, stream := range info.Streams {
		if strings.TrimSpace(stream.CodecType) == "" {
			continue
		}
		return formatName, majorBrand, compatibleBrands, strings.TrimSpace(stream.CodecName), strings.TrimSpace(stream.CodecType), nil
	}

	return formatName, majorBrand, compatibleBrands, "", "", nil
}

// TranscodePathToPNG renders the first frame of a still image or video into a
// bounded PNG that callers can reuse for thumbnails or previews.
func TranscodePathToPNG(ctx context.Context, path string, maxDimension int, sourceIsVideo bool) (string, func(), error) {
	if maxDimension <= 0 {
		maxDimension = 256
	}

	outputFile, err := os.CreateTemp("", "hydrus-go-ffmpeg-*.png")
	if err != nil {
		return "", func() {}, fmt.Errorf("create ffmpeg output file: %w", err)
	}
	outputPath := outputFile.Name()
	if err := outputFile.Close(); err != nil {
		_ = os.Remove(outputPath)
		return "", func() {}, fmt.Errorf("close ffmpeg output file: %w", err)
	}
	cleanup := func() {
		_ = os.Remove(outputPath)
	}

	filter := fmt.Sprintf(
		"scale=w='min(iw,%d)':h='min(ih,%d)':force_original_aspect_ratio=decrease",
		maxDimension,
		maxDimension,
	)
	if sourceIsVideo {
		filter = "thumbnail," + filter
	}

	cmd := exec.CommandContext(
		ctx,
		"ffmpeg",
		"-nostdin",
		"-v", "error",
		"-y",
		"-i", path,
		"-frames:v", "1",
		"-vf", filter,
		outputPath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		cleanup()
		trimmed := strings.TrimSpace(string(output))
		if trimmed != "" {
			return "", func() {}, fmt.Errorf("run ffmpeg thumbnail: %s", trimmed)
		}
		return "", func() {}, fmt.Errorf("run ffmpeg thumbnail: %w", err)
	}

	return outputPath, cleanup, nil
}

// TranscodeBytesToPNG writes the payload to a temp file, runs ffmpeg on it,
// and returns the resulting PNG bytes.
func TranscodeBytesToPNG(ctx context.Context, payload []byte, inputExt string, maxDimension int, sourceIsVideo bool) ([]byte, error) {
	inputFile, err := os.CreateTemp("", "hydrus-go-ffmpeg-input-*"+normalizedSuffix(inputExt))
	if err != nil {
		return nil, fmt.Errorf("create ffmpeg input file: %w", err)
	}
	inputPath := inputFile.Name()
	defer os.Remove(inputPath)

	if _, err := inputFile.Write(payload); err != nil {
		_ = inputFile.Close()
		return nil, fmt.Errorf("write ffmpeg input file: %w", err)
	}
	if err := inputFile.Close(); err != nil {
		return nil, fmt.Errorf("close ffmpeg input file: %w", err)
	}

	outputPath, cleanup, err := TranscodePathToPNG(ctx, inputPath, maxDimension, sourceIsVideo)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	output, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("read ffmpeg output file: %w", err)
	}

	return output, nil
}

func normalizedSuffix(ext string) string {
	trimmed := strings.TrimSpace(ext)
	if trimmed == "" {
		return ".bin"
	}
	if strings.HasPrefix(trimmed, ".") {
		return trimmed
	}
	return "." + trimmed
}
