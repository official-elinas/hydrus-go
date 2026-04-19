package daemonclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr string
	}{
		{
			name: "adds default http scheme",
			raw:  "127.0.0.1:45869",
			want: "http://127.0.0.1:45869",
		},
		{
			name: "strips path query and fragment",
			raw:  "https://example.test:9443/api?x=1#frag",
			want: "https://example.test:9443",
		},
		{
			name:    "rejects blank value",
			raw:     "   ",
			wantErr: "daemon URL is required",
		},
		{
			name:    "rejects hostless URL",
			raw:     "http:///missing-host",
			wantErr: "daemon URL must include a host",
		},
	}

	for _, tt := range tests {
		caseData := tt
		t.Run(caseData.name, func(t *testing.T) {
			got, err := normalizeBaseURL(caseData.raw)
			if caseData.wantErr != "" {
				if err == nil {
					t.Fatal("normalizeBaseURL() error = nil, want error")
				}

				if !strings.Contains(err.Error(), caseData.wantErr) {
					t.Fatalf("normalizeBaseURL() error = %v, want substring %q", err, caseData.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("normalizeBaseURL() error = %v", err)
			}

			if got != caseData.want {
				t.Fatalf("normalizeBaseURL() = %q, want %q", got, caseData.want)
			}
		})
	}
}

func TestClientSetConnectionClearsSessionKey(t *testing.T) {
	client := New()
	client.sessionKey = "stale-session"

	if err := client.SetConnection(" https://daemon.test:9443/path?x=1#frag ", "  access-key  "); err != nil {
		t.Fatalf("SetConnection() error = %v", err)
	}

	if client.BaseURL() != "https://daemon.test:9443" {
		t.Fatalf("client.BaseURL() = %q, want https://daemon.test:9443", client.BaseURL())
	}

	if client.AccessKey() != "access-key" {
		t.Fatalf("client.AccessKey() = %q, want access-key", client.AccessKey())
	}

	if client.sessionKey != "" {
		t.Fatalf("client.sessionKey = %q, want cleared session key", client.sessionKey)
	}
}

func TestClientBootstrapAndSessionAuth(t *testing.T) {
	accessKey := strings.Repeat("a", 64)
	sessionKey := "session-key-123"

	client := newClientWithRoundTripper(
		t,
		"https://daemon.test:9443",
		accessKey,
		roundTripFunc(func(r *http.Request) (*http.Response, error) {
			assertUserAgent(t, r)

			switch r.URL.Path {
			case "/verify_access_key":
				assertMethodAndPath(t, r, http.MethodGet, "/verify_access_key")
				assertHeader(t, r, "Hydrus-Client-API-Access-Key", accessKey)
				assertHeader(t, r, "Hydrus-Client-API-Session-Key", "")
				return jsonResponse(t, r, http.StatusOK, VerifyAccessKeyResponse{
					Name:              "desktop-test",
					HumanDescription:  "desktop contract",
					PermitsEverything: true,
				}), nil
			case "/session_key":
				assertMethodAndPath(t, r, http.MethodGet, "/session_key")
				assertHeader(t, r, "Hydrus-Client-API-Access-Key", accessKey)
				assertHeader(t, r, "Hydrus-Client-API-Session-Key", "")
				return jsonResponse(t, r, http.StatusOK, map[string]string{"session_key": sessionKey}), nil
			case "/v1/library/recent":
				assertMethodAndPath(t, r, http.MethodGet, "/v1/library/recent")
				assertHeader(t, r, "Hydrus-Client-API-Access-Key", "")
				assertHeader(t, r, "Hydrus-Client-API-Session-Key", sessionKey)
				assertQueryValue(t, r.URL, "offset", "5")
				assertQueryValue(t, r.URL, "limit", "10")
				return jsonResponse(t, r, http.StatusOK, RecentPage{
					Offset:  5,
					Limit:   10,
					HasMore: false,
					Items: []RecentItem{{
						FileID:       7,
						Hash:         strings.Repeat("b", 64),
						MIME:         "image/png",
						HasThumbnail: true,
						ThumbnailURL: "/v1/files/thumbnail?file_id=7",
						ContentURL:   "/v1/files/content?file_id=7",
						MetadataURL:  "/get_files/file_metadata?file_id=7",
					}},
				}), nil
			default:
				return nil, fmt.Errorf("unexpected path %q", r.URL.Path)
			}
		}),
	)

	verification, err := client.VerifyAccessKey(context.Background())
	if err != nil {
		t.Fatalf("VerifyAccessKey() error = %v", err)
	}

	if verification.Name != "desktop-test" {
		t.Fatalf("verification.Name = %q, want desktop-test", verification.Name)
	}

	gotSessionKey, err := client.CreateSession(context.Background())
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	if gotSessionKey != sessionKey {
		t.Fatalf("CreateSession() = %q, want %q", gotSessionKey, sessionKey)
	}

	recent, err := client.ListRecent(context.Background(), 5, 10)
	if err != nil {
		t.Fatalf("ListRecent() error = %v", err)
	}

	if len(recent.Items) != 1 || recent.Items[0].FileID != 7 {
		t.Fatalf("recent.Items = %#v, want one item with file_id 7", recent.Items)
	}
}

func TestClientCreateSessionRejectsBlankSessionKey(t *testing.T) {
	client := newClientWithRoundTripper(
		t,
		"http://daemon.test",
		strings.Repeat("b", 64),
		roundTripFunc(func(r *http.Request) (*http.Response, error) {
			assertMethodAndPath(t, r, http.MethodGet, "/session_key")
			assertHeader(t, r, "Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
			assertHeader(t, r, "Hydrus-Client-API-Session-Key", "")
			return jsonResponse(t, r, http.StatusOK, map[string]string{"session_key": "   "}), nil
		}),
	)

	_, err := client.CreateSession(context.Background())
	if err == nil {
		t.Fatal("CreateSession() error = nil, want error")
	}

	if !strings.Contains(err.Error(), "empty session key") {
		t.Fatalf("CreateSession() error = %v, want empty session key error", err)
	}
}

func TestClientGetFileMetadata(t *testing.T) {
	t.Run("returns selected metadata row with decoded tags", func(t *testing.T) {
		accessKey := strings.Repeat("c", 64)
		sessionKey := "metadata-session"

		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			accessKey,
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodGet, "/get_files/file_metadata")
				assertHeader(t, r, "Hydrus-Client-API-Access-Key", "")
				assertHeader(t, r, "Hydrus-Client-API-Session-Key", sessionKey)
				assertQueryValue(t, r.URL, "file_id", "42")
				assertQueryValue(t, r.URL, "include_services_object", "false")
				return jsonResponse(t, r, http.StatusOK, map[string]any{
					"metadata": []map[string]any{{
						"file_id":    42,
						"hash":       strings.Repeat("d", 64),
						"mime":       "image/png",
						"size":       1234,
						"is_local":   true,
						"is_trashed": false,
						"is_deleted": false,
						"tags": map[string]any{
							"74616773": map[string]any{
								"name":        "my tags",
								"type":        5,
								"type_pretty": "local tag domain",
								"storage_tags": map[string][]string{
									"0": {"creator:alice"},
								},
								"display_tags": map[string][]string{
									"0": {"creator:alice"},
								},
							},
						},
						"service_keys_to_statuses_to_tags": map[string]any{
							"74616773": map[string][]string{
								"0": {"creator:alice"},
							},
						},
					}},
				}), nil
			}),
		)
		client.sessionKey = sessionKey

		metadata, err := client.GetFileMetadata(context.Background(), 42)
		if err != nil {
			t.Fatalf("GetFileMetadata() error = %v", err)
		}

		if metadata.FileID != 42 || metadata.MIME != "image/png" || metadata.Size != 1234 {
			t.Fatalf("metadata = %#v, want decoded file_id/mime/size", metadata)
		}

		tagService, ok := metadata.Tags["74616773"]
		if !ok {
			t.Fatalf("metadata.Tags = %#v, want decoded service-keyed tag payload", metadata.Tags)
		}

		if tagService.Name != "my tags" || tagService.TypePretty != "local tag domain" {
			t.Fatalf("tagService = %#v, want decoded name/type_pretty", tagService)
		}

		if got := tagService.DisplayTags["0"]; len(got) != 1 || got[0] != "creator:alice" {
			t.Fatalf("tagService.DisplayTags[0] = %v, want [creator:alice]", got)
		}
	})

	t.Run("rejects empty metadata list", func(t *testing.T) {
		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("e", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodGet, "/get_files/file_metadata")
				assertHeader(t, r, "Hydrus-Client-API-Access-Key", "")
				assertHeader(t, r, "Hydrus-Client-API-Session-Key", "metadata-session")
				assertQueryValue(t, r.URL, "file_id", "99")
				assertQueryValue(t, r.URL, "include_services_object", "false")
				return jsonResponse(t, r, http.StatusOK, map[string]any{"metadata": []any{}}), nil
			}),
		)
		client.sessionKey = "metadata-session"

		_, err := client.GetFileMetadata(context.Background(), 99)
		if err == nil {
			t.Fatal("GetFileMetadata() error = nil, want error")
		}

		if !strings.Contains(err.Error(), "daemon returned no metadata") {
			t.Fatalf("GetFileMetadata() error = %v, want no metadata error", err)
		}
	})
}

