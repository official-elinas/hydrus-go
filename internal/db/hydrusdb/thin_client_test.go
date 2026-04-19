package hydrusdb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/official-elinas/hydrus-go/internal/core/fileassets"
	"github.com/official-elinas/hydrus-go/internal/core/filemetadata"
	"github.com/official-elinas/hydrus-go/internal/core/filetrash"
	"github.com/official-elinas/hydrus-go/internal/core/librarybrowse"
	"github.com/official-elinas/hydrus-go/internal/storage/clientfiles"
)

func TestBundleListRecent(t *testing.T) {
	dir, fixture := createTestBundle(t)

	mainDB := openSQLiteForTest(t, filepath.Join(dir, "client.db"))
	defer mainDB.Close()

	mustExec(
		t,
		mainDB,
		`INSERT INTO current_files_4 (hash_id, timestamp_ms) VALUES (?, ?)`,
		2,
		700127,
	)

	writeManagedThumbnailForTest(t, dir, fixture.hash1Hex, []byte("thumb-1"))
	writeManagedThumbnailForTest(t, dir, fixture.hash2Hex, []byte("thumb-2"))

	bundle, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() {
		if err := bundle.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}()

	page, err := bundle.ListRecent(context.Background(), librarybrowse.Request{Offset: 0, Limit: 1})
	if err != nil {
		t.Fatalf("ListRecent() error = %v", err)
	}

	if !page.HasMore {
		t.Fatal("page.HasMore = false, want true")
	}

	if len(page.Items) != 1 {
		t.Fatalf("len(page.Items) = %d, want 1", len(page.Items))
	}

	item := page.Items[0]
	if item.FileID != 2 {
		t.Fatalf("item.FileID = %d, want 2", item.FileID)
	}

	if item.Hash != fixture.hash2Hex {
		t.Fatalf("item.Hash = %q, want %q", item.Hash, fixture.hash2Hex)
	}

	if item.MIME != "image/png" {
		t.Fatalf("item.MIME = %q, want image/png", item.MIME)
	}

	if item.ImportedAtMS == nil || *item.ImportedAtMS != 700127 {
		t.Fatalf("item.ImportedAtMS = %v, want 700127", item.ImportedAtMS)
	}

	if !item.HasThumbnail {
		t.Fatal("item.HasThumbnail = false, want true")
	}

	page, err = bundle.ListRecent(context.Background(), librarybrowse.Request{Offset: 1, Limit: 5})
	if err != nil {
		t.Fatalf("ListRecent(offset) error = %v", err)
	}

	if len(page.Items) != 1 || page.Items[0].FileID != 1 {
		t.Fatalf("offset page items = %+v, want only file_id 1", page.Items)
	}

	if page.HasMore {
		t.Fatal("offset page.HasMore = true, want false")
	}
}

func TestBundleResolveFileContent(t *testing.T) {
	dir, fixture := createTestBundle(t)

	writeManagedFileForTest(t, dir, fixture.hash1Hex, ".jpg", []byte("jpeg bytes"))
	writeManagedFileForTest(t, dir, fixture.hash2Hex, ".png", []byte("png bytes"))

	bundle, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() {
		if err := bundle.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}()

	descriptor, err := bundle.ResolveFileContent(context.Background(), 1)
	if err != nil {
		t.Fatalf("ResolveFileContent(1) error = %v", err)
	}

	if descriptor.Filename != fixture.hash1Hex+".jpg" {
		t.Fatalf("descriptor.Filename = %q, want %q", descriptor.Filename, fixture.hash1Hex+".jpg")
	}

	if descriptor.MIME != "image/jpeg" {
		t.Fatalf("descriptor.MIME = %q, want image/jpeg", descriptor.MIME)
	}

	if _, err := os.Stat(descriptor.Path); err != nil {
		t.Fatalf("Stat(descriptor.Path) error = %v", err)
	}

	descriptor, err = bundle.ResolveFileContent(context.Background(), 2)
	if err != nil {
		t.Fatalf("ResolveFileContent(2) error = %v", err)
	}

	if descriptor.Filename != fixture.hash2Hex+".png" {
		t.Fatalf("descriptor.Filename = %q, want %q", descriptor.Filename, fixture.hash2Hex+".png")
	}
}

func TestBundleResolveThumbnail(t *testing.T) {
	dir, fixture := createTestBundle(t)

	writeManagedThumbnailForTest(t, dir, fixture.hash1Hex, []byte("thumb-1"))

	bundle, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() {
		if err := bundle.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}()

	descriptor, err := bundle.ResolveThumbnail(context.Background(), 1)
	if err != nil {
		t.Fatalf("ResolveThumbnail(1) error = %v", err)
	}

	if descriptor.Filename != fixture.hash1Hex+".thumbnail" {
		t.Fatalf(
			"descriptor.Filename = %q, want %q",
			descriptor.Filename,
			fixture.hash1Hex+".thumbnail",
		)
	}

	_, err = bundle.ResolveThumbnail(context.Background(), 2)
	if err == nil {
		t.Fatal("ResolveThumbnail(2) error = nil, want not found")
	}

	var notFoundError *fileassets.NotFoundError
	if !errors.As(err, &notFoundError) {
		t.Fatalf("ResolveThumbnail(2) error = %T, want *fileassets.NotFoundError", err)
	}
}

