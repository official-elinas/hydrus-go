package hydrusdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	coretags "github.com/official-elinas/hydrus-go/internal/core/tags"
)

const nullNamespaceID int64 = 1

type masterTagSchemaMode int

const (
	masterTagSchemaEmpty masterTagSchemaMode = iota
	masterTagSchemaLegacyFlat
	masterTagSchemaSplit
)

// EnsureTagID normalizes a Hydrus tag string and guarantees a stable master
// tag ID on writable bundles.
func (b *Bundle) EnsureTagID(ctx context.Context, tag string) (int64, error) {
	cleanTag := coretags.Clean(tag)
	if err := coretags.CheckNotEmpty(cleanTag); err != nil {
		return 0, fmt.Errorf("validate tag %q: %w", tag, err)
	}

	namespace, subtag := coretags.Split(cleanTag)

	var tagID int64
	err := b.withImmediateMasterTx(ctx, func(tx *ImmediateTx) error {
		tableNames, err := lookupImmediateMainTableNames(ctx, tx)
		if err != nil {
			return err
		}

		tagColumns, err := lookupTagColumns(ctx, tx, `PRAGMA table_info(tags)`, tableNames)
		if err != nil {
			return err
		}

		schemaMode, err := masterTagSchemaModeFromTableNames(tableNames, tagColumns)
		if err != nil {
			return err
		}

		if schemaMode == masterTagSchemaLegacyFlat {
			return errors.New(
				"legacy flat external_master.tags schema is read-compatible only; tag writes require the split namespaces/subtags/tags schema",
			)
		}

		if err := ensureMasterTagSchema(ctx, tx); err != nil {
			return err
		}

		namespaceID, err := ensureNamespaceID(ctx, tx, namespace)
		if err != nil {
			return err
		}

		subtagID, err := ensureSubtagID(ctx, tx, subtag)
		if err != nil {
			return err
		}

		tagID, err = ensureMasterTagID(ctx, tx, namespaceID, subtagID)
		return err
	})
	if err != nil {
		return 0, err
	}

	return tagID, nil
}

func (b *Bundle) withImmediateMasterTx(
	ctx context.Context,
	fn func(*ImmediateTx) error,
) (err error) {
	if b == nil {
		return errors.New("hydrus bundle is nil")
	}

	if b.mode != modeReadWrite {
		return errors.New("hydrus bundle is read-only")
	}

	if err := b.acquireWriteGate(ctx); err != nil {
		return err
	}
	defer b.releaseWriteGate()

	db, err := sql.Open("sqlite", sqliteModeURI(b.paths.master, modeReadWrite))
	if err != nil {
		return fmt.Errorf("open master sqlite database: %w", err)
	}
	defer db.Close()

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open dedicated master sqlite connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin immediate master transaction: %w", err)
	}

	committed := false
	defer func() {
		rollbackCtx := context.WithoutCancel(ctx)

		if recovered := recover(); recovered != nil {
			_ = rollbackImmediateTx(rollbackCtx, conn)
			panic(recovered)
		}

		if committed {
			return
		}

		if rollbackErr := rollbackImmediateTx(rollbackCtx, conn); rollbackErr != nil {
			if err == nil {
				err = rollbackErr
				return
			}

			err = errors.Join(err, rollbackErr)
		}
	}()

	if err := fn(&ImmediateTx{conn: conn}); err != nil {
		return err
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit immediate master transaction: %w", err)
	}

	committed = true
	return nil
}

func lookupImmediateMainTableNames(
	ctx context.Context,
	tx *ImmediateTx,
) (map[string]struct{}, error) {
	rows, err := tx.QueryContext(
		ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table'`,
	)
	if err != nil {
		return nil, fmt.Errorf("query sqlite table names for master transaction: %w", err)
	}
	defer rows.Close()

	tableNames := map[string]struct{}{}
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return nil, fmt.Errorf("scan sqlite table name for master transaction: %w", err)
		}

		tableNames[tableName] = struct{}{}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sqlite table names for master transaction: %w", err)
	}

	return tableNames, nil
}

type queryContextQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func lookupTagColumns(
	ctx context.Context,
	q queryContextQuerier,
	pragma string,
	tableNames map[string]struct{},
) (map[string]struct{}, error) {
	if _, ok := tableNames["tags"]; !ok {
		return map[string]struct{}{}, nil
	}

	rows, err := q.QueryContext(ctx, pragma)
	if err != nil {
		return nil, fmt.Errorf("query tag table columns: %w", err)
	}
	defer rows.Close()

	tagColumns := map[string]struct{}{}
	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)

		if err := rows.Scan(
			&cid,
			&name,
			&columnType,
			&notNull,
			&defaultVal,
			&pk,
		); err != nil {
			return nil, fmt.Errorf("scan tag table column: %w", err)
		}

		tagColumns[name] = struct{}{}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tag table columns: %w", err)
	}

	return tagColumns, nil
}

func hasColumn(columns map[string]struct{}, name string) bool {
	_, ok := columns[name]
	return ok
}

func (b *Bundle) lookupMasterTagSchemaMode(
	ctx context.Context,
) (masterTagSchemaMode, error) {
	tableNames, err := b.lookupSchemaTableNames(ctx, "external_master")
	if err != nil {
		return masterTagSchemaEmpty, err
	}

	tagColumns, err := lookupTagColumns(ctx, b.conn, `PRAGMA external_master.table_info(tags)`, tableNames)
	if err != nil {
		return masterTagSchemaEmpty, err
	}

	return masterTagSchemaModeFromTableNames(tableNames, tagColumns)
}

