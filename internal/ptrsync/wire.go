// Package ptrsync manages daemon-owned Public Tag Repository sync state.
package ptrsync

import (
	"bytes"
	"compress/zlib"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	coreptrsync "github.com/official-elinas/hydrus-go/internal/core/ptrsync"
)

const (
	hydrusMetaTypeJSONOK             = 0
	hydrusMetaTypeJSONBytes          = 1
	hydrusMetaTypeHydrusSerializable = 2

	hydrusSerialisableTypeDictionary        = 21
	hydrusSerialisableTypeList              = 26
	hydrusSerialisableTypeContentUpdate     = 34
	hydrusSerialisableTypeDefinitionsUpdate = 36
	hydrusSerialisableTypeMetadata          = 37
	hydrusSerialisableTypeTagFilter         = 44
)

// Hydrus PTR endpoint bodies are zlib-compressed JSON whose top-level values
// are Hydrus serialisable tuples. Raw HTTP body limits are enforced in
// client.go, and decompressed limits are enforced when these bytes are inflated
// here.

func decodeAccountResponse(body []byte) (coreptrsync.AccountSnapshot, error) {
	args, err := decodeHydrusArgsBytes(body)
	if err != nil {
		return coreptrsync.AccountSnapshot{}, err
	}

	rawAccount, ok := args["account"]
	if !ok {
		return coreptrsync.AccountSnapshot{}, fmt.Errorf("Hydrus args missing account")
	}

	accountTuple, ok := rawAccount.([]any)
	if !ok || len(accountTuple) != 5 {
		return coreptrsync.AccountSnapshot{}, fmt.Errorf("account payload had unexpected shape")
	}

	accountKeyHex, ok := accountTuple[0].(string)
	if !ok {
		return coreptrsync.AccountSnapshot{}, fmt.Errorf("account key had type %T", accountTuple[0])
	}

	accountKey, err := hex.DecodeString(strings.TrimSpace(accountKeyHex))
	if err != nil {
		return coreptrsync.AccountSnapshot{}, fmt.Errorf("decode account key: %w", err)
	}

	created, err := anyToInt64(accountTuple[2])
	if err != nil {
		return coreptrsync.AccountSnapshot{}, fmt.Errorf("decode account created: %w", err)
	}

	expires, err := anyToOptionalInt64(accountTuple[3])
	if err != nil {
		return coreptrsync.AccountSnapshot{}, fmt.Errorf("decode account expires: %w", err)
	}

	dictionaryString, ok := accountTuple[4].(string)
	if !ok {
		return coreptrsync.AccountSnapshot{}, fmt.Errorf("account dictionary had type %T", accountTuple[4])
	}

	snapshot := coreptrsync.AccountSnapshot{
		AccountKey: accountKey,
		Created:    created,
		Expires:    expires,
	}

	if err := decodeAccountDictionaryString(dictionaryString, &snapshot); err != nil {
		return coreptrsync.AccountSnapshot{}, err
	}

	return snapshot, nil
}

func decodeOptionsResponse(body []byte) (coreptrsync.ServiceOptions, error) {
	args, err := decodeHydrusArgsBytes(body)
	if err != nil {
		return coreptrsync.ServiceOptions{}, err
	}

	rawOptions, ok := args["service_options"]
	if !ok {
		return coreptrsync.ServiceOptions{}, fmt.Errorf("Hydrus args missing service_options")
	}

	serviceOptions, ok := rawOptions.(map[string]any)
	if !ok {
		return coreptrsync.ServiceOptions{}, fmt.Errorf("service_options had type %T", rawOptions)
	}

	updatePeriod, err := mapInt64(serviceOptions, "update_period")
	if err != nil {
		return coreptrsync.ServiceOptions{}, err
	}

	nullificationPeriod, err := mapInt64(serviceOptions, "nullification_period")
	if err != nil {
		return coreptrsync.ServiceOptions{}, err
	}

	return coreptrsync.ServiceOptions{
		UpdatePeriod:        updatePeriod,
		NullificationPeriod: nullificationPeriod,
	}, nil
}

