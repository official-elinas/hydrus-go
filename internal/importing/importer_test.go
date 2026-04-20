package importing

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/official-elinas/hydrus-go/internal/core/filemetadata"
	"github.com/official-elinas/hydrus-go/internal/core/services"
	"github.com/official-elinas/hydrus-go/internal/db/hydrusdb"
	"github.com/official-elinas/hydrus-go/internal/storage/clientfiles"
	_ "modernc.org/sqlite"
)

func TestImporterImportPreparedFile(t *testing.T) {
	t.Run("round-trips the imported file through existing metadata APIs", func(t *testing.T) {
		dir, fixture := createImportTestBundle(t)

		bundle, err := hydrusdb.OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}

		importer, err := NewDefaultImporter(bundle, dir)
		if err != nil {
			t.Fatalf("NewDefaultImporter() error = %v", err)
		}

		sourcePath, hashHex, size := writePreparedSourceFile(
			t,
			t.TempDir(),
			"image.png",
			[]byte("pretend png bytes for prepared import round trip"),
		)

		width := int64(123)
		height := int64(456)
		hasAudio := false
		hasTransparency := true
		fileModifiedAtMS := int64(910123)
		importedAtMS := int64(920123)
		pixelHashHex := strings.Repeat("ef", 32)

		result, err := importer.ImportPreparedFile(context.Background(), PreparedFile{
			SourcePath:       sourcePath,
			HashHex:          hashHex,
			Size:             size,
			Mime:             2,
			Width:            &width,
			Height:           &height,
			PixelHashHex:     pixelHashHex,
			HasTransparency:  &hasTransparency,
			HasAudio:         &hasAudio,
			ImportedAtMS:     importedAtMS,
			FileModifiedAtMS: &fileModifiedAtMS,
		})
		if err != nil {
			t.Fatalf("ImportPreparedFile() error = %v", err)
		}

		if result.ManagedFileAlreadyPresent {
			t.Fatal("result.ManagedFileAlreadyPresent = true, want false")
		}

		if result.AlreadyImported {
			t.Fatal("result.AlreadyImported = true, want false")
		}

		managedBytes, err := os.ReadFile(result.ManagedPath)
		if err != nil {
			t.Fatalf("ReadFile(managed path) error = %v", err)
		}

		if string(managedBytes) != "pretend png bytes for prepared import round trip" {
			t.Fatalf("managed file contents = %q, want round-trip source bytes", string(managedBytes))
		}

		if err := bundle.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}

		bundle, err = hydrusdb.Open(context.Background(), dir)
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		identifierRows, err := bundle.GetMetadata(context.Background(), filemetadata.Request{
			Hashes:                []string{hashHex},
			OnlyReturnIdentifiers: true,
		})
		if err != nil {
			t.Fatalf("GetMetadata(identifier) error = %v", err)
		}

		if got := identifierRows[0]["file_id"]; got != result.FileID {
			t.Fatalf("identifier file_id = %v, want %d", got, result.FileID)
		}

		basicRows, err := bundle.GetMetadata(context.Background(), filemetadata.Request{
			Hashes:                     []string{hashHex},
			OnlyReturnBasicInformation: true,
		})
		if err != nil {
			t.Fatalf("GetMetadata(basic) error = %v", err)
		}

		basic := basicRows[0]
		if got := basic["size"]; got != size {
			t.Fatalf("basic size = %v, want %d", got, size)
		}

		if got := basic["mime"]; got != "image/png" {
			t.Fatalf("basic mime = %v, want image/png", got)
		}

		if got := basic["ext"]; got != ".png" {
			t.Fatalf("basic ext = %v, want .png", got)
		}

		if got := basic["width"]; got != width {
			t.Fatalf("basic width = %v, want %d", got, width)
		}

		if got := basic["height"]; got != height {
			t.Fatalf("basic height = %v, want %d", got, height)
		}

		if got := basic["has_audio"]; got != false {
			t.Fatalf("basic has_audio = %v, want false", got)
		}

		fullRows, err := bundle.GetMetadata(context.Background(), filemetadata.Request{
			Hashes:                []string{hashHex},
			IncludeMilliseconds:   true,
			IncludeServicesObject: true,
		})
		if err != nil {
			t.Fatalf("GetMetadata(full) error = %v", err)
		}

		full := fullRows[0]
		if got := full["is_inbox"]; got != true {
			t.Fatalf("full is_inbox = %v, want true", got)
		}

		if got := full["is_local"]; got != true {
			t.Fatalf("full is_local = %v, want true", got)
		}

		if got := full["is_trashed"]; got != false {
			t.Fatalf("full is_trashed = %v, want false", got)
		}

		if got := full["is_deleted"]; got != false {
			t.Fatalf("full is_deleted = %v, want false", got)
		}

		if got := full["pixel_hash"]; got != pixelHashHex {
			t.Fatalf("full pixel_hash = %v, want %q", got, pixelHashHex)
		}

		if got := full["has_transparency"]; got != true {
			t.Fatalf("full has_transparency = %v, want true", got)
		}

		if got := full["time_modified"]; got != 910.123 {
			t.Fatalf("full time_modified = %v, want 910.123", got)
		}

		timeModifiedDetails, ok := full["time_modified_details"].(map[string]any)
		if !ok {
			t.Fatalf("time_modified_details type = %T, want map[string]any", full["time_modified_details"])
		}

		if got := timeModifiedDetails["local"]; got != 910.123 {
			t.Fatalf("time_modified_details[local] = %v, want 910.123", got)
		}

		fileServices, ok := full["file_services"].(map[string]any)
		if !ok {
			t.Fatalf("file_services type = %T, want map[string]any", full["file_services"])
		}

		currentServices, ok := fileServices["current"].(map[string]map[string]any)
		if !ok {
			t.Fatalf("file_services[current] type = %T, want map[string]map[string]any", fileServices["current"])
		}

		if got := currentServices[fixture.localFilesServiceKeyHex]["time_imported"]; got != 920.123 {
			t.Fatalf("local files time_imported = %v, want 920.123", got)
		}

		if got := currentServices[fixture.hydrusLocalFilesServiceKeyHex]["time_imported"]; got != 920.123 {
			t.Fatalf("hydrus local storage time_imported = %v, want 920.123", got)
		}

		if got := currentServices[fixture.combinedLocalMediaServiceKeyHex]["time_imported"]; got != 920.123 {
			t.Fatalf("combined local media time_imported = %v, want 920.123", got)
		}

		if got := currentServices[fixture.allKnownFilesServiceKeyHex]["time_imported"]; got != nil {
			t.Fatalf("all known files time_imported = %v, want nil", got)
		}
	})

	t.Run("removes a newly placed managed file when DB recording fails", func(t *testing.T) {
		dir, _ := createImportTestBundle(t)

		bundle, err := hydrusdb.OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		importer, err := NewDefaultImporter(bundle, dir)
		if err != nil {
			t.Fatalf("NewDefaultImporter() error = %v", err)
		}

		mainDB := openSQLiteForTest(t, filepath.Join(dir, "client.db"))
		defer mainDB.Close()

		nextHashID := int64(1)
		if _, err := mainDB.Exec(
			`INSERT INTO current_files_3 (hash_id, timestamp_ms) VALUES (?, ?)`,
			nextHashID,
			1,
		); err != nil {
			t.Fatalf("seed conflict row error = %v", err)
		}

		sourcePath, hashHex, size := writePreparedSourceFile(
			t,
			t.TempDir(),
			"cleanup.png",
			[]byte("cleanup-on-db-failure"),
		)

		layout, err := clientfiles.NewLayout(
			clientfiles.DefaultFileRoot(dir),
			clientfiles.DefaultPrefixLength,
		)
		if err != nil {
			t.Fatalf("NewLayout() error = %v", err)
		}

		expectedPath, err := layout.ResolveFilePath(hashHex, ".png")
		if err != nil {
			t.Fatalf("ResolveFilePath() error = %v", err)
		}

		_, err = importer.ImportPreparedFile(context.Background(), PreparedFile{
			SourcePath:   sourcePath,
			HashHex:      hashHex,
			Size:         size,
			Mime:         2,
			ImportedAtMS: 930123,
		})
		if err == nil {
			t.Fatal("ImportPreparedFile() error = nil, want error")
		}

		if _, err := os.Stat(expectedPath); !os.IsNotExist(err) {
			t.Fatalf("managed file cleanup err = %v, want not exists", err)
		}
	})

	t.Run("skips managed cleanup once the DB already records the file", func(t *testing.T) {
		dir, _ := createImportTestBundle(t)

		bundle, err := hydrusdb.OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		importer, err := NewDefaultImporter(bundle, dir)
		if err != nil {
			t.Fatalf("NewDefaultImporter() error = %v", err)
		}

		sourcePath, hashHex, size := writePreparedSourceFile(
			t,
			t.TempDir(),
			"cleanup-skip.png",
			[]byte("cleanup should skip when db state exists"),
		)

		result, err := importer.ImportPreparedFile(context.Background(), PreparedFile{
			SourcePath:   sourcePath,
			HashHex:      hashHex,
			Size:         size,
			Mime:         2,
			ImportedAtMS: 935123,
		})
		if err != nil {
			t.Fatalf("ImportPreparedFile() error = %v", err)
		}

		if err := importer.cleanupFailedPlacement(
			context.Background(),
			hashHex,
			result.ManagedPath,
		); err != nil {
			t.Fatalf("cleanupFailedPlacement() error = %v", err)
		}

		managedBytes, err := os.ReadFile(result.ManagedPath)
		if err != nil {
			t.Fatalf("ReadFile(managed path) error = %v", err)
		}

		if string(managedBytes) != "cleanup should skip when db state exists" {
			t.Fatalf("managed file contents = %q, want preserved bytes", string(managedBytes))
		}
	})

	t.Run("preserves an already present managed file on duplicate conflict", func(t *testing.T) {
		dir, _ := createImportTestBundle(t)

		bundle, err := hydrusdb.OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		importer, err := NewDefaultImporter(bundle, dir)
		if err != nil {
			t.Fatalf("NewDefaultImporter() error = %v", err)
		}

		sourcePath, hashHex, size := writePreparedSourceFile(
			t,
			t.TempDir(),
			"duplicate.png",
			[]byte("keep managed file on duplicate conflict"),
		)

		first, err := importer.ImportPreparedFile(context.Background(), PreparedFile{
			SourcePath:   sourcePath,
			HashHex:      hashHex,
			Size:         size,
			Mime:         2,
			ImportedAtMS: 940123,
		})
		if err != nil {
			t.Fatalf("first ImportPreparedFile() error = %v", err)
		}

		_, err = importer.ImportPreparedFile(context.Background(), PreparedFile{
			SourcePath:   sourcePath,
			HashHex:      hashHex,
			Size:         size + 1,
			Mime:         2,
			ImportedAtMS: 940123,
		})
		if err == nil {
			t.Fatal("second ImportPreparedFile() error = nil, want error")
		}

		managedBytes, err := os.ReadFile(first.ManagedPath)
		if err != nil {
			t.Fatalf("ReadFile(managed path) error = %v", err)
		}

		if string(managedBytes) != "keep managed file on duplicate conflict" {
			t.Fatalf("managed file contents = %q, want preserved bytes", string(managedBytes))
		}
	})

	t.Run("reports already-present placement and already-imported DB state on exact retry", func(t *testing.T) {
		dir, _ := createImportTestBundle(t)

		bundle, err := hydrusdb.OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		importer, err := NewDefaultImporter(bundle, dir)
		if err != nil {
			t.Fatalf("NewDefaultImporter() error = %v", err)
		}

		sourcePath, hashHex, size := writePreparedSourceFile(
			t,
			t.TempDir(),
			"retry.png",
			[]byte("exact retry should be idempotent"),
		)

		prepared := PreparedFile{
			SourcePath:   sourcePath,
			HashHex:      hashHex,
			Size:         size,
			Mime:         2,
			ImportedAtMS: 950123,
		}

		first, err := importer.ImportPreparedFile(context.Background(), prepared)
		if err != nil {
			t.Fatalf("first ImportPreparedFile() error = %v", err)
		}

		second, err := importer.ImportPreparedFile(context.Background(), prepared)
		if err != nil {
			t.Fatalf("second ImportPreparedFile() error = %v", err)
		}

		if !second.ManagedFileAlreadyPresent {
			t.Fatal("second.ManagedFileAlreadyPresent = false, want true")
		}

		if !second.AlreadyImported {
			t.Fatal("second.AlreadyImported = false, want true")
		}

		if second.FileID != first.FileID {
			t.Fatalf("second.FileID = %d, want %d", second.FileID, first.FileID)
		}
	})
}

