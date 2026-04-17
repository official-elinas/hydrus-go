package clientfiles

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// ErrManagedDestinationConflict reports that a managed artifact already exists
// at the deterministic destination path with different metadata or contents.
var ErrManagedDestinationConflict = errors.New("managed destination already exists with different metadata")

// PlacementResult describes the result of placing a managed artifact.
type PlacementResult struct {
	Path           string
	AlreadyPresent bool
}

// PlaceFileFromPath copies a source file into its deterministic managed file
// location.
func (l Layout) PlaceFileFromPath(
	sourcePath string,
	hashHex string,
	ext string,
) (PlacementResult, error) {
	destinationPath, err := l.ResolveFilePath(hashHex, ext)
	if err != nil {
		return PlacementResult{}, err
	}

	return placePath(sourcePath, destinationPath)
}

// PlaceThumbnailFromPath copies a source file into its deterministic managed
// thumbnail location.
func (l Layout) PlaceThumbnailFromPath(
	sourcePath string,
	hashHex string,
) (PlacementResult, error) {
	destinationPath, err := l.ResolveThumbnailPath(hashHex)
	if err != nil {
		return PlacementResult{}, err
	}

	return placePath(sourcePath, destinationPath)
}

func placePath(sourcePath string, destinationPath string) (PlacementResult, error) {
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return PlacementResult{}, fmt.Errorf("stat source path: %w", err)
	}

	if !sourceInfo.Mode().IsRegular() {
		return PlacementResult{}, fmt.Errorf("source path must be a regular file")
	}

	if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
		return PlacementResult{}, fmt.Errorf("create managed parent directories: %w", err)
	}

	if result, handled, err := existingPlacementResult(
		sourcePath,
		sourceInfo,
		destinationPath,
	); handled {
		return result, err
	}

	tempFile, err := os.CreateTemp(
		filepath.Dir(destinationPath),
		".hydrus-go-place-*",
	)
	if err != nil {
		return PlacementResult{}, fmt.Errorf("create managed temp file: %w", err)
	}

	tempPath := tempFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tempPath)
		}
	}()

	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		_ = tempFile.Close()
		return PlacementResult{}, fmt.Errorf("open source path: %w", err)
	}
	defer sourceFile.Close()

	if _, err := io.Copy(tempFile, sourceFile); err != nil {
		_ = tempFile.Close()
		return PlacementResult{}, fmt.Errorf("copy managed file bytes: %w", err)
	}

	if err := tempFile.Sync(); err != nil {
		_ = tempFile.Close()
		return PlacementResult{}, fmt.Errorf("sync managed temp file: %w", err)
	}

	if err := tempFile.Close(); err != nil {
		return PlacementResult{}, fmt.Errorf("close managed temp file: %w", err)
	}

	if err := os.Chtimes(tempPath, sourceInfo.ModTime(), sourceInfo.ModTime()); err != nil {
		return PlacementResult{}, fmt.Errorf("preserve managed file timestamp: %w", err)
	}

	conflictAppeared, err := linkTempFileIntoPlace(tempPath, destinationPath)
	if err != nil {
		return PlacementResult{}, err
	}

	if conflictAppeared {
		result, handled, checkErr := existingPlacementResult(
			sourcePath,
			sourceInfo,
			destinationPath,
		)
		if handled {
			return result, checkErr
		}

		return PlacementResult{}, fmt.Errorf(
			"managed destination unexpectedly appeared during placement",
		)
	}

	if err := os.Remove(tempPath); err != nil {
		return PlacementResult{}, fmt.Errorf("remove managed temp file: %w", err)
	}

	cleanup = false
	return PlacementResult{Path: destinationPath}, nil
}

func existingPlacementResult(
	sourcePath string,
	sourceInfo os.FileInfo,
	destinationPath string,
) (PlacementResult, bool, error) {
	destinationInfo, err := os.Stat(destinationPath)
	if os.IsNotExist(err) {
		return PlacementResult{}, false, nil
	}

	if err != nil {
		return PlacementResult{}, true, fmt.Errorf("stat managed destination: %w", err)
	}

	if !destinationInfo.Mode().IsRegular() {
		return PlacementResult{}, true, fmt.Errorf(
			"managed destination must be a regular file",
		)
	}

	identical, err := sameFilePlacement(
		sourcePath,
		sourceInfo,
		destinationPath,
		destinationInfo,
	)
	if err != nil {
		return PlacementResult{}, true, err
	}

	if identical {
		return PlacementResult{
			Path:           destinationPath,
			AlreadyPresent: true,
		}, true, nil
	}

	return PlacementResult{}, true, ErrManagedDestinationConflict
}

func sameFilePlacement(
	sourcePath string,
	sourceInfo os.FileInfo,
	destinationPath string,
	destinationInfo os.FileInfo,
) (bool, error) {
	if sameFile(sourcePath, sourceInfo, destinationPath, destinationInfo) {
		return true, nil
	}

	if sourceInfo.Size() != destinationInfo.Size() {
		return false, nil
	}

	sourceTime := normalizeFileTime(sourceInfo.ModTime())
	destinationTime := normalizeFileTime(destinationInfo.ModTime())
	if sourceTime != destinationTime && coarseFileTime(sourceTime) != coarseFileTime(destinationTime) {
		return false, nil
	}

	sameContents, err := sameFileContents(sourcePath, destinationPath)
	if err != nil {
		return false, fmt.Errorf("compare existing managed contents: %w", err)
	}

	return sameContents, nil
}

func sameFile(
	sourcePath string,
	sourceInfo os.FileInfo,
	destinationPath string,
	destinationInfo os.FileInfo,
) bool {
	if sourcePath == destinationPath {
		return true
	}

	return os.SameFile(sourceInfo, destinationInfo)
}

func normalizeFileTime(value time.Time) time.Time {
	return value.UTC()
}

func coarseFileTime(value time.Time) time.Time {
	return normalizeFileTime(value).Truncate(time.Second)
}

func sameFileContents(sourcePath string, destinationPath string) (bool, error) {
	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return false, fmt.Errorf("open source path for comparison: %w", err)
	}
	defer sourceFile.Close()

	destinationFile, err := os.Open(destinationPath)
	if err != nil {
		return false, fmt.Errorf("open destination path for comparison: %w", err)
	}
	defer destinationFile.Close()

	sourceBuffer := make([]byte, 32*1024)
	destinationBuffer := make([]byte, 32*1024)

	for {
		sourceN, sourceErr := sourceFile.Read(sourceBuffer)
		destinationN, destinationErr := destinationFile.Read(destinationBuffer)

		if sourceN != destinationN {
			return false, nil
		}

		if !bytes.Equal(sourceBuffer[:sourceN], destinationBuffer[:destinationN]) {
			return false, nil
		}

		if sourceErr == io.EOF && destinationErr == io.EOF {
			return true, nil
		}

		if sourceErr != nil && sourceErr != io.EOF {
			return false, fmt.Errorf("read source contents: %w", sourceErr)
		}

		if destinationErr != nil && destinationErr != io.EOF {
			return false, fmt.Errorf("read destination contents: %w", destinationErr)
		}
	}
}

func linkTempFileIntoPlace(tempPath string, destinationPath string) (bool, error) {
	if err := os.Link(tempPath, destinationPath); err != nil {
		if os.IsExist(err) {
			return true, nil
		}

		return false, fmt.Errorf("link managed temp file into place: %w", err)
	}

	return false, nil
}
