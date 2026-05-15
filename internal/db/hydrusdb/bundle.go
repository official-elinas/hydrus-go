// Package hydrusdb opens the Hydrus client SQLite bundle and exposes the first
// DB-backed surfaces used by hydrus-go.
package hydrusdb

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/official-elinas/hydrus-go/internal/core/services"
	"github.com/official-elinas/hydrus-go/internal/storage/clientfiles"
	_ "modernc.org/sqlite"
)

type attachment struct {
	alias string
	path  string
}

type openMode string

const (
	modeReadOnly  openMode = "ro"
	modeReadWrite openMode = "rw"

	sqliteBusyTimeoutMS = 5000
	sqliteMmapSize8GiB  = 8 * 1024 * 1024 * 1024
	sqliteCacheSize256M = -262144

	// readPoolSize controls how many pre-ATTACHed connections the read-only
	// bundle pool holds. SQLite WAL mode allows N concurrent readers without
	// write-blocking each other; each connection keeps its own page cache so
	// keep this small to bound per-bundle memory pressure.
	readPoolSize = 4
)

// Bundle is a Hydrus client DB bundle. Read-only bundles open readPoolSize
// fully-ATTACHed connections in connPool so concurrent callers run in parallel
// instead of serializing on a single conn. Write bundles use a single conn
// guarded by writeGate.
type Bundle struct {
	db       *sql.DB
	conn     *sql.Conn
	connPool chan *sql.Conn

	mode      openMode
	writeGate chan struct{}
	paths     bundlePaths

	managedLayoutMu  sync.RWMutex
	managedLayout    clientfiles.Layout
	hasManagedLayout bool

	recentBrowseTableMu  sync.RWMutex
	recentBrowseTable    string
	hasRecentBrowseTable bool

	schemaTableNamesCache sync.Map

	serviceDefsOnce sync.Once
	serviceDefsVal  []serviceDefinition
	serviceDefsErr  error

	tagSchemaModeOnce sync.Once
	tagSchemaModeVal  masterTagSchemaMode
	tagSchemaModeErr  error
}

type bundlePaths struct {
	main        string
	master      string
	caches      string
	mappings    string
	temp        string
	hasTempFile bool
}

// Open opens a Hydrus client DB bundle read-only.
func Open(ctx context.Context, dir string) (*Bundle, error) {
	return openBundle(ctx, dir, modeReadOnly)
}

// OpenWritable opens a Hydrus client DB bundle read-write for internal daemon
// mutation workflows. Public HTTP surfaces should not use this until the write
// path is fully designed and documented.
func OpenWritable(ctx context.Context, dir string) (*Bundle, error) {
	b, err := openBundle(ctx, dir, modeReadWrite)
	if err != nil {
		return nil, err
	}
	go b.ensureMappingIndexes(ctx)
	return b, nil
}