func TestClientMutationRequests(t *testing.T) {
	t.Run("imports local file through session-backed JSON request", func(t *testing.T) {
		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("f", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodPost, "/v1/import/local_file")
				assertHeader(t, r, "Hydrus-Client-API-Access-Key", "")
				assertHeader(t, r, "Hydrus-Client-API-Session-Key", "session-import")
				assertHeader(t, r, "Content-Type", "application/json")

				var payload map[string]string
				decodeJSONBody(t, r, &payload)
				if payload["path"] != "/tmp/example.png" {
					t.Fatalf("payload[path] = %q, want /tmp/example.png", payload["path"])
				}

				return jsonResponse(t, r, http.StatusOK, ImportResult{FileID: 11, Hash: strings.Repeat("f", 64)}), nil
			}),
		)
		client.sessionKey = "session-import"

		result, err := client.ImportLocalFile(context.Background(), "  /tmp/example.png  ")
		if err != nil {
			t.Fatalf("ImportLocalFile() error = %v", err)
		}

		if result.FileID != 11 {
			t.Fatalf("result.FileID = %d, want 11", result.FileID)
		}
	})

	t.Run("uploads file through session-backed multipart request", func(t *testing.T) {
		sourcePath := filepath.Join(t.TempDir(), "upload.png")
		if err := os.WriteFile(sourcePath, []byte("png-bytes"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("f", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodPost, "/v1/import/upload")
				assertHeader(t, r, "Hydrus-Client-API-Access-Key", "")
				assertHeader(t, r, "Hydrus-Client-API-Session-Key", "session-upload")

				contentType := r.Header.Get("Content-Type")
				if !strings.HasPrefix(contentType, "multipart/form-data; boundary=") {
					t.Fatalf("Content-Type = %q, want multipart/form-data boundary", contentType)
				}

				fields, filename, payload := decodeMultipartBody(t, r)
				if filename != "upload.png" {
					t.Fatalf("multipart filename = %q, want upload.png", filename)
				}

				if string(payload) != "png-bytes" {
					t.Fatalf("multipart payload = %q, want png-bytes", string(payload))
				}

				if strings.TrimSpace(fields["file_modified_at_ms"]) == "" {
					t.Fatal("file_modified_at_ms field is empty, want upload timestamp")
				}

				return jsonResponse(t, r, http.StatusOK, ImportResult{FileID: 12, Hash: strings.Repeat("e", 64)}), nil
			}),
		)
		client.sessionKey = "session-upload"

		result, err := client.UploadFile(context.Background(), sourcePath)
		if err != nil {
			t.Fatalf("UploadFile() error = %v", err)
		}

		if result.FileID != 12 {
			t.Fatalf("result.FileID = %d, want 12", result.FileID)
		}
	})

	t.Run("trashes file through session-backed JSON request", func(t *testing.T) {
		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("7", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodPost, "/v1/files/trash")
				assertHeader(t, r, "Hydrus-Client-API-Access-Key", "")
				assertHeader(t, r, "Hydrus-Client-API-Session-Key", "session-trash")
				assertHeader(t, r, "Content-Type", "application/json")

				var payload map[string]int64
				decodeJSONBody(t, r, &payload)
				if payload["file_id"] != 73 {
					t.Fatalf("payload[file_id] = %d, want 73", payload["file_id"])
				}

				return jsonResponse(t, r, http.StatusOK, TrashResult{FileID: 73, Trashed: true, RemovedFromRecent: true, State: "trashed"}), nil
			}),
		)
		client.sessionKey = "session-trash"

		result, err := client.TrashFile(context.Background(), 73)
		if err != nil {
			t.Fatalf("TrashFile() error = %v", err)
		}

		if !result.Trashed || result.FileID != 73 {
			t.Fatalf("result = %#v, want trashed file_id 73", result)
		}
	})
}

