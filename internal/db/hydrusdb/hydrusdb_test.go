package hydrusdb

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/official-elinas/hydrus-go/internal/core/filemetadata"
	"github.com/official-elinas/hydrus-go/internal/core/services"
	"github.com/official-elinas/hydrus-go/internal/storage/clientfiles"
)

func TestBundleServices(t *testing.T) {
	dir, fixture := createTestBundle(t)

	bundle, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() {
		if err := bundle.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}()

	catalog, err := bundle.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if len(catalog) != 10 {
		t.Fatalf("len(catalog) = %d, want 10", len(catalog))
	}

	if catalog[0].Name != "my tags" {
		t.Fatalf("catalog[0].Name = %q, want my tags", catalog[0].Name)
	}

	if catalog[1].Name != "downloader tags" {
		t.Fatalf("catalog[1].Name = %q, want downloader tags", catalog[1].Name)
	}

	if _, ok := catalog.ByName("all known tags"); !ok {
		t.Fatal("all known tags service missing from discovery catalog")
	}

	if _, ok := catalog.ByName("client api"); ok {
		t.Fatal("client api service unexpectedly returned from discovery list")
	}

	if _, ok := catalog.ByName("my ipfs"); ok {
		t.Fatal("ipfs service unexpectedly returned from discovery list")
	}

	service, ok, err := bundle.ByKey(
		context.Background(),
		hex.EncodeToString(fixture.clientAPIServiceKey),
	)
	if err != nil {
		t.Fatalf("ByKey() error = %v", err)
	}

	if !ok {
		t.Fatal("ByKey() ok = false, want true")
	}

	if service.Type != services.TypeClientAPIService {
		t.Fatalf(
			"service.Type = %d, want %d",
			service.Type,
			services.TypeClientAPIService,
		)
	}

	lookupTests := []struct {
		name     string
		query    string
		wantName string
		wantType services.Type
	}{
		{
			name:     "exact hidden service name lookup succeeds",
			query:    "client api",
			wantName: "client api",
			wantType: services.TypeClientAPIService,
		},
		{
			name:     "case insensitive hidden service name lookup succeeds",
			query:    "CLIENT API",
			wantName: "client api",
			wantType: services.TypeClientAPIService,
		},
		{
			name:     "case insensitive visible service name lookup succeeds",
			query:    "MY STARS",
			wantName: "my stars",
			wantType: services.TypeLocalRatingNumerical,
		},
	}

	for _, tt := range lookupTests {
		t.Run(tt.name, func(t *testing.T) {
			service, ok, err := bundle.ByName(context.Background(), tt.query)
			if err != nil {
				t.Fatalf("ByName() error = %v", err)
			}

			if !ok {
				t.Fatal("ByName() ok = false, want true")
			}

			if service.Name != tt.wantName {
				t.Fatalf("service.Name = %q, want %q", service.Name, tt.wantName)
			}

			if service.Type != tt.wantType {
				t.Fatalf("service.Type = %d, want %d", service.Type, tt.wantType)
			}
		})
	}

	ratingService, ok := catalog.ByName("my stars")
	if !ok {
		t.Fatal("rating service missing from discovery catalog")
	}

	if ratingService.StarShape != "circle" {
		t.Fatalf("ratingService.StarShape = %q, want circle", ratingService.StarShape)
	}

	if ratingService.AllowsZero == nil || !*ratingService.AllowsZero {
		t.Fatal("ratingService.AllowsZero missing or false, want true")
	}

	if ratingService.MaxStars == nil || *ratingService.MaxStars != 7 {
		t.Fatalf("ratingService.MaxStars = %v, want 7", ratingService.MaxStars)
	}

	legacy := catalog.LegacyMap()[ratingService.ServiceKey]
	if legacy.Colours["like"].Pen != "#010203" {
		t.Fatalf(
			"legacy like pen = %q, want #010203",
			legacy.Colours["like"].Pen,
		)
	}

	favourites, ok := catalog.ByName("favourites")
	if !ok {
		t.Fatal("favourites service missing from discovery catalog")
	}

	if favourites.StarShape != "fat star" {
		t.Fatalf("favourites.StarShape = %q, want fat star", favourites.StarShape)
	}

	if favourites.ShowInThumbnail == nil || *favourites.ShowInThumbnail {
		t.Fatal("favourites.ShowInThumbnail missing or true, want explicit false")
	}

	if favourites.ShowInThumbnailEvenWhenNull == nil || *favourites.ShowInThumbnailEvenWhenNull {
		t.Fatal("favourites.ShowInThumbnailEvenWhenNull missing or true, want explicit false")
	}

	if favourites.Colours["like"].Brush != "#F0F041" {
		t.Fatalf(
			"favourites like brush = %q, want #F0F041",
			favourites.Colours["like"].Brush,
		)
	}

	if favourites.Colours["dislike"].Brush != "#C85078" {
		t.Fatalf(
			"favourites dislike brush = %q, want #C85078",
			favourites.Colours["dislike"].Brush,
		)
	}

	if favourites.Colours["null"].Brush != "#BFBFBF" {
		t.Fatalf(
			"favourites null brush = %q, want #BFBFBF",
			favourites.Colours["null"].Brush,
		)
	}

	if favourites.Colours["mixed"].Brush != "#5F5F5F" {
		t.Fatalf(
			"favourites mixed brush = %q, want #5F5F5F",
			favourites.Colours["mixed"].Brush,
		)
	}
}

func TestBundleByName_PrefersExactMatchBeforeCaseInsensitiveFallback(t *testing.T) {
	dir, fixture := createTestBundle(t)
	mainDB, err := sql.Open("sqlite", filepath.Join(dir, "client.db"))
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer mainDB.Close()

	exactCaseKey := []byte("client-api-exact")
	mustExec(
		t,
		mainDB,
		`INSERT INTO services (service_id, service_key, service_type, name, dictionary_string) VALUES (?, ?, ?, ?, ?);`,
		13,
		exactCaseKey,
		int(services.TypeClientAPIService),
		"Client API",
		"{}",
	)

	bundle, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() {
		if err := bundle.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}()

	exactMatch, ok, err := bundle.ByName(context.Background(), "Client API")
	if err != nil {
		t.Fatalf("ByName(exact) error = %v", err)
	}

	if !ok {
		t.Fatal("ByName(exact) ok = false, want true")
	}

	if exactMatch.ServiceKey != hex.EncodeToString(exactCaseKey) {
		t.Fatalf("exactMatch.ServiceKey = %q, want %q", exactMatch.ServiceKey, hex.EncodeToString(exactCaseKey))
	}

	foldedMatch, ok, err := bundle.ByName(context.Background(), "CLIENT API")
	if err != nil {
		t.Fatalf("ByName(folded) error = %v", err)
	}

	if !ok {
		t.Fatal("ByName(folded) ok = false, want true")
	}

	if foldedMatch.ServiceKey != hex.EncodeToString(fixture.clientAPIServiceKey) {
		t.Fatalf(
			"foldedMatch.ServiceKey = %q, want %q",
			foldedMatch.ServiceKey,
			hex.EncodeToString(fixture.clientAPIServiceKey),
		)
	}
}