func decodeTagFilterResponse(body []byte) (coreptrsync.TagFilterSnapshot, error) {
	args, err := decodeHydrusArgsBytes(body)
	if err != nil {
		return coreptrsync.TagFilterSnapshot{}, err
	}

	rawTagFilter, ok := args["tag_filter"]
	if !ok {
		return coreptrsync.TagFilterSnapshot{}, fmt.Errorf("Hydrus args missing tag_filter")
	}

	tagFilter, ok := rawTagFilter.(coreptrsync.TagFilterSnapshot)
	if !ok {
		return coreptrsync.TagFilterSnapshot{}, fmt.Errorf("tag_filter had type %T", rawTagFilter)
	}

	return tagFilter, nil
}

func decodeMetadataResponse(body []byte) (coreptrsync.MetadataSlice, error) {
	args, err := decodeHydrusArgsBytes(body)
	if err != nil {
		return coreptrsync.MetadataSlice{}, err
	}

	rawMetadata, ok := args["metadata_slice"]
	if !ok {
		return coreptrsync.MetadataSlice{}, fmt.Errorf("Hydrus args missing metadata_slice")
	}

	metadata, ok := rawMetadata.(coreptrsync.MetadataSlice)
	if !ok {
		return coreptrsync.MetadataSlice{}, fmt.Errorf("metadata_slice had type %T", rawMetadata)
	}

	return metadata, nil
}

func classifyUpdatePayload(body []byte) (int, error) {
	decoded, err := decodeHydrusNetworkBytes(body)
	if err != nil {
		return 0, err
	}

	tuple, ok := decoded.([]any)
	if !ok {
		return 0, fmt.Errorf("expected serialisable tuple, got %T", decoded)
	}

	if len(tuple) != 3 && len(tuple) != 4 {
		return 0, fmt.Errorf("serialisable tuple had %d elements", len(tuple))
	}

	serialisableType, err := anyToInt(tuple[0])
	if err != nil {
		return 0, err
	}

	switch serialisableType {
	case hydrusSerialisableTypeDefinitionsUpdate:
		return 28, nil
	case hydrusSerialisableTypeContentUpdate:
		return 29, nil
	default:
		return 0, fmt.Errorf("unsupported PTR update serialisable type %d", serialisableType)
	}
}

func decodeHydrusArgsBytes(body []byte) (map[string]any, error) {
	decoded, err := decodeHydrusNetworkBytes(body)
	if err != nil {
		return nil, err
	}

	value, err := decodeHydrusSerialisable(decoded)
	if err != nil {
		return nil, fmt.Errorf("decode Hydrus serialisable payload: %w", err)
	}

	args, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("decoded Hydrus args had type %T", value)
	}

	return args, nil
}

func decodeHydrusNetworkBytes(body []byte) (any, error) {
	reader, err := zlib.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create zlib reader: %w", err)
	}
	defer reader.Close()

	payload, err := readLimitedBytes(reader, ptrSyncMaxDecompressedResponseBytes, "decompressed Hydrus payload")
	if err != nil {
		return nil, fmt.Errorf("read compressed Hydrus payload: %w", err)
	}

	return decodeJSONAny(payload)
}

func decodeJSONAny(payload []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()

	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode JSON payload: %w", err)
	}

	return decoded, nil
}

func decodeHydrusSerialisable(value any) (any, error) {
	tuple, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("expected serialisable tuple, got %T", value)
	}

	if len(tuple) != 3 && len(tuple) != 4 {
		return nil, fmt.Errorf("serialisable tuple had %d elements", len(tuple))
	}

	serialisableType, err := anyToInt(tuple[0])
	if err != nil {
		return nil, err
	}

	serialisableInfo := tuple[2]
	if len(tuple) == 4 {
		serialisableInfo = tuple[3]
	}

	switch serialisableType {
	case hydrusSerialisableTypeDictionary:
		return decodeHydrusDictionary(serialisableInfo)
	case hydrusSerialisableTypeList:
		return decodeHydrusList(serialisableInfo)
	case hydrusSerialisableTypeMetadata:
		return decodeHydrusMetadata(serialisableInfo)
	case hydrusSerialisableTypeTagFilter:
		return decodeHydrusTagFilter(serialisableInfo)
	default:
		return nil, fmt.Errorf("unsupported serialisable type %d", serialisableType)
	}
}