func TestClientFetchGridImage(t *testing.T) {
	t.Run("rejects items without thumbnails before making a request", func(t *testing.T) {
		client := New()
		_, err := client.FetchGridImage(context.Background(), RecentItem{FileID: 5, HasThumbnail: false})
		if err == nil {
			t.Fatal("FetchGridImage() error = nil, want error")
		}

		if !strings.Contains(err.Error(), "no thumbnail is available") {
			t.Fatalf("FetchGridImage() error = %v, want no thumbnail error", err)
		}
	})

	t.Run("returns bytes for available thumbnails", func(t *testing.T) {
		thumbnailBytes := []byte{0x89, 'P', 'N', 'G'}

		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("9", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodGet, "/v1/files/thumbnail")
				assertHeader(t, r, "Hydrus-Client-API-Access-Key", "")
				assertHeader(t, r, "Hydrus-Client-API-Session-Key", "thumb-session")
				assertQueryValue(t, r.URL, "file_id", "91")
				return bytesResponse(r, http.StatusOK, thumbnailBytes), nil
			}),
		)
		client.sessionKey = "thumb-session"

		payload, err := client.FetchGridImage(context.Background(), RecentItem{
			FileID:       91,
			HasThumbnail: true,
			ThumbnailURL: "/v1/files/thumbnail?file_id=91",
		})
		if err != nil {
			t.Fatalf("FetchGridImage() error = %v", err)
		}

		if string(payload) != string(thumbnailBytes) {
			t.Fatalf("FetchGridImage() bytes = %v, want %v", payload, thumbnailBytes)
		}
	})

	t.Run("rejects empty thumbnail payloads", func(t *testing.T) {
		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("1", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodGet, "/v1/files/thumbnail")
				assertHeader(t, r, "Hydrus-Client-API-Access-Key", "")
				assertHeader(t, r, "Hydrus-Client-API-Session-Key", "thumb-session")
				assertQueryValue(t, r.URL, "file_id", "92")
				return bytesResponse(r, http.StatusOK, nil), nil
			}),
		)
		client.sessionKey = "thumb-session"

		_, err := client.FetchGridImage(context.Background(), RecentItem{
			FileID:       92,
			HasThumbnail: true,
			ThumbnailURL: "/v1/files/thumbnail?file_id=92",
		})
		if err == nil {
			t.Fatal("FetchGridImage() error = nil, want error")
		}

		if !strings.Contains(err.Error(), "empty thumbnail") {
			t.Fatalf("FetchGridImage() error = %v, want empty thumbnail error", err)
		}
	})
}

