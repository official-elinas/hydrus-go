package hydrusdb

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/official-elinas/hydrus-go/internal/core/services"
)

const (
	metaTypeJSONOK             = 0
	metaTypeJSONBytes          = 1
	metaTypeHydrusSerializable = 2

	serialisableTypeDictionary = 21
	serialisableTypeList       = 26

	ratingStateLike    = 0
	ratingStateDislike = 1
	ratingStateNull    = 2
	ratingStateMixed   = 4
)

var ratingShapeLookup = map[int]string{
	0:   "circle",
	1:   "square",
	2:   "fat star",
	3:   "pentagram star",
	4:   "six point star",
	5:   "eight point star",
	6:   "x shape",
	7:   "square cross",
	30:  "triangle up",
	31:  "triangle down",
	32:  "triangle right",
	33:  "triangle left",
	40:  "diamond",
	42:  "rhombus right",
	43:  "rhombus left",
	44:  "hourglass",
	50:  "pentagon",
	60:  "hexagon",
	61:  "small hexagon",
	101: "heart",
	102: "teardrop",
	103: "crescent moon",
}

func applyServiceExtras(
	serviceType services.Type,
	dictionaryString string,
	service *services.Service,
) error {
	if !isRatingService(serviceType) || strings.TrimSpace(dictionaryString) == "" {
		return nil
	}

	dictionary, err := decodeSerialisableDictionaryString(dictionaryString)
	if err != nil {
		return fmt.Errorf("decode service dictionary: %w", err)
	}

	if value, ok, err := mapBool(dictionary, "show_in_thumbnail"); err != nil {
		return err
	} else if ok {
		service.ShowInThumbnail = &value
	}

	if value, ok, err := mapBool(dictionary, "show_in_thumbnail_even_when_null"); err != nil {
		return err
	} else if ok {
		service.ShowInThumbnailEvenWhenNull = &value
	}

	if colours, ok, err := parseRatingColours(dictionary["colours"], serviceType); err != nil {
		return err
	} else if ok {
		service.Colours = colours
	}

	if serviceType == services.TypeLocalRatingLike ||
		serviceType == services.TypeLocalRatingNumerical ||
		serviceType == services.TypeRatingLikeRepository ||
		serviceType == services.TypeRatingNumericalRepository {
		if ratingSVG, exists := dictionary["rating_svg"]; exists && ratingSVG != nil {
			service.StarShape = "svg"
		} else if shape, ok, err := mapInt(dictionary, "shape"); err != nil {
			return err
		} else if ok {
			service.StarShape = ratingShapeLookup[shape]
		}
	}

	if serviceType == services.TypeLocalRatingNumerical ||
		serviceType == services.TypeRatingNumericalRepository {
		allowsZero, ok, err := mapBool(dictionary, "allow_zero")
		if err != nil {
			return err
		}
		if ok {
			service.AllowsZero = &allowsZero
		}

		numStars, ok, err := mapInt(dictionary, "num_stars")
		if err != nil {
			return err
		}
		if ok {
			minStars := 1
			if allowsZero {
				minStars = 0
			}
			maxStars := numStars
			service.MinStars = &minStars
			service.MaxStars = &maxStars
		}
	}

	return nil
}

func isRatingService(serviceType services.Type) bool {
	switch serviceType {
	case services.TypeLocalRatingLike,
		services.TypeLocalRatingNumerical,
		services.TypeLocalRatingIncDec,
		services.TypeRatingLikeRepository,
		services.TypeRatingNumericalRepository:
		return true
	default:
		return false
	}
}

