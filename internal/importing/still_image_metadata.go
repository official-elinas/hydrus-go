package importing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"strings"
	"time"

	"github.com/official-elinas/hydrus-go/internal/core/mimes"
	"github.com/official-elinas/hydrus-go/internal/media/ffmpegutil"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

type stillImageImportMetadata struct {
	Width           *int64
	Height          *int64
	PixelHashHex    string
	HasTransparency *bool
}

func detectStillImageImportMetadata(path string, mimeEnum int) stillImageImportMetadata {
	width, height := detectImageDimensions(path, mimeEnum)
	metadata := stillImageImportMetadata{
		Width:  width,
		Height: height,
	}

	if !supportsStillImageImportEnrichment(mimeEnum) {
		return metadata
	}

	if !supportsDecodeConfigDimensions(mimeEnum) {
		return metadata
	}

	file, err := os.Open(path)
	if err != nil {
		return metadata
	}
	defer file.Close()

	decodedImage, _, err := image.Decode(file)
	if err != nil {
		return metadata
	}

	if metadata.Width == nil || metadata.Height == nil {
		bounds := decodedImage.Bounds()
		width := int64(bounds.Dx())
		height := int64(bounds.Dy())
		metadata.Width = &width
		metadata.Height = &height
	}

	hasTransparency := imageHasUsefulTransparency(decodedImage)
	metadata.HasTransparency = &hasTransparency
	metadata.PixelHashHex = pixelHashHexForDecodedImage(decodedImage, hasTransparency)

	return metadata
}

func detectImageDimensions(path string, mimeEnum int) (*int64, *int64) {
	if supportsDecodeConfigDimensions(mimeEnum) {
		file, err := os.Open(path)
		if err == nil {
			defer file.Close()
			config, _, decodeErr := image.DecodeConfig(file)
			if decodeErr == nil {
				width := int64(config.Width)
				height := int64(config.Height)
				return &width, &height
			}
		}
	}

	mimeType := strings.ToLower(strings.TrimSpace(mimes.Lookup(mimeEnum).Mimetype))
	if !strings.HasPrefix(mimeType, "image/") && !strings.HasPrefix(mimeType, "video/") {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	width, height, err := ffmpegutil.ProbeDimensions(ctx, path)
	if err != nil {
		return nil, nil
	}

	return width, height
}

func imageHasUsefulTransparency(decodedImage image.Image) bool {
	bounds := decodedImage.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			_, _, _, alpha := decodedImage.At(x, y).RGBA()
			if alpha != 0xffff {
				return true
			}
		}
	}

	return false
}

func supportsStillImageImportEnrichment(mimeEnum int) bool {
	switch mimeEnum {
	case 1, 2, 4, 23, 33, 34:
		return true
	default:
		return false
	}
}

func pixelHashHexForDecodedImage(decodedImage image.Image, includeAlpha bool) string {
	hasher := sha256.New()
	var pixel [4]byte
	bounds := decodedImage.Bounds()

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			nrgba := color.NRGBAModel.Convert(decodedImage.At(x, y)).(color.NRGBA)
			pixel[0] = nrgba.R
			pixel[1] = nrgba.G
			pixel[2] = nrgba.B
			if includeAlpha {
				pixel[3] = nrgba.A
				_, _ = hasher.Write(pixel[:])
				continue
			}

			_, _ = hasher.Write(pixel[:3])
		}
	}

	return hex.EncodeToString(hasher.Sum(nil))
}