func decodeHydrusDictionary(value any) (map[string]any, error) {
	pairs, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("dictionary payload had type %T", value)
	}

	dictionary := map[string]any{}
	for _, pairValue := range pairs {
		pair, ok := pairValue.([]any)
		if !ok || len(pair) != 2 {
			return nil, fmt.Errorf("dictionary pair had unexpected shape")
		}

		keyValue, err := decodeHydrusMetaTuple(pair[0])
		if err != nil {
			return nil, err
		}

		key, ok := keyValue.(string)
		if !ok {
			return nil, fmt.Errorf("dictionary key had type %T", keyValue)
		}

		decodedValue, err := decodeHydrusMetaTuple(pair[1])
		if err != nil {
			return nil, err
		}

		dictionary[key] = decodedValue
	}

	return dictionary, nil
}

func decodeHydrusList(value any) ([]any, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("list payload had type %T", value)
	}

	decoded := make([]any, 0, len(items))
	for _, itemValue := range items {
		item, err := decodeHydrusMetaTuple(itemValue)
		if err != nil {
			return nil, err
		}

		decoded = append(decoded, item)
	}

	return decoded, nil
}

func decodeHydrusMetaTuple(value any) (any, error) {
	tuple, ok := value.([]any)
	if !ok || len(tuple) != 2 {
		return nil, fmt.Errorf("meta tuple had unexpected shape")
	}

	metaType, err := anyToInt(tuple[0])
	if err != nil {
		return nil, err
	}

	switch metaType {
	case hydrusMetaTypeJSONOK:
		return tuple[1], nil
	case hydrusMetaTypeJSONBytes:
		encoded, ok := tuple[1].(string)
		if !ok {
			return nil, fmt.Errorf("byte meta tuple had type %T", tuple[1])
		}

		decoded, err := hex.DecodeString(strings.TrimSpace(encoded))
		if err != nil {
			return nil, fmt.Errorf("decode meta bytes: %w", err)
		}

		return decoded, nil
	case hydrusMetaTypeHydrusSerializable:
		return decodeHydrusSerialisable(tuple[1])
	default:
		return nil, fmt.Errorf("unsupported meta serialisable type %d", metaType)
	}
}

func decodeHydrusMetadata(value any) (coreptrsync.MetadataSlice, error) {
	items, ok := value.([]any)
	if !ok || len(items) != 2 {
		return coreptrsync.MetadataSlice{}, fmt.Errorf("metadata payload had unexpected shape")
	}

	serialisableMetadata, ok := items[0].([]any)
	if !ok {
		return coreptrsync.MetadataSlice{}, fmt.Errorf("metadata rows had type %T", items[0])
	}

	nextUpdateDue, err := anyToInt64(items[1])
	if err != nil {
		return coreptrsync.MetadataSlice{}, fmt.Errorf("decode metadata next_update_due: %w", err)
	}

	updates := make([]coreptrsync.MetadataUpdate, 0, len(serialisableMetadata))
	for _, updateValue := range serialisableMetadata {
		updateTuple, ok := updateValue.([]any)
		if !ok || len(updateTuple) != 4 {
			return coreptrsync.MetadataSlice{}, fmt.Errorf("metadata row had unexpected shape")
		}

		updateIndex, err := anyToInt64(updateTuple[0])
		if err != nil {
			return coreptrsync.MetadataSlice{}, fmt.Errorf("decode metadata update index: %w", err)
		}

		hashValues, ok := updateTuple[1].([]any)
		if !ok {
			return coreptrsync.MetadataSlice{}, fmt.Errorf("metadata update hashes had type %T", updateTuple[1])
		}

		updateHashes := make([][]byte, 0, len(hashValues))
		for _, hashValue := range hashValues {
			hashHex, ok := hashValue.(string)
			if !ok {
				return coreptrsync.MetadataSlice{}, fmt.Errorf("metadata update hash had type %T", hashValue)
			}

			updateHash, err := hex.DecodeString(strings.TrimSpace(hashHex))
			if err != nil {
				return coreptrsync.MetadataSlice{}, fmt.Errorf("decode metadata update hash: %w", err)
			}

			updateHashes = append(updateHashes, updateHash)
		}

		begin, err := anyToInt64(updateTuple[2])
		if err != nil {
			return coreptrsync.MetadataSlice{}, fmt.Errorf("decode metadata begin: %w", err)
		}

		end, err := anyToInt64(updateTuple[3])
		if err != nil {
			return coreptrsync.MetadataSlice{}, fmt.Errorf("decode metadata end: %w", err)
		}

		updates = append(updates, coreptrsync.MetadataUpdate{
			UpdateIndex:  updateIndex,
			UpdateHashes: updateHashes,
			Begin:        begin,
			End:          end,
		})
	}

	return coreptrsync.MetadataSlice{
		Updates:       updates,
		NextUpdateDue: nextUpdateDue,
	}, nil
}