type importTestFixture struct {
	localFilesServiceKeyHex         string
	hydrusLocalFilesServiceKeyHex   string
	combinedLocalMediaServiceKeyHex string
	allKnownFilesServiceKeyHex      string
}

func createImportTestBundle(t *testing.T) (string, importTestFixture) {
	t.Helper()

	dir := t.TempDir()
	mainPath := filepath.Join(dir, "client.db")
	masterPath := filepath.Join(dir, "client.master.db")
	cachesPath := filepath.Join(dir, "client.caches.db")
	mappingsPath := filepath.Join(dir, "client.mappings.db")

	localFilesKey := []byte("local-files")
	hydrusLocalFilesKey := []byte("all-local-files")
	combinedLocalMediaKey := []byte("all-local-media")
	combinedFilesKey := []byte("combined-files")

	mainDB := openSQLiteForTest(t, mainPath)
	defer mainDB.Close()

	mustExec(t, mainDB, `
		CREATE TABLE services (
			service_id INTEGER PRIMARY KEY AUTOINCREMENT,
			service_key BLOB UNIQUE,
			service_type INTEGER,
			name TEXT,
			dictionary_string TEXT
		);
	`)
	mustExec(t, mainDB, `
		CREATE TABLE files_info (
			hash_id INTEGER PRIMARY KEY,
			size INTEGER,
			mime INTEGER,
			width INTEGER,
			height INTEGER,
			duration INTEGER,
			num_frames INTEGER,
			has_audio INTEGER,
			num_words INTEGER
		);
	`)
	mustExec(t, mainDB, `CREATE TABLE files_info_forced_filetypes (hash_id INTEGER PRIMARY KEY, forced_mime INTEGER);`)
	mustExec(t, mainDB, `CREATE TABLE file_inbox (hash_id INTEGER PRIMARY KEY);`)
	mustExec(t, mainDB, `CREATE TABLE archive_timestamps (hash_id INTEGER PRIMARY KEY, archived_timestamp_ms INTEGER);`)
	mustExec(t, mainDB, `CREATE TABLE file_modified_timestamps (hash_id INTEGER PRIMARY KEY, file_modified_timestamp_ms INTEGER);`)
	mustExec(t, mainDB, `CREATE TABLE file_domain_modified_timestamps (hash_id INTEGER, domain_id INTEGER, file_modified_timestamp_ms INTEGER, PRIMARY KEY (hash_id, domain_id));`)
	mustExec(t, mainDB, `CREATE TABLE url_map (hash_id INTEGER, url_id INTEGER, PRIMARY KEY (hash_id, url_id));`)
	mustExec(t, mainDB, `CREATE TABLE service_filenames (service_id INTEGER, hash_id INTEGER, filename TEXT, PRIMARY KEY (service_id, hash_id));`)
	mustExec(t, mainDB, `CREATE TABLE pixel_hash_map (hash_id INTEGER, pixel_hash_id INTEGER, PRIMARY KEY (hash_id, pixel_hash_id));`)
	mustExec(t, mainDB, `CREATE TABLE has_transparency (hash_id INTEGER PRIMARY KEY);`)
	mustExec(t, mainDB, `CREATE TABLE has_exif (hash_id INTEGER PRIMARY KEY);`)
	mustExec(t, mainDB, `CREATE TABLE has_human_readable_embedded_metadata (hash_id INTEGER PRIMARY KEY);`)
	mustExec(t, mainDB, `CREATE TABLE has_icc_profile (hash_id INTEGER PRIMARY KEY);`)
	mustExec(t, mainDB, `CREATE TABLE current_client_files_locations (location_id INTEGER PRIMARY KEY, location TEXT UNIQUE);`)
	mustExec(t, mainDB, `CREATE TABLE client_files_subfolders (prefix TEXT, location_id INTEGER, PRIMARY KEY (prefix, location_id));`)
	mustExec(t, mainDB, `CREATE TABLE ideal_client_files_locations (location_id INTEGER PRIMARY KEY, weight INTEGER, max_num_bytes INTEGER);`)
	mustExec(t, mainDB, `CREATE TABLE ideal_thumbnail_override_location (location_id INTEGER);`)
	mustExec(t, mainDB, `CREATE TABLE current_storage_granularity (granularity INTEGER);`)
	mustExec(t, mainDB, `CREATE TABLE current_files_2 (hash_id INTEGER PRIMARY KEY, timestamp_ms INTEGER);`)
	mustExec(t, mainDB, `CREATE TABLE current_files_3 (hash_id INTEGER PRIMARY KEY, timestamp_ms INTEGER);`)
	mustExec(t, mainDB, `CREATE TABLE current_files_4 (hash_id INTEGER PRIMARY KEY, timestamp_ms INTEGER);`)
	mustExec(t, mainDB, `CREATE TABLE current_files_5 (hash_id INTEGER PRIMARY KEY, timestamp_ms INTEGER);`)
	seedImportTestStorage(t, mainDB, dir)

	mustExec(
		t,
		mainDB,
		`INSERT INTO services (service_id, service_key, service_type, name, dictionary_string) VALUES (?, ?, ?, ?, ?), (?, ?, ?, ?, ?), (?, ?, ?, ?, ?), (?, ?, ?, ?, ?);`,
		2, localFilesKey, int(services.TypeLocalFileDomain), "my files", "{}",
		3, hydrusLocalFilesKey, int(services.TypeHydrusLocalFileStorage), "all local files", "{}",
		4, combinedLocalMediaKey, int(services.TypeCombinedLocalFileDomains), "all local media", "{}",
		5, combinedFilesKey, int(services.TypeCombinedFile), "all known files", "{}",
	)

	masterDB := openSQLiteForTest(t, masterPath)
	defer masterDB.Close()

	mustExec(t, masterDB, `CREATE TABLE hashes (hash_id INTEGER PRIMARY KEY, hash BLOB UNIQUE);`)
	mustExec(t, masterDB, `CREATE TABLE blurhashes (hash_id INTEGER PRIMARY KEY, blurhash TEXT);`)
	mustExec(t, masterDB, `CREATE TABLE url_domains (domain_id INTEGER PRIMARY KEY, domain TEXT UNIQUE);`)
	mustExec(t, masterDB, `CREATE TABLE urls (url_id INTEGER PRIMARY KEY, domain_id INTEGER, url TEXT UNIQUE);`)

	createEmptySQLiteFile(t, cachesPath)
	createEmptySQLiteFile(t, mappingsPath)

	return dir, importTestFixture{
		localFilesServiceKeyHex:         hex.EncodeToString(localFilesKey),
		hydrusLocalFilesServiceKeyHex:   hex.EncodeToString(hydrusLocalFilesKey),
		combinedLocalMediaServiceKeyHex: hex.EncodeToString(combinedLocalMediaKey),
		allKnownFilesServiceKeyHex:      hex.EncodeToString(combinedFilesKey),
	}
}

