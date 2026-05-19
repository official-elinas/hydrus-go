package hydrusdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/official-elinas/hydrus-go/internal/core/clientsessions"
)

func (b *Bundle) ensureSessionsTable(ctx context.Context) error {
	_, err := b.conn.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS client_sessions (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			name             TEXT    NOT NULL DEFAULT '',
			query            TEXT    NOT NULL DEFAULT '',
			sort_mode        TEXT    NOT NULL DEFAULT 'import_newest',
			selected_file_id INTEGER NOT NULL DEFAULT 0,
			position         INTEGER NOT NULL DEFAULT 0
		)
	`)
	return err
}

func (b *Bundle) ListSessions(ctx context.Context) ([]clientsessions.Session, error) {
	if b == nil {
		return nil, errors.New("hydrus bundle is nil")
	}
	conn, err := b.acquireReadConn(ctx)
	if err != nil {
		return nil, err
	}
	defer b.releaseReadConn(conn)

	rows, err := conn.QueryContext(ctx,
		`SELECT id, name, query, sort_mode, selected_file_id, position
		 FROM client_sessions ORDER BY position ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []clientsessions.Session
	for rows.Next() {
		var s clientsessions.Session
		if err := rows.Scan(&s.ID, &s.Name, &s.Query, &s.SortMode, &s.SelectedFileID, &s.Position); err != nil {
			return nil, fmt.Errorf("scan session row: %w", err)
		}
		sessions = append(sessions, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate session rows: %w", err)
	}

	return sessions, nil
}

func (b *Bundle) CreateSession(ctx context.Context, req clientsessions.CreateRequest) (clientsessions.Session, error) {
	sortMode := req.SortMode
	if sortMode == "" {
		sortMode = "import_newest"
	}

	var created clientsessions.Session
	err := b.WithImmediateTx(ctx, func(tx *ImmediateTx) error {
		var maxPos sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT MAX(position) FROM client_sessions`).Scan(&maxPos); err != nil {
			return fmt.Errorf("get max position: %w", err)
		}
		pos := 0
		if maxPos.Valid {
			pos = int(maxPos.Int64) + 1
		}

		result, err := tx.ExecContext(ctx,
			`INSERT INTO client_sessions (name, query, sort_mode, selected_file_id, position)
			 VALUES (?, '', ?, 0, ?)`,
			req.Name, sortMode, pos)
		if err != nil {
			return fmt.Errorf("insert session: %w", err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("last insert id: %w", err)
		}
		created = clientsessions.Session{
			ID:       id,
			Name:     req.Name,
			SortMode: sortMode,
			Position: pos,
		}
		return nil
	})
	return created, err
}

func (b *Bundle) UpdateSession(ctx context.Context, id int64, req clientsessions.UpdateRequest) (clientsessions.Session, error) {
	var updated clientsessions.Session
	err := b.WithImmediateTx(ctx, func(tx *ImmediateTx) error {
		row := tx.QueryRowContext(ctx,
			`SELECT id, name, query, sort_mode, selected_file_id, position FROM client_sessions WHERE id = ?`, id)
		if err := row.Scan(&updated.ID, &updated.Name, &updated.Query, &updated.SortMode, &updated.SelectedFileID, &updated.Position); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return &clientsessions.NotFoundError{ID: id}
			}
			return fmt.Errorf("load session for update: %w", err)
		}

		if req.Name != nil {
			updated.Name = *req.Name
		}
		if req.Query != nil {
			updated.Query = *req.Query
		}
		if req.SortMode != nil {
			updated.SortMode = *req.SortMode
		}
		if req.SelectedFileID != nil {
			updated.SelectedFileID = *req.SelectedFileID
		}
		if req.Position != nil {
			updated.Position = *req.Position
		}

		_, err := tx.ExecContext(ctx,
			`UPDATE client_sessions SET name=?, query=?, sort_mode=?, selected_file_id=?, position=? WHERE id=?`,
			updated.Name, updated.Query, updated.SortMode, updated.SelectedFileID, updated.Position, id)
		return err
	})
	return updated, err
}

func (b *Bundle) DeleteSession(ctx context.Context, id int64) error {
	return b.WithImmediateTx(ctx, func(tx *ImmediateTx) error {
		result, err := tx.ExecContext(ctx, `DELETE FROM client_sessions WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("delete session: %w", err)
		}
		n, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("rows affected: %w", err)
		}
		if n == 0 {
			return &clientsessions.NotFoundError{ID: id}
		}
		return nil
	})
}

var _ clientsessions.Store = (*Bundle)(nil)