func TestBundleManagedLayout_UsesConfiguredSplitRootsAndPortablePaths(t *testing.T) {
	dir, fixture := createTestBundle(t)
	mainDB := openSQLiteForTest(t, filepath.Join(dir, "client.db"))
	defer mainDB.Close()

	portableFileRoot := "custom_files"
	thumbnailRoot := filepath.Join(filepath.Dir(dir), "custom-thumbnails")
	if err := os.MkdirAll(filepath.Join(dir, portableFileRoot), 0o755); err != nil {
		t.Fatalf("MkdirAll(portableFileRoot) error = %v", err)
	}
	if err := os.MkdirAll(thumbnailRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(thumbnailRoot) error = %v", err)
	}

	mustExec(
		t,
		mainDB,
		`UPDATE current_client_files_locations SET location = CASE location_id WHEN 1 THEN ? WHEN 2 THEN ? ELSE location END`,
		portableFileRoot,
		thumbnailRoot,
	)

	bundle, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() {
		if err := bundle.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}()

	layout, err := bundle.ManagedLayout(context.Background())
	if err != nil {
		t.Fatalf("ManagedLayout() error = %v", err)
	}

	filePath, err := layout.ResolveFilePath(fixture.hash1Hex, ".jpg")
	if err != nil {
		t.Fatalf("ResolveFilePath() error = %v", err)
	}

	wantFilePath := filepath.Join(dir, portableFileRoot, "f01", fixture.hash1Hex+".jpg")
	if filePath != wantFilePath {
		t.Fatalf("filePath = %q, want %q", filePath, wantFilePath)
	}

	thumbnailPath, err := layout.ResolveThumbnailPath(fixture.hash1Hex)
	if err != nil {
		t.Fatalf("ResolveThumbnailPath() error = %v", err)
	}

	wantThumbnailPath := filepath.Join(thumbnailRoot, "t01", fixture.hash1Hex+".thumbnail")
	if thumbnailPath != wantThumbnailPath {
		t.Fatalf("thumbnailPath = %q, want %q", thumbnailPath, wantThumbnailPath)
	}

	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatalf("MkdirAll(filePath dir) error = %v", err)
	}
	if err := os.WriteFile(filePath, []byte("jpeg bytes"), 0o644); err != nil {
		t.Fatalf("WriteFile(filePath) error = %v", err)
	}

	if err := os.MkdirAll(filepath.Dir(thumbnailPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(thumbnailPath dir) error = %v", err)
	}
	if err := os.WriteFile(thumbnailPath, []byte("thumb bytes"), 0o644); err != nil {
		t.Fatalf("WriteFile(thumbnailPath) error = %v", err)
	}

	fileDescriptor, err := bundle.ResolveFileContent(context.Background(), 1)
	if err != nil {
		t.Fatalf("ResolveFileContent() error = %v", err)
	}

	if fileDescriptor.Path != wantFilePath {
		t.Fatalf("fileDescriptor.Path = %q, want %q", fileDescriptor.Path, wantFilePath)
	}

	thumbnailDescriptor, err := bundle.ResolveThumbnail(context.Background(), 1)
	if err != nil {
		t.Fatalf("ResolveThumbnail() error = %v", err)
	}

	if thumbnailDescriptor.Path != wantThumbnailPath {
		t.Fatalf(
			"thumbnailDescriptor.Path = %q, want %q",
			thumbnailDescriptor.Path,
			wantThumbnailPath,
		)
	}
}