func seedImportTestStorage(t *testing.T, db *sql.DB, dbDir string) {
	t.Helper()

	fileRoot := clientfiles.DefaultFileRoot(dbDir)
	thumbnailRoot := clientfiles.DefaultThumbnailRoot(dbDir)

	mustExec(t, db, `INSERT INTO current_storage_granularity (granularity) VALUES (?);`, clientfiles.DefaultPrefixLength)
	mustExec(t, db, `INSERT INTO current_client_files_locations (location_id, location) VALUES (?, ?), (?, ?);`, 1, fileRoot, 2, thumbnailRoot)
	mustExec(t, db, `INSERT INTO ideal_client_files_locations (location_id, weight, max_num_bytes) VALUES (?, ?, NULL);`, 1, 1)
	mustExec(t, db, `INSERT INTO ideal_thumbnail_override_location (location_id) VALUES (?);`, 2)

	for _, prefix := range testStoragePrefixes(clientfiles.KindFile, clientfiles.DefaultPrefixLength) {
		mustExec(t, db, `INSERT INTO client_files_subfolders (prefix, location_id) VALUES (?, ?);`, prefix, 1)
	}

	for _, prefix := range testStoragePrefixes(clientfiles.KindThumbnail, clientfiles.DefaultPrefixLength) {
		mustExec(t, db, `INSERT INTO client_files_subfolders (prefix, location_id) VALUES (?, ?);`, prefix, 2)
	}

	if err := os.MkdirAll(fileRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(fileRoot) error = %v", err)
	}

	if err := os.MkdirAll(thumbnailRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(thumbnailRoot) error = %v", err)
	}
}

func testStoragePrefixes(kind clientfiles.Kind, prefixLength int) []string {
	prefixes := make([]string, 0, 1)
	var build func(string, int)
	build = func(prefix string, remaining int) {
		if remaining == 0 {
			prefixes = append(prefixes, string(kind)+prefix)
			return
		}

		for _, digit := range "0123456789abcdef" {
			build(prefix+string(digit), remaining-1)
		}
	}

	build("", prefixLength)
	return prefixes
}

func createEmptySQLiteFile(t *testing.T, path string) {
	t.Helper()

	db := openSQLiteForTest(t, path)
	mustExec(t, db, `PRAGMA user_version = 0;`)
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func openSQLiteForTest(t *testing.T, path string) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open(%q) error = %v", path, err)
	}

	return db
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()

	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("Exec(%q) error = %v", query, err)
	}
}

func writePreparedSourceFile(
	t *testing.T,
	dir string,
	name string,
	contents []byte,
) (string, string, int64) {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	sum := sha256.Sum256(contents)
	return path, hex.EncodeToString(sum[:]), int64(len(contents))
}