func parseRatingColours(
	value any,
	serviceType services.Type,
) (map[string]services.RatingColour, bool, error) {
	if value == nil {
		return nil, false, nil
	}

	items, ok := value.([]any)
	if !ok {
		return nil, false, fmt.Errorf("rating colours had unexpected type %T", value)
	}

	stateNames := map[int]string{}
	switch serviceType {
	case services.TypeLocalRatingIncDec:
		stateNames = map[int]string{
			ratingStateLike:  "like",
			ratingStateMixed: "mixed",
		}
	default:
		stateNames = map[int]string{
			ratingStateLike:    "like",
			ratingStateDislike: "dislike",
			ratingStateNull:    "null",
			ratingStateMixed:   "mixed",
		}
	}

	colours := map[string]services.RatingColour{}
	for _, itemValue := range items {
		item, ok := itemValue.([]any)
		if !ok || len(item) != 2 {
			return nil, false, fmt.Errorf("rating colour item had unexpected shape")
		}

		state, err := anyToInt(item[0])
		if err != nil {
			return nil, false, err
		}

		name, ok := stateNames[state]
		if !ok {
			continue
		}

		rgbPairs, ok := item[1].([]any)
		if !ok || len(rgbPairs) != 2 {
			return nil, false, fmt.Errorf("rating colour RGB pair had unexpected shape")
		}

		pen, err := rgbHex(rgbPairs[0])
		if err != nil {
			return nil, false, err
		}

		brush, err := rgbHex(rgbPairs[1])
		if err != nil {
			return nil, false, err
		}

		colours[name] = services.RatingColour{Pen: pen, Brush: brush}
	}

	return colours, len(colours) > 0, nil
}

func rgbHex(value any) (string, error) {
	rgb, ok := value.([]any)
	if !ok || len(rgb) != 3 {
		return "", fmt.Errorf("RGB value had unexpected shape")
	}

	red, err := anyToInt(rgb[0])
	if err != nil {
		return "", err
	}

	green, err := anyToInt(rgb[1])
	if err != nil {
		return "", err
	}

	blue, err := anyToInt(rgb[2])
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("#%02X%02X%02X", red, green, blue), nil
}

func decodeSerialisableDictionaryString(raw string) (map[string]any, error) {
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, fmt.Errorf("unmarshal service dictionary: %w", err)
	}

	value, err := decodeHydrusSerialisable(decoded)
	if err != nil {
		return nil, err
	}

	dictionary, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("decoded service dictionary had type %T", value)
	}

	return dictionary, nil
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
	case serialisableTypeDictionary:
		return decodeHydrusDictionary(serialisableInfo)
	case serialisableTypeList:
		return decodeHydrusList(serialisableInfo)
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

		key, err := decodeHydrusMetaTuple(pair[0])
		if err != nil {
			return nil, err
		}

		decodedValue, err := decodeHydrusMetaTuple(pair[1])
		if err != nil {
			return nil, err
		}

		keyString, ok := key.(string)
		if !ok {
			return nil, fmt.Errorf("dictionary key had type %T", key)
		}

		dictionary[keyString] = decodedValue
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
	case metaTypeJSONOK:
		return tuple[1], nil
	case metaTypeJSONBytes:
		encoded, ok := tuple[1].(string)
		if !ok {
			return nil, fmt.Errorf("byte meta tuple had type %T", tuple[1])
		}

		decoded, err := hex.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("decode meta bytes: %w", err)
		}

		return decoded, nil
	case metaTypeHydrusSerializable:
		return decodeHydrusSerialisable(tuple[1])
	default:
		return nil, fmt.Errorf("unsupported meta serialisable type %d", metaType)
	}
}

func mapBool(values map[string]any, key string) (bool, bool, error) {
	raw, ok := values[key]
	if !ok {
		return false, false, nil
	}

	value, ok := raw.(bool)
	if !ok {
		return false, false, fmt.Errorf("%s had type %T", key, raw)
	}

	return value, true, nil
}

func mapInt(values map[string]any, key string) (int, bool, error) {
	raw, ok := values[key]
	if !ok {
		return 0, false, nil
	}

	value, err := anyToInt(raw)
	if err != nil {
		return 0, false, fmt.Errorf("%s: %w", key, err)
	}

	return value, true, nil
}

func anyToInt(value any) (int, error) {
	floatValue, ok := value.(float64)
	if !ok {
		return 0, fmt.Errorf("expected JSON number, got %T", value)
	}

	return int(floatValue), nil
}