func TestClientFetchFileContent(t *testing.T) {
	t.Run("rejects items without content urls", func(t *testing.T) {
		client := New()
		_, err := client.FetchFileContent(context.Background(), RecentItem{FileID: 61}, 1024)
		if err == nil {
			t.Fatal("FetchFileContent() error = nil, want error")
		}

		if !strings.Contains(err.Error(), "no content URL is available") {
			t.Fatalf("FetchFileContent() error = %v, want missing content URL error", err)
		}
	})

	t.Run("returns bytes for available original content", func(t *testing.T) {
		contentBytes := []byte("managed-original-bytes")

		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("2", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodGet, "/v1/files/content")
				assertHeader(t, r, "Hydrus-Client-API-Access-Key", "")
				assertHeader(t, r, "Hydrus-Client-API-Session-Key", "content-session")
				assertQueryValue(t, r.URL, "file_id", "93")
				return bytesResponse(r, http.StatusOK, contentBytes), nil
			}),
		)
		client.sessionKey = "content-session"

		payload, err := client.FetchFileContent(context.Background(), RecentItem{
			FileID:     93,
			ContentURL: "/v1/files/content?file_id=93",
		}, 1024)
		if err != nil {
			t.Fatalf("FetchFileContent() error = %v", err)
		}

		if string(payload) != string(contentBytes) {
			t.Fatalf("FetchFileContent() bytes = %q, want %q", payload, contentBytes)
		}
	})

	t.Run("surfaces content endpoint errors", func(t *testing.T) {
		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("3", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodGet, "/v1/files/content")
				assertHeader(t, r, "Hydrus-Client-API-Access-Key", "")
				assertHeader(t, r, "Hydrus-Client-API-Session-Key", "content-session")
				assertQueryValue(t, r.URL, "file_id", "94")
				return textResponse(r, http.StatusNotFound, "missing original"), nil
			}),
		)
		client.sessionKey = "content-session"

		_, err := client.FetchFileContent(context.Background(), RecentItem{
			FileID:     94,
			ContentURL: "/v1/files/content?file_id=94",
		}, 1024)
		if err == nil {
			t.Fatal("FetchFileContent() error = nil, want error")
		}

		if !strings.Contains(err.Error(), "daemon returned HTTP 404") || !strings.Contains(err.Error(), "missing original") {
			t.Fatalf("FetchFileContent() error = %v, want HTTP 404 body text", err)
		}
	})

	t.Run("rejects oversized original responses", func(t *testing.T) {
		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("4", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodGet, "/v1/files/content")
				assertHeader(t, r, "Hydrus-Client-API-Access-Key", "")
				assertHeader(t, r, "Hydrus-Client-API-Session-Key", "content-session")
				assertQueryValue(t, r.URL, "file_id", "95")
				return bytesResponse(r, http.StatusOK, []byte("0123456789abcdef")), nil
			}),
		)
		client.sessionKey = "content-session"

		_, err := client.FetchFileContent(context.Background(), RecentItem{
			FileID:     95,
			ContentURL: "/v1/files/content?file_id=95",
		}, 8)
		if err == nil {
			t.Fatal("FetchFileContent() error = nil, want error")
		}

		if !strings.Contains(err.Error(), "exceeded 8 bytes") {
			t.Fatalf("FetchFileContent() error = %v, want size limit error", err)
		}
	})
}