func masterTagSchemaModeFromTableNames(
	tableNames map[string]struct{},
	tagColumns map[string]struct{},
) (masterTagSchemaMode, error) {
	_, hasTags := tableNames["tags"]
	_, hasNamespaces := tableNames["namespaces"]
	_, hasSubtags := tableNames["subtags"]
	hasFlatTagColumn := hasColumn(tagColumns, "tag")
	hasSplitColumns := hasColumn(tagColumns, "namespace_id") && hasColumn(tagColumns, "subtag_id")

	switch {
	case !hasTags && !hasNamespaces && !hasSubtags:
		return masterTagSchemaEmpty, nil
	case hasTags && hasNamespaces && hasSubtags && hasSplitColumns && !hasFlatTagColumn:
		return masterTagSchemaSplit, nil
	case hasTags && !hasNamespaces && !hasSubtags && hasFlatTagColumn && !hasSplitColumns:
		return masterTagSchemaLegacyFlat, nil
	default:
		return masterTagSchemaEmpty, errors.New(
			"external_master tag schema is incompatible; expected flat tags(tag) or split namespaces/subtags/tags(namespace_id, subtag_id)",
		)
	}
}

func ensureMasterTagSchema(ctx context.Context, tx *ImmediateTx) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS namespaces (
			namespace_id INTEGER PRIMARY KEY,
			namespace TEXT UNIQUE
		);`,
		`CREATE TABLE IF NOT EXISTS subtags (
			subtag_id INTEGER PRIMARY KEY,
			subtag TEXT UNIQUE
		);`,
		`CREATE TABLE IF NOT EXISTS tags (
			tag_id INTEGER PRIMARY KEY,
			namespace_id INTEGER,
			subtag_id INTEGER
		);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS tags_namespace_subtag_idx
		ON tags (namespace_id, subtag_id);`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("ensure master tag schema: %w", err)
		}
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT OR IGNORE INTO namespaces (namespace_id, namespace)
		VALUES (?, ?)`,
		nullNamespaceID,
		"",
	); err != nil {
		return fmt.Errorf("seed null namespace row: %w", err)
	}

	return nil
}

func ensureNamespaceID(
	ctx context.Context,
	tx *ImmediateTx,
	namespace string,
) (int64, error) {
	namespaceID, exists, err := lookupNamespaceID(ctx, tx, namespace)
	if err != nil {
		return 0, err
	}

	if exists {
		return namespaceID, nil
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT OR IGNORE INTO namespaces (namespace) VALUES (?)`,
		namespace,
	); err != nil {
		return 0, fmt.Errorf("insert namespaces row: %w", err)
	}

	namespaceID, exists, err = lookupNamespaceID(ctx, tx, namespace)
	if err != nil {
		return 0, err
	}

	if !exists {
		return 0, errors.New("inserted namespace row was not readable inside transaction")
	}

	return namespaceID, nil
}

func ensureSubtagID(
	ctx context.Context,
	tx *ImmediateTx,
	subtag string,
) (int64, error) {
	subtagID, exists, err := lookupSubtagID(ctx, tx, subtag)
	if err != nil {
		return 0, err
	}

	if exists {
		return subtagID, nil
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT OR IGNORE INTO subtags (subtag) VALUES (?)`,
		subtag,
	); err != nil {
		return 0, fmt.Errorf("insert subtags row: %w", err)
	}

	subtagID, exists, err = lookupSubtagID(ctx, tx, subtag)
	if err != nil {
		return 0, err
	}

	if !exists {
		return 0, errors.New("inserted subtag row was not readable inside transaction")
	}

	return subtagID, nil
}

func ensureMasterTagID(
	ctx context.Context,
	tx *ImmediateTx,
	namespaceID int64,
	subtagID int64,
) (int64, error) {
	tagID, exists, err := lookupTagIDByParts(ctx, tx, namespaceID, subtagID)
	if err != nil {
		return 0, err
	}

	if exists {
		return tagID, nil
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT OR IGNORE INTO tags (namespace_id, subtag_id)
		VALUES (?, ?)`,
		namespaceID,
		subtagID,
	); err != nil {
		return 0, fmt.Errorf("insert tags row: %w", err)
	}

	tagID, exists, err = lookupTagIDByParts(ctx, tx, namespaceID, subtagID)
	if err != nil {
		return 0, err
	}

	if !exists {
		return 0, errors.New("inserted tag row was not readable inside transaction")
	}

	return tagID, nil
}

func lookupNamespaceID(
	ctx context.Context,
	q rowQuerier,
	namespace string,
) (int64, bool, error) {
	row := q.QueryRowContext(
		ctx,
		`SELECT namespace_id
		FROM namespaces
		WHERE namespace = ?`,
		namespace,
	)

	var namespaceID int64
	if err := row.Scan(&namespaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}

		return 0, false, fmt.Errorf("query namespace row: %w", err)
	}

	return namespaceID, true, nil
}

func lookupSubtagID(
	ctx context.Context,
	q rowQuerier,
	subtag string,
) (int64, bool, error) {
	row := q.QueryRowContext(
		ctx,
		`SELECT subtag_id
		FROM subtags
		WHERE subtag = ?`,
		subtag,
	)

	var subtagID int64
	if err := row.Scan(&subtagID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}

		return 0, false, fmt.Errorf("query subtag row: %w", err)
	}

	return subtagID, true, nil
}

func lookupTagIDByParts(
	ctx context.Context,
	q rowQuerier,
	namespaceID int64,
	subtagID int64,
) (int64, bool, error) {
	row := q.QueryRowContext(
		ctx,
		`SELECT tag_id
		FROM tags
		WHERE namespace_id = ? AND subtag_id = ?`,
		namespaceID,
		subtagID,
	)

	var tagID int64
	if err := row.Scan(&tagID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}

		return 0, false, fmt.Errorf("query tag row: %w", err)
	}

	return tagID, true, nil
}
