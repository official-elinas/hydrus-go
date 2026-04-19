package bootstrap

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/official-elinas/hydrus-go/internal/buildinfo"
	"github.com/official-elinas/hydrus-go/internal/core/librarybrowse"
	"github.com/official-elinas/hydrus-go/internal/core/services"
	"github.com/official-elinas/hydrus-go/internal/db/hydrusdb"
	"github.com/official-elinas/hydrus-go/internal/storage/clientfiles"
	_ "modernc.org/sqlite"
)

const (
	emptyServiceDictionaryString      = `[21,2,[]]`
	favouritesServiceDictionaryString = `[
		21,
		2,
		[
			[[0,"colours"],[2,[26,3,[[0,[0,[[0,0,0],[240,240,65]]]],[0,[1,[[0,0,0],[200,80,120]]]],[0,[2,[[0,0,0],[191,191,191]]]],[0,[4,[[0,0,0],[95,95,95]]]]]]]],
			[[0,"show_in_thumbnail"],[0,false]],
			[[0,"show_in_thumbnail_even_when_null"],[0,false]],
			[[0,"shape"],[0,2]],
			[[0,"rating_svg"],[0,null]]
		]
	]`
)

type seededService struct {
	id            int64
	service       services.Service
	createCurrent bool
	createDeleted bool
}

var createFreshClientBundle = defaultCreateFreshClientBundle
var verifyFreshClientBundle = defaultVerifyFreshClientBundle

func defaultCreateFreshClientBundle(ctx context.Context, dbDir string) error {
	parentDir := filepath.Dir(dbDir)
	stagingDir, err := os.MkdirTemp(parentDir, ".hydrus-go-bootstrap-*")
	if err != nil {
		return fmt.Errorf("create bootstrap staging directory: %w", err)
	}
	defer os.RemoveAll(stagingDir)

	stagingPaths := bundleFilePaths(stagingDir)
	targetPaths := bundleFilePaths(dbDir)
	thumbnailRoot := clientfiles.DefaultThumbnailRoot(dbDir)

	if err := createMainBootstrapDB(
		ctx,
		stagingPaths.main,
		portableStorageLocation(dbDir, targetPaths.files),
		portableStorageLocation(dbDir, thumbnailRoot),
	); err != nil {
		return err
	}

	if err := createMasterBootstrapDB(ctx, stagingPaths.master); err != nil {
		return err
	}

	if err := createEmptyBootstrapDB(ctx, stagingPaths.caches); err != nil {
		return err
	}

	if err := createEmptyBootstrapDB(ctx, stagingPaths.mappings); err != nil {
		return err
	}

	if err := createEmptyBootstrapDB(ctx, stagingPaths.temp); err != nil {
		return err
	}

	if err := os.MkdirAll(stagingPaths.files, 0o755); err != nil {
		return fmt.Errorf("create staged client_files root: %w", err)
	}

	if err := moveBootstrappedPathsIntoPlace(stagingPaths, targetPaths); err != nil {
		return err
	}

	if err := os.MkdirAll(thumbnailRoot, 0o755); err != nil {
		return fmt.Errorf("create managed thumbnails root: %w", err)
	}

	return nil
}

func defaultVerifyFreshClientBundle(ctx context.Context, dbDir string) error {
	readBundle, err := hydrusdb.Open(ctx, dbDir)
	if err != nil {
		return fmt.Errorf("open read bundle: %w", err)
	}
	defer readBundle.Close()

	writeBundle, err := hydrusdb.OpenWritable(ctx, dbDir)
	if err != nil {
		return fmt.Errorf("open write bundle: %w", err)
	}
	defer writeBundle.Close()

	catalog, err := readBundle.List(ctx)
	if err != nil {
		return fmt.Errorf("list seeded services: %w", err)
	}

	mainPath := filepath.Join(dbDir, "client.db")

	serviceRows, err := lookupSeededServices(ctx, mainPath)
	if err != nil {
		return err
	}

	mainTableNames, err := lookupTableNames(ctx, mainPath)
	if err != nil {
		return err
	}

	if err := verifySeededRuntimeServices(catalog, serviceRows, mainTableNames); err != nil {
		return err
	}

	if err := verifySeededBootstrapTables(mainTableNames); err != nil {
		return err
	}

	if err := verifySeededBootstrapStorage(ctx, dbDir, mainPath); err != nil {
		return err
	}

	if err := verifySeededBootstrapVersion(ctx, mainPath); err != nil {
		return err
	}

	page, err := readBundle.ListRecent(ctx, librarybrowse.Request{Offset: 0, Limit: 1})
	if err != nil {
		return fmt.Errorf("list recent files on fresh bundle: %w", err)
	}

	if len(page.Items) != 0 {
		return fmt.Errorf("fresh bundle should start empty, found %d recent items", len(page.Items))
	}

	return nil
}