func TestBundleMetadata(t *testing.T) {
	dir, fixture := createTestBundle(t)

	bundle, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() {
		if err := bundle.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}()

	t.Run("identifier mode preserves order and includes unknown hashes", func(t *testing.T) {
		rows, err := bundle.GetMetadata(context.Background(), filemetadata.Request{
			Hashes:                []string{fixture.hash2Hex, fixture.unknownHashHex, fixture.hash1Hex},
			OnlyReturnIdentifiers: true,
		})
		if err != nil {
			t.Fatalf("GetMetadata() error = %v", err)
		}

		if got := rows[0]["file_id"]; got != int64(2) {
			t.Fatalf("rows[0][file_id] = %v, want 2", got)
		}

		if got := rows[1]["file_id"]; got != nil {
			t.Fatalf("rows[1][file_id] = %v, want nil", got)
		}

		if got := rows[2]["file_id"]; got != int64(1) {
			t.Fatalf("rows[2][file_id] = %v, want 1", got)
		}
	})

	t.Run("basic mode reports forced filetypes and missing rows", func(t *testing.T) {
		rows, err := bundle.GetMetadata(context.Background(), filemetadata.Request{
			Hashes:                     []string{fixture.hash1Hex, fixture.hash2Hex, fixture.hash3Hex, fixture.unknownHashHex},
			OnlyReturnBasicInformation: true,
			IncludeBlurhash:            true,
		})
		if err != nil {
			t.Fatalf("GetMetadata() error = %v", err)
		}

		if got := rows[0]["blurhash"]; got != fixture.hash1Blurhash {
			t.Fatalf("rows[0][blurhash] = %v, want %q", got, fixture.hash1Blurhash)
		}

		if got := rows[1]["mime"]; got != "image/jpeg" {
			t.Fatalf("rows[1][mime] = %v, want image/jpeg", got)
		}

		if got := rows[1]["original_mime"]; got != "image/png" {
			t.Fatalf("rows[1][original_mime] = %v, want image/png", got)
		}

		if got := rows[1]["filetype_forced"]; got != true {
			t.Fatalf("rows[1][filetype_forced] = %v, want true", got)
		}

		if got := rows[2]["file_id"]; got != nil {
			t.Fatalf("rows[2][file_id] = %v, want nil", got)
		}

		if got := rows[3]["file_id"]; got != nil {
			t.Fatalf("rows[3][file_id] = %v, want nil", got)
		}
	})

	t.Run("full mode returns metadata including tag payloads", func(t *testing.T) {
		rows, err := bundle.GetMetadata(context.Background(), filemetadata.Request{
			Hashes: []string{fixture.hash1Hex, fixture.hash2Hex, fixture.unknownHashHex},
		})
		if err != nil {
			t.Fatalf("GetMetadata() error = %v", err)
		}

		row1 := rows[0]
		if got := row1["blurhash"]; got != fixture.hash1Blurhash {
			t.Fatalf("rows[0][blurhash] = %v, want %q", got, fixture.hash1Blurhash)
		}

		if got := row1["pixel_hash"]; got != fixture.hash1PixelHashHex {
			t.Fatalf("rows[0][pixel_hash] = %v, want %q", got, fixture.hash1PixelHashHex)
		}

		if got := row1["time_modified"]; got != int64(20) {
			t.Fatalf("rows[0][time_modified] = %v, want 20", got)
		}

		if got := row1["time_archived"]; got != int64(500) {
			t.Fatalf("rows[0][time_archived] = %v, want 500", got)
		}

		if got := row1["is_local"]; got != true {
			t.Fatalf("rows[0][is_local] = %v, want true", got)
		}

		if got := row1["is_trashed"]; got != false {
			t.Fatalf("rows[0][is_trashed] = %v, want false", got)
		}

		if got := row1["is_deleted"]; got != false {
			t.Fatalf("rows[0][is_deleted] = %v, want false", got)
		}

		if got := row1["has_transparency"]; got != true {
			t.Fatalf("rows[0][has_transparency] = %v, want true", got)
		}

		if got := row1["has_exif"]; got != true {
			t.Fatalf("rows[0][has_exif] = %v, want true", got)
		}

		if got := row1["has_human_readable_embedded_metadata"]; got != false {
			t.Fatalf("rows[0][has_human_readable_embedded_metadata] = %v, want false", got)
		}

		if got := row1["has_icc_profile"]; got != true {
			t.Fatalf("rows[0][has_icc_profile] = %v, want true", got)
		}

		tags, ok := row1["tags"].(map[string]map[string]any)
		if !ok {
			t.Fatalf("rows[0][tags] type = %T, want map[string]map[string]any", row1["tags"])
		}

		localTagService, ok := tags[fixture.localTagServiceKeyHex]
		if !ok {
			t.Fatalf("rows[0][tags] missing local tag service %q", fixture.localTagServiceKeyHex)
		}

		localStorageTags, ok := localTagService["storage_tags"].(map[string][]string)
		if !ok {
			t.Fatalf("rows[0][tags][local][storage_tags] type = %T, want map[string][]string", localTagService["storage_tags"])
		}

		localDisplayTags, ok := localTagService["display_tags"].(map[string][]string)
		if !ok {
			t.Fatalf("rows[0][tags][local][display_tags] type = %T, want map[string][]string", localTagService["display_tags"])
		}

		if got := localStorageTags["0"]; !slices.Equal(got, []string{"creator:alice", "series:zeta"}) {
			t.Fatalf("rows[0][tags][local][storage_tags][0] = %v, want [creator:alice series:zeta]", got)
		}

		if got := localStorageTags["2"]; !slices.Equal(got, []string{"old:tag"}) {
			t.Fatalf("rows[0][tags][local][storage_tags][2] = %v, want [old:tag]", got)
		}

		if got := localDisplayTags["2"]; !slices.Equal(got, []string{"old:tag"}) {
			t.Fatalf("rows[0][tags][local][display_tags][2] = %v, want [old:tag]", got)
		}

		downloaderTagService, ok := tags[fixture.downloaderTagsServiceKeyHex]
		if !ok {
			t.Fatalf("rows[0][tags] missing downloader tag service %q", fixture.downloaderTagsServiceKeyHex)
		}

		downloaderStorageTags, ok := downloaderTagService["storage_tags"].(map[string][]string)
		if !ok {
			t.Fatalf("rows[0][tags][downloader][storage_tags] type = %T, want map[string][]string", downloaderTagService["storage_tags"])
		}

		downloaderDisplayTags, ok := downloaderTagService["display_tags"].(map[string][]string)
		if !ok {
			t.Fatalf("rows[0][tags][downloader][display_tags] type = %T, want map[string][]string", downloaderTagService["display_tags"])
		}

		if got := downloaderStorageTags["0"]; !slices.Equal(got, []string{"character:bob", "series:zeta"}) {
			t.Fatalf("rows[0][tags][downloader][storage_tags][0] = %v, want [character:bob series:zeta]", got)
		}

		if got := downloaderStorageTags["1"]; !slices.Equal(got, []string{"pending:review"}) {
			t.Fatalf("rows[0][tags][downloader][storage_tags][1] = %v, want [pending:review]", got)
		}

		if got := downloaderStorageTags["3"]; !slices.Equal(got, []string{"petitioned:cleanup"}) {
			t.Fatalf("rows[0][tags][downloader][storage_tags][3] = %v, want [petitioned:cleanup]", got)
		}

		if got := downloaderDisplayTags["0"]; !slices.Equal(got, []string{"character:robert", "group:cast", "series:zeta"}) {
			t.Fatalf("rows[0][tags][downloader][display_tags][0] = %v, want [character:robert group:cast series:zeta]", got)
		}

		if got := downloaderDisplayTags["1"]; !slices.Equal(got, []string{"meta:pending", "workflow:review"}) {
			t.Fatalf("rows[0][tags][downloader][display_tags][1] = %v, want [meta:pending workflow:review]", got)
		}

		if got := downloaderDisplayTags["3"]; !slices.Equal(got, []string{"petitioned:cleanup"}) {
			t.Fatalf("rows[0][tags][downloader][display_tags][3] = %v, want [petitioned:cleanup]", got)
		}

		combinedTagService, ok := tags[fixture.combinedTagServiceKeyHex]
		if !ok {
			t.Fatalf("rows[0][tags] missing combined tag service %q", fixture.combinedTagServiceKeyHex)
		}

		combinedStorageTags, ok := combinedTagService["storage_tags"].(map[string][]string)
		if !ok {
			t.Fatalf("rows[0][tags][combined][storage_tags] type = %T, want map[string][]string", combinedTagService["storage_tags"])
		}

		if got := combinedStorageTags["0"]; !slices.Equal(got, []string{"character:bob", "creator:alice", "series:zeta"}) {
			t.Fatalf("rows[0][tags][combined][storage_tags][0] = %v, want [character:bob creator:alice series:zeta]", got)
		}

		combinedDisplayTags, ok := combinedTagService["display_tags"].(map[string][]string)
		if !ok {
			t.Fatalf("rows[0][tags][combined][display_tags] type = %T, want map[string][]string", combinedTagService["display_tags"])
		}

		if got := combinedDisplayTags["0"]; !slices.Equal(got, []string{"character:robert", "creator:alice", "group:cast", "series:zeta"}) {
			t.Fatalf("rows[0][tags][combined][display_tags][0] = %v, want [character:robert creator:alice group:cast series:zeta]", got)
		}

		if got := combinedDisplayTags["1"]; !slices.Equal(got, []string{"meta:pending", "workflow:review"}) {
			t.Fatalf("rows[0][tags][combined][display_tags][1] = %v, want [meta:pending workflow:review]", got)
		}

		if got := combinedDisplayTags["2"]; !slices.Equal(got, []string{"old:tag"}) {
			t.Fatalf("rows[0][tags][combined][display_tags][2] = %v, want [old:tag]", got)
		}

		if got := combinedDisplayTags["3"]; !slices.Equal(got, []string{"petitioned:cleanup"}) {
			t.Fatalf("rows[0][tags][combined][display_tags][3] = %v, want [petitioned:cleanup]", got)
		}

		if _, ok := row1["service_keys_to_statuses_to_tags"]; ok {
			t.Fatal("rows[0][service_keys_to_statuses_to_tags] unexpectedly present when legacy tag keys are hidden")
		}

		timeModifiedDetails, ok := row1["time_modified_details"].(map[string]any)
		if !ok {
			t.Fatalf("rows[0][time_modified_details] type = %T, want map[string]any", row1["time_modified_details"])
		}

		if got := timeModifiedDetails["local"]; got != int64(20) {
			t.Fatalf("rows[0][time_modified_details][local] = %v, want 20", got)
		}

		if got := timeModifiedDetails["otherbooru.org"]; got != int64(30) {
			t.Fatalf("rows[0][time_modified_details][otherbooru.org] = %v, want 30", got)
		}

		knownURLs, ok := row1["known_urls"].([]string)
		if !ok {
			t.Fatalf("rows[0][known_urls] type = %T, want []string", row1["known_urls"])
		}

		if len(knownURLs) != 2 {
			t.Fatalf("len(rows[0][known_urls]) = %d, want 2", len(knownURLs))
		}

		if got := knownURLs[0]; got != "https://img.weirdbooru.com/images/ab/cd/abcdblahblahblah.jpg" {
			t.Fatalf("rows[0][known_urls][0] = %q, want img URL", got)
		}

		if got := knownURLs[1]; got != "https://otherbooru.org/index.php?page=post&s=view&id=123456" {
			t.Fatalf("rows[0][known_urls][1] = %q, want post URL", got)
		}

		ipfsMultihashes, ok := row1["ipfs_multihashes"].(map[string]string)
		if !ok {
			t.Fatalf("rows[0][ipfs_multihashes] type = %T, want map[string]string", row1["ipfs_multihashes"])
		}

		if got := ipfsMultihashes[fixture.ipfsServiceKeyHex]; got != fixture.hash1IPFSMultihash {
			t.Fatalf("rows[0][ipfs_multihashes][ipfs] = %q, want %q", got, fixture.hash1IPFSMultihash)
		}

		fileServices, ok := row1["file_services"].(map[string]any)
		if !ok {
			t.Fatalf("rows[0][file_services] type = %T, want map[string]any", row1["file_services"])
		}

		currentServices, ok := fileServices["current"].(map[string]map[string]any)
		if !ok {
			t.Fatalf("rows[0][file_services][current] type = %T, want map[string]map[string]any", fileServices["current"])
		}

		if got := currentServices[fixture.localFilesServiceKeyHex]["time_imported"]; got != int64(500) {
			t.Fatalf("rows[0][file_services][current][local_files][time_imported] = %v, want 500", got)
		}

		if got := currentServices[fixture.hydrusLocalFilesServiceKeyHex]["type_pretty"]; got != services.TypePretty(services.TypeHydrusLocalFileStorage) {
			t.Fatalf("rows[0][file_services][current][all_local_files][type_pretty] = %v, want %q", got, services.TypePretty(services.TypeHydrusLocalFileStorage))
		}

		if got := currentServices[fixture.allKnownFilesServiceKeyHex]["time_imported"]; got != nil {
			t.Fatalf("rows[0][file_services][current][all_known_files][time_imported] = %v, want nil", got)
		}

		row2 := rows[1]
		if got := row2["time_modified"]; got != int64(2) {
			t.Fatalf("rows[1][time_modified] = %v, want 2", got)
		}

		if got := row2["is_inbox"]; got != true {
			t.Fatalf("rows[1][is_inbox] = %v, want true", got)
		}

		if got := row2["is_local"]; got != true {
			t.Fatalf("rows[1][is_local] = %v, want true", got)
		}

		if got := row2["is_trashed"]; got != true {
			t.Fatalf("rows[1][is_trashed] = %v, want true", got)
		}

		if got := row2["is_deleted"]; got != true {
			t.Fatalf("rows[1][is_deleted] = %v, want true", got)
		}

		if _, ok := row2["time_archived"]; ok {
			t.Fatal("rows[1][time_archived] unexpectedly present for inbox file")
		}

		row2FileServices, ok := row2["file_services"].(map[string]any)
		if !ok {
			t.Fatalf("rows[1][file_services] type = %T, want map[string]any", row2["file_services"])
		}

		deletedServices, ok := row2FileServices["deleted"].(map[string]map[string]any)
		if !ok {
			t.Fatalf("rows[1][file_services][deleted] type = %T, want map[string]map[string]any", row2FileServices["deleted"])
		}

		if got := deletedServices[fixture.combinedLocalMediaServiceKeyHex]["time_deleted"]; got != int64(450) {
			t.Fatalf("rows[1][file_services][deleted][all_local_media][time_deleted] = %v, want 450", got)
		}

		if got := deletedServices[fixture.combinedLocalMediaServiceKeyHex]["time_imported"]; got != int64(300) {
			t.Fatalf("rows[1][file_services][deleted][all_local_media][time_imported] = %v, want 300", got)
		}

		if got := deletedServices[fixture.allKnownFilesServiceKeyHex]["time_deleted"]; got != nil {
			t.Fatalf("rows[1][file_services][deleted][all_known_files][time_deleted] = %v, want nil", got)
		}

		if got := rows[2]["file_id"]; got != nil {
			t.Fatalf("rows[2][file_id] = %v, want nil", got)
		}
	})

	t.Run("full mode prefers specific display caches for single-file groups", func(t *testing.T) {
		rows, err := bundle.GetMetadata(context.Background(), filemetadata.Request{
			Hashes: []string{fixture.hash1Hex},
		})
		if err != nil {
			t.Fatalf("GetMetadata() error = %v", err)
		}

		tags, ok := rows[0]["tags"].(map[string]map[string]any)
		if !ok {
			t.Fatalf("rows[0][tags] type = %T, want map[string]map[string]any", rows[0]["tags"])
		}

		downloaderTagService, ok := tags[fixture.downloaderTagsServiceKeyHex]
		if !ok {
			t.Fatalf("rows[0][tags] missing downloader tag service %q", fixture.downloaderTagsServiceKeyHex)
		}

		downloaderStorageTags, ok := downloaderTagService["storage_tags"].(map[string][]string)
		if !ok {
			t.Fatalf("rows[0][tags][downloader][storage_tags] type = %T, want map[string][]string", downloaderTagService["storage_tags"])
		}

		downloaderDisplayTags, ok := downloaderTagService["display_tags"].(map[string][]string)
		if !ok {
			t.Fatalf("rows[0][tags][downloader][display_tags] type = %T, want map[string][]string", downloaderTagService["display_tags"])
		}

		if got := downloaderStorageTags["0"]; !slices.Equal(got, []string{"storage:downloader-current"}) {
			t.Fatalf("rows[0][tags][downloader][storage_tags][0] = %v, want [storage:downloader-current]", got)
		}

		if got := downloaderStorageTags["1"]; !slices.Equal(got, []string{"storage:downloader-pending"}) {
			t.Fatalf("rows[0][tags][downloader][storage_tags][1] = %v, want [storage:downloader-pending]", got)
		}

		if got := downloaderStorageTags["2"]; !slices.Equal(got, []string{"storage:downloader-deleted"}) {
			t.Fatalf("rows[0][tags][downloader][storage_tags][2] = %v, want [storage:downloader-deleted]", got)
		}

		if got := downloaderDisplayTags["0"]; !slices.Equal(got, []string{"cache:downloader-current"}) {
			t.Fatalf("rows[0][tags][downloader][display_tags][0] = %v, want [cache:downloader-current]", got)
		}

		if got := downloaderDisplayTags["1"]; !slices.Equal(got, []string{"cache:downloader-pending"}) {
			t.Fatalf("rows[0][tags][downloader][display_tags][1] = %v, want [cache:downloader-pending]", got)
		}

		if got := downloaderDisplayTags["2"]; !slices.Equal(got, []string{"storage:downloader-deleted"}) {
			t.Fatalf("rows[0][tags][downloader][display_tags][2] = %v, want [storage:downloader-deleted]", got)
		}

		if got := downloaderDisplayTags["3"]; !slices.Equal(got, []string{"petitioned:cleanup"}) {
			t.Fatalf("rows[0][tags][downloader][display_tags][3] = %v, want [petitioned:cleanup]", got)
		}

		combinedTagService, ok := tags[fixture.combinedTagServiceKeyHex]
		if !ok {
			t.Fatalf("rows[0][tags] missing combined tag service %q", fixture.combinedTagServiceKeyHex)
		}

		combinedStorageTags, ok := combinedTagService["storage_tags"].(map[string][]string)
		if !ok {
			t.Fatalf("rows[0][tags][combined][storage_tags] type = %T, want map[string][]string", combinedTagService["storage_tags"])
		}

		combinedDisplayTags, ok := combinedTagService["display_tags"].(map[string][]string)
		if !ok {
			t.Fatalf("rows[0][tags][combined][display_tags] type = %T, want map[string][]string", combinedTagService["display_tags"])
		}

		if got := combinedStorageTags["0"]; !slices.Equal(got, []string{"creator:alice", "series:zeta", "storage:downloader-current"}) {
			t.Fatalf("rows[0][tags][combined][storage_tags][0] = %v, want [creator:alice series:zeta storage:downloader-current]", got)
		}

		if got := combinedStorageTags["1"]; !slices.Equal(got, []string{"storage:downloader-pending"}) {
			t.Fatalf("rows[0][tags][combined][storage_tags][1] = %v, want [storage:downloader-pending]", got)
		}

		if got := combinedStorageTags["2"]; !slices.Equal(got, []string{"old:tag", "storage:downloader-deleted"}) {
			t.Fatalf("rows[0][tags][combined][storage_tags][2] = %v, want [old:tag storage:downloader-deleted]", got)
		}

		if got := combinedDisplayTags["0"]; !slices.Equal(got, []string{"cache:downloader-current", "creator:alice", "series:zeta"}) {
			t.Fatalf("rows[0][tags][combined][display_tags][0] = %v, want [cache:downloader-current creator:alice series:zeta]", got)
		}

		if got := combinedDisplayTags["1"]; !slices.Equal(got, []string{"cache:downloader-pending"}) {
			t.Fatalf("rows[0][tags][combined][display_tags][1] = %v, want [cache:downloader-pending]", got)
		}

		if got := combinedDisplayTags["2"]; !slices.Equal(got, []string{"old:tag", "storage:downloader-deleted"}) {
			t.Fatalf("rows[0][tags][combined][display_tags][2] = %v, want [old:tag storage:downloader-deleted]", got)
		}
	})

	t.Run("full mode can include legacy service-key tag maps", func(t *testing.T) {
		rows, err := bundle.GetMetadata(context.Background(), filemetadata.Request{
			Hashes:                       []string{fixture.hash1Hex},
			IncludeLegacyServiceKeysTags: true,
		})
		if err != nil {
			t.Fatalf("GetMetadata() error = %v", err)
		}

		storageByService, ok := rows[0]["service_keys_to_statuses_to_tags"].(map[string]map[string][]string)
		if !ok {
			t.Fatalf("rows[0][service_keys_to_statuses_to_tags] type = %T, want map[string]map[string][]string", rows[0]["service_keys_to_statuses_to_tags"])
		}

		if got := storageByService[fixture.combinedTagServiceKeyHex]["0"]; !slices.Equal(got, []string{"creator:alice", "series:zeta", "storage:downloader-current"}) {
			t.Fatalf("rows[0][service_keys_to_statuses_to_tags][combined][0] = %v, want [creator:alice series:zeta storage:downloader-current]", got)
		}

		displayByService, ok := rows[0]["service_keys_to_statuses_to_display_tags"].(map[string]map[string][]string)
		if !ok {
			t.Fatalf("rows[0][service_keys_to_statuses_to_display_tags] type = %T, want map[string]map[string][]string", rows[0]["service_keys_to_statuses_to_display_tags"])
		}

		if got := displayByService[fixture.downloaderTagsServiceKeyHex]["3"]; !slices.Equal(got, []string{"petitioned:cleanup"}) {
			t.Fatalf("rows[0][service_keys_to_statuses_to_display_tags][downloader][3] = %v, want [petitioned:cleanup]", got)
		}

		if got := displayByService[fixture.downloaderTagsServiceKeyHex]["0"]; !slices.Equal(got, []string{"cache:downloader-current"}) {
			t.Fatalf("rows[0][service_keys_to_statuses_to_display_tags][downloader][0] = %v, want [cache:downloader-current]", got)
		}
	})

	t.Run("full mode supports millisecond timestamps", func(t *testing.T) {
		rows, err := bundle.GetMetadata(context.Background(), filemetadata.Request{
			FileIDs:             []int64{1},
			IncludeMilliseconds: true,
		})
		if err != nil {
			t.Fatalf("GetMetadata() error = %v", err)
		}

		if got := rows[0]["time_modified"]; got != 20.123 {
			t.Fatalf("rows[0][time_modified] = %v, want 20.123", got)
		}

		if got := rows[0]["time_archived"]; got != 500.127 {
			t.Fatalf("rows[0][time_archived] = %v, want 500.127", got)
		}

		timeModifiedDetails, ok := rows[0]["time_modified_details"].(map[string]any)
		if !ok {
			t.Fatalf("rows[0][time_modified_details] type = %T, want map[string]any", rows[0]["time_modified_details"])
		}

		if got := timeModifiedDetails["local"]; got != 20.123 {
			t.Fatalf("rows[0][time_modified_details][local] = %v, want 20.123", got)
		}

		if got := timeModifiedDetails["otherbooru.org"]; got != 30.123 {
			t.Fatalf("rows[0][time_modified_details][otherbooru.org] = %v, want 30.123", got)
		}

		fileServices, ok := rows[0]["file_services"].(map[string]any)
		if !ok {
			t.Fatalf("rows[0][file_services] type = %T, want map[string]any", rows[0]["file_services"])
		}

		currentServices, ok := fileServices["current"].(map[string]map[string]any)
		if !ok {
			t.Fatalf("rows[0][file_services][current] type = %T, want map[string]map[string]any", fileServices["current"])
		}

		if got := currentServices[fixture.localFilesServiceKeyHex]["time_imported"]; got != 500.127 {
			t.Fatalf("rows[0][file_services][current][local_files][time_imported] = %v, want 500.127", got)
		}

		rows, err = bundle.GetMetadata(context.Background(), filemetadata.Request{
			FileIDs:             []int64{2},
			IncludeMilliseconds: true,
		})
		if err != nil {
			t.Fatalf("GetMetadata() error = %v", err)
		}

		fileServices, ok = rows[0]["file_services"].(map[string]any)
		if !ok {
			t.Fatalf("rows[0][file_services] type = %T, want map[string]any", rows[0]["file_services"])
		}

		deletedServices, ok := fileServices["deleted"].(map[string]map[string]any)
		if !ok {
			t.Fatalf("rows[0][file_services][deleted] type = %T, want map[string]map[string]any", fileServices["deleted"])
		}

		if got := deletedServices[fixture.combinedLocalMediaServiceKeyHex]["time_deleted"]; got != 450.124 {
			t.Fatalf("rows[0][file_services][deleted][all_local_media][time_deleted] = %v, want 450.124", got)
		}

		if got := deletedServices[fixture.combinedLocalMediaServiceKeyHex]["time_imported"]; got != 300.125 {
			t.Fatalf("rows[0][file_services][deleted][all_local_media][time_imported] = %v, want 300.125", got)
		}
	})

	t.Run("missing file IDs return not found", func(t *testing.T) {
		_, err := bundle.GetMetadata(context.Background(), filemetadata.Request{
			FileIDs:               []int64{999},
			OnlyReturnIdentifiers: true,
		})
		if err == nil {
			t.Fatal("GetMetadata() error = nil, want error")
		}

		if _, ok := err.(*filemetadata.NotFoundError); !ok {
			t.Fatalf("error type = %T, want *filemetadata.NotFoundError", err)
		}
	})

	t.Run("unsupported write-semantics are rejected", func(t *testing.T) {
		_, err := bundle.GetMetadata(context.Background(), filemetadata.Request{
			Hashes:                []string{fixture.hash1Hex},
			CreateNewFileIDs:      true,
			OnlyReturnIdentifiers: true,
		})
		if err == nil {
			t.Fatal("GetMetadata() error = nil, want error")
		}

		if _, ok := err.(*filemetadata.UnsupportedError); !ok {
			t.Fatalf("error type = %T, want *filemetadata.UnsupportedError", err)
		}
	})

	t.Run("unsupported full mode flags are rejected", func(t *testing.T) {
		_, err := bundle.GetMetadata(context.Background(), filemetadata.Request{
			Hashes:                 []string{fixture.hash1Hex},
			DetailedURLInformation: true,
		})
		if err == nil {
			t.Fatal("GetMetadata() error = nil, want error")
		}

		if _, ok := err.(*filemetadata.UnsupportedError); !ok {
			t.Fatalf("error type = %T, want *filemetadata.UnsupportedError", err)
		}

		_, err = bundle.GetMetadata(context.Background(), filemetadata.Request{
			Hashes:       []string{fixture.hash1Hex},
			IncludeNotes: true,
		})
		if err == nil {
			t.Fatal("GetMetadata() error = nil, want error")
		}

		if _, ok := err.(*filemetadata.UnsupportedError); !ok {
			t.Fatalf("error type = %T, want *filemetadata.UnsupportedError", err)
		}
	})

	t.Run("full-only flags do not affect identifier and basic modes", func(t *testing.T) {
		rows, err := bundle.GetMetadata(context.Background(), filemetadata.Request{
			Hashes:                 []string{fixture.hash1Hex},
			OnlyReturnIdentifiers:  true,
			DetailedURLInformation: true,
			IncludeNotes:           true,
		})
		if err != nil {
			t.Fatalf("GetMetadata() error = %v", err)
		}

		if got := rows[0]["file_id"]; got != int64(1) {
			t.Fatalf("rows[0][file_id] = %v, want 1", got)
		}

		rows, err = bundle.GetMetadata(context.Background(), filemetadata.Request{
			Hashes:                     []string{fixture.hash1Hex},
			OnlyReturnBasicInformation: true,
			DetailedURLInformation:     true,
			IncludeNotes:               true,
		})
		if err != nil {
			t.Fatalf("GetMetadata() error = %v", err)
		}

		if got := rows[0]["file_id"]; got != int64(1) {
			t.Fatalf("rows[0][file_id] = %v, want 1", got)
		}
	})

	t.Run("bundle connection is read-only", func(t *testing.T) {
		_, err := bundle.conn.ExecContext(
			context.Background(),
			`INSERT INTO main.services (service_key, service_type, name, dictionary_string) VALUES (?, ?, ?, ?)`,
			[]byte("blocked"),
			int(services.TypeLocalTag),
			"blocked",
			"{}",
		)
		if err == nil {
			t.Fatal("ExecContext() error = nil, want read-only failure")
		}
	})
}