func TestClientErrorHandling(t *testing.T) {
	t.Run("surfaces daemon response body on non-2xx responses", func(t *testing.T) {
		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("b", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodGet, "/verify_access_key")
				assertHeader(t, r, "Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
				return textResponse(r, http.StatusForbidden, "bad access"), nil
			}),
		)

		_, err := client.VerifyAccessKey(context.Background())
		if err == nil {
			t.Fatal("VerifyAccessKey() error = nil, want error")
		}

		if !strings.Contains(err.Error(), "daemon returned HTTP 403") || !strings.Contains(err.Error(), "bad access") {
			t.Fatalf("VerifyAccessKey() error = %v, want HTTP status and body text", err)
		}
	})

	t.Run("falls back to status text when error body is empty", func(t *testing.T) {
		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("c", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodGet, "/verify_access_key")
				assertHeader(t, r, "Hydrus-Client-API-Access-Key", strings.Repeat("c", 64))
				return textResponse(r, http.StatusBadGateway, ""), nil
			}),
		)

		_, err := client.VerifyAccessKey(context.Background())
		if err == nil {
			t.Fatal("VerifyAccessKey() error = nil, want error")
		}

		if !strings.Contains(err.Error(), "daemon returned HTTP 502: Bad Gateway") {
			t.Fatalf("VerifyAccessKey() error = %v, want HTTP status text fallback", err)
		}
	})

	t.Run("returns JSON decode errors", func(t *testing.T) {
		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("d", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodGet, "/verify_access_key")
				assertHeader(t, r, "Hydrus-Client-API-Access-Key", strings.Repeat("d", 64))
				return textResponse(r, http.StatusOK, "not json"), nil
			}),
		)

		_, err := client.VerifyAccessKey(context.Background())
		if err == nil {
			t.Fatal("VerifyAccessKey() error = nil, want error")
		}

		if !strings.Contains(err.Error(), "decode daemon response") {
			t.Fatalf("VerifyAccessKey() error = %v, want decode error", err)
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func newClientWithRoundTripper(
	t *testing.T,
	baseURL string,
	accessKey string,
	transport roundTripFunc,
) *Client {
	t.Helper()

	client := New()
	client.httpClient.Transport = transport
	if err := client.SetConnection(baseURL, accessKey); err != nil {
		t.Fatalf("SetConnection() error = %v", err)
	}

	return client
}

func jsonResponse(t *testing.T, request *http.Request, status int, payload any) *http.Response {
	t.Helper()

	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	return responseWithBody(request, status, encoded, "application/json")
}

func textResponse(request *http.Request, status int, body string) *http.Response {
	return responseWithBody(request, status, []byte(body), "text/plain")
}

func bytesResponse(request *http.Request, status int, body []byte) *http.Response {
	return responseWithBody(request, status, body, "application/octet-stream")
}

func responseWithBody(
	request *http.Request,
	status int,
	body []byte,
	contentType string,
) *http.Response {
	response := &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(body))),
		Request:    request,
	}

	if contentType != "" {
		response.Header.Set("Content-Type", contentType)
	}

	return response
}

