package hydrusdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ImmediateTx wraps the dedicated bundle connection while a BEGIN IMMEDIATE
// transaction is active. Callers should only issue SQL through this type while
// inside WithImmediateTx.
type ImmediateTx struct {
	conn *sql.Conn
}

// ExecContext executes a statement inside the active immediate transaction.
func (tx *ImmediateTx) ExecContext(
	ctx context.Context,
	query string,
	args ...any,
) (sql.Result, error) {
	return tx.conn.ExecContext(ctx, query, args...)
}

// QueryContext runs a query inside the active immediate transaction.
func (tx *ImmediateTx) QueryContext(
	ctx context.Context,
	query string,
	args ...any,
) (*sql.Rows, error) {
	return tx.conn.QueryContext(ctx, query, args...)
}

// QueryRowContext runs a row query inside the active immediate transaction.
func (tx *ImmediateTx) QueryRowContext(
	ctx context.Context,
	query string,
	args ...any,
) *sql.Row {
	return tx.conn.QueryRowContext(ctx, query, args...)
}

// WithImmediateTx serializes Hydrus bundle writes behind a single BEGIN
// IMMEDIATE transaction runner. This is the only supported mutation path for a
// writable bundle.
func (b *Bundle) WithImmediateTx(
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

	if _, err := b.conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin immediate transaction: %w", err)
	}

	committed := false
	defer func() {
		rollbackCtx := context.WithoutCancel(ctx)

		if recovered := recover(); recovered != nil {
			_ = rollbackImmediateTx(rollbackCtx, b.conn)
			panic(recovered)
		}

		if committed {
			return
		}

		if rollbackErr := rollbackImmediateTx(rollbackCtx, b.conn); rollbackErr != nil {
			if err == nil {
				err = rollbackErr
				return
			}

			err = errors.Join(err, rollbackErr)
		}
	}()

	if err := fn(&ImmediateTx{conn: b.conn}); err != nil {
		return err
	}

	if _, err := b.conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit immediate transaction: %w", err)
	}

	committed = true
	return nil
}

func rollbackImmediateTx(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
		return fmt.Errorf("rollback immediate transaction: %w", err)
	}

	return nil
}

func (b *Bundle) acquireWriteGate(ctx context.Context) error {
	select {
	case <-b.writeGate:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for serialized write slot: %w", ctx.Err())
	}
}

func (b *Bundle) releaseWriteGate() {
	b.writeGate <- struct{}{}
}
