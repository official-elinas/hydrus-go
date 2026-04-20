package hydrusdb

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/official-elinas/hydrus-go/internal/core/filemetadata"
	coretags "github.com/official-elinas/hydrus-go/internal/core/tags"
)

func TestBundleEnsureTagID(t *testing.T) {
	t.Run("writable bundles reuse existing normalized tag IDs", func(t *testing.T) {
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

		tagID, err := bundle.EnsureTagID(context.Background(), "  CREATOR:ALICE  ")
		if err != nil {
			t.Fatalf("EnsureTagID() error = %v", err)
		}

		if tagID != 1 {
			t.Fatalf("tagID = %d, want 1", tagID)
		}

		repeatedTagID, err := bundle.EnsureTagID(context.Background(), "creator:alice")
		if err != nil {
			t.Fatalf("EnsureTagID(repeated) error = %v", err)
		}

		if repeatedTagID != tagID {
			t.Fatalf("repeatedTagID = %d, want %d", repeatedTagID, tagID)
		}

		masterDB := openSQLiteForTest(t, filepath.Join(dir, "client.master.db"))
		defer masterDB.Close()

		var tagCount int
		if err := masterDB.QueryRow(
			`SELECT COUNT(*)
			FROM tags t
			JOIN namespaces n USING (namespace_id)
			JOIN subtags s USING (subtag_id)
			WHERE n.namespace = ? AND s.subtag = ?`,
			"creator",
			"alice",
		).Scan(&tagCount); err != nil {
			t.Fatalf("QueryRow(tag count) error = %v", err)
		}

		if tagCount != 1 {
			t.Fatalf("tagCount = %d, want 1", tagCount)
		}
	})

	t.Run("writable bundles create anonymous tags", func(t *testing.T) {
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

		tagID, err := bundle.EnsureTagID(context.Background(), "  Loose   Tag  ")
		if err != nil {
			t.Fatalf("EnsureTagID() error = %v", err)
		}

		if tagID <= 17 {
			t.Fatalf("tagID = %d, want a newly allocated ID > 17", tagID)
		}

		masterDB := openSQLiteForTest(t, filepath.Join(dir, "client.master.db"))
		defer masterDB.Close()

		var (
			namespace string
			subtag    string
		)
		if err := masterDB.QueryRow(
			`SELECT n.namespace, s.subtag
			FROM tags t
			JOIN namespaces n USING (namespace_id)
			JOIN subtags s USING (subtag_id)
			WHERE t.tag_id = ?`,
			tagID,
		).Scan(&namespace, &subtag); err != nil {
			t.Fatalf("QueryRow(created tag) error = %v", err)
		}

		if namespace != "" {
			t.Fatalf("namespace = %q, want empty namespace", namespace)
		}

		if subtag != "loose tag" {
			t.Fatalf("subtag = %q, want %q", subtag, "loose tag")
		}

		repeatedTagID, err := bundle.EnsureTagID(context.Background(), "loose tag")
		if err != nil {
			t.Fatalf("EnsureTagID(repeated anonymous) error = %v", err)
		}

		if repeatedTagID != tagID {
			t.Fatalf("repeatedTagID = %d, want %d", repeatedTagID, tagID)
		}
	})

	t.Run("writable bundles reject legacy flat tag schemas", func(t *testing.T) {
		dir, _ := createTestBundle(t)
		convertMasterTagsToLegacyFlatSchema(t, dir)

		bundle, err := OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		_, err = bundle.EnsureTagID(context.Background(), "creator:alice")
		if err == nil {
			t.Fatal("EnsureTagID() error = nil, want legacy schema failure")
		}

		if !strings.Contains(err.Error(), "legacy flat external_master.tags schema") {
			t.Fatalf("EnsureTagID() error = %v, want legacy schema guidance", err)
		}
	})

	t.Run("invalid tags are rejected", func(t *testing.T) {
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

		_, err = bundle.EnsureTagID(context.Background(), "  :  ")
		if !errors.Is(err, coretags.ErrEmptyTag) {
			t.Fatalf("EnsureTagID() error = %v, want ErrEmptyTag", err)
		}
	})

	t.Run("writable bundles serialize concurrent ensures for the same tag", func(t *testing.T) {
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

		inputs := []string{
			"creator:parallel",
			"  CREATOR:PARALLEL  ",
			"creator:parallel",
			"Creator:Parallel",
		}

		start := make(chan struct{})
		ids := make(chan int64, len(inputs))
		errCh := make(chan error, len(inputs))

		var wg sync.WaitGroup
		for _, input := range inputs {
			input := input
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start

				id, err := bundle.EnsureTagID(context.Background(), input)
				if err != nil {
					errCh <- err
					return
				}

				ids <- id
			}()
		}

		close(start)
		wg.Wait()
		close(errCh)
		close(ids)

		for err := range errCh {
			t.Fatalf("EnsureTagID(concurrent) error = %v", err)
		}

		var firstID int64
		for id := range ids {
			if firstID == 0 {
				firstID = id
				continue
			}

			if id != firstID {
				t.Fatalf("concurrent id = %d, want %d", id, firstID)
			}
		}

		if firstID <= 17 {
			t.Fatalf("firstID = %d, want a newly allocated ID > 17", firstID)
		}

		masterDB := openSQLiteForTest(t, filepath.Join(dir, "client.master.db"))
		defer masterDB.Close()

		var tagCount int
		if err := masterDB.QueryRow(
			`SELECT COUNT(*)
			FROM tags t
			JOIN namespaces n USING (namespace_id)
			JOIN subtags s USING (subtag_id)
			WHERE n.namespace = ? AND s.subtag = ?`,
			"creator",
			"parallel",
		).Scan(&tagCount); err != nil {
			t.Fatalf("QueryRow(concurrent tag count) error = %v", err)
		}

		if tagCount != 1 {
			t.Fatalf("tagCount = %d, want 1", tagCount)
		}
	})
}