type bundlePaths struct {
	main     string
	master   string
	caches   string
	mappings string
	temp     string
	files    string
}

type verifiedServiceRow struct {
	id          int64
	serviceKey  string
	serviceType services.Type
	name        string
}

func bundleFilePaths(dir string) bundlePaths {
	return bundlePaths{
		main:     filepath.Join(dir, "client.db"),
		master:   filepath.Join(dir, "client.master.db"),
		caches:   filepath.Join(dir, "client.caches.db"),
		mappings: filepath.Join(dir, "client.mappings.db"),
		temp:     filepath.Join(dir, "client.temp.db"),
		files:    filepath.Join(dir, "client_files"),
	}
}

func moveBootstrappedPathsIntoPlace(stagingPaths bundlePaths, targetPaths bundlePaths) error {
	moves := [][2]string{
		{stagingPaths.main, targetPaths.main},
		{stagingPaths.master, targetPaths.master},
		{stagingPaths.caches, targetPaths.caches},
		{stagingPaths.mappings, targetPaths.mappings},
		{stagingPaths.temp, targetPaths.temp},
		{stagingPaths.files, targetPaths.files},
	}

	movedTargets := []string{}
	for _, move := range moves {
		if err := os.Rename(move[0], move[1]); err != nil {
			cleanupMovedTargets(movedTargets)
			return fmt.Errorf("move bootstrapped path into place: %w", err)
		}

		movedTargets = append(movedTargets, move[1])
	}

	return nil
}

func cleanupMovedTargets(paths []string) {
	for _, path := range paths {
		_ = os.RemoveAll(path)
	}
}