func decodeHydrusTagFilter(value any) (coreptrsync.TagFilterSnapshot, error) {
	rawRules, ok := value.([]any)
	if !ok {
		return coreptrsync.TagFilterSnapshot{}, fmt.Errorf("tag filter payload had type %T", value)
	}

	rules := make(map[string]int, len(rawRules))
	for _, ruleValue := range rawRules {
		ruleTuple, ok := ruleValue.([]any)
		if !ok || len(ruleTuple) != 2 {
			return coreptrsync.TagFilterSnapshot{}, fmt.Errorf("tag filter rule had unexpected shape")
		}

		tagSlice, ok := ruleTuple[0].(string)
		if !ok {
			return coreptrsync.TagFilterSnapshot{}, fmt.Errorf("tag filter key had type %T", ruleTuple[0])
		}

		rule, err := anyToInt(ruleTuple[1])
		if err != nil {
			return coreptrsync.TagFilterSnapshot{}, fmt.Errorf("decode tag filter rule: %w", err)
		}

		rules[tagSlice] = rule
	}

	return coreptrsync.TagFilterSnapshot{Rules: rules}, nil
}

func decodeAccountDictionaryString(raw string, snapshot *coreptrsync.AccountSnapshot) error {
	decoded, err := decodeJSONAny([]byte(raw))
	if err != nil {
		return fmt.Errorf("decode account dictionary: %w", err)
	}

	fieldDecoders := map[string]func(any) error{
		"message": func(metaValue any) error {
			value, err := decodeHydrusMetaTuple(metaValue)
			if err != nil {
				return err
			}

			if value == nil {
				snapshot.Message = ""
				return nil
			}

			message, ok := value.(string)
			if !ok {
				return fmt.Errorf("message had type %T", value)
			}

			snapshot.Message = message
			return nil
		},
		"message_created": func(metaValue any) error {
			value, err := decodeHydrusMetaTuple(metaValue)
			if err != nil {
				return err
			}

			messageCreated, err := anyToInt64(value)
			if err != nil {
				return err
			}

			snapshot.MessageCreated = messageCreated
			return nil
		},
		"banned_info": func(metaValue any) error {
			value, err := decodeHydrusMetaTuple(metaValue)
			if err != nil {
				return err
			}

			if value == nil {
				return nil
			}

			items, ok := value.([]any)
			if !ok || len(items) != 3 {
				return fmt.Errorf("banned_info had unexpected shape")
			}

			reason, ok := items[0].(string)
			if !ok {
				return fmt.Errorf("banned_info reason had type %T", items[0])
			}

			created, err := anyToInt64(items[1])
			if err != nil {
				return err
			}

			expires, err := anyToOptionalInt64(items[2])
			if err != nil {
				return err
			}

			snapshot.BannedReason = reason
			snapshot.BannedCreated = int64Ptr(created)
			snapshot.BannedExpires = expires
			return nil
		},
	}

	if err := decodeHydrusSerialisableDictionarySelective(decoded, fieldDecoders); err != nil {
		return fmt.Errorf("decode account dictionary: %w", err)
	}

	return nil
}