func TestBundleMetadata_AnonymousAndLegacyTagSchemas(t *testing.T) {
	t.Run("split master schema round-trips anonymous tags through metadata", func(t *testing.T) {
		dir, fixture := createTestBundle(t)

		writeBundle, err := OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}

		anonymousTagID, err := writeBundle.EnsureTagID(context.Background(), "loose tag")
		if err != nil {
			t.Fatalf("EnsureTagID(loose tag) error = %v", err)
		}

		colonTagID, err := writeBundle.EnsureTagID(context.Background(), "::d")
		if err != nil {
			t.Fatalf("EnsureTagID(::d) error = %v", err)
		}

		if err := writeBundle.Close(); err != nil {
			t.Fatalf("writeBundle.Close() error = %v", err)
		}

		mappingsDB := openSQLiteForTest(t, filepath.Join(dir, "client.mappings.db"))
		mustExec(
			t,
			mappingsDB,
			`INSERT INTO current_mappings_1 (tag_id, hash_id) VALUES (?, ?), (?, ?);`,
			anonymousTagID, 1,
			colonTagID, 1,
		)
		if err := mappingsDB.Close(); err != nil {
			t.Fatalf("mappingsDB.Close() error = %v", err)
		}

		bundle, err := Open(context.Background(), dir)
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		rows, err := bundle.GetMetadata(context.Background(), filemetadata.Request{
			Hashes: []string{fixture.hash1Hex},
		})
		if err != nil {
			t.Fatalf("GetMetadata() error = %v", err)
		}

		tagsByService, ok := rows[0]["tags"].(map[string]map[string]any)
		if !ok {
			t.Fatalf("rows[0][tags] type = %T, want map[string]map[string]any", rows[0]["tags"])
		}

		localTagService, ok := tagsByService[fixture.localTagServiceKeyHex]
		if !ok {
			t.Fatalf("rows[0][tags] missing local tag service %q", fixture.localTagServiceKeyHex)
		}

		localStorageTags, ok := localTagService["storage_tags"].(map[string][]string)
		if !ok {
			t.Fatalf("rows[0][tags][local][storage_tags] type = %T, want map[string][]string", localTagService["storage_tags"])
		}

		if got := localStorageTags["0"]; !slices.Equal(got, []string{"::d", "creator:alice", "loose tag", "series:zeta"}) {
			t.Fatalf("rows[0][tags][local][storage_tags][0] = %v, want [::d creator:alice loose tag series:zeta]", got)
		}
	})

	t.Run("legacy flat tag schemas still resolve metadata payloads", func(t *testing.T) {
		dir, fixture := createTestBundle(t)
		convertMasterTagsToLegacyFlatSchema(t, dir)

		bundle, err := Open(context.Background(), dir)
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		rows, err := bundle.GetMetadata(context.Background(), filemetadata.Request{
			Hashes:                       []string{fixture.hash1Hex},
			IncludeLegacyServiceKeysTags: true,
		})
		if err != nil {
			t.Fatalf("GetMetadata() error = %v", err)
		}

		tagsByService, ok := rows[0]["tags"].(map[string]map[string]any)
		if !ok {
			t.Fatalf("rows[0][tags] type = %T, want map[string]map[string]any", rows[0]["tags"])
		}

		localTagService, ok := tagsByService[fixture.localTagServiceKeyHex]
		if !ok {
			t.Fatalf("rows[0][tags] missing local tag service %q", fixture.localTagServiceKeyHex)
		}

		localStorageTags, ok := localTagService["storage_tags"].(map[string][]string)
		if !ok {
			t.Fatalf("rows[0][tags][local][storage_tags] type = %T, want map[string][]string", localTagService["storage_tags"])
		}

		if got := localStorageTags["0"]; !slices.Equal(got, []string{"creator:alice", "series:zeta"}) {
			t.Fatalf("rows[0][tags][local][storage_tags][0] = %v, want [creator:alice series:zeta]", got)
		}

		storageByService, ok := rows[0]["service_keys_to_statuses_to_tags"].(map[string]map[string][]string)
		if !ok {
			t.Fatalf("rows[0][service_keys_to_statuses_to_tags] type = %T, want map[string]map[string][]string", rows[0]["service_keys_to_statuses_to_tags"])
		}

		if got := storageByService[fixture.combinedTagServiceKeyHex]["0"]; !slices.Equal(got, []string{"creator:alice", "series:zeta", "storage:downloader-current"}) {
			t.Fatalf("rows[0][service_keys_to_statuses_to_tags][combined][0] = %v, want [creator:alice series:zeta storage:downloader-current]", got)
		}
	})
}

func convertMasterTagsToLegacyFlatSchema(t *testing.T, dir string) {
	t.Helper()

	masterDB := openSQLiteForTest(t, filepath.Join(dir, "client.master.db"))
	defer masterDB.Close()

	mustExec(t, masterDB, `ALTER TABLE tags RENAME TO tags_split_backup;`)
	mustExec(t, masterDB, `CREATE TABLE tags (tag_id INTEGER PRIMARY KEY, tag TEXT UNIQUE);`)
	mustExec(
		t,
		masterDB,
		`INSERT INTO tags (tag_id, tag)
		SELECT t.tag_id,
			CASE
				WHEN n.namespace = '' THEN
					CASE
						WHEN instr(s.subtag, ':') > 0 THEN ':' || s.subtag
						ELSE s.subtag
					END
				ELSE n.namespace || ':' || s.subtag
			END
		FROM tags_split_backup t
		JOIN namespaces n USING (namespace_id)
		JOIN subtags s USING (subtag_id);`,
	)
	mustExec(t, masterDB, `DROP TABLE tags_split_backup;`)
	mustExec(t, masterDB, `DROP TABLE namespaces;`)
	mustExec(t, masterDB, `DROP TABLE subtags;`)
}