func TestSeparateReadAndWriteBundles_IsolateUncommittedImports(t *testing.T) {
	dir, _ := createTestBundle(t)

	readBundle, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() {
		if err := readBundle.Close(); err != nil {
			t.Fatalf("Close(readBundle) error = %v", err)
		}
	}()

	writeBundle, err := OpenWritable(context.Background(), dir)
	if err != nil {
		t.Fatalf("OpenWritable() error = %v", err)
	}
	defer func() {
		if err := writeBundle.Close(); err != nil {
			t.Fatalf("Close(writeBundle) error = %v", err)
		}
	}()

	hashHex := strings.Repeat("09", 32)
	hashBytes := mustDecodeHex(t, hashHex)
	started := make(chan struct{})
	release := make(chan struct{})
	writeDone := make(chan error, 1)

	go func() {
		writeDone <- writeBundle.WithImmediateTx(context.Background(), func(tx *ImmediateTx) error {
			if _, err := tx.ExecContext(
				context.Background(),
				`INSERT INTO external_master.hashes (hash) VALUES (?)`,
				hashBytes,
			); err != nil {
				return err
			}

			hashID, exists, err := lookupHashIDByHash(context.Background(), tx, hashBytes)
			if err != nil {
				return err
			}

			if !exists {
				return errors.New("expected inserted hash row to exist inside tx")
			}

			if _, err := tx.ExecContext(
				context.Background(),
				`INSERT INTO main.files_info (hash_id, size, mime, width, height, duration, num_frames, has_audio, num_words)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				hashID,
				123,
				2,
				nil,
				nil,
				nil,
				nil,
				nil,
				nil,
			); err != nil {
				return err
			}

			close(started)
			<-release
			return nil
		})
	}()

	<-started

	if fileID, exists, err := readBundle.LookupImportedFileIDByHash(context.Background(), hashHex); err != nil {
		t.Fatalf("LookupImportedFileIDByHash(uncommitted) error = %v", err)
	} else if exists {
		t.Fatalf("LookupImportedFileIDByHash(uncommitted) = (%d, true), want not found", fileID)
	}

	close(release)
	if err := <-writeDone; err != nil {
		t.Fatalf("write bundle WithImmediateTx() error = %v", err)
	}

	fileID, exists, err := readBundle.LookupImportedFileIDByHash(context.Background(), hashHex)
	if err != nil {
		t.Fatalf("LookupImportedFileIDByHash(committed) error = %v", err)
	}

	if !exists || fileID <= 0 {
		t.Fatalf("LookupImportedFileIDByHash(committed) = (%d, %t), want existing file", fileID, exists)
	}
}

func TestBundleTrashFile_RemovesFileFromRecentBrowse(t *testing.T) {
	dir, _ := createTestBundle(t)

	readBundle, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() {
		if err := readBundle.Close(); err != nil {
			t.Fatalf("Close(readBundle) error = %v", err)
		}
	}()

	writeBundle, err := OpenWritable(context.Background(), dir)
	if err != nil {
		t.Fatalf("OpenWritable() error = %v", err)
	}
	defer func() {
		if err := writeBundle.Close(); err != nil {
			t.Fatalf("Close(writeBundle) error = %v", err)
		}
	}()

	result, err := writeBundle.TrashFile(context.Background(), filetrash.Request{FileID: 1})
	if err != nil {
		t.Fatalf("TrashFile() error = %v", err)
	}

	if !result.Trashed {
		t.Fatal("result.Trashed = false, want true")
	}

	page, err := readBundle.ListRecent(context.Background(), librarybrowse.Request{Offset: 0, Limit: 10})
	if err != nil {
		t.Fatalf("ListRecent() after trash error = %v", err)
	}

	if len(page.Items) != 0 {
		t.Fatalf("len(page.Items) = %d, want 0 after trash", len(page.Items))
	}

	rows, err := readBundle.GetMetadata(
		context.Background(),
		filemetadata.Request{FileIDs: []int64{1}, IncludeServicesObject: false},
	)
	if err != nil {
		t.Fatalf("GetMetadata() after trash error = %v", err)
	}

	row := rows[0]
	if got := row["is_trashed"]; got != true {
		t.Fatalf("row[is_trashed] = %v, want true", got)
	}

	if got := row["is_deleted"]; got != true {
		t.Fatalf("row[is_deleted] = %v, want true", got)
	}

	if got := row["is_local"]; got != false {
		t.Fatalf("row[is_local] = %v, want false", got)
	}

	second, err := writeBundle.TrashFile(context.Background(), filetrash.Request{FileID: 1})
	if err != nil {
		t.Fatalf("second TrashFile() error = %v", err)
	}

	if second.RemovedFromRecent {
		t.Fatal("second.RemovedFromRecent = true, want false for already trashed file")
	}
}

func writeManagedFileForTest(
	t *testing.T,
	dbDir string,
	hashHex string,
	ext string,
	contents []byte,
) {
	t.Helper()

	layout, err := clientfiles.NewLayout(
		clientfiles.DefaultFileRoot(dbDir),
		clientfiles.DefaultPrefixLength,
	)
	if err != nil {
		t.Fatalf("NewLayout() error = %v", err)
	}

	path, err := layout.ResolveFilePath(hashHex, ext)
	if err != nil {
		t.Fatalf("ResolveFilePath() error = %v", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func writeManagedThumbnailForTest(
	t *testing.T,
	dbDir string,
	hashHex string,
	contents []byte,
) {
	t.Helper()

	layout, err := clientfiles.NewSplitLayout(
		clientfiles.DefaultFileRoot(dbDir),
		clientfiles.DefaultThumbnailRoot(dbDir),
		clientfiles.DefaultPrefixLength,
	)
	if err != nil {
		t.Fatalf("NewLayout() error = %v", err)
	}

	path, err := layout.ResolveThumbnailPath(hashHex)
	if err != nil {
		t.Fatalf("ResolveThumbnailPath() error = %v", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
