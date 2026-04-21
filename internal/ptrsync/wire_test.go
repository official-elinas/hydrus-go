package ptrsync

import (
	"bytes"
	"compress/zlib"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestDecodeAccountResponse(t *testing.T) {
	t.Run("decodes the daemon account subset and ignores unsupported nested fields", func(t *testing.T) {
		bannedCreated := int64(1700000300)
		bannedExpires := int64(1700000400)

		body := hydrusArgsBytes(t, hydrusDictEntry{
			key: "account",
			metaValue: metaJSON(
				[]any{
					strings.Repeat("aa", 32),
					unsupportedSerialisable(102),
					int64(1699990000),
					int64(1700000100),
					serialisableDictionaryString(t,
						hydrusDictEntry{key: "banned_info", metaValue: metaJSON([]any{"banned", bannedCreated, bannedExpires})},
						hydrusDictEntry{key: "bandwidth_tracker", metaValue: metaHydrus(unsupportedSerialisable(39))},
						hydrusDictEntry{key: "message", metaValue: metaJSON("shared read-only")},
						hydrusDictEntry{key: "message_created", metaValue: metaJSON(int64(1699990100))},
					),
				},
			),
		})

		account, err := decodeAccountResponse(body)
		if err != nil {
			t.Fatalf("decodeAccountResponse() error = %v", err)
		}

		if got := hex.EncodeToString(account.AccountKey); got != strings.Repeat("aa", 32) {
			t.Fatalf("account.AccountKey = %q, want %q", got, strings.Repeat("aa", 32))
		}

		if account.Created != 1699990000 {
			t.Fatalf("account.Created = %d, want 1699990000", account.Created)
		}

		if account.Expires == nil || *account.Expires != 1700000100 {
			t.Fatalf("account.Expires = %#v, want 1700000100", account.Expires)
		}

		if account.Message != "shared read-only" {
			t.Fatalf("account.Message = %q, want %q", account.Message, "shared read-only")
		}

		if account.MessageCreated != 1699990100 {
			t.Fatalf("account.MessageCreated = %d, want 1699990100", account.MessageCreated)
		}

		if account.BannedReason != "banned" {
			t.Fatalf("account.BannedReason = %q, want %q", account.BannedReason, "banned")
		}

		if account.BannedCreated == nil || *account.BannedCreated != bannedCreated {
			t.Fatalf("account.BannedCreated = %#v, want %d", account.BannedCreated, bannedCreated)
		}

		if account.BannedExpires == nil || *account.BannedExpires != bannedExpires {
			t.Fatalf("account.BannedExpires = %#v, want %d", account.BannedExpires, bannedExpires)
		}
	})
}

func TestDecodeOptionsResponse(t *testing.T) {
	t.Run("decodes service options from Hydrus args", func(t *testing.T) {
		body := hydrusArgsBytes(t, hydrusDictEntry{
			key:       "service_options",
			metaValue: metaHydrus(serialisableDictionary(hydrusDictEntry{key: "update_period", metaValue: metaJSON(int64(3600))}, hydrusDictEntry{key: "nullification_period", metaValue: metaJSON(int64(86400))})),
		})

		options, err := decodeOptionsResponse(body)
		if err != nil {
			t.Fatalf("decodeOptionsResponse() error = %v", err)
		}

		if options.UpdatePeriod != 3600 {
			t.Fatalf("options.UpdatePeriod = %d, want 3600", options.UpdatePeriod)
		}

		if options.NullificationPeriod != 86400 {
			t.Fatalf("options.NullificationPeriod = %d, want 86400", options.NullificationPeriod)
		}
	})

	t.Run("rejects oversized decompressed payloads", func(t *testing.T) {
		originalLimit := ptrSyncMaxDecompressedResponseBytes
		ptrSyncMaxDecompressedResponseBytes = 64
		defer func() { ptrSyncMaxDecompressedResponseBytes = originalLimit }()

		body := hydrusArgsBytes(
			t,
			hydrusDictEntry{key: "service_options", metaValue: metaHydrus(serialisableDictionary(hydrusDictEntry{key: "update_period", metaValue: metaJSON(int64(3600))}, hydrusDictEntry{key: "nullification_period", metaValue: metaJSON(int64(86400))}))},
			hydrusDictEntry{key: "padding", metaValue: metaJSON(strings.Repeat("x", 256))},
		)

		_, err := decodeOptionsResponse(body)
		if err == nil || !strings.Contains(err.Error(), "exceeded") {
			t.Fatalf("decodeOptionsResponse() error = %v, want decompressed size limit failure", err)
		}
	})
}

func TestDecodeTagFilterResponse(t *testing.T) {
	t.Run("decodes raw tag filter rules", func(t *testing.T) {
		body := hydrusArgsBytes(t, hydrusDictEntry{
			key:       "tag_filter",
			metaValue: metaHydrus(serialisableTagFilter(map[string]int{":": 1, "creator:": 0})),
		})

		tagFilter, err := decodeTagFilterResponse(body)
		if err != nil {
			t.Fatalf("decodeTagFilterResponse() error = %v", err)
		}

		if tagFilter.Rules[":"] != 1 || tagFilter.Rules["creator:"] != 0 {
			t.Fatalf("tagFilter.Rules = %#v, want : => 1 and creator: => 0", tagFilter.Rules)
		}
	})
}

func TestDecodeMetadataResponse(t *testing.T) {
	t.Run("decodes metadata slice update rows", func(t *testing.T) {
		body := hydrusArgsBytes(t, hydrusDictEntry{
			key:       "metadata_slice",
			metaValue: metaHydrus(serialisableMetadata(1700000200, metadataRow{updateIndex: 0, updateHashes: []string{strings.Repeat("11", 32), strings.Repeat("22", 32)}, begin: 10, end: 20}, metadataRow{updateIndex: 1, updateHashes: []string{strings.Repeat("33", 32)}, begin: 21, end: 30})),
		})

		metadata, err := decodeMetadataResponse(body)
		if err != nil {
			t.Fatalf("decodeMetadataResponse() error = %v", err)
		}

		if metadata.NextUpdateDue != 1700000200 {
			t.Fatalf("metadata.NextUpdateDue = %d, want 1700000200", metadata.NextUpdateDue)
		}

		if len(metadata.Updates) != 2 {
			t.Fatalf("len(metadata.Updates) = %d, want 2", len(metadata.Updates))
		}

		if metadata.Updates[0].UpdateIndex != 0 || metadata.Updates[1].UpdateIndex != 1 {
			t.Fatalf("metadata update indices = [%d %d], want [0 1]", metadata.Updates[0].UpdateIndex, metadata.Updates[1].UpdateIndex)
		}

		if got := hex.EncodeToString(metadata.Updates[0].UpdateHashes[0]); got != strings.Repeat("11", 32) {
			t.Fatalf("first metadata hash = %q, want %q", got, strings.Repeat("11", 32))
		}
	})
}

type hydrusDictEntry struct {
	key       string
	metaValue any
}

type metadataRow struct {
	updateIndex  int64
	updateHashes []string
	begin        int64
	end          int64
}

func hydrusArgsBytes(t *testing.T, entries ...hydrusDictEntry) []byte {
	t.Helper()
	return hydrusNetworkBytes(t, serialisableDictionary(entries...))
}

func hydrusNetworkBytes(t *testing.T, serialisable any) []byte {
	t.Helper()

	payload, err := json.Marshal(serialisable)
	if err != nil {
		t.Fatalf("json.Marshal(serialisable) error = %v", err)
	}

	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("writer.Write() error = %v", err)
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close() error = %v", err)
	}

	return compressed.Bytes()
}