func decodeHydrusSerialisableDictionarySelective(
	value any,
	fieldDecoders map[string]func(any) error,
) error {
	tuple, ok := value.([]any)
	if !ok {
		return fmt.Errorf("expected serialisable tuple, got %T", value)
	}

	if len(tuple) != 3 && len(tuple) != 4 {
		return fmt.Errorf("serialisable tuple had %d elements", len(tuple))
	}

	serialisableType, err := anyToInt(tuple[0])
	if err != nil {
		return err
	}

	if serialisableType != hydrusSerialisableTypeDictionary {
		return fmt.Errorf("serialisable tuple had type %d, want %d", serialisableType, hydrusSerialisableTypeDictionary)
	}

	serialisableInfo := tuple[2]
	if len(tuple) == 4 {
		serialisableInfo = tuple[3]
	}

	return decodeHydrusDictionarySelective(serialisableInfo, fieldDecoders)
}

func decodeHydrusDictionarySelective(
	value any,
	fieldDecoders map[string]func(any) error,
) error {
	pairs, ok := value.([]any)
	if !ok {
		return fmt.Errorf("dictionary payload had type %T", value)
	}

	for _, pairValue := range pairs {
		pair, ok := pairValue.([]any)
		if !ok || len(pair) != 2 {
			return fmt.Errorf("dictionary pair had unexpected shape")
		}

		keyValue, err := decodeHydrusMetaTuple(pair[0])
		if err != nil {
			return err
		}

		key, ok := keyValue.(string)
		if !ok {
			return fmt.Errorf("dictionary key had type %T", keyValue)
		}

		fieldDecoder, ok := fieldDecoders[key]
		if !ok {
			continue
		}

		if err := fieldDecoder(pair[1]); err != nil {
			return fmt.Errorf("decode dictionary field %q: %w", key, err)
		}
	}

	return nil
}

func mapInt64(dictionary map[string]any, key string) (int64, error) {
	value, ok := dictionary[key]
	if !ok {
		return 0, fmt.Errorf("dictionary missing %q", key)
	}

	result, err := anyToInt64(value)
	if err != nil {
		return 0, fmt.Errorf("decode %q: %w", key, err)
	}

	return result, nil
}

func anyToInt(value any) (int, error) {
	result, err := anyToInt64(value)
	if err != nil {
		return 0, err
	}

	return int(result), nil
}

func anyToInt64(value any) (int64, error) {
	switch typed := value.(type) {
	case json.Number:
		if intValue, err := typed.Int64(); err == nil {
			return intValue, nil
		}

		floatValue, err := typed.Float64()
		if err != nil {
			return 0, fmt.Errorf("parse json.Number %q: %w", typed.String(), err)
		}

		if math.Trunc(floatValue) != floatValue {
			return 0, fmt.Errorf("number %v was not integral", floatValue)
		}

		return int64(floatValue), nil
	case float64:
		if math.Trunc(typed) != typed {
			return 0, fmt.Errorf("number %v was not integral", typed)
		}

		return int64(typed), nil
	case int:
		return int64(typed), nil
	case int8:
		return int64(typed), nil
	case int16:
		return int64(typed), nil
	case int32:
		return int64(typed), nil
	case int64:
		return typed, nil
	case uint:
		return int64(typed), nil
	case uint8:
		return int64(typed), nil
	case uint16:
		return int64(typed), nil
	case uint32:
		return int64(typed), nil
	case uint64:
		if typed > math.MaxInt64 {
			return 0, fmt.Errorf("number %d overflowed int64", typed)
		}

		return int64(typed), nil
	default:
		return 0, fmt.Errorf("expected integral number, got %T", value)
	}
}

func anyToOptionalInt64(value any) (*int64, error) {
	if value == nil {
		return nil, nil
	}

	result, err := anyToInt64(value)
	if err != nil {
		return nil, err
	}

	return int64Ptr(result), nil
}

func int64Ptr(value int64) *int64 {
	return &value
}