func TestBundleWriteTransactions(t *testing.T) {
	t.Run("read-only bundles reject immediate transactions", func(t *testing.T) {
		dir, _ := createTestBundle(t)

		bundle, err := Open(context.Background(), dir)
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		ran := false
		err = bundle.WithImmediateTx(context.Background(), func(tx *ImmediateTx) error {
			ran = true
			_, execErr := tx.ExecContext(
				context.Background(),
				`INSERT INTO file_inbox (hash_id) VALUES (?)`,
				1,
			)
			return execErr
		})
		if err == nil {
			t.Fatal("WithImmediateTx() error = nil, want error")
		}

		if ran {
			t.Fatal("write callback ran for read-only bundle")
		}
	})

	t.Run("writable bundles commit controlled mutations", func(t *testing.T) {
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

		err = bundle.WithImmediateTx(context.Background(), func(tx *ImmediateTx) error {
			_, execErr := tx.ExecContext(
				context.Background(),
				`INSERT INTO archive_timestamps (hash_id, archived_timestamp_ms) VALUES (?, ?)`,
				2,
				900123,
			)
			return execErr
		})
		if err != nil {
			t.Fatalf("WithImmediateTx() error = %v", err)
		}

		db := openSQLiteForTest(t, filepath.Join(dir, "client.db"))
		defer db.Close()

		var archivedTimestampMS int64
		if err := db.QueryRow(
			`SELECT archived_timestamp_ms FROM archive_timestamps WHERE hash_id = ?`,
			2,
		).Scan(&archivedTimestampMS); err != nil {
			t.Fatalf("Scan() error = %v", err)
		}

		if archivedTimestampMS != 900123 {
			t.Fatalf("archivedTimestampMS = %d, want 900123", archivedTimestampMS)
		}
	})

	t.Run("writable bundles roll back on callback error", func(t *testing.T) {
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

		expectedErr := errors.New("force rollback")
		err = bundle.WithImmediateTx(context.Background(), func(tx *ImmediateTx) error {
			if _, execErr := tx.ExecContext(
				context.Background(),
				`INSERT INTO file_inbox (hash_id) VALUES (?)`,
				1,
			); execErr != nil {
				return execErr
			}

			return expectedErr
		})
		if !errors.Is(err, expectedErr) {
			t.Fatalf("WithImmediateTx() error = %v, want %v", err, expectedErr)
		}

		db := openSQLiteForTest(t, filepath.Join(dir, "client.db"))
		defer db.Close()

		var count int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM file_inbox WHERE hash_id = ?`,
			1,
		).Scan(&count); err != nil {
			t.Fatalf("Scan() error = %v", err)
		}

		if count != 0 {
			t.Fatalf("file_inbox count = %d, want 0 after rollback", count)
		}
	})

	t.Run("writable bundles serialize concurrent write callbacks", func(t *testing.T) {
		dir, _ := createTestBundle(t)

		db := openSQLiteForTest(t, filepath.Join(dir, "client.db"))
		mustExec(t, db, `CREATE TABLE tx_log (id INTEGER PRIMARY KEY AUTOINCREMENT, label TEXT);`)
		if err := db.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}

		bundle, err := OpenWritable(context.Background(), dir)
		if err != nil {
			t.Fatalf("OpenWritable() error = %v", err)
		}
		defer func() {
			if err := bundle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()

		firstEntered := make(chan struct{})
		releaseFirst := make(chan struct{})
		firstDone := make(chan error, 1)
		secondEntered := make(chan struct{})
		secondDone := make(chan error, 1)

		go func() {
			firstDone <- bundle.WithImmediateTx(context.Background(), func(tx *ImmediateTx) error {
				if _, execErr := tx.ExecContext(
					context.Background(),
					`INSERT INTO tx_log (label) VALUES (?)`,
					"first-start",
				); execErr != nil {
					return execErr
				}

				close(firstEntered)
				<-releaseFirst

				_, execErr := tx.ExecContext(
					context.Background(),
					`INSERT INTO tx_log (label) VALUES (?)`,
					"first-end",
				)
				return execErr
			})
		}()

		<-firstEntered

		secondCtx, cancelSecond := context.WithTimeout(
			context.Background(),
			100*time.Millisecond,
		)
		defer cancelSecond()

		go func() {
			secondDone <- bundle.WithImmediateTx(secondCtx, func(tx *ImmediateTx) error {
				close(secondEntered)
				_, execErr := tx.ExecContext(
					context.Background(),
					`INSERT INTO tx_log (label) VALUES (?)`,
					"second",
				)
				return execErr
			})
		}()

		if err := <-secondDone; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("second WithImmediateTx() error = %v, want context deadline exceeded", err)
		}

		select {
		case <-secondEntered:
			t.Fatal("second write callback entered while first transaction held the write gate")
		default:
		}

		close(releaseFirst)

		if err := <-firstDone; err != nil {
			t.Fatalf("first WithImmediateTx() error = %v", err)
		}

		err = bundle.WithImmediateTx(context.Background(), func(tx *ImmediateTx) error {
			_, execErr := tx.ExecContext(
				context.Background(),
				`INSERT INTO tx_log (label) VALUES (?)`,
				"third",
			)
			return execErr
		})
		if err != nil {
			t.Fatalf("third WithImmediateTx() error = %v", err)
		}

		db = openSQLiteForTest(t, filepath.Join(dir, "client.db"))
		defer db.Close()

		rows, err := db.Query(`SELECT label FROM tx_log ORDER BY id ASC`)
		if err != nil {
			t.Fatalf("Query() error = %v", err)
		}
		defer rows.Close()

		labels := []string{}
		for rows.Next() {
			var label string
			if err := rows.Scan(&label); err != nil {
				t.Fatalf("Scan() error = %v", err)
			}

			labels = append(labels, label)
		}

		if err := rows.Err(); err != nil {
			t.Fatalf("rows.Err() = %v", err)
		}

		if len(labels) != 3 {
			t.Fatalf("len(labels) = %d, want 3", len(labels))
		}

		if labels[0] != "first-start" || labels[1] != "first-end" || labels[2] != "third" {
			t.Fatalf("labels = %v, want [first-start first-end third]", labels)
		}
	})
}

type testFixture struct {
	hash1Hex                        string
	hash2Hex                        string
	hash3Hex                        string
	unknownHashHex                  string
	hash1Blurhash                   string
	hash1PixelHashHex               string
	hash1IPFSMultihash              string
	clientAPIServiceKey             []byte
	localTagServiceKeyHex           string
	downloaderTagsServiceKeyHex     string
	combinedTagServiceKeyHex        string
	localFilesServiceKeyHex         string
	allKnownFilesServiceKeyHex      string
	hydrusLocalFilesServiceKeyHex   string
	combinedLocalMediaServiceKeyHex string
	ipfsServiceKeyHex               string
}

func createTestBundle(t *testing.T) (string, testFixture) {
	t.Helper()

	dir := t.TempDir()
	mainPath := filepath.Join(dir, "client.db")
	masterPath := filepath.Join(dir, "client.master.db")
	cachesPath := filepath.Join(dir, "client.caches.db")
	mappingsPath := filepath.Join(dir, "client.mappings.db")

	hash1 := strings.Repeat("01", 32)
	hash2 := strings.Repeat("02", 32)
	hash3 := strings.Repeat("03", 32)
	unknownHash := strings.Repeat("de", 32)
	pixelHash := strings.Repeat("ab", 32)
	blurhash := "LKO2?U%2Tw=w]~RBVZRi};RPxuwH"
	ipfsMultihash := "QmReHtaET3dsgh7ho5NVyHb5U13UgJoGipSWbZsnuuM8tb"
	ratingDictionary := `[
		21,
		2,
		[
			[[0,"colours"],[2,[26,3,[[0,[0,[[1,2,3],[4,5,6]]]],[0,[1,[[7,8,9],[10,11,12]]]],[0,[2,[[13,14,15],[16,17,18]]]],[0,[4,[[19,20,21],[22,23,24]]]]]]]],
			[[0,"show_in_thumbnail"],[0,true]],
			[[0,"show_in_thumbnail_even_when_null"],[0,false]],
			[[0,"shape"],[0,0]],
			[[0,"rating_svg"],[0,null]],
			[[0,"num_stars"],[0,7]],
			[[0,"allow_zero"],[0,true]],
			[[0,"custom_pad"],[0,0]],
			[[0,"show_fraction_beside_stars"],[0,0]]
		]
	]`
	favouritesDictionary := `[
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

	localTagKey := []byte("local-tags")
	localFilesKey := []byte("local-files")
	hydrusLocalFilesKey := []byte("all-local-files")
	combinedLocalMediaKey := []byte("all-local-media")
	combinedFilesKey := []byte("combined-files")
	combinedTagsKey := []byte("all-known-tags")
	trashKey := []byte("trash")
	ipfsKey := []byte("my-ipfs")
	clientAPIKey := []byte("client-api")
	downloaderTagsKey := []byte("downloader tags")
	favouritesKey := []byte("favourites")

	mainDB := openSQLiteForTest(t, mainPath)
	defer mainDB.Close()

	mustExec(t, mainDB, `
		CREATE TABLE services (
			service_id INTEGER PRIMARY KEY AUTOINCREMENT,
			service_key BLOB UNIQUE,
			service_type INTEGER,
			name TEXT,
			dictionary_string TEXT
		);
	`)
	mustExec(t, mainDB, `
		CREATE TABLE files_info (
			hash_id INTEGER PRIMARY KEY,
			size INTEGER,
			mime INTEGER,
			width INTEGER,
			height INTEGER,
			duration INTEGER,
			num_frames INTEGER,
			has_audio INTEGER,
			num_words INTEGER
		);
	`)
	mustExec(t, mainDB, `
		CREATE TABLE files_info_forced_filetypes (
			hash_id INTEGER PRIMARY KEY,
			forced_mime INTEGER
		);
	`)
	mustExec(t, mainDB, `CREATE TABLE file_inbox (hash_id INTEGER PRIMARY KEY);`)
	mustExec(t, mainDB, `CREATE TABLE archive_timestamps (hash_id INTEGER PRIMARY KEY, archived_timestamp_ms INTEGER);`)
	mustExec(t, mainDB, `CREATE TABLE file_modified_timestamps (hash_id INTEGER PRIMARY KEY, file_modified_timestamp_ms INTEGER);`)
	mustExec(t, mainDB, `CREATE TABLE file_domain_modified_timestamps (hash_id INTEGER, domain_id INTEGER, file_modified_timestamp_ms INTEGER, PRIMARY KEY (hash_id, domain_id));`)
	mustExec(t, mainDB, `CREATE TABLE url_map (hash_id INTEGER, url_id INTEGER, PRIMARY KEY (hash_id, url_id));`)
	mustExec(t, mainDB, `CREATE TABLE service_filenames (service_id INTEGER, hash_id INTEGER, filename TEXT, PRIMARY KEY (service_id, hash_id));`)
	mustExec(t, mainDB, `CREATE TABLE pixel_hash_map (hash_id INTEGER, pixel_hash_id INTEGER, PRIMARY KEY (hash_id, pixel_hash_id));`)
	mustExec(t, mainDB, `CREATE TABLE has_transparency (hash_id INTEGER PRIMARY KEY);`)
	mustExec(t, mainDB, `CREATE TABLE has_exif (hash_id INTEGER PRIMARY KEY);`)
	mustExec(t, mainDB, `CREATE TABLE has_human_readable_embedded_metadata (hash_id INTEGER PRIMARY KEY);`)
	mustExec(t, mainDB, `CREATE TABLE has_icc_profile (hash_id INTEGER PRIMARY KEY);`)
	mustExec(t, mainDB, `CREATE TABLE current_client_files_locations (location_id INTEGER PRIMARY KEY, location TEXT UNIQUE);`)
	mustExec(t, mainDB, `CREATE TABLE client_files_subfolders (prefix TEXT, location_id INTEGER, PRIMARY KEY (prefix, location_id));`)
	mustExec(t, mainDB, `CREATE TABLE ideal_client_files_locations (location_id INTEGER PRIMARY KEY, weight INTEGER, max_num_bytes INTEGER);`)
	mustExec(t, mainDB, `CREATE TABLE ideal_thumbnail_override_location (location_id INTEGER);`)
	mustExec(t, mainDB, `CREATE TABLE current_storage_granularity (granularity INTEGER);`)
	mustExec(t, mainDB, `CREATE TABLE current_files_2 (hash_id INTEGER PRIMARY KEY, timestamp_ms INTEGER);`)
	mustExec(t, mainDB, `CREATE TABLE current_files_3 (hash_id INTEGER PRIMARY KEY, timestamp_ms INTEGER);`)
	mustExec(t, mainDB, `CREATE TABLE current_files_4 (hash_id INTEGER PRIMARY KEY, timestamp_ms INTEGER);`)
	mustExec(t, mainDB, `CREATE TABLE current_files_5 (hash_id INTEGER PRIMARY KEY, timestamp_ms INTEGER);`)
	mustExec(t, mainDB, `CREATE TABLE deleted_files_5 (hash_id INTEGER PRIMARY KEY, timestamp_ms INTEGER, original_timestamp_ms INTEGER);`)
	mustExec(t, mainDB, `CREATE TABLE deleted_files_4 (hash_id INTEGER PRIMARY KEY, timestamp_ms INTEGER, original_timestamp_ms INTEGER);`)
	mustExec(t, mainDB, `CREATE TABLE current_files_6 (hash_id INTEGER PRIMARY KEY, timestamp_ms INTEGER);`)
	mustExec(t, mainDB, `CREATE TABLE current_files_8 (hash_id INTEGER PRIMARY KEY, timestamp_ms INTEGER);`)
	seedHydrusDBTestStorage(t, mainDB, dir)

	mustExec(
		t,
		mainDB,
		`INSERT INTO services (service_id, service_key, service_type, name, dictionary_string) VALUES (?, ?, ?, ?, ?), (?, ?, ?, ?, ?), (?, ?, ?, ?, ?), (?, ?, ?, ?, ?), (?, ?, ?, ?, ?), (?, ?, ?, ?, ?), (?, ?, ?, ?, ?), (?, ?, ?, ?, ?), (?, ?, ?, ?, ?), (?, ?, ?, ?, ?), (?, ?, ?, ?, ?), (?, ?, ?, ?, ?);`,
		1, localTagKey, int(services.TypeLocalTag), "my tags", "{}",
		2, localFilesKey, int(services.TypeLocalFileDomain), "my files", "{}",
		3, hydrusLocalFilesKey, int(services.TypeHydrusLocalFileStorage), "all local files", "{}",
		4, combinedLocalMediaKey, int(services.TypeCombinedLocalFileDomains), "all local media", "{}",
		5, combinedFilesKey, int(services.TypeCombinedFile), "all known files", "{}",
		6, trashKey, int(services.TypeLocalFileTrashDomain), "trash", "{}",
		7, []byte("my-stars"), int(services.TypeLocalRatingNumerical), "my stars", ratingDictionary,
		8, ipfsKey, int(services.TypeIPFS), "my ipfs", "{}",
		9, clientAPIKey, int(services.TypeClientAPIService), "client api", "{}",
		10, downloaderTagsKey, int(services.TypeLocalTag), "downloader tags", "{}",
		11, favouritesKey, int(services.TypeLocalRatingLike), "favourites", favouritesDictionary,
		12, combinedTagsKey, int(services.TypeCombinedTag), "all known tags", "{}",
	)
	mustExec(
		t,
		mainDB,
		`INSERT INTO files_info (hash_id, size, mime, width, height, duration, num_frames, has_audio, num_words) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?);`,
		1, 111, 1, 640, 480, nil, nil, 0, nil,
		2, 222, 2, 100, 200, 1200, 36, 1, 7,
	)
	mustExec(
		t,
		mainDB,
		`INSERT INTO files_info_forced_filetypes (hash_id, forced_mime) VALUES (?, ?);`,
		2, 1,
	)
	mustExec(t, mainDB, `INSERT INTO file_inbox (hash_id) VALUES (?);`, 2)
	mustExec(t, mainDB, `INSERT INTO archive_timestamps (hash_id, archived_timestamp_ms) VALUES (?, ?);`, 1, 500127)
	mustExec(
		t,
		mainDB,
		`INSERT INTO file_modified_timestamps (hash_id, file_modified_timestamp_ms) VALUES (?, ?), (?, ?);`,
		1, 20123,
		2, 6150,
	)
	mustExec(
		t,
		mainDB,
		`INSERT INTO file_domain_modified_timestamps (hash_id, domain_id, file_modified_timestamp_ms) VALUES (?, ?, ?), (?, ?, ?);`,
		1, 1, 30123,
		2, 1, 2000,
	)
	mustExec(
		t,
		mainDB,
		`INSERT INTO url_map (hash_id, url_id) VALUES (?, ?), (?, ?), (?, ?);`,
		1, 1,
		1, 2,
		2, 1,
	)
	mustExec(
		t,
		mainDB,
		`INSERT INTO service_filenames (service_id, hash_id, filename) VALUES (?, ?, ?);`,
		8, 1, ipfsMultihash,
	)
	mustExec(
		t,
		mainDB,
		`INSERT INTO pixel_hash_map (hash_id, pixel_hash_id) VALUES (?, ?);`,
		1, 101,
	)
	mustExec(t, mainDB, `INSERT INTO has_transparency (hash_id) VALUES (?);`, 1)
	mustExec(t, mainDB, `INSERT INTO has_exif (hash_id) VALUES (?);`, 1)
	mustExec(t, mainDB, `INSERT INTO has_human_readable_embedded_metadata (hash_id) VALUES (?);`, 2)
	mustExec(t, mainDB, `INSERT INTO has_icc_profile (hash_id) VALUES (?);`, 1)
	mustExec(
		t,
		mainDB,
		`INSERT INTO current_files_2 (hash_id, timestamp_ms) VALUES (?, ?);`,
		1, 500127,
	)
	mustExec(
		t,
		mainDB,
		`INSERT INTO current_files_3 (hash_id, timestamp_ms) VALUES (?, ?), (?, ?);`,
		1, 500227,
		2, 600127,
	)
	mustExec(
		t,
		mainDB,
		`INSERT INTO current_files_4 (hash_id, timestamp_ms) VALUES (?, ?);`,
		1, 500327,
	)
	mustExec(
		t,
		mainDB,
		`INSERT INTO current_files_5 (hash_id, timestamp_ms) VALUES (?, ?);`,
		1, nil,
	)
	mustExec(
		t,
		mainDB,
		`INSERT INTO deleted_files_4 (hash_id, timestamp_ms, original_timestamp_ms) VALUES (?, ?, ?);`,
		2, 450124, 300125,
	)
	mustExec(
		t,
		mainDB,
		`INSERT INTO deleted_files_5 (hash_id, timestamp_ms, original_timestamp_ms) VALUES (?, ?, ?);`,
		2, nil, nil,
	)
	mustExec(
		t,
		mainDB,
		`INSERT INTO current_files_6 (hash_id, timestamp_ms) VALUES (?, ?);`,
		2, 610127,
	)
	mustExec(
		t,
		mainDB,
		`INSERT INTO current_files_8 (hash_id, timestamp_ms) VALUES (?, ?);`,
		1, 123456126,
	)

	masterDB := openSQLiteForTest(t, masterPath)
	defer masterDB.Close()

	mustExec(t, masterDB, `CREATE TABLE hashes (hash_id INTEGER PRIMARY KEY, hash BLOB UNIQUE);`)
	mustExec(t, masterDB, `CREATE TABLE blurhashes (hash_id INTEGER PRIMARY KEY, blurhash TEXT);`)
	mustExec(t, masterDB, `CREATE TABLE tags (tag_id INTEGER PRIMARY KEY, tag TEXT UNIQUE);`)
	mustExec(t, masterDB, `CREATE TABLE url_domains (domain_id INTEGER PRIMARY KEY, domain TEXT UNIQUE);`)
	mustExec(t, masterDB, `CREATE TABLE urls (url_id INTEGER PRIMARY KEY, domain_id INTEGER, url TEXT UNIQUE);`)
	mustExec(
		t,
		masterDB,
		`INSERT INTO hashes (hash_id, hash) VALUES (?, ?), (?, ?), (?, ?), (?, ?);`,
		1, mustDecodeHex(t, hash1),
		2, mustDecodeHex(t, hash2),
		3, mustDecodeHex(t, hash3),
		101, mustDecodeHex(t, pixelHash),
	)
	mustExec(
		t,
		masterDB,
		`INSERT INTO blurhashes (hash_id, blurhash) VALUES (?, ?);`,
		1, blurhash,
	)
	mustExec(
		t,
		masterDB,
		`INSERT INTO tags (tag_id, tag) VALUES (?, ?), (?, ?), (?, ?), (?, ?), (?, ?), (?, ?), (?, ?), (?, ?), (?, ?), (?, ?), (?, ?), (?, ?), (?, ?), (?, ?), (?, ?), (?, ?), (?, ?);`,
		1, "creator:alice",
		2, "series:zeta",
		3, "old:tag",
		4, "character:bob",
		5, "pending:review",
		6, "petitioned:cleanup",
		7, "character:robert",
		8, "group:cast",
		9, "workflow:review",
		10, "meta:pending",
		11, "history:retired",
		12, "history:archive",
		13, "cache:downloader-current",
		14, "cache:downloader-pending",
		15, "storage:downloader-current",
		16, "storage:downloader-pending",
		17, "storage:downloader-deleted",
	)
	mustExec(
		t,
		masterDB,
		`INSERT INTO url_domains (domain_id, domain) VALUES (?, ?), (?, ?);`,
		1, "otherbooru.org",
		2, "img.weirdbooru.com",
	)
	mustExec(
		t,
		masterDB,
		`INSERT INTO urls (url_id, domain_id, url) VALUES (?, ?, ?), (?, ?, ?);`,
		1, 1, "https://otherbooru.org/index.php?page=post&s=view&id=123456",
		2, 2, "https://img.weirdbooru.com/images/ab/cd/abcdblahblahblah.jpg",
	)

	createEmptySQLiteFile(t, cachesPath)
	cachesDB := openSQLiteForTest(t, cachesPath)
	defer cachesDB.Close()

	mustExec(
		t,
		cachesDB,
		`CREATE TABLE specific_current_mappings_cache_2_10 (tag_id INTEGER, hash_id INTEGER, PRIMARY KEY (tag_id, hash_id));`,
	)
	mustExec(
		t,
		cachesDB,
		`CREATE TABLE specific_deleted_mappings_cache_2_10 (tag_id INTEGER, hash_id INTEGER, PRIMARY KEY (tag_id, hash_id));`,
	)
	mustExec(
		t,
		cachesDB,
		`CREATE TABLE specific_pending_mappings_cache_2_10 (tag_id INTEGER, hash_id INTEGER, PRIMARY KEY (tag_id, hash_id));`,
	)
	mustExec(
		t,
		cachesDB,
		`CREATE TABLE specific_display_current_mappings_cache_2_10 (tag_id INTEGER, hash_id INTEGER, PRIMARY KEY (tag_id, hash_id));`,
	)
	mustExec(
		t,
		cachesDB,
		`CREATE TABLE specific_display_pending_mappings_cache_2_10 (tag_id INTEGER, hash_id INTEGER, PRIMARY KEY (tag_id, hash_id));`,
	)
	mustExec(
		t,
		cachesDB,
		`CREATE TABLE actual_tag_siblings_lookup_cache_1 (bad_tag_id INTEGER PRIMARY KEY, ideal_tag_id INTEGER);`,
	)
	mustExec(
		t,
		cachesDB,
		`CREATE TABLE actual_tag_parents_lookup_cache_1 (child_tag_id INTEGER, ancestor_tag_id INTEGER, PRIMARY KEY (child_tag_id, ancestor_tag_id));`,
	)
	mustExec(
		t,
		cachesDB,
		`CREATE TABLE actual_tag_siblings_lookup_cache_10 (bad_tag_id INTEGER PRIMARY KEY, ideal_tag_id INTEGER);`,
	)
	mustExec(
		t,
		cachesDB,
		`CREATE TABLE actual_tag_parents_lookup_cache_10 (child_tag_id INTEGER, ancestor_tag_id INTEGER, PRIMARY KEY (child_tag_id, ancestor_tag_id));`,
	)
	mustExec(
		t,
		cachesDB,
		`INSERT INTO specific_current_mappings_cache_2_10 (tag_id, hash_id) VALUES (?, ?);`,
		15, 1,
	)
	mustExec(
		t,
		cachesDB,
		`INSERT INTO specific_deleted_mappings_cache_2_10 (tag_id, hash_id) VALUES (?, ?);`,
		17, 1,
	)
	mustExec(
		t,
		cachesDB,
		`INSERT INTO specific_pending_mappings_cache_2_10 (tag_id, hash_id) VALUES (?, ?);`,
		16, 1,
	)
	mustExec(
		t,
		cachesDB,
		`INSERT INTO specific_display_current_mappings_cache_2_10 (tag_id, hash_id) VALUES (?, ?);`,
		13, 1,
	)
	mustExec(
		t,
		cachesDB,
		`INSERT INTO specific_display_pending_mappings_cache_2_10 (tag_id, hash_id) VALUES (?, ?);`,
		14, 1,
	)
	mustExec(
		t,
		cachesDB,
		`INSERT INTO actual_tag_siblings_lookup_cache_1 (bad_tag_id, ideal_tag_id) VALUES (?, ?);`,
		3, 11,
	)
	mustExec(
		t,
		cachesDB,
		`INSERT INTO actual_tag_parents_lookup_cache_1 (child_tag_id, ancestor_tag_id) VALUES (?, ?);`,
		11, 12,
	)
	mustExec(
		t,
		cachesDB,
		`INSERT INTO actual_tag_siblings_lookup_cache_10 (bad_tag_id, ideal_tag_id) VALUES (?, ?), (?, ?), (?, ?);`,
		4, 7,
		5, 9,
		6, 11,
	)
	mustExec(
		t,
		cachesDB,
		`INSERT INTO actual_tag_parents_lookup_cache_10 (child_tag_id, ancestor_tag_id) VALUES (?, ?), (?, ?), (?, ?);`,
		7, 8,
		9, 10,
		11, 12,
	)

	mappingsDB := openSQLiteForTest(t, mappingsPath)
	defer mappingsDB.Close()

	mustExec(t, mappingsDB, `CREATE TABLE current_mappings_1 (tag_id INTEGER, hash_id INTEGER, PRIMARY KEY (tag_id, hash_id));`)
	mustExec(t, mappingsDB, `CREATE TABLE deleted_mappings_1 (tag_id INTEGER, hash_id INTEGER, PRIMARY KEY (tag_id, hash_id));`)
	mustExec(t, mappingsDB, `CREATE TABLE pending_mappings_1 (tag_id INTEGER, hash_id INTEGER, PRIMARY KEY (tag_id, hash_id));`)
	mustExec(t, mappingsDB, `CREATE TABLE petitioned_mappings_1 (tag_id INTEGER, hash_id INTEGER, reason_id INTEGER, PRIMARY KEY (tag_id, hash_id));`)
	mustExec(t, mappingsDB, `CREATE TABLE current_mappings_10 (tag_id INTEGER, hash_id INTEGER, PRIMARY KEY (tag_id, hash_id));`)
	mustExec(t, mappingsDB, `CREATE TABLE deleted_mappings_10 (tag_id INTEGER, hash_id INTEGER, PRIMARY KEY (tag_id, hash_id));`)
	mustExec(t, mappingsDB, `CREATE TABLE pending_mappings_10 (tag_id INTEGER, hash_id INTEGER, PRIMARY KEY (tag_id, hash_id));`)
	mustExec(t, mappingsDB, `CREATE TABLE petitioned_mappings_10 (tag_id INTEGER, hash_id INTEGER, reason_id INTEGER, PRIMARY KEY (tag_id, hash_id));`)

	mustExec(
		t,
		mappingsDB,
		`INSERT INTO current_mappings_1 (tag_id, hash_id) VALUES (?, ?), (?, ?), (?, ?);`,
		1, 1,
		2, 1,
		2, 2,
	)
	mustExec(
		t,
		mappingsDB,
		`INSERT INTO deleted_mappings_1 (tag_id, hash_id) VALUES (?, ?);`,
		3, 1,
	)
	mustExec(
		t,
		mappingsDB,
		`INSERT INTO current_mappings_10 (tag_id, hash_id) VALUES (?, ?), (?, ?);`,
		2, 1,
		4, 1,
	)
	mustExec(
		t,
		mappingsDB,
		`INSERT INTO pending_mappings_10 (tag_id, hash_id) VALUES (?, ?);`,
		5, 1,
	)
	mustExec(
		t,
		mappingsDB,
		`INSERT INTO petitioned_mappings_10 (tag_id, hash_id, reason_id) VALUES (?, ?, ?);`,
		6, 1, 1,
	)

	return dir, testFixture{
		hash1Hex:                        hash1,
		hash2Hex:                        hash2,
		hash3Hex:                        hash3,
		unknownHashHex:                  unknownHash,
		hash1Blurhash:                   blurhash,
		hash1PixelHashHex:               pixelHash,
		hash1IPFSMultihash:              ipfsMultihash,
		clientAPIServiceKey:             clientAPIKey,
		localTagServiceKeyHex:           hex.EncodeToString(localTagKey),
		downloaderTagsServiceKeyHex:     hex.EncodeToString(downloaderTagsKey),
		combinedTagServiceKeyHex:        hex.EncodeToString(combinedTagsKey),
		localFilesServiceKeyHex:         hex.EncodeToString(localFilesKey),
		allKnownFilesServiceKeyHex:      hex.EncodeToString(combinedFilesKey),
		hydrusLocalFilesServiceKeyHex:   hex.EncodeToString(hydrusLocalFilesKey),
		combinedLocalMediaServiceKeyHex: hex.EncodeToString(combinedLocalMediaKey),
		ipfsServiceKeyHex:               hex.EncodeToString(ipfsKey),
	}
}

func seedHydrusDBTestStorage(t *testing.T, db *sql.DB, dbDir string) {
	t.Helper()

	fileRoot := clientfiles.DefaultFileRoot(dbDir)
	thumbnailRoot := clientfiles.DefaultThumbnailRoot(dbDir)

	mustExec(t, db, `INSERT INTO current_storage_granularity (granularity) VALUES (?);`, clientfiles.DefaultPrefixLength)
	mustExec(t, db, `INSERT INTO current_client_files_locations (location_id, location) VALUES (?, ?), (?, ?);`, 1, fileRoot, 2, thumbnailRoot)
	mustExec(t, db, `INSERT INTO ideal_client_files_locations (location_id, weight, max_num_bytes) VALUES (?, ?, NULL);`, 1, 1)
	mustExec(t, db, `INSERT INTO ideal_thumbnail_override_location (location_id) VALUES (?);`, 2)

	for _, prefix := range hydrusDBTestStoragePrefixes(clientfiles.KindFile, clientfiles.DefaultPrefixLength) {
		mustExec(t, db, `INSERT INTO client_files_subfolders (prefix, location_id) VALUES (?, ?);`, prefix, 1)
	}

	for _, prefix := range hydrusDBTestStoragePrefixes(clientfiles.KindThumbnail, clientfiles.DefaultPrefixLength) {
		mustExec(t, db, `INSERT INTO client_files_subfolders (prefix, location_id) VALUES (?, ?);`, prefix, 2)
	}

	if err := os.MkdirAll(fileRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(fileRoot) error = %v", err)
	}

	if err := os.MkdirAll(thumbnailRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(thumbnailRoot) error = %v", err)
	}
}

func hydrusDBTestStoragePrefixes(kind clientfiles.Kind, prefixLength int) []string {
	prefixes := []string{}
	var build func(string, int)
	build = func(prefix string, remaining int) {
		if remaining == 0 {
			prefixes = append(prefixes, string(kind)+prefix)
			return
		}

		for _, digit := range "0123456789abcdef" {
			build(prefix+string(digit), remaining-1)
		}
	}

	build("", prefixLength)
	return prefixes
}

func createEmptySQLiteFile(t *testing.T, path string) {
	t.Helper()

	db := openSQLiteForTest(t, path)
	mustExec(t, db, `PRAGMA user_version = 0;`)
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func openSQLiteForTest(t *testing.T, path string) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open(%q) error = %v", path, err)
	}

	return db
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()

	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("Exec(%q) error = %v", query, err)
	}
}

func mustDecodeHex(t *testing.T, value string) []byte {
	t.Helper()

	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("hex.DecodeString(%q) error = %v", value, err)
	}

	return decoded
}

func TestResolveBundlePaths_RequiresBundleFiles(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "client.db"), []byte(""), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := resolveBundlePaths(dir)
	if err == nil {
		t.Fatal("resolveBundlePaths() error = nil, want error")
	}
}