func serialisableDictionary(entries ...hydrusDictEntry) any {
	pairs := make([]any, 0, len(entries))
	for _, entry := range entries {
		pairs = append(pairs, []any{metaJSON(entry.key), entry.metaValue})
	}

	return []any{hydrusSerialisableTypeDictionary, 2, pairs}
}

func serialisableDictionaryString(t *testing.T, entries ...hydrusDictEntry) string {
	t.Helper()

	payload, err := json.Marshal(serialisableDictionary(entries...))
	if err != nil {
		t.Fatalf("json.Marshal(serialisableDictionary) error = %v", err)
	}

	return string(payload)
}

func serialisableTagFilter(rules map[string]int) any {
	items := make([]any, 0, len(rules))
	for key, rule := range rules {
		items = append(items, []any{key, rule})
	}

	return []any{hydrusSerialisableTypeTagFilter, 1, items}
}

func serialisableMetadata(nextUpdateDue int64, rows ...metadataRow) any {
	serialisableRows := make([]any, 0, len(rows))
	for _, row := range rows {
		serialisableRows = append(serialisableRows, []any{row.updateIndex, row.updateHashes, row.begin, row.end})
	}

	return []any{hydrusSerialisableTypeMetadata, 1, []any{serialisableRows, nextUpdateDue}}
}

func unsupportedSerialisable(serialisableType int) any {
	return []any{serialisableType, 1, []any{}}
}

func metaJSON(value any) any {
	return []any{hydrusMetaTypeJSONOK, value}
}

func metaHydrus(value any) any {
	return []any{hydrusMetaTypeHydrusSerializable, value}
}

func formatRequestPath(path string, rawQuery string) string {
	if strings.TrimSpace(rawQuery) == "" {
		return path
	}

	return fmt.Sprintf("%s?%s", path, rawQuery)
}
