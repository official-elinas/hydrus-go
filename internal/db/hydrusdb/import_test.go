package hydrusdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/official-elinas/hydrus-go/internal/core/services"
)

func TestBundleRecordPreparedLocalImport(t *testing.T) {
	t.Run("records the minimal import rows and supports exact retry", func(t *testing.T) {
		dir, _ := createTestBundle(t)

		bundle, err := OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		width := int64(320)
		height := int64(240)
		hasAudio := false
		fileModifiedAtMS := int64(700123)
		prepared := PreparedLocalImport{
			HashHex:          strings.Repeat("04", 32),
			Size:             333,
			Mime:             2,
			Width:            &width,
			Height:           &height,
			HasAudio:         &hasAudio,
			ImportedAtMS:     800123,
			FileModifiedAtMS: &fileModifiedAtMS,
		}

		result, err := bundle.RecordPreparedLocalImport(context.Background(), prepared)
		if err != nil {
			t.Fatalf("RecordPreparedLocalImport() error = %v", err)
		}

		if result.FileID <= 0 {
			t.Fatalf("result.FileID = %d, want > 0", result.FileID)
		}

		if result.AlreadyImported {
			t.Fatal("result.AlreadyImported = true, want false")
		}

		storedHashHex := selectString(
			t,
			bundle.conn,
			`SELECT lower(hex(hash)) FROM external_master.hashes WHERE hash_id = ?`,
			result.FileID,
		)
		if storedHashHex != prepared.HashHex {
			t.Fatalf("stored hash = %q, want %q", storedHashHex, prepared.HashHex)
		}

		row := bundle.conn.QueryRowContext(
			context.Background(),
			`SELECT size, mime, width, height, duration, num_frames, has_audio, num_words
			FROM main.files_info
			WHERE hash_id = ?`,
			result.FileID,
		)

		var (
			size      int64
			mime      int64
			storedW   sql.NullInt64
			storedH   sql.NullInt64
			duration  sql.NullInt64
			frames    sql.NullInt64
			storedAud sql.NullInt64
			words     sql.NullInt64
		)
		if err := row.Scan(
			&size,
			&mime,
			&storedW,
			&storedH,
			&duration,
			&frames,
			&storedAud,
			&words,
		); err != nil {
			t.Fatalf("Scan(files_info) error = %v", err)
		}

		if size != prepared.Size || mime != int64(prepared.Mime) {
			t.Fatalf("files_info size/mime = (%d,%d), want (%d,%d)", size, mime, prepared.Size, prepared.Mime)
		}

		if !storedW.Valid || storedW.Int64 != width {
			t.Fatalf("stored width = %v, want %d", storedW, width)
		}

		if !storedH.Valid || storedH.Int64 != height {
			t.Fatalf("stored height = %v, want %d", storedH, height)
		}

		if duration.Valid || frames.Valid || words.Valid {
			t.Fatalf("unexpected nullable rows present: duration=%v frames=%v words=%v", duration, frames, words)
		}

		if !storedAud.Valid || storedAud.Int64 != 0 {
			t.Fatalf("stored has_audio = %v, want 0", storedAud)
		}

		if !rowExistsInDB(
			t,
			bundle.conn,
			`SELECT 1 FROM main.file_inbox WHERE hash_id = ?`,
			result.FileID,
		) {
			t.Fatal("file_inbox row missing")
		}

		storedModifiedAt := selectNullableInt64(
			t,
			bundle.conn,
			`SELECT file_modified_timestamp_ms FROM main.file_modified_timestamps WHERE hash_id = ?`,
			result.FileID,
		)
		if !storedModifiedAt.Valid || storedModifiedAt.Int64 != fileModifiedAtMS {
			t.Fatalf("stored file_modified_timestamp_ms = %v, want %d", storedModifiedAt, fileModifiedAtMS)
		}

		localFileService := mustUniqueServiceDefinitionByType(
			t,
			bundle,
			services.TypeLocalFileDomain,
		)
		localStorageService := mustUniqueServiceDefinitionByType(
			t,
			bundle,
			services.TypeHydrusLocalFileStorage,
		)
		combinedLocalService := mustUniqueServiceDefinitionByType(
			t,
			bundle,
			services.TypeCombinedLocalFileDomains,
		)
		combinedFileService := mustUniqueServiceDefinitionByType(
			t,
			bundle,
			services.TypeCombinedFile,
		)

		for _, service := range []serviceDefinition{
			localFileService,
			localStorageService,
			combinedLocalService,
		} {
			storedImportedAt := selectNullableInt64(
				t,
				bundle.conn,
				fmt.Sprintf(
					`SELECT timestamp_ms FROM main.current_files_%d WHERE hash_id = ?`,
					service.id,
				),
				result.FileID,
			)
			if !storedImportedAt.Valid || storedImportedAt.Int64 != prepared.ImportedAtMS {
				t.Fatalf(
					"current service %q timestamp = %v, want %d",
					service.name,
					storedImportedAt,
					prepared.ImportedAtMS,
				)
			}
		}

		combinedFileImportedAt := selectNullableInt64(
			t,
			bundle.conn,
			fmt.Sprintf(
				`SELECT timestamp_ms FROM main.current_files_%d WHERE hash_id = ?`,
				combinedFileService.id,
			),
			result.FileID,
		)
		if combinedFileImportedAt.Valid {
			t.Fatalf("combined file timestamp = %v, want NULL", combinedFileImportedAt)
		}

		retryResult, err := bundle.RecordPreparedLocalImport(context.Background(), prepared)
		if err != nil {
			t.Fatalf("retry RecordPreparedLocalImport() error = %v", err)
		}

		if !retryResult.AlreadyImported {
			t.Fatal("retry result.AlreadyImported = false, want true")
		}

		if retryResult.FileID != result.FileID {
			t.Fatalf("retry result.FileID = %d, want %d", retryResult.FileID, result.FileID)
		}

		if _, err := bundle.conn.ExecContext(
			context.Background(),
			`DELETE FROM main.file_inbox WHERE hash_id = ?`,
			result.FileID,
		); err != nil {
			t.Fatalf("delete file_inbox row error = %v", err)
		}

		if _, err := bundle.conn.ExecContext(
			context.Background(),
			`INSERT INTO main.archive_timestamps (hash_id, archived_timestamp_ms) VALUES (?, ?)`,
			result.FileID,
			900123,
		); err != nil {
			t.Fatalf("insert archive_timestamps row error = %v", err)
		}

		if _, err := bundle.conn.ExecContext(
			context.Background(),
			`INSERT INTO main.current_files_8 (hash_id, timestamp_ms) VALUES (?, ?)`,
			result.FileID,
			123456789,
		); err != nil {
			t.Fatalf("insert extra current service row error = %v", err)
		}

		archivedRetry, err := bundle.RecordPreparedLocalImport(context.Background(), prepared)
		if err != nil {
			t.Fatalf("archived retry RecordPreparedLocalImport() error = %v", err)
		}

		if !archivedRetry.AlreadyImported {
			t.Fatal("archivedRetry.AlreadyImported = false, want true")
		}

		if archivedRetry.FileID != result.FileID {
			t.Fatalf("archivedRetry.FileID = %d, want %d", archivedRetry.FileID, result.FileID)
		}
	})

	t.Run("rejects conflicting duplicate metadata", func(t *testing.T) {
		dir, _ := createTestBundle(t)

		bundle, err := OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		prepared := PreparedLocalImport{
			HashHex:      strings.Repeat("05", 32),
			Size:         444,
			Mime:         2,
			ImportedAtMS: 900123,
		}

		result, err := bundle.RecordPreparedLocalImport(context.Background(), prepared)
		if err != nil {
			t.Fatalf("RecordPreparedLocalImport() error = %v", err)
		}

		prepared.Size++
		_, err = bundle.RecordPreparedLocalImport(context.Background(), prepared)
		if err == nil {
			t.Fatal("RecordPreparedLocalImport() error = nil, want error")
		}

		storedSize := selectInt64(
			t,
			bundle.conn,
			`SELECT size FROM main.files_info WHERE hash_id = ?`,
			result.FileID,
		)
		if storedSize != 444 {
			t.Fatalf("stored size after conflict = %d, want 444", storedSize)
		}
	})

	t.Run("rolls back partially written rows when a later insert fails", func(t *testing.T) {
		dir, _ := createTestBundle(t)

		bundle, err := OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		nextHashID := selectInt64(
			t,
			bundle.conn,
			`SELECT COALESCE(MAX(hash_id), 0) + 1 FROM external_master.hashes`,
		)

		localStorageService := mustUniqueServiceDefinitionByType(
			t,
			bundle,
			services.TypeHydrusLocalFileStorage,
		)
		conflictTable := fmt.Sprintf("main.current_files_%d", localStorageService.id)
		if _, err := bundle.conn.ExecContext(
			context.Background(),
			fmt.Sprintf(
				`INSERT INTO %s (hash_id, timestamp_ms) VALUES (?, ?)`,
				conflictTable,
			),
			nextHashID,
			111,
		); err != nil {
			t.Fatalf("seed conflict row error = %v", err)
		}

		prepared := PreparedLocalImport{
			HashHex:      strings.Repeat("06", 32),
			Size:         555,
			Mime:         2,
			ImportedAtMS: 910123,
		}

		_, err = bundle.RecordPreparedLocalImport(context.Background(), prepared)
		if err == nil {
			t.Fatal("RecordPreparedLocalImport() error = nil, want error")
		}

		if rowExistsInDB(
			t,
			bundle.conn,
			`SELECT 1 FROM external_master.hashes WHERE hash = ?`,
			mustDecodeHex(t, prepared.HashHex),
		) {
			t.Fatal("hash row persisted after rollback")
		}

		if rowExistsInDB(
			t,
			bundle.conn,
			`SELECT 1 FROM main.files_info WHERE hash_id = ?`,
			nextHashID,
		) {
			t.Fatal("files_info row persisted after rollback")
		}

		localFileService := mustUniqueServiceDefinitionByType(
			t,
			bundle,
			services.TypeLocalFileDomain,
		)
		if rowExistsInDB(
			t,
			bundle.conn,
			fmt.Sprintf(
				`SELECT 1 FROM main.current_files_%d WHERE hash_id = ?`,
				localFileService.id,
			),
			nextHashID,
		) {
			t.Fatal("local current membership row persisted after rollback")
		}
	})

	t.Run("requires an explicit local file service when multiple local domains exist", func(t *testing.T) {
		dir, fixture := createTestBundle(t)

		bundle, err := OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		otherLocalKey := []byte("other-local-files")
		inserted, err := bundle.conn.ExecContext(
			context.Background(),
			`INSERT INTO main.services (service_key, service_type, name, dictionary_string) VALUES (?, ?, ?, ?)`,
			otherLocalKey,
			int(services.TypeLocalFileDomain),
			"other files",
			"{}",
		)
		if err != nil {
			t.Fatalf("insert second local file service error = %v", err)
		}

		otherLocalServiceID, err := inserted.LastInsertId()
		if err != nil {
			t.Fatalf("LastInsertId() error = %v", err)
		}

		if _, err := bundle.conn.ExecContext(
			context.Background(),
			fmt.Sprintf(
				`CREATE TABLE main.current_files_%d (hash_id INTEGER PRIMARY KEY, timestamp_ms INTEGER)`,
				otherLocalServiceID,
			),
		); err != nil {
			t.Fatalf("create second local current table error = %v", err)
		}

		prepared := PreparedLocalImport{
			HashHex:      strings.Repeat("07", 32),
			Size:         666,
			Mime:         2,
			ImportedAtMS: 920123,
		}

		_, err = bundle.RecordPreparedLocalImport(context.Background(), prepared)
		if err == nil {
			t.Fatal("RecordPreparedLocalImport() error = nil, want error")
		}

		prepared.LocalFileServiceKey = fixture.localFilesServiceKeyHex
		result, err := bundle.RecordPreparedLocalImport(context.Background(), prepared)
		if err != nil {
			t.Fatalf("RecordPreparedLocalImport() with explicit local service error = %v", err)
		}

		if result.FileID <= 0 {
			t.Fatalf("result.FileID = %d, want > 0", result.FileID)
		}
	})
}

