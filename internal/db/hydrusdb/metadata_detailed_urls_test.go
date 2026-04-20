package hydrusdb

import "testing"

func TestBuildDetailedKnownURLsPayload(t *testing.T) {
	tests := []struct {
		name      string
		knownURLs []string
		wantLen   int
		assert    func(*testing.T, []map[string]any)
	}{
		{
			name:      "valid unknown full URL returns Hydrus unknown payload",
			knownURLs: []string{"https://img.weirdbooru.com/images/ab/cd/abcdblahblahblah.jpg"},
			wantLen:   1,
			assert: func(t *testing.T, payload []map[string]any) {
				t.Helper()

				if got := payload[0]["normalised_url"]; got != "https://img.weirdbooru.com/images/ab/cd/abcdblahblahblah.jpg" {
					t.Fatalf("payload[0][normalised_url] = %v, want weirdbooru image URL", got)
				}

				if got := payload[0]["url_type"]; got != hydrusURLTypeUnknown {
					t.Fatalf("payload[0][url_type] = %v, want %d", got, hydrusURLTypeUnknown)
				}
			},
		},
		{
			name:      "otherbooru post URL normalises to Hydrus order",
			knownURLs: []string{"https://otherbooru.org/index.php?page=post&s=view&id=123456"},
			wantLen:   1,
			assert: func(t *testing.T, payload []map[string]any) {
				t.Helper()

				if got := payload[0]["normalised_url"]; got != "https://otherbooru.org/index.php?id=123456&page=post&s=view" {
					t.Fatalf("payload[0][normalised_url] = %v, want normalised otherbooru URL", got)
				}

				if got := payload[0]["url_type"]; got != hydrusURLTypePost {
					t.Fatalf("payload[0][url_type] = %v, want %d", got, hydrusURLTypePost)
				}
			},
		},
		{
			name:      "default HTTPS port still matches seeded otherbooru post URL class",
			knownURLs: []string{"https://otherbooru.org:443/index.php?page=post&s=view&id=123456"},
			wantLen:   1,
			assert: func(t *testing.T, payload []map[string]any) {
				t.Helper()

				if got := payload[0]["match_name"]; got != "otherbooru file page" {
					t.Fatalf("payload[0][match_name] = %v, want otherbooru file page", got)
				}

				if got := payload[0]["normalised_url"]; got != "https://otherbooru.org/index.php?id=123456&page=post&s=view" {
					t.Fatalf("payload[0][normalised_url] = %v, want normalised otherbooru URL", got)
				}
			},
		},
		{
			name:      "non-full URLs are skipped like Hydrus URL-class exceptions",
			knownURLs: []string{"not a full url"},
			wantLen:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := buildDetailedKnownURLsPayload(tt.knownURLs)

			if len(payload) != tt.wantLen {
				t.Fatalf("len(payload) = %d, want %d", len(payload), tt.wantLen)
			}

			if tt.assert != nil {
				tt := tt.assert
				tt(t, payload)
			}
		})
	}
}
