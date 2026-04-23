package hydrusdb

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	coretags "github.com/official-elinas/hydrus-go/internal/core/tags"
)

const (
	defaultTagSuggestionLimit = 20
	maxTagSuggestionLimit     = 100
)

// SuggestTags returns canonical Hydrus tag suggestions for one normalized prefix.
func (b *Bundle) SuggestTags(
	ctx context.Context,
	prefix string,
	limit int,
) ([]string, error) {
	if b == nil {
		return nil, fmt.Errorf("hydrus bundle is nil")
	}

	normalizedPrefix := coretags.Clean(prefix)
	if strings.TrimSpace(normalizedPrefix) == "" {
		return []string{}, nil
	}

	if limit <= 0 {
		limit = defaultTagSuggestionLimit
	}
	if limit > maxTagSuggestionLimit {
		limit = maxTagSuggestionLimit
	}

	schemaMode, err := b.lookupMasterTagSchemaMode(ctx)
	if err != nil {
		return nil, err
	}

	switch schemaMode {
	case masterTagSchemaEmpty:
		return []string{}, nil
	case masterTagSchemaLegacyFlat:
		return b.lookupFlatTagSuggestions(ctx, normalizedPrefix, limit)
	case masterTagSchemaSplit:
		return b.lookupSplitTagSuggestions(ctx, normalizedPrefix, limit)
	default:
		return []string{}, nil
	}
}

func (b *Bundle) lookupFlatTagSuggestions(
	ctx context.Context,
	prefix string,
	limit int,
) ([]string, error) {
	rows, err := b.conn.QueryContext(
		ctx,
		`SELECT tag
		FROM external_master.tags
		WHERE tag LIKE ?
		ORDER BY tag ASC
		LIMIT ?`,
		prefix+"%",
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query flat tag suggestions: %w", err)
	}
	defer rows.Close()

	suggestions := []string{}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, fmt.Errorf("scan flat tag suggestion: %w", err)
		}

		suggestions = append(suggestions, tag)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate flat tag suggestions: %w", err)
	}

	return suggestions, nil
}

func (b *Bundle) lookupSplitTagSuggestions(
	ctx context.Context,
	prefix string,
	limit int,
) ([]string, error) {
	containsNamespace := strings.Contains(prefix, ":")

	var (
		rows *sql.Rows
		err  error
	)

	if containsNamespace {
		namespace, subtagPrefix := coretags.Split(prefix)
		rows, err = b.conn.QueryContext(
			ctx,
			`SELECT DISTINCT n.namespace, s.subtag
			FROM external_master.tags t
			JOIN external_master.namespaces n USING (namespace_id)
			JOIN external_master.subtags s USING (subtag_id)
			WHERE n.namespace = ? AND s.subtag LIKE ?
			ORDER BY s.subtag ASC
			LIMIT ?`,
			namespace,
			subtagPrefix+"%",
			limit,
		)
	} else {
		rows, err = b.conn.QueryContext(
			ctx,
			`SELECT DISTINCT n.namespace, s.subtag
			FROM external_master.tags t
			JOIN external_master.namespaces n USING (namespace_id)
			JOIN external_master.subtags s USING (subtag_id)
			WHERE s.subtag LIKE ?
				OR (n.namespace != '' AND (n.namespace || ':' || s.subtag) LIKE ?)
			ORDER BY CASE WHEN n.namespace = '' THEN 0 ELSE 1 END ASC,
				n.namespace ASC,
				s.subtag ASC
			LIMIT ?`,
			prefix+"%",
			prefix+"%",
			limit,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("query split tag suggestions: %w", err)
	}
	defer rows.Close()

	suggestions := []string{}
	for rows.Next() {
		var (
			namespace string
			subtag    string
		)
		if err := rows.Scan(&namespace, &subtag); err != nil {
			return nil, fmt.Errorf("scan split tag suggestion: %w", err)
		}

		suggestions = append(suggestions, coretags.Combine(namespace, subtag))
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate split tag suggestions: %w", err)
	}

	return suggestions, nil
}