func createMainBootstrapDB(
	ctx context.Context,
	path string,
	clientFilesRoot string,
	thumbnailRoot string,
) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("open main bootstrap DB %q: %w", path, err)
	}
	defer db.Close()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin main bootstrap transaction: %w", err)
	}
	defer tx.Rollback()

	for _, statement := range []string{
		`PRAGMA user_version = 0;`,
		`CREATE TABLE version (version INTEGER);`,
		`CREATE TABLE services (
			service_id INTEGER PRIMARY KEY AUTOINCREMENT,
			service_key BLOB UNIQUE,
			service_type INTEGER,
			name TEXT,
			dictionary_string TEXT
		);`,
		`CREATE TABLE service_info (
			service_id INTEGER,
			info_type INTEGER,
			info INTEGER,
			PRIMARY KEY (service_id, info_type)
		);`,
		`CREATE TABLE files_info (
			hash_id INTEGER PRIMARY KEY,
			size INTEGER,
			mime INTEGER,
			width INTEGER,
			height INTEGER,
			duration INTEGER,
			num_frames INTEGER,
			has_audio INTEGER,
			num_words INTEGER
		);`,
		`CREATE TABLE files_info_forced_filetypes (
			hash_id INTEGER PRIMARY KEY,
			forced_mime INTEGER
		);`,
		`CREATE TABLE file_inbox (hash_id INTEGER PRIMARY KEY);`,
		`CREATE TABLE archive_timestamps (
			hash_id INTEGER PRIMARY KEY,
			archived_timestamp_ms INTEGER
		);`,
		`CREATE TABLE file_modified_timestamps (
			hash_id INTEGER PRIMARY KEY,
			file_modified_timestamp_ms INTEGER
		);`,
		`CREATE TABLE file_domain_modified_timestamps (
			hash_id INTEGER,
			domain_id INTEGER,
			file_modified_timestamp_ms INTEGER,
			PRIMARY KEY (hash_id, domain_id)
		);`,
		`CREATE TABLE url_map (
			hash_id INTEGER,
			url_id INTEGER,
			PRIMARY KEY (hash_id, url_id)
		);`,
		`CREATE TABLE service_filenames (
			service_id INTEGER,
			hash_id INTEGER,
			filename TEXT,
			PRIMARY KEY (service_id, hash_id)
		);`,
		`CREATE TABLE pixel_hash_map (
			hash_id INTEGER,
			pixel_hash_id INTEGER,
			PRIMARY KEY (hash_id, pixel_hash_id)
		);`,
		`CREATE TABLE has_transparency (hash_id INTEGER PRIMARY KEY);`,
		`CREATE TABLE has_exif (hash_id INTEGER PRIMARY KEY);`,
		`CREATE TABLE has_human_readable_embedded_metadata (hash_id INTEGER PRIMARY KEY);`,
		`CREATE TABLE has_icc_profile (hash_id INTEGER PRIMARY KEY);`,
		`CREATE TABLE current_client_files_locations (
			location_id INTEGER PRIMARY KEY,
			location TEXT UNIQUE
		);`,
		`CREATE TABLE client_files_subfolders (
			prefix TEXT,
			location_id INTEGER,
			PRIMARY KEY (prefix, location_id)
		);`,
		`CREATE TABLE ideal_client_files_locations (
			location_id INTEGER PRIMARY KEY,
			weight INTEGER,
			max_num_bytes INTEGER
		);`,
		`CREATE TABLE ideal_thumbnail_override_location (location_id INTEGER);`,
		`CREATE TABLE current_storage_granularity (granularity INTEGER);`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("exec main bootstrap schema: %w", err)
		}
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO version (version) VALUES (?)`,
		buildinfo.ReferenceHydrusVersion,
	); err != nil {
		return fmt.Errorf("seed bootstrap version: %w", err)
	}

	seededServices, err := seedBootstrapServices(ctx, tx)
	if err != nil {
		return err
	}

	for _, seeded := range seededServices {
		if seeded.createCurrent {
			statement := fmt.Sprintf(
				`CREATE TABLE current_files_%d (
					hash_id INTEGER PRIMARY KEY,
					timestamp_ms INTEGER
				);`,
				seeded.id,
			)
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("create current membership table for %q: %w", seeded.service.Name, err)
			}
		}

		if seeded.createDeleted {
			statement := fmt.Sprintf(
				`CREATE TABLE deleted_files_%d (
					hash_id INTEGER PRIMARY KEY,
					timestamp_ms INTEGER,
					original_timestamp_ms INTEGER
				);`,
				seeded.id,
			)
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("create deleted membership table for %q: %w", seeded.service.Name, err)
			}
		}
	}

	if err := seedBootstrapStorage(ctx, tx, clientFilesRoot, thumbnailRoot); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit main bootstrap transaction: %w", err)
	}

	return nil
}

func createMasterBootstrapDB(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("open master bootstrap DB %q: %w", path, err)
	}
	defer db.Close()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin master bootstrap transaction: %w", err)
	}
	defer tx.Rollback()

	for _, statement := range []string{
		`PRAGMA user_version = 0;`,
		`CREATE TABLE hashes (hash_id INTEGER PRIMARY KEY, hash BLOB UNIQUE);`,
		`CREATE TABLE blurhashes (hash_id INTEGER PRIMARY KEY, blurhash TEXT);`,
		`CREATE TABLE url_domains (domain_id INTEGER PRIMARY KEY, domain TEXT UNIQUE);`,
		`CREATE TABLE urls (url_id INTEGER PRIMARY KEY, domain_id INTEGER, url TEXT UNIQUE);`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("exec master bootstrap schema: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit master bootstrap transaction: %w", err)
	}

	return nil
}

func createEmptyBootstrapDB(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("open bootstrap DB %q: %w", path, err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 0;`); err != nil {
		return fmt.Errorf("initialize bootstrap DB %q: %w", path, err)
	}

	return nil
}