func (b *Bundle) ensureMappingIndexes(ctx context.Context) {
	prefixes := []string{
		"current_mappings_",
		"deleted_mappings_",
		"pending_mappings_",
		"petitioned_mappings_",
	}

	rows, err := b.conn.QueryContext(ctx,
		`SELECT name FROM external_mappings.sqlite_master WHERE type='table'`)
	if err != nil {
		slog.Error("ensureMappingIndexes: list tables", "err", err)
		return
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			slog.Error("ensureMappingIndexes: scan table name", "err", err)
			return
		}
		for _, prefix := range prefixes {
			if strings.HasPrefix(name, prefix) {
				tables = append(tables, name)
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		slog.Error("ensureMappingIndexes: iterate tables", "err", err)
		return
	}

	for _, table := range tables {
		idxName := table + "_hash_id_idx"
		existing, err := b.conn.QueryContext(ctx,
			`SELECT 1 FROM external_mappings.sqlite_master WHERE type='index' AND name=?`, idxName)
		if err != nil {
			slog.Error("ensureMappingIndexes: check index", "index", idxName, "err", err)
			return
		}
		exists := existing.Next()
		_ = existing.Close()
		if exists {
			continue
		}

		slog.Info("ensureMappingIndexes: building hash_id index", "table", table)
		t := time.Now()
		query := fmt.Sprintf(
			`CREATE INDEX IF NOT EXISTS external_mappings.%s ON %s (hash_id)`,
			idxName, table,
		)
		if _, err := b.conn.ExecContext(ctx, query); err != nil {
			slog.Error("ensureMappingIndexes: create index", "index", idxName, "err", err)
			return
		}
		slog.Info("ensureMappingIndexes: index built", "table", table, "elapsed", time.Since(t))
	}
}

// MainDBPath returns the canonical path to client.db for this bundle.
func (b *Bundle) MainDBPath() string {
	if b == nil {
		return ""
	}

	return b.paths.main
}

func openBundle(ctx context.Context, dir string, mode openMode) (*Bundle, error) {
	paths, err := resolveBundlePaths(dir)
	if err != nil {
		return nil, err
	}

	poolSize := 1
	if mode == modeReadOnly {
		poolSize = readPoolSize
	}

	db, err := sql.Open("sqlite", sqliteModeURI(paths.main, mode))
	if err != nil {
		return nil, fmt.Errorf("open main sqlite database: %w", err)
	}

	db.SetMaxOpenConns(poolSize)
	db.SetMaxIdleConns(poolSize)

	attachments := []attachment{
		{alias: "external_master", path: paths.master},
		{alias: "external_caches", path: paths.caches},
		{alias: "external_mappings", path: paths.mappings},
	}

	if paths.hasTempFile {
		attachments = append(attachments, attachment{
			alias: "durable_temp",
			path:  paths.temp,
		})
	}

	conns := make([]*sql.Conn, 0, poolSize)
	for i := range poolSize {
		conn, err := db.Conn(ctx)
		if err != nil {
			for _, c := range conns {
				_ = c.Close()
			}
			_ = db.Close()
			return nil, fmt.Errorf("open sqlite connection %d: %w", i, err)
		}

		if err := configureSQLiteConnection(ctx, conn); err != nil {
			_ = conn.Close()
			for _, c := range conns {
				_ = c.Close()
			}
			_ = db.Close()
			return nil, err
		}

		for _, att := range attachments {
			if err := attachDatabase(ctx, conn, att, mode); err != nil {
				_ = conn.Close()
				for _, c := range conns {
					_ = c.Close()
				}
				_ = db.Close()
				return nil, err
			}
		}

		if mode == modeReadWrite {
			if err := configureSQLiteWriteConnection(ctx, conn, attachments); err != nil {
				_ = conn.Close()
				for _, c := range conns {
					_ = c.Close()
				}
				_ = db.Close()
				return nil, err
			}
		}

		if mode == modeReadOnly {
			if _, err := conn.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
				_ = conn.Close()
				for _, c := range conns {
					_ = c.Close()
				}
				_ = db.Close()
				return nil, fmt.Errorf("enable sqlite query_only mode: %w", err)
			}
		}

		conns = append(conns, conn)
	}

	writeGate := make(chan struct{}, 1)
	writeGate <- struct{}{}

	var connPool chan *sql.Conn
	if mode == modeReadOnly {
		connPool = make(chan *sql.Conn, poolSize)
		for _, c := range conns {
			connPool <- c
		}
	}

	return &Bundle{
		db:        db,
		conn:      conns[0],
		connPool:  connPool,
		mode:      mode,
		writeGate: writeGate,
		paths:     paths,
	}, nil
}

func (b *Bundle) acquireReadConn(ctx context.Context) (*sql.Conn, error) {
	if b.connPool == nil {
		return b.conn, nil
	}

	select {
	case conn := <-b.connPool:
		return conn, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for read connection: %w", ctx.Err())
	}
}

func (b *Bundle) releaseReadConn(conn *sql.Conn) {
	if b.connPool == nil {
		return
	}

	b.connPool <- conn
}

// Close releases all SQLite connections in the bundle.
func (b *Bundle) Close() error {
	if b == nil {
		return nil
	}

	if b.connPool != nil {
		var errs []error
		for range cap(b.connPool) {
			conn := <-b.connPool
			errs = append(errs, conn.Close())
		}
		errs = append(errs, b.db.Close())
		return errors.Join(errs...)
	}

	return errors.Join(b.conn.Close(), b.db.Close())
}

