package hydrusdb

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

const (
	hydrusURLTypePost      = 0
	hydrusURLTypeUnknown   = 5
	otherbooruPostURLName  = "otherbooru file page"
	unknownURLName         = "unknown url"
	unknownURLParseFailure = "unknown url class"
)

var hydrusURLTypeStringLookup = map[int]string{
	hydrusURLTypePost:    "post url",
	hydrusURLTypeUnknown: "unknown url",
}

type detailedKnownURL struct {
	normalisedURL     string
	urlType           int
	urlTypeString     string
	matchName         string
	canParse          bool
	cannotParseReason string
}

func buildDetailedKnownURLsByHashID(
	knownURLsByHashID map[int64][]string,
) map[int64][]map[string]any {
	detailedKnownURLsByHashID := make(map[int64][]map[string]any, len(knownURLsByHashID))
	for hashID, knownURLs := range knownURLsByHashID {
		detailedKnownURLsByHashID[hashID] = buildDetailedKnownURLsPayload(knownURLs)
	}

	return detailedKnownURLsByHashID
}

func buildDetailedKnownURLsPayload(knownURLs []string) []map[string]any {
	if len(knownURLs) == 0 {
		return []map[string]any{}
	}

	detailedKnownURLs := make([]map[string]any, 0, len(knownURLs))
	for _, knownURL := range knownURLs {
		detailedKnownURL, ok := describeDetailedKnownURL(knownURL)
		if !ok {
			continue
		}

		detailedKnownURLs = append(detailedKnownURLs, detailedKnownURL.payload())
	}

	return detailedKnownURLs
}

func describeDetailedKnownURL(knownURL string) (detailedKnownURL, bool) {
	parsedURL, ok := parseFullURL(knownURL)
	if !ok {
		return detailedKnownURL{}, false
	}

	if detailedKnownURL, ok := classifyOtherbooruPostURL(parsedURL); ok {
		return detailedKnownURL, true
	}

	normalisedURL := normaliseUnknownURL(parsedURL)
	return detailedKnownURL{
		normalisedURL:     normalisedURL,
		urlType:           hydrusURLTypeUnknown,
		urlTypeString:     hydrusURLTypeStringLookup[hydrusURLTypeUnknown],
		matchName:         unknownURLName,
		canParse:          false,
		cannotParseReason: unknownURLParseFailure,
	}, true
}

func parseFullURL(rawURL string) (*url.URL, bool) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, false
	}

	scheme := strings.ToLower(strings.TrimSpace(parsedURL.Scheme))
	if scheme != "http" && scheme != "https" {
		return nil, false
	}

	host := strings.TrimSpace(parsedURL.Hostname())
	if host == "" {
		return nil, false
	}

	clone := *parsedURL
	clone.Scheme = scheme
	clone.Host = normaliseURLHost(parsedURL)
	clone.Fragment = ""
	clone.RawFragment = ""

	return &clone, true
}

func normaliseUnknownURL(parsedURL *url.URL) string {
	clone := *parsedURL
	clone.Fragment = ""
	clone.RawFragment = ""
	return clone.String()
}

func normaliseURLHost(parsedURL *url.URL) string {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(parsedURL.Hostname()), "."))
	port := strings.TrimSpace(parsedURL.Port())
	if port == "" {
		return host
	}

	return net.JoinHostPort(host, port)
}

func canonicalURLHostForMatching(parsedURL *url.URL) string {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(parsedURL.Hostname()), "."))
	port := strings.TrimSpace(parsedURL.Port())
	switch {
	case strings.EqualFold(parsedURL.Scheme, "http") && port == "80":
		port = ""
	case strings.EqualFold(parsedURL.Scheme, "https") && port == "443":
		port = ""
	}

	if port == "" {
		return host
	}

	return net.JoinHostPort(host, port)
}

func classifyOtherbooruPostURL(parsedURL *url.URL) (detailedKnownURL, bool) {
	if canonicalURLHostForMatching(parsedURL) != "otherbooru.org" {
		return detailedKnownURL{}, false
	}

	if parsedURL.Path != "/index.php" {
		return detailedKnownURL{}, false
	}

	query := parsedURL.Query()
	id := strings.TrimSpace(query.Get("id"))
	if id == "" || query.Get("page") != "post" || query.Get("s") != "view" {
		return detailedKnownURL{}, false
	}

	normalisedURL := *parsedURL
	normalisedURL.Scheme = "https"
	normalisedURL.Host = "otherbooru.org"
	normalisedURL.Fragment = ""
	normalisedURL.RawFragment = ""
	normalisedURL.RawQuery = url.Values{
		"id":   []string{id},
		"page": []string{"post"},
		"s":    []string{"view"},
	}.Encode()

	return detailedKnownURL{
		normalisedURL:     normalisedURL.String(),
		urlType:           hydrusURLTypePost,
		urlTypeString:     hydrusURLTypeStringLookup[hydrusURLTypePost],
		matchName:         otherbooruPostURLName,
		canParse:          false,
		cannotParseReason: fmt.Sprintf("Could not find a parser for %s URL Class!", otherbooruPostURLName),
	}, true
}

func (d detailedKnownURL) payload() map[string]any {
	payload := map[string]any{
		"normalised_url":  d.normalisedURL,
		"url_type":        d.urlType,
		"url_type_string": d.urlTypeString,
		"match_name":      d.matchName,
		"can_parse":       d.canParse,
	}

	if !d.canParse {
		payload["cannot_parse_reason"] = d.cannotParseReason
	}

	return payload
}