func seedBootstrapServices(ctx context.Context, tx *sql.Tx) ([]seededService, error) {
	bootstrapCatalog := services.BootstrapCatalog()
	seeded := make([]seededService, 0, len(bootstrapCatalog))

	for _, service := range bootstrapCatalog {
		serviceKeyBytes, err := hex.DecodeString(service.ServiceKey)
		if err != nil {
			return nil, fmt.Errorf("decode service key for %q: %w", service.Name, err)
		}

		result, err := tx.ExecContext(
			ctx,
			`INSERT INTO services (service_key, service_type, name, dictionary_string)
			VALUES (?, ?, ?, ?)`,
			serviceKeyBytes,
			int(service.Type),
			service.Name,
			bootstrapServiceDictionaryString(service),
		)
		if err != nil {
			return nil, fmt.Errorf("insert bootstrap service %q: %w", service.Name, err)
		}

		serviceID, err := result.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("read inserted service id for %q: %w", service.Name, err)
		}

		seeded = append(seeded, seededService{
			id:            serviceID,
			service:       service,
			createCurrent: shouldCreateCurrentMembershipTable(service.Type),
			createDeleted: shouldCreateDeletedMembershipTable(service.Type),
		})
	}

	return seeded, nil
}

func seedBootstrapStorage(
	ctx context.Context,
	tx *sql.Tx,
	clientFilesRoot string,
	thumbnailRoot string,
) error {
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO current_storage_granularity (granularity) VALUES (?)`,
		clientfiles.DefaultPrefixLength,
	); err != nil {
		return fmt.Errorf("seed client-files storage granularity: %w", err)
	}

	fileLocationID, err := seedStorageLocation(ctx, tx, clientFilesRoot)
	if err != nil {
		return fmt.Errorf("seed client-files location: %w", err)
	}

	thumbnailLocationID, err := seedStorageLocation(ctx, tx, thumbnailRoot)
	if err != nil {
		return fmt.Errorf("seed thumbnail location: %w", err)
	}

	for _, prefix := range storagePrefixes(clientfiles.KindFile, clientfiles.DefaultPrefixLength) {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO client_files_subfolders (prefix, location_id) VALUES (?, ?)`,
			prefix,
			fileLocationID,
		); err != nil {
			return fmt.Errorf("seed client-files subfolder %q: %w", prefix, err)
		}
	}

	for _, prefix := range storagePrefixes(clientfiles.KindThumbnail, clientfiles.DefaultPrefixLength) {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO client_files_subfolders (prefix, location_id) VALUES (?, ?)`,
			prefix,
			thumbnailLocationID,
		); err != nil {
			return fmt.Errorf("seed client-files subfolder %q: %w", prefix, err)
		}
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO ideal_client_files_locations (location_id, weight, max_num_bytes) VALUES (?, ?, NULL)`,
		fileLocationID,
		1,
	); err != nil {
		return fmt.Errorf("seed ideal client-files location: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO ideal_thumbnail_override_location (location_id) VALUES (?)`,
		thumbnailLocationID,
	); err != nil {
		return fmt.Errorf("seed ideal thumbnail override location: %w", err)
	}

	return nil
}

func seedStorageLocation(ctx context.Context, tx *sql.Tx, location string) (int64, error) {
	result, err := tx.ExecContext(
		ctx,
		`INSERT OR IGNORE INTO current_client_files_locations (location) VALUES (?)`,
		location,
	)
	if err != nil {
		return 0, err
	}

	if locationID, err := result.LastInsertId(); err != nil {
		return 0, fmt.Errorf("read seeded location id: %w", err)
	} else if locationID > 0 {
		return locationID, nil
	}

	row := tx.QueryRowContext(
		ctx,
		`SELECT location_id FROM current_client_files_locations WHERE location = ? LIMIT 1`,
		location,
	)

	var locationID int64
	if err := row.Scan(&locationID); err != nil {
		return 0, fmt.Errorf("look up seeded location id: %w", err)
	}

	return locationID, nil
}