// List returns the Hydrus service discovery catalog.
func (b *Bundle) List(ctx context.Context) (services.Catalog, error) {
	types := services.DiscoveryTypes()
	args := make([]any, 0, len(types))
	for _, serviceType := range types {
		args = append(args, int(serviceType))
	}

	query := fmt.Sprintf(
		`SELECT lower(hex(service_key)), service_type, name, dictionary_string
		FROM main.services
		WHERE service_type IN (%s)
		ORDER BY service_id ASC`,
		placeholders(len(args)),
	)

	rows, err := b.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query services: %w", err)
	}
	defer rows.Close()

	catalog := services.Catalog{}
	for rows.Next() {
		service, err := scanCatalogService(rows)
		if err != nil {
			return nil, err
		}

		catalog = append(catalog, service)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate services: %w", err)
	}

	services.SortDiscoveryCatalog(catalog)

	return catalog, nil
}

// ByKey looks up a service by service key.
func (b *Bundle) ByKey(
	ctx context.Context,
	serviceKey string,
) (services.Service, bool, error) {
	normalized := strings.ToLower(strings.TrimSpace(serviceKey))
	keyBytes, err := hex.DecodeString(normalized)
	if err != nil {
		return services.Service{}, false, fmt.Errorf("decode service key: %w", err)
	}

	row := b.conn.QueryRowContext(
		ctx,
		`SELECT lower(hex(service_key)), service_type, name, dictionary_string
		FROM main.services
		WHERE service_key = ?`,
		keyBytes,
	)

	service, err := scanCatalogService(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return services.Service{}, false, nil
		}

		return services.Service{}, false, err
	}

	return service, true, nil
}

// ByName looks up a service by display name.
func (b *Bundle) ByName(
	ctx context.Context,
	name string,
) (services.Service, bool, error) {
	service, ok, err := b.lookupServiceByName(
		ctx,
		`SELECT lower(hex(service_key)), service_type, name, dictionary_string
		FROM main.services
		WHERE name = ?
		ORDER BY service_id ASC
		LIMIT 1`,
		name,
	)
	if err != nil || ok {
		return service, ok, err
	}

	return b.lookupCaseInsensitiveServiceByName(ctx, name)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanCatalogService(s scanner) (services.Service, error) {
	var (
		serviceKey       string
		serviceType      int
		name             string
		dictionaryString sql.NullString
	)

	if err := s.Scan(&serviceKey, &serviceType, &name, &dictionaryString); err != nil {
		return services.Service{}, fmt.Errorf("scan service catalog row: %w", err)
	}

	service := services.Service{
		Name:       name,
		ServiceKey: serviceKey,
		Type:       services.Type(serviceType),
		TypePretty: services.TypePretty(services.Type(serviceType)),
	}

	if dictionaryString.Valid {
		if err := applyServiceExtras(service.Type, dictionaryString.String, &service); err != nil {
			return services.Service{}, err
		}
	}

	return service, nil
}

func scanService(s scanner) (services.Service, error) {
	var (
		serviceKey  string
		serviceType int
		name        string
	)

	if err := s.Scan(&serviceKey, &serviceType, &name); err != nil {
		return services.Service{}, fmt.Errorf("scan service: %w", err)
	}

	return services.Service{
		Name:       name,
		ServiceKey: serviceKey,
		Type:       services.Type(serviceType),
		TypePretty: services.TypePretty(services.Type(serviceType)),
	}, nil
}

func (b *Bundle) lookupServiceByName(
	ctx context.Context,
	query string,
	name string,
) (services.Service, bool, error) {
	row := b.conn.QueryRowContext(ctx, query, name)

	service, err := scanCatalogService(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return services.Service{}, false, nil
		}

		return services.Service{}, false, err
	}

	return service, true, nil
}