func assertMethodAndPath(t *testing.T, r *http.Request, wantMethod string, wantPath string) {
	t.Helper()

	if r.Method != wantMethod {
		t.Fatalf("request method = %s, want %s", r.Method, wantMethod)
	}

	if r.URL.Path != wantPath {
		t.Fatalf("request path = %q, want %q", r.URL.Path, wantPath)
	}
}

func assertHeader(t *testing.T, r *http.Request, name string, want string) {
	t.Helper()

	if got := r.Header.Get(name); got != want {
		t.Fatalf("header %s = %q, want %q", name, got, want)
	}
}

func assertQueryValue(t *testing.T, rawURL *url.URL, key string, want string) {
	t.Helper()

	if got := rawURL.Query().Get(key); got != want {
		t.Fatalf("query %s = %q, want %q", key, got, want)
	}
}

func assertUserAgent(t *testing.T, r *http.Request) {
	t.Helper()

	assertHeader(t, r, "User-Agent", userAgent)
}

func decodeJSONBody(t *testing.T, r *http.Request, target any) {
	t.Helper()

	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		t.Fatalf("json.NewDecoder().Decode() error = %v", err)
	}
}

func decodeMultipartBody(t *testing.T, r *http.Request) (map[string]string, string, []byte) {
	t.Helper()

	reader, err := r.MultipartReader()
	if err != nil {
		t.Fatalf("MultipartReader() error = %v", err)
	}

	fields := map[string]string{}
	var filename string
	var payload []byte

	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}

		if err != nil {
			t.Fatalf("NextPart() error = %v", err)
		}

		body, err := io.ReadAll(part)
		_ = part.Close()
		if err != nil {
			t.Fatalf("ReadAll(part) error = %v", err)
		}

		if part.FileName() != "" {
			filename = part.FileName()
			payload = body
			continue
		}

		fields[part.FormName()] = string(body)
	}

	return fields, filename, payload
}