func portableStorageLocation(dbDir string, absolutePath string) string {
	relativePath, err := filepath.Rel(dbDir, absolutePath)
	if err == nil && relativePath != "." && !strings.HasPrefix(relativePath, "..") {
		if os.PathSeparator == '\\' {
			return strings.ReplaceAll(relativePath, `\`, `/`)
		}

		return relativePath
	}

	if os.PathSeparator == '\\' {
		return strings.ReplaceAll(absolutePath, `\`, `/`)
	}

	return absolutePath
}

func bootstrapServiceDictionaryString(service services.Service) string {
	if service.Name == "favourites" {
		return favouritesServiceDictionaryString
	}

	return emptyServiceDictionaryString
}

func shouldCreateCurrentMembershipTable(serviceType services.Type) bool {
	switch serviceType {
	case services.TypeLocalFileDomain,
		services.TypeHydrusLocalFileStorage,
		services.TypeCombinedLocalFileDomains,
		services.TypeCombinedFile,
		services.TypeCombinedDeletedFile,
		services.TypeLocalFileTrashDomain:
		return true
	default:
		return false
	}
}

func shouldCreateDeletedMembershipTable(serviceType services.Type) bool {
	switch serviceType {
	case services.TypeCombinedLocalFileDomains,
		services.TypeCombinedFile:
		return true
	default:
		return false
	}
}

func storagePrefixes(kind clientfiles.Kind, prefixLength int) []string {
	suffixes := []string{""}
	for range prefixLength {
		next := make([]string, 0, len(suffixes)*16)
		for _, suffix := range suffixes {
			for _, digit := range "0123456789abcdef" {
				next = append(next, suffix+string(digit))
			}
		}
		suffixes = next
	}

	prefixes := make([]string, 0, len(suffixes))
	for _, suffix := range suffixes {
		prefixes = append(prefixes, string(kind)+suffix)
	}

	return prefixes
}

func lookupSeededServices(ctx context.Context, mainPath string) ([]verifiedServiceRow, error) {
	db, err := sql.Open("sqlite", mainPath)
	if err != nil {
		return nil, fmt.Errorf("open main DB for service verification: %w", err)
	}
	defer db.Close()

	rows, err := db.QueryContext(
		ctx,
		`SELECT service_id, lower(hex(service_key)), service_type, name
		FROM services
		ORDER BY service_id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("query seeded services: %w", err)
	}
	defer rows.Close()

	verified := []verifiedServiceRow{}
	for rows.Next() {
		var row verifiedServiceRow
		if err := rows.Scan(&row.id, &row.serviceKey, &row.serviceType, &row.name); err != nil {
			return nil, fmt.Errorf("scan seeded service row: %w", err)
		}

		verified = append(verified, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate seeded service rows: %w", err)
	}

	return verified, nil
}

func lookupTableNames(ctx context.Context, mainPath string) (map[string]struct{}, error) {
	db, err := sql.Open("sqlite", mainPath)
	if err != nil {
		return nil, fmt.Errorf("open main DB for table verification: %w", err)
	}
	defer db.Close()

	rows, err := db.QueryContext(
		ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table'`,
	)
	if err != nil {
		return nil, fmt.Errorf("query main DB table names: %w", err)
	}
	defer rows.Close()

	names := map[string]struct{}{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan main DB table name: %w", err)
		}

		names[name] = struct{}{}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate main DB table names: %w", err)
	}

	return names, nil
}

func verifySeededRuntimeServices(
	catalog services.Catalog,
	serviceRows []verifiedServiceRow,
	mainTableNames map[string]struct{},
) error {
	if len(serviceRows) != len(services.BootstrapCatalog()) {
		return fmt.Errorf(
			"expected %d seeded services, found %d",
			len(services.BootstrapCatalog()),
			len(serviceRows),
		)
	}

	rowsByKey := map[string]verifiedServiceRow{}
	serviceTypes := map[services.Type][]verifiedServiceRow{}
	for _, row := range serviceRows {
		rowsByKey[row.serviceKey] = row
		serviceTypes[row.serviceType] = append(serviceTypes[row.serviceType], row)
	}

	for _, expected := range services.BootstrapCatalog() {
		row, ok := rowsByKey[expected.ServiceKey]
		if !ok {
			return fmt.Errorf("required seeded service %q is missing", expected.Name)
		}

		if row.serviceType != expected.Type || row.name != expected.Name {
			return fmt.Errorf(
				"seeded service %q did not match expected type/name",
				expected.Name,
			)
		}
	}

	for _, requiredType := range []services.Type{
		services.TypeLocalFileDomain,
		services.TypeHydrusLocalFileStorage,
		services.TypeCombinedLocalFileDomains,
		services.TypeCombinedFile,
		services.TypeCombinedDeletedFile,
		services.TypeLocalFileTrashDomain,
	} {
		rows := serviceTypes[requiredType]
		if len(rows) != 1 {
			return fmt.Errorf("expected exactly one seeded service of type %d, found %d", requiredType, len(rows))
		}

		tableName := fmt.Sprintf("current_files_%d", rows[0].id)
		if _, ok := mainTableNames[tableName]; !ok {
			return fmt.Errorf("required seeded membership table %q is missing", tableName)
		}
	}

	for _, requiredType := range []services.Type{
		services.TypeCombinedLocalFileDomains,
		services.TypeCombinedFile,
	} {
		rows := serviceTypes[requiredType]
		tableName := fmt.Sprintf("deleted_files_%d", rows[0].id)
		if _, ok := mainTableNames[tableName]; !ok {
			return fmt.Errorf("required seeded deleted-membership table %q is missing", tableName)
		}
	}

	if len(catalog) != len(services.DefaultCatalog()) {
		return fmt.Errorf(
			"expected %d seeded discovery services, found %d",
			len(services.DefaultCatalog()),
			len(catalog),
		)
	}

	for _, expected := range services.DefaultCatalog() {
		service, ok := catalog.ByKey(expected.ServiceKey)
		if !ok {
			return fmt.Errorf("required discovery service %q is missing", expected.Name)
		}

		if service.Type != expected.Type || service.Name != expected.Name {
			return fmt.Errorf("discovery service %q did not match expected type/name", expected.Name)
		}
	}

	if err := verifyFavouritesService(catalog); err != nil {
		return err
	}

	return nil
}

func verifyFavouritesService(catalog services.Catalog) error {
	favourites, ok := catalog.ByName("favourites")
	if !ok {
		return fmt.Errorf("required discovery service %q is missing", "favourites")
	}

	if favourites.StarShape != "fat star" {
		return fmt.Errorf("favourites star shape = %q, want %q", favourites.StarShape, "fat star")
	}

	if favourites.ShowInThumbnail == nil || *favourites.ShowInThumbnail {
		return fmt.Errorf("favourites show_in_thumbnail missing or true, want explicit false")
	}

	if favourites.ShowInThumbnailEvenWhenNull == nil || *favourites.ShowInThumbnailEvenWhenNull {
		return fmt.Errorf(
			"favourites show_in_thumbnail_even_when_null missing or true, want explicit false",
		)
	}

	if got := favourites.Colours["like"].Brush; got != "#F0F041" {
		return fmt.Errorf("favourites like brush = %q, want %q", got, "#F0F041")
	}

	if got := favourites.Colours["dislike"].Brush; got != "#C85078" {
		return fmt.Errorf("favourites dislike brush = %q, want %q", got, "#C85078")
	}

	if got := favourites.Colours["null"].Brush; got != "#BFBFBF" {
		return fmt.Errorf("favourites null brush = %q, want %q", got, "#BFBFBF")
	}

	if got := favourites.Colours["mixed"].Brush; got != "#5F5F5F" {
		return fmt.Errorf("favourites mixed brush = %q, want %q", got, "#5F5F5F")
	}

	return nil
}

func verifySeededBootstrapTables(mainTableNames map[string]struct{}) error {
	for _, requiredTable := range []string{
		"version",
		"service_info",
		"current_client_files_locations",
		"client_files_subfolders",
		"ideal_client_files_locations",
		"ideal_thumbnail_override_location",
		"current_storage_granularity",
	} {
		if _, ok := mainTableNames[requiredTable]; !ok {
			return fmt.Errorf("required seeded table %q is missing", requiredTable)
		}
	}

	return nil
}

func verifySeededBootstrapStorage(ctx context.Context, dbDir string, mainPath string) error {
	clientFilesRoot := clientfiles.DefaultFileRoot(dbDir)
	thumbnailRoot := clientfiles.DefaultThumbnailRoot(dbDir)
	storedClientFilesRoot := portableStorageLocation(dbDir, clientFilesRoot)
	storedThumbnailRoot := portableStorageLocation(dbDir, thumbnailRoot)

	info, err := os.Stat(clientFilesRoot)
	if err != nil {
		return fmt.Errorf("stat seeded client_files root: %w", err)
	}

	if !info.IsDir() {
		return fmt.Errorf("seeded client_files root %q must be a directory", clientFilesRoot)
	}

	thumbnailInfo, err := os.Stat(thumbnailRoot)
	if err != nil {
		return fmt.Errorf("stat seeded thumbnails root: %w", err)
	}

	if !thumbnailInfo.IsDir() {
		return fmt.Errorf("seeded thumbnails root %q must be a directory", thumbnailRoot)
	}

	if tempInfo, err := os.Stat(filepath.Join(dbDir, "client.temp.db")); err != nil {
		return fmt.Errorf("stat seeded temp DB: %w", err)
	} else if tempInfo.IsDir() {
		return fmt.Errorf("seeded temp DB must be a file")
	}

	db, err := sql.Open("sqlite", mainPath)
	if err != nil {
		return fmt.Errorf("open main DB for storage verification: %w", err)
	}
	defer db.Close()

	granularity, err := querySingleInt(
		ctx,
		db,
		`SELECT granularity FROM current_storage_granularity LIMIT 1`,
	)
	if err != nil {
		return fmt.Errorf("verify client-files storage granularity: %w", err)
	}

	if granularity != clientfiles.DefaultPrefixLength {
		return fmt.Errorf(
			"seeded client-files storage granularity = %d, want %d",
			granularity,
			clientfiles.DefaultPrefixLength,
		)
	}

	locationCount, err := querySingleInt(
		ctx,
		db,
		`SELECT COUNT(*) FROM current_client_files_locations`,
	)
	if err != nil {
		return fmt.Errorf("count seeded client-files locations: %w", err)
	}

	if locationCount != 2 {
		return fmt.Errorf("expected exactly two seeded client-files locations, found %d", locationCount)
	}

	fileLocation, err := querySingleString(
		ctx,
		db,
		`SELECT cfl.location
		FROM current_client_files_locations cfl
		JOIN client_files_subfolders cfs USING (location_id)
		WHERE cfs.prefix LIKE 'f%'
		ORDER BY cfs.rowid ASC
		LIMIT 1`,
	)
	if err != nil {
		return fmt.Errorf("read seeded file location: %w", err)
	}

	if fileLocation != storedClientFilesRoot {
		return fmt.Errorf("seeded file location = %q, want %q", fileLocation, storedClientFilesRoot)
	}

	thumbnailLocation, err := querySingleString(
		ctx,
		db,
		`SELECT cfl.location
		FROM current_client_files_locations cfl
		JOIN client_files_subfolders cfs USING (location_id)
		WHERE cfs.prefix LIKE 't%'
		ORDER BY cfs.rowid ASC
		LIMIT 1`,
	)
	if err != nil {
		return fmt.Errorf("read seeded thumbnail location: %w", err)
	}

	if thumbnailLocation != storedThumbnailRoot {
		return fmt.Errorf("seeded thumbnail location = %q, want %q", thumbnailLocation, storedThumbnailRoot)
	}

	idealLocationCount, err := querySingleInt(
		ctx,
		db,
		`SELECT COUNT(*) FROM ideal_client_files_locations`,
	)
	if err != nil {
		return fmt.Errorf("count ideal client-files locations: %w", err)
	}

	if idealLocationCount != 1 {
		return fmt.Errorf("expected exactly one ideal client-files location, found %d", idealLocationCount)
	}

	thumbnailOverrideCount, err := querySingleInt(
		ctx,
		db,
		`SELECT COUNT(*) FROM ideal_thumbnail_override_location`,
	)
	if err != nil {
		return fmt.Errorf("count ideal thumbnail override locations: %w", err)
	}

	if thumbnailOverrideCount != 1 {
		return fmt.Errorf(
			"expected one ideal thumbnail override location, found %d",
			thumbnailOverrideCount,
		)
	}

	idealThumbnailLocation, err := querySingleString(
		ctx,
		db,
		`SELECT cfl.location
		FROM ideal_thumbnail_override_location itol
		JOIN current_client_files_locations cfl USING (location_id)
		LIMIT 1`,
	)
	if err != nil {
		return fmt.Errorf("read ideal thumbnail override location: %w", err)
	}

	if idealThumbnailLocation != storedThumbnailRoot {
		return fmt.Errorf(
			"ideal thumbnail override location = %q, want %q",
			idealThumbnailLocation,
			storedThumbnailRoot,
		)
	}

	expectedPrefixes := expectedPrefixCount(clientfiles.DefaultPrefixLength)
	totalPrefixCount, err := querySingleInt(
		ctx,
		db,
		`SELECT COUNT(*) FROM client_files_subfolders`,
	)
	if err != nil {
		return fmt.Errorf("count seeded client-files prefixes: %w", err)
	}

	if totalPrefixCount != expectedPrefixes*2 {
		return fmt.Errorf(
			"expected %d seeded client-files prefixes, found %d",
			expectedPrefixes*2,
			totalPrefixCount,
		)
	}

	for _, kind := range []clientfiles.Kind{clientfiles.KindFile, clientfiles.KindThumbnail} {
		count, err := querySingleInt(
			ctx,
			db,
			`SELECT COUNT(*) FROM client_files_subfolders WHERE prefix LIKE ?`,
			string(kind)+"%",
		)
		if err != nil {
			return fmt.Errorf("count seeded %s client-files prefixes: %w", kind, err)
		}

		if count != expectedPrefixes {
			return fmt.Errorf(
				"expected %d seeded %s client-files prefixes, found %d",
				expectedPrefixes,
				kind,
				count,
			)
		}
	}

	for _, check := range []struct {
		kind string
		want string
	}{
		{kind: "f", want: clientFilesRoot},
		{kind: "t", want: thumbnailRoot},
	} {
		storedWant := portableStorageLocation(dbDir, check.want)
		count, err := querySingleInt(
			ctx,
			db,
			`SELECT COUNT(*)
			FROM client_files_subfolders cfs
			JOIN current_client_files_locations cfl USING (location_id)
			WHERE cfs.prefix LIKE ? AND cfl.location = ?`,
			check.kind+"%",
			storedWant,
		)
		if err != nil {
			return fmt.Errorf("count seeded %s-prefix locations: %w", check.kind, err)
		}

		if count != expectedPrefixes {
			return fmt.Errorf(
				"expected %d seeded %s-prefix rows at %q, found %d",
				expectedPrefixes,
				check.kind,
				storedWant,
				count,
			)
		}
	}

	return nil
}

func verifySeededBootstrapVersion(ctx context.Context, mainPath string) error {
	db, err := sql.Open("sqlite", mainPath)
	if err != nil {
		return fmt.Errorf("open main DB for version verification: %w", err)
	}
	defer db.Close()

	version, err := querySingleInt(ctx, db, `SELECT version FROM version LIMIT 1`)
	if err != nil {
		return fmt.Errorf("read seeded version row: %w", err)
	}

	if version != buildinfo.ReferenceHydrusVersion {
		return fmt.Errorf(
			"seeded version = %d, want %d",
			version,
			buildinfo.ReferenceHydrusVersion,
		)
	}

	return nil
}

func expectedPrefixCount(prefixLength int) int {
	count := 1
	for range prefixLength {
		count *= 16
	}

	return count
}

func querySingleInt(ctx context.Context, db *sql.DB, query string, args ...any) (int, error) {
	var value int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&value); err != nil {
		return 0, err
	}

	return value, nil
}

func querySingleString(ctx context.Context, db *sql.DB, query string, args ...any) (string, error) {
	var value string
	if err := db.QueryRowContext(ctx, query, args...).Scan(&value); err != nil {
		return "", err
	}

	return value, nil
}
