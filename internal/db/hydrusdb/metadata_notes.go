package hydrusdb

import (
	"context"
	"fmt"
)

func (b *Bundle) lookupFileNotes(
	ctx context.Context,
	fileIDs []int64,
) (map[int64]map[string]string, error) {
	notesByHashID := map[int64]map[string]string{}
	if len(fileIDs) == 0 {
		return notesByHashID, nil
	}

	mainTableNames, err := b.lookupMainTableNames(ctx)
	if err != nil {
		return nil, err
	}

	if _, ok := mainTableNames["file_notes"]; !ok {
		return notesByHashID, nil
	}

	masterTableNames, err := b.lookupSchemaTableNames(ctx, "external_master")
	if err != nil {
		return nil, err
	}

	if _, ok := masterTableNames["labels"]; !ok {
		return notesByHashID, nil
	}

	if _, ok := masterTableNames["notes"]; !ok {
		return notesByHashID, nil
	}

	uniqueFileIDs := dedupeInt64s(fileIDs)
	query := fmt.Sprintf(
		`SELECT fn.hash_id, l.label, n.note
		FROM main.file_notes fn
		JOIN external_master.labels l ON fn.name_id = l.label_id
		JOIN external_master.notes n ON fn.note_id = n.note_id
		WHERE fn.hash_id IN (%s)
		ORDER BY fn.hash_id ASC, l.label ASC`,
		placeholders(len(uniqueFileIDs)),
	)

	rows, err := b.conn.QueryContext(ctx, query, int64Args(uniqueFileIDs)...)
	if err != nil {
		return nil, fmt.Errorf("query file notes: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			hashID int64
			label  string
			note   string
		)

		if err := rows.Scan(&hashID, &label, &note); err != nil {
			return nil, fmt.Errorf("scan file notes row: %w", err)
		}

		if _, ok := notesByHashID[hashID]; !ok {
			notesByHashID[hashID] = map[string]string{}
		}

		notesByHashID[hashID][label] = note
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate file notes rows: %w", err)
	}

	return notesByHashID, nil
}