func mustUniqueServiceDefinitionByType(
	t *testing.T,
	bundle *Bundle,
	serviceType services.Type,
) serviceDefinition {
	t.Helper()

	service, ok, err := findUniqueServiceByTypeFromBundle(bundle, serviceType)
	if err != nil {
		t.Fatalf("findUniqueServiceByTypeFromBundle() error = %v", err)
	}

	if !ok {
		t.Fatalf("service type %d missing", serviceType)
	}

	return service
}

func findUniqueServiceByTypeFromBundle(
	bundle *Bundle,
	serviceType services.Type,
) (serviceDefinition, bool, error) {
	definitions, err := bundle.lookupAllServiceDefinitions(context.Background())
	if err != nil {
		return serviceDefinition{}, false, err
	}

	return findUniqueServiceByType(definitions, serviceType)
}

func rowExistsInDB(
	t *testing.T,
	conn *sql.Conn,
	query string,
	args ...any,
) bool {
	t.Helper()

	row := conn.QueryRowContext(context.Background(), query, args...)
	var ignored int
	if err := row.Scan(&ignored); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false
		}

		t.Fatalf("Scan(row exists) error = %v", err)
	}

	return true
}

func selectString(t *testing.T, conn *sql.Conn, query string, args ...any) string {
	t.Helper()

	row := conn.QueryRowContext(context.Background(), query, args...)
	var value string
	if err := row.Scan(&value); err != nil {
		t.Fatalf("Scan(string) error = %v", err)
	}

	return value
}

func selectInt64(t *testing.T, conn *sql.Conn, query string, args ...any) int64 {
	t.Helper()

	row := conn.QueryRowContext(context.Background(), query, args...)
	var value int64
	if err := row.Scan(&value); err != nil {
		t.Fatalf("Scan(int64) error = %v", err)
	}

	return value
}

func selectNullableInt64(
	t *testing.T,
	conn *sql.Conn,
	query string,
	args ...any,
) sql.NullInt64 {
	t.Helper()

	row := conn.QueryRowContext(context.Background(), query, args...)
	var value sql.NullInt64
	if err := row.Scan(&value); err != nil {
		t.Fatalf("Scan(nullable int64) error = %v", err)
	}

	return value
}
