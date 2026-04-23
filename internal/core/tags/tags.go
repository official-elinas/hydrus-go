package tags

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
)

var (
	ErrEmptyTag                = errors.New("received a zero-length tag")
	reLeadingSingleColonNoMore = regexp.MustCompile(`^:[^:]+$`)
)

// Clean normalizes a tag string toward Hydrus semantics.
func Clean(tag string) string {
	tag = truncateRunes(tag, 1024)
	tag = strings.ToLower(tag)

	if reLeadingSingleColonNoMore.MatchString(tag) {
		tag = ":" + tag
	}

	if strings.Contains(tag, ":") {
		tag = StripTextOfGumpf(tag)
		namespace, subtag := Split(tag)
		namespace = StripTextOfGumpf(namespace)
		subtag = StripTextOfGumpf(subtag)
		return Combine(namespace, subtag)
	}

	return StripTextOfGumpf(tag)
}

// CheckNotEmpty rejects zero-length subtags after normalization.
func CheckNotEmpty(tag string) error {
	_, subtag := Split(tag)
	if subtag == "" {
		return ErrEmptyTag
	}

	return nil
}

// Split separates a Hydrus tag into namespace and subtag.
func Split(tag string) (string, string) {
	parts := strings.SplitN(tag, ":", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}

	return "", tag
}

// Combine reconstructs a Hydrus tag from namespace and subtag.
func Combine(namespace string, subtag string) string {
	if namespace == "" {
		if strings.Contains(subtag, ":") {
			return ":" + subtag
		}

		return subtag
	}

	return namespace + ":" + subtag
}

// StripTextOfGumpf removes control characters and normalizes whitespace.
func StripTextOfGumpf(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}

		return r
	}, value)

	value = strings.Join(strings.Fields(value), " ")
	return strings.TrimSpace(value)
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}

	return string(runes[:limit])
}
