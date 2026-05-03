// Package hydrusdb opens the Hydrus client SQLite bundle and exposes the first
// DB-backed surfaces used by hydrus-go.
package hydrusdb

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

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
	sqliteBusyTimeoutMS     = 5000
)

// Bundle is a Hydrus client DB bundle opened on a single dedicated SQLite
// connection so ATTACH aliases remain stable.
type Bundle struct {
	db   *sql.DB
	conn *sql.Conn

	mode      openMode
	writeGate chan struct{}
	paths     bundlePaths

	managedLayoutMu  sync.RWMutex
	managedLayout    clientfiles.Layout
	hasManagedLayout bool
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
	return openBundle(ctx, dir, modeReadWrite)
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

	db, err := sql.Open("sqlite", sqliteModeURI(paths.main, mode))
	if err != nil {
		return nil, fmt.Errorf("open main sqlite database: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open dedicated sqlite connection: %w", err)
	}

	if err := configureSQLiteConnection(ctx, conn); err != nil {
		_ = conn.Close()
		_ = db.Close()
		return nil, err
	}

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

	for _, attachment := range attachments {
		if err := attachDatabase(ctx, conn, attachment, mode); err != nil {
			_ = conn.Close()
			_ = db.Close()
			return nil, err
		}
	}

	if mode == modeReadOnly {
		if _, err := conn.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
			_ = conn.Close()
			_ = db.Close()
			return nil, fmt.Errorf("enable sqlite query_only mode: %w", err)
		}
	}

	writeGate := make(chan struct{}, 1)
	writeGate <- struct{}{}

	return &Bundle{
		db:        db,
		conn:      conn,
		mode:      mode,
		writeGate: writeGate,
		paths:     paths,
	}, nil
}

// Close releases the dedicated SQLite connection.
func (b *Bundle) Close() error {
	if b == nil {
		return nil
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

	if _, err := conn.ExecContext(
		ctx,
		fmt.Sprintf("PRAGMA busy_timeout = %d", sqliteBusyTimeoutMS),
	); err != nil {
		return fmt.Errorf("set sqlite busy_timeout pragma: %w", err)
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