func (b *Bundle) lookupCaseInsensitiveServiceByName(
	ctx context.Context,
	name string,
) (services.Service, bool, error) {
	// Scan in service_id order so the folded fallback stays deterministic and
	// matches Go's Unicode-aware EqualFold semantics rather than SQLite's
	// ASCII-oriented NOCASE collation.
	rows, err := b.conn.QueryContext(
		ctx,
		`SELECT lower(hex(service_key)), service_type, name, dictionary_string
		FROM main.services
		ORDER BY service_id ASC`,
	)
	if err != nil {
		return services.Service{}, false, fmt.Errorf("query services by name: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		service, err := scanCatalogService(rows)
		if err != nil {
			return services.Service{}, false, err
		}

		if strings.EqualFold(service.Name, name) {
			return service, true, nil
		}
	}

	if err := rows.Err(); err != nil {
		return services.Service{}, false, fmt.Errorf("iterate services by name: %w", err)
	}

	return services.Service{}, false, nil
}

func resolveBundlePaths(dir string) (bundlePaths, error) {
	resolvedDir, err := filepath.Abs(strings.TrimSpace(dir))
	if err != nil {
		return bundlePaths{}, fmt.Errorf("resolve DB directory: %w", err)
	}

	paths := bundlePaths{
		main:     filepath.Join(resolvedDir, "client.db"),
		master:   filepath.Join(resolvedDir, "client.master.db"),
		caches:   filepath.Join(resolvedDir, "client.caches.db"),
		mappings: filepath.Join(resolvedDir, "client.mappings.db"),
		temp:     filepath.Join(resolvedDir, "client.temp.db"),
	}

	for _, requiredPath := range []string{paths.main, paths.master, paths.caches, paths.mappings} {
		if err := requireFile(requiredPath); err != nil {
			return bundlePaths{}, err
		}
	}

	if _, err := os.Stat(paths.temp); err == nil {
		paths.hasTempFile = true
	}

	return paths, nil
}

func requireFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", filepath.Base(path), err)
	}

	if info.IsDir() {
		return fmt.Errorf("%s must be a file", filepath.Base(path))
	}

	return nil
}

func attachDatabase(
	ctx context.Context,
	conn *sql.Conn,
	attachment attachment,
	mode openMode,
) error {
	query := fmt.Sprintf("ATTACH DATABASE ? AS %s", attachment.alias)
	if _, err := conn.ExecContext(ctx, query, sqliteModeURI(attachment.path, mode)); err != nil {
		return fmt.Errorf("attach %s: %w", attachment.alias, err)
	}

	return nil
}

func configureSQLiteConnection(ctx context.Context, conn *sql.Conn) error {
	if conn == nil {
		return errors.New("sqlite connection is nil")
	}

	pragmas := []struct {
		name  string
		value int64
	}{
		{"busy_timeout", sqliteBusyTimeoutMS},
		{"mmap_size", sqliteMmapSize8GiB},
		{"cache_size", sqliteCacheSize256M},
	}

	for _, p := range pragmas {
		if _, err := conn.ExecContext(
			ctx,
			fmt.Sprintf("PRAGMA %s = %d", p.name, p.value),
		); err != nil {
			return fmt.Errorf("set sqlite %s pragma: %w", p.name, err)
		}
	}

	return nil
}

func configureSQLiteWriteConnection(ctx context.Context, conn *sql.Conn, attachments []attachment) error {
	aliases := []string{"main"}
	for _, attachment := range attachments {
		aliases = append(aliases, attachment.alias)
	}

	for _, alias := range aliases {
		var journalMode string
		if err := conn.QueryRowContext(
			ctx,
			fmt.Sprintf("PRAGMA %s.journal_mode = WAL", alias),
		).Scan(&journalMode); err != nil {
			return fmt.Errorf("set sqlite journal_mode WAL for %s: %w", alias, err)
		}
	}

	return nil
}

func sqliteModeURI(path string, mode openMode) string {
	uri := url.URL{Scheme: "file", Path: path}
	query := uri.Query()
	query.Set("mode", string(mode))
	uri.RawQuery = query.Encode()
	return uri.String()
}

func placeholders(count int) string {
	if count <= 0 {
		return ""
	}

	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func rowPlaceholders(count int) string {
	if count <= 0 {
		return ""
	}

	return strings.TrimSuffix(strings.Repeat("(?),", count), ",")
}
