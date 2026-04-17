package importing

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"

	"github.com/official-elinas/hydrus-go/internal/storage/clientfiles"

	xdraw "golang.org/x/image/draw"
)

const managedThumbnailMaxDimension = 256

func (i *Importer) ensureManagedThumbnail(
	ctx context.Context,
	sourcePath string,
	hashHex string,
	mimeEnum int,
) error {
	if i == nil {
		return fmt.Errorf("importer is nil")
	}

	if !supportsGeneratedThumbnail(mimeEnum) {
		return nil
	}

	thumbnailPath, cleanup, err := writeThumbnailTempFile(ctx, sourcePath)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := i.placeManagedThumbnail(thumbnailPath, hashHex); err != nil {
		return err
	}

	return nil
}

func (i *Importer) placeManagedThumbnail(sourcePath string, hashHex string) error {
	if _, err := i.layout.PlaceThumbnailFromPath(sourcePath, hashHex); err == nil {
		return nil
	} else {
		if !errors.Is(err, clientfiles.ErrManagedDestinationConflict) {
			return fmt.Errorf("place managed thumbnail: %w", err)
		}

		destinationPath, resolveErr := i.layout.ResolveThumbnailPath(hashHex)
		if resolveErr != nil {
			return fmt.Errorf("place managed thumbnail: %w", errors.Join(err, resolveErr))
		}

		info, statErr := os.Stat(destinationPath)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				return fmt.Errorf("place managed thumbnail: %w", err)
			}

			return fmt.Errorf("stat managed thumbnail for repair: %w", errors.Join(err, statErr))
		}

		if !info.Mode().IsRegular() {
			return fmt.Errorf("place managed thumbnail: %w", err)
		}

		if removeErr := os.Remove(destinationPath); removeErr != nil {
			return fmt.Errorf("remove stale managed thumbnail: %w", errors.Join(err, removeErr))
		}

		if _, retryErr := i.layout.PlaceThumbnailFromPath(sourcePath, hashHex); retryErr != nil {
			return fmt.Errorf("replace managed thumbnail: %w", errors.Join(err, retryErr))
		}
	}

	return nil
}

func writeThumbnailTempFile(
	ctx context.Context,
	sourcePath string,
) (string, func(), error) {
	if err := ctx.Err(); err != nil {
		return "", func() {}, fmt.Errorf("start thumbnail generation: %w", err)
	}

	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return "", func() {}, fmt.Errorf("stat thumbnail source: %w", err)
	}

	file, err := os.Open(sourcePath)
	if err != nil {
		return "", func() {}, fmt.Errorf("open thumbnail source: %w", err)
	}
	defer file.Close()

	sourceImage, _, err := image.Decode(file)
	if err != nil {
		return "", func() {}, fmt.Errorf("decode thumbnail source: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return "", func() {}, fmt.Errorf("thumbnail generation canceled: %w", err)
	}

	thumbnail := resizeThumbnailToFit(sourceImage, managedThumbnailMaxDimension)
	tempFile, err := os.CreateTemp("", "hydrus-go-thumbnail-*.png")
	if err != nil {
		return "", func() {}, fmt.Errorf("create thumbnail temp file: %w", err)
	}

	tempPath := tempFile.Name()
	cleanup := func() {
		_ = os.Remove(tempPath)
	}

	if err := png.Encode(tempFile, thumbnail); err != nil {
		_ = tempFile.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("encode thumbnail png: %w", err)
	}

	if err := tempFile.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("close thumbnail temp file: %w", err)
	}

	if err := os.Chtimes(tempPath, sourceInfo.ModTime(), sourceInfo.ModTime()); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("preserve thumbnail timestamp: %w", err)
	}

	return tempPath, cleanup, nil
}

func resizeThumbnailToFit(source image.Image, maxDimension int) image.Image {
	bounds := source.Bounds()
	sourceWidth := bounds.Dx()
	sourceHeight := bounds.Dy()
	if sourceWidth <= 0 || sourceHeight <= 0 {
		return image.NewNRGBA(image.Rect(0, 0, 1, 1))
	}

	if maxDimension <= 0 {
		maxDimension = managedThumbnailMaxDimension
	}

	targetWidth := sourceWidth
	targetHeight := sourceHeight
	if sourceWidth > maxDimension || sourceHeight > maxDimension {
		if sourceWidth >= sourceHeight {
			targetWidth = maxDimension
			targetHeight = max(1, sourceHeight*maxDimension/sourceWidth)
		} else {
			targetHeight = maxDimension
			targetWidth = max(1, sourceWidth*maxDimension/sourceHeight)
		}
	}

	target := image.NewNRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	xdraw.CatmullRom.Scale(target, target.Bounds(), source, bounds, xdraw.Over, nil)
	return target
}

func supportsGeneratedThumbnail(mimeEnum int) bool {
	switch mimeEnum {
	case 1, 2, 3:
		return true
	default:
		return false
	}
}
