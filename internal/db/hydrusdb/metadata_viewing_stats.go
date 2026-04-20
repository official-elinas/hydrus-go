package hydrusdb

import (
	"context"
	"database/sql"
	"fmt"
)

const (
	canvasMediaViewer = 0
	canvasPreview     = 1
	canvasClientAPI   = 4
)

type viewingStatRecord struct {
	canvasType            int
	lastViewedTimestampMS sql.NullInt64
	views                 int64
	viewtimeMS            int64
}

var apiViewingCanvasTypes = []struct {
	canvasType int
	pretty     string
}{
	{canvasType: canvasMediaViewer, pretty: "media viewer"},
	{canvasType: canvasPreview, pretty: "preview viewer"},
	{canvasType: canvasClientAPI, pretty: "client api viewer"},
}

func (b *Bundle) lookupFileViewingStatistics(
	ctx context.Context,
	fileIDs []int64,
) (map[int64][]map[string]any, error) {
	payloads := map[int64][]map[string]any{}
	if len(fileIDs) == 0 {
		return payloads, nil
	}

	uniqueFileIDs := dedupeInt64s(fileIDs)
	recordsByHashID := map[int64]map[int]viewingStatRecord{}

	mainTableNames, err := b.lookupMainTableNames(ctx)
	if err != nil {
		return nil, err
	}

	if _, ok := mainTableNames["file_viewing_stats"]; ok {
		args := int64Args(uniqueFileIDs)
		args = append(
			args,
			canvasMediaViewer,
			canvasPreview,
			canvasClientAPI,
		)

		query := fmt.Sprintf(
			`SELECT hash_id, canvas_type, last_viewed_timestamp_ms, views, viewtime_ms
			FROM main.file_viewing_stats
			WHERE hash_id IN (%s)
			AND canvas_type IN (?, ?, ?)`,
			placeholders(len(uniqueFileIDs)),
		)

		rows, err := b.conn.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("query file viewing stats: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var (
				hashID int64
				record viewingStatRecord
			)

			if err := rows.Scan(
				&hashID,
				&record.canvasType,
				&record.lastViewedTimestampMS,
				&record.views,
				&record.viewtimeMS,
			); err != nil {
				return nil, fmt.Errorf("scan file viewing stats row: %w", err)
			}

			if _, ok := recordsByHashID[hashID]; !ok {
				recordsByHashID[hashID] = map[int]viewingStatRecord{}
			}

			recordsByHashID[hashID][record.canvasType] = record
		}

		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate file viewing stats rows: %w", err)
		}
	}

	for _, hashID := range uniqueFileIDs {
		payloads[hashID] = buildFileViewingStatisticsPayload(recordsByHashID[hashID])
	}

	return payloads, nil
}

func buildFileViewingStatisticsPayload(
	recordsByCanvasType map[int]viewingStatRecord,
) []map[string]any {
	payload := make([]map[string]any, 0, len(apiViewingCanvasTypes))
	for _, canvas := range apiViewingCanvasTypes {
		views := int64(0)
		viewtime := 0.0
		var lastViewedTimestamp any

		if record, ok := recordsByCanvasType[canvas.canvasType]; ok {
			views = record.views
			viewtime = secondiseMSFloat(record.viewtimeMS)
			if record.lastViewedTimestampMS.Valid {
				lastViewedTimestamp = secondiseMSFloat(
					record.lastViewedTimestampMS.Int64,
				)
			}
		}

		payload = append(payload, map[string]any{
			"canvas_type":           canvas.canvasType,
			"canvas_type_pretty":    canvas.pretty,
			"views":                 views,
			"viewtime":              viewtime,
			"last_viewed_timestamp": lastViewedTimestamp,
		})
	}

	return payload
}

func secondiseMSFloat(timestampMS int64) float64 {
	return float64(timestampMS) / 1000.0
}
