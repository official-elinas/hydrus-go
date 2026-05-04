package daemonclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	coredownloader "github.com/official-elinas/hydrus-go/internal/core/downloader"
	"github.com/official-elinas/hydrus-go/internal/core/fileimport"
	coreptrsync "github.com/official-elinas/hydrus-go/internal/core/ptrsync"
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
						"ratings": map[string]any{
							"favorites-service": true,
						},
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

		if got, ok := metadata.Ratings["favorites-service"].(bool); !ok || !got {
			t.Fatalf("metadata.Ratings[favorites-service] = %v (present=%t), want true", metadata.Ratings["favorites-service"], ok)
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

func TestClientSuggestTags(t *testing.T) {
	client := newClientWithRoundTripper(
		t,
		"http://daemon.test",
		strings.Repeat("a", 64),
		roundTripFunc(func(r *http.Request) (*http.Response, error) {
			assertMethodAndPath(t, r, http.MethodGet, "/v1/tags/autocomplete")
			assertHeader(t, r, "Hydrus-Client-API-Access-Key", "")
			assertHeader(t, r, "Hydrus-Client-API-Session-Key", "session-tags")
			assertQueryValue(t, r.URL, "q", "creator:a")
			assertQueryValue(t, r.URL, "limit", "7")

			return jsonResponse(t, r, http.StatusOK, map[string]any{
				"query":       "creator:a",
				"suggestions": []string{"creator:alice", "creator:alina"},
			}), nil
		}),
	)
	client.sessionKey = "session-tags"

	suggestions, err := client.SuggestTags(context.Background(), "creator:a", 7)
	if err != nil {
		t.Fatalf("SuggestTags() error = %v", err)
	}

	if len(suggestions) != 2 || suggestions[0] != "creator:alice" || suggestions[1] != "creator:alina" {
		t.Fatalf("suggestions = %v, want [creator:alice creator:alina]", suggestions)
	}
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

	t.Run("imports direct URL through session-backed JSON request", func(t *testing.T) {
		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("f", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodPost, "/v1/import/url")
				assertHeader(t, r, "Hydrus-Client-API-Access-Key", "")
				assertHeader(t, r, "Hydrus-Client-API-Session-Key", "session-url-import")
				assertHeader(t, r, "Content-Type", "application/json")

				var payload map[string]string
				decodeJSONBody(t, r, &payload)
				if payload["url"] != "https://example.com/image.png" {
					t.Fatalf("payload[url] = %q, want https://example.com/image.png", payload["url"])
				}
				if payload["referral_url"] != "https://example.com/post/123" {
					t.Fatalf("payload[referral_url] = %q, want https://example.com/post/123", payload["referral_url"])
				}

				return jsonResponse(t, r, http.StatusOK, ImportResult{FileID: 12, Hash: strings.Repeat("e", 64)}), nil
			}),
		)
		client.sessionKey = "session-url-import"

		result, err := client.ImportURL(context.Background(), fileimport.URLRequest{
			URL:         "https://example.com/image.png",
			ReferralURL: "https://example.com/post/123",
		})
		if err != nil {
			t.Fatalf("ImportURL() error = %v", err)
		}

		if result.FileID != 12 {
			t.Fatalf("result.FileID = %d, want 12", result.FileID)
		}
	})

	t.Run("gets downloader status through session-backed request", func(t *testing.T) {
		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("f", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodGet, "/v1/downloader/status")
				assertHeader(t, r, "Hydrus-Client-API-Access-Key", "")
				assertHeader(t, r, "Hydrus-Client-API-Session-Key", "session-downloader")
				return jsonResponse(t, r, http.StatusOK, map[string]any{
					"downloader": map[string]any{
						"enabled": true,
						"configured": true,
						"running": true,
						"autoimport": true,
						"urls_queued": 3,
					},
				}), nil
			}),
		)
		client.sessionKey = "session-downloader"

		status, err := client.GetDownloaderStatus(context.Background())
		if err != nil {
			t.Fatalf("GetDownloaderStatus() error = %v", err)
		}
		if !status.Running || status.URLsQueued != 3 {
			t.Fatalf("status = %#v, want running with queued count 3", status)
		}
	})

	t.Run("queues downloader URL through session-backed JSON request", func(t *testing.T) {
		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("f", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodPost, "/v1/downloader/url")
				assertHeader(t, r, "Hydrus-Client-API-Access-Key", "")
				assertHeader(t, r, "Hydrus-Client-API-Session-Key", "session-downloader")
				assertHeader(t, r, "Content-Type", "application/json")

				var payload map[string]any
				decodeJSONBody(t, r, &payload)
				if payload["url"] != "https://example.com/post/1" {
					t.Fatalf("payload[url] = %v, want https://example.com/post/1", payload["url"])
				}

				return jsonResponse(t, r, http.StatusOK, map[string]any{"queued": true}), nil
			}),
		)
		client.sessionKey = "session-downloader"

		if err := client.QueueDownloaderURL(context.Background(), coredownloader.URLRequest{URL: "https://example.com/post/1"}); err != nil {
			t.Fatalf("QueueDownloaderURL() error = %v", err)
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

	t.Run("stages pending mappings through session-backed JSON request", func(t *testing.T) {
		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("8", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodPost, "/add_tags/add_tags")
				assertHeader(t, r, "Hydrus-Client-API-Access-Key", "")
				assertHeader(t, r, "Hydrus-Client-API-Session-Key", "session-tags")
				assertHeader(t, r, "Content-Type", "application/json")

				var payload map[string]any
				decodeJSONBody(t, r, &payload)

				if payload["file_id"] != float64(73) {
					t.Fatalf("payload[file_id] = %v, want 73", payload["file_id"])
				}

				actionsByService := payload["service_keys_to_actions_to_tags"].(map[string]any)
				actions := actionsByService[coreptrsync.DaemonServiceKeyHex()].(map[string]any)
				tags := actions[hydrusContentUpdatePendAction].([]any)
				if len(tags) != 2 || tags[0] != "creator:alice" || tags[1] != "series:zeta" {
					t.Fatalf("payload tags = %v, want creator:alice and series:zeta", tags)
				}

				return jsonResponse(t, r, http.StatusOK, coreptrsync.PendingMappingsResult{
					ServiceKey:    coreptrsync.DaemonServiceKeyHex(),
					AddedMappings: 2,
				}), nil
			}),
		)
		client.sessionKey = "session-tags"

		result, err := client.AddPendingMappings(context.Background(), coreptrsync.PendingMappingsRequest{
			FileIDs: []int64{73},
			Tags:    []string{"creator:alice", "series:zeta"},
		})
		if err != nil {
			t.Fatalf("AddPendingMappings() error = %v", err)
		}

		if result.AddedMappings != 2 || result.ServiceKey != coreptrsync.DaemonServiceKeyHex() {
			t.Fatalf("result = %#v, want added_mappings=2 and daemon service key", result)
		}
	})

	t.Run("commits pending mappings through session-backed JSON request", func(t *testing.T) {
		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("9", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodPost, "/manage_services/commit_pending")
				assertHeader(t, r, "Hydrus-Client-API-Access-Key", "")
				assertHeader(t, r, "Hydrus-Client-API-Session-Key", "session-commit")
				assertHeader(t, r, "Content-Type", "application/json")

				var payload map[string]any
				decodeJSONBody(t, r, &payload)
				if payload["service_key"] != coreptrsync.DaemonServiceKeyHex() {
					t.Fatalf("payload[service_key] = %v, want daemon service key", payload["service_key"])
				}

				return jsonResponse(t, r, http.StatusOK, coreptrsync.CommitPendingResult{
					ServiceKey:        coreptrsync.DaemonServiceKeyHex(),
					CommittedMappings: 5,
				}), nil
			}),
		)
		client.sessionKey = "session-commit"

		result, err := client.CommitPending(context.Background(), coreptrsync.CommitPendingRequest{ServiceKey: coreptrsync.DaemonServiceKeyHex()})
		if err != nil {
			t.Fatalf("CommitPending() error = %v", err)
		}

		if result.CommittedMappings != 5 || result.ServiceKey != coreptrsync.DaemonServiceKeyHex() {
			t.Fatalf("result = %#v, want committed_mappings=5 and daemon service key", result)
		}
	})
}

func TestClientGenerateGridThumbnail(t *testing.T) {
	encodePNG := func(t *testing.T, size int) []byte {
		t.Helper()

		img := image.NewNRGBA(image.Rect(0, 0, size, size))
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				img.Set(x, y, color.NRGBA{R: uint8(40 + x), G: uint8(80 + y), B: 120, A: 255})
			}
		}

		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			t.Fatalf("png.Encode() error = %v", err)
		}

		return buf.Bytes()
	}

	encodeFFmpegStill := func(t *testing.T, ext string) []byte {
		t.Helper()
		if _, err := exec.LookPath("ffmpeg"); err != nil {
			t.Skip("ffmpeg is required for this fallback test")
		}

		dir := t.TempDir()
		inputPath := filepath.Join(dir, "input.png")
		if err := os.WriteFile(inputPath, encodePNG(t, 4), 0o644); err != nil {
			t.Fatalf("WriteFile(input.png) error = %v", err)
		}

		outputPath := filepath.Join(dir, "output"+ext)
		cmd := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-y", "-i", inputPath, outputPath)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg convert %q error = %v\n%s", ext, err, string(output))
		}

		payload, err := os.ReadFile(outputPath)
		if err != nil {
			t.Fatalf("ReadFile(output%s) error = %v", ext, err)
		}

		return payload
	}

	t.Run("rejects items without content URLs", func(t *testing.T) {
		client := New()
		_, err := client.GenerateGridThumbnail(context.Background(), RecentItem{FileID: 5}, 64)
		if err == nil {
			t.Fatal("GenerateGridThumbnail() error = nil, want error")
		}

		if !strings.Contains(err.Error(), "no content URL is available") {
			t.Fatalf("GenerateGridThumbnail() error = %v, want missing content URL error", err)
		}
	})

	t.Run("returns scaled png bytes for a valid original", func(t *testing.T) {
		originalBytes := encodePNG(t, 4)

		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("9", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodGet, "/v1/files/content")
				assertHeader(t, r, "Hydrus-Client-API-Access-Key", "")
				assertHeader(t, r, "Hydrus-Client-API-Session-Key", "thumb-session")
				assertQueryValue(t, r.URL, "file_id", "91")
				return bytesResponse(r, http.StatusOK, originalBytes), nil
			}),
		)
		client.sessionKey = "thumb-session"

		payload, err := client.GenerateGridThumbnail(context.Background(), RecentItem{
			FileID:     91,
			ContentURL: "/v1/files/content?file_id=91",
		}, 2)
		if err != nil {
			t.Fatalf("GenerateGridThumbnail() error = %v", err)
		}

		decoded, _, err := image.Decode(bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("image.Decode() error = %v", err)
		}

		if decoded.Bounds().Dx() != 2 || decoded.Bounds().Dy() != 2 {
			t.Fatalf("generated thumbnail size = %dx%d, want 2x2", decoded.Bounds().Dx(), decoded.Bounds().Dy())
		}
	})

	t.Run("prefers daemon thumbnail bytes when available", func(t *testing.T) {
		thumbnailBytes := encodePNG(t, 2)
		contentCalls := 0

		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("9", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				switch r.URL.Path {
				case "/v1/files/thumbnail":
					assertMethodAndPath(t, r, http.MethodGet, "/v1/files/thumbnail")
					assertHeader(t, r, "Hydrus-Client-API-Access-Key", "")
					assertHeader(t, r, "Hydrus-Client-API-Session-Key", "thumb-session")
					assertQueryValue(t, r.URL, "file_id", "91")
					return bytesResponse(r, http.StatusOK, thumbnailBytes), nil
				case "/v1/files/content":
					contentCalls++
					return bytesResponse(r, http.StatusInternalServerError, []byte("should not fetch original")), nil
				default:
					t.Fatalf("unexpected path %q", r.URL.Path)
					return nil, nil
				}
			}),
		)
		client.sessionKey = "thumb-session"

		payload, err := client.GenerateGridThumbnail(context.Background(), RecentItem{
			FileID:       91,
			ContentURL:   "/v1/files/content?file_id=91",
			ThumbnailURL: "/v1/files/thumbnail?file_id=91",
		}, 2)
		if err != nil {
			t.Fatalf("GenerateGridThumbnail() error = %v", err)
		}
		if contentCalls != 0 {
			t.Fatalf("contentCalls = %d, want 0", contentCalls)
		}

		decoded, _, err := image.Decode(bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("image.Decode() error = %v", err)
		}
		if decoded.Bounds().Dx() != 2 || decoded.Bounds().Dy() != 2 {
			t.Fatalf("daemon thumbnail size = %dx%d, want 2x2", decoded.Bounds().Dx(), decoded.Bounds().Dy())
		}
	})

	t.Run("returns error when daemon request fails", func(t *testing.T) {
		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("1", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodGet, "/v1/files/content")
				assertQueryValue(t, r.URL, "file_id", "92")
				return bytesResponse(r, http.StatusInternalServerError, []byte("boom")), nil
			}),
		)
		client.sessionKey = "thumb-session"

		_, err := client.GenerateGridThumbnail(context.Background(), RecentItem{
			FileID:     92,
			ContentURL: "/v1/files/content?file_id=92",
		}, 64)
		if err == nil {
			t.Fatal("GenerateGridThumbnail() error = nil, want error")
		}
	})

	t.Run("falls back to ffmpeg for unsupported still-image payloads", func(t *testing.T) {
		originalBytes := encodeFFmpegStill(t, ".avif")

		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("9", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodGet, "/v1/files/content")
				assertQueryValue(t, r.URL, "file_id", "93")
				return bytesResponse(r, http.StatusOK, originalBytes), nil
			}),
		)
		client.sessionKey = "thumb-session"

		payload, err := client.GenerateGridThumbnail(context.Background(), RecentItem{
			FileID:     93,
			MIME:       "image/avif",
			ContentURL: "/v1/files/content?file_id=93",
		}, 2)
		if err != nil {
			t.Fatalf("GenerateGridThumbnail() error = %v", err)
		}

		decoded, _, err := image.Decode(bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("image.Decode() error = %v", err)
		}
		if decoded.Bounds().Dx() != 2 || decoded.Bounds().Dy() != 2 {
			t.Fatalf("fallback thumbnail size = %dx%d, want 2x2", decoded.Bounds().Dx(), decoded.Bounds().Dy())
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

func TestPTRStatus(t *testing.T) {
	t.Run("parses verified mapping count and running state", func(t *testing.T) {
		mappingCount := int64(99)
		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("f", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodGet, "/service/ptr/status")
				assertHeader(t, r, "Hydrus-Client-API-Session-Key", "session-ptr")

				status := PTRStatusResponse{
					PTR: coreptrsync.Status{
						Enabled:                         true,
						Phase:                           "idle",
						IsRunning:                       true,
						IsUpToDate:                      true,
						DownloadedUpdateBytes:           4096,
						CurrentRunDownloadedBytes:       1024,
						CurrentRunDownloadMS:            250,
						CurrentRunBytesPerSecond:        4096,
						CurrentRunNetworkFetchedBytes:   1024,
						CurrentRunNetworkFetchMS:        125,
						CurrentRunNetworkBytesPerSecond: 8192,
						PendingDownloadCount:            2,
						PendingProcessCount:             3,
						NextUpdateDue:                   1700003600,
						LastSyncMappingCount:            &mappingCount,
					},
				}
				return jsonResponse(t, r, http.StatusOK, status), nil
			}),
		)
		client.sessionKey = "session-ptr"

		res, err := client.GetPTRStatus(context.Background())
		if err != nil {
			t.Fatalf("GetPTRStatus() error = %v", err)
		}

		if !res.PTR.Enabled {
			t.Error("Expected enabled = true")
		}
		if res.PTR.Phase != "idle" {
			t.Errorf("Expected phase = idle, got %q", res.PTR.Phase)
		}
		if !res.PTR.IsRunning {
			t.Error("Expected is_running = true")
		}
		if res.PTR.LastSyncMappingCount == nil || *res.PTR.LastSyncMappingCount != 99 {
			t.Fatalf("LastSyncMappingCount = %v, want 99", res.PTR.LastSyncMappingCount)
		}
		if res.PTR.DownloadedUpdateBytes != 4096 {
			t.Fatalf("DownloadedUpdateBytes = %d, want 4096", res.PTR.DownloadedUpdateBytes)
		}
		if res.PTR.CurrentRunBytesPerSecond != 4096 {
			t.Fatalf("CurrentRunBytesPerSecond = %d, want 4096", res.PTR.CurrentRunBytesPerSecond)
		}
		if res.PTR.CurrentRunNetworkBytesPerSecond != 8192 {
			t.Fatalf("CurrentRunNetworkBytesPerSecond = %d, want 8192", res.PTR.CurrentRunNetworkBytesPerSecond)
		}
		if res.PTR.PendingDownloadCount != 2 {
			t.Fatalf("PendingDownloadCount = %d, want 2", res.PTR.PendingDownloadCount)
		}
		if !res.PTR.IsUpToDate {
			t.Fatal("Expected is_up_to_date = true")
		}
	})

	t.Run("returns error on non-200 response", func(t *testing.T) {
		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("f", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodGet, "/service/ptr/status")
				return bytesResponse(r, http.StatusInternalServerError, []byte("boom")), nil
			}),
		)
		client.sessionKey = "session-ptr"

		_, err := client.GetPTRStatus(context.Background())
		if err == nil {
			t.Fatal("GetPTRStatus() error = nil, want error")
		}
	})
}

func TestPTRSync(t *testing.T) {
	t.Run("parses running trigger response", func(t *testing.T) {
		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("f", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodPost, "/service/ptr/sync")
				assertHeader(t, r, "Hydrus-Client-API-Session-Key", "session-ptr")

				status := PTRStatusResponse{
					PTR: coreptrsync.Status{
						Enabled:   true,
						Phase:     "syncing",
						IsRunning: true,
					},
				}
				return jsonResponse(t, r, http.StatusOK, status), nil
			}),
		)
		client.sessionKey = "session-ptr"

		res, err := client.TriggerPTRSync(context.Background())
		if err != nil {
			t.Fatalf("TriggerPTRSync() error = %v", err)
		}

		if !res.PTR.Enabled {
			t.Error("Expected enabled = true")
		}
		if res.PTR.Phase != "syncing" {
			t.Errorf("Expected phase = syncing, got %q", res.PTR.Phase)
		}
		if !res.PTR.IsRunning {
			t.Error("Expected is_running = true")
		}
	})

	t.Run("returns error on non-200 response", func(t *testing.T) {
		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("f", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodPost, "/service/ptr/sync")
				return bytesResponse(r, http.StatusBadRequest, []byte("disabled")), nil
			}),
		)
		client.sessionKey = "session-ptr"

		_, err := client.TriggerPTRSync(context.Background())
		if err == nil {
			t.Fatal("TriggerPTRSync() error = nil, want error")
		}
	})
}

func TestClientSearchByTags(t *testing.T) {
	t.Run("encodes tags and pagination as query params", func(t *testing.T) {
		sessionKey := "session-search"

		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("e", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodGet, "/v1/library/search")
				assertHeader(t, r, "Hydrus-Client-API-Session-Key", sessionKey)

				q := r.URL.Query()
				tags := q["tags"]
				if len(tags) != 2 {
					return nil, fmt.Errorf("expected 2 tags, got %v", tags)
				}
				if tags[0] != "character:samus" || tags[1] != "series:metroid" {
					return nil, fmt.Errorf("unexpected tags %v", tags)
				}
				assertQueryValue(t, r.URL, "offset", "0")
				assertQueryValue(t, r.URL, "limit", "10")

				return jsonResponse(t, r, http.StatusOK, RecentPage{
					Offset:  0,
					Limit:   10,
					HasMore: false,
					Items: []RecentItem{{
						FileID:       99,
						Hash:         strings.Repeat("f", 64),
						MIME:         "image/jpeg",
						HasThumbnail: true,
						ThumbnailURL: "/v1/files/thumbnail?file_id=99",
						ContentURL:   "/v1/files/content?file_id=99",
					}},
				}), nil
			}),
		)
		client.sessionKey = sessionKey

		page, err := client.SearchByTags(context.Background(), []string{"character:samus", "series:metroid"}, 0, 10)
		if err != nil {
			t.Fatalf("SearchByTags() error = %v", err)
		}

		if len(page.Items) != 1 || page.Items[0].FileID != 99 {
			t.Fatalf("page.Items = %#v, want one item with file_id 99", page.Items)
		}

		if page.HasMore {
			t.Fatal("page.HasMore = true, want false")
		}
	})

	t.Run("empty tags slice sends no tags param", func(t *testing.T) {
		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("e", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodGet, "/v1/library/search")

				if got := r.URL.Query()["tags"]; len(got) != 0 {
					return nil, fmt.Errorf("expected no tags param, got %v", got)
				}

				return jsonResponse(t, r, http.StatusOK, RecentPage{}), nil
			}),
		)
		client.sessionKey = "session-empty"

		_, err := client.SearchByTags(context.Background(), nil, 0, 20)
		if err != nil {
			t.Fatalf("SearchByTags() error = %v", err)
		}
	})

	t.Run("encodes sort_by and system_predicates when set", func(t *testing.T) {
		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("e", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodGet, "/v1/library/search")
				assertHeader(t, r, "Hydrus-Client-API-Session-Key", "session-opts")

				q := r.URL.Query()
				assertQueryValue(t, r.URL, "sort_by", "size_desc")

				preds := q["system_predicates[]"]
				if len(preds) != 2 {
					return nil, fmt.Errorf("expected 2 system_predicates[], got %v", preds)
				}
				if preds[0] != "size>=2048" || preds[1] != "width<800" {
					return nil, fmt.Errorf("unexpected system_predicates[] %v", preds)
				}

				return jsonResponse(t, r, http.StatusOK, RecentPage{
					Items: []RecentItem{{FileID: 55}},
				}), nil
			}),
		)
		client.sessionKey = "session-opts"

		page, err := client.SearchByTags(
			context.Background(),
			[]string{"creator:alice"},
			0, 10,
			SearchOptions{
				SortBy:           "size_desc",
				SystemPredicates: []string{"size>=2048", "width<800"},
			},
		)
		if err != nil {
			t.Fatalf("SearchByTags() error = %v", err)
		}

		if len(page.Items) != 1 || page.Items[0].FileID != 55 {
			t.Fatalf("page.Items = %#v, want one item with file_id 55", page.Items)
		}
	})

	t.Run("omits sort_by and system_predicates when not set", func(t *testing.T) {
		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("e", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodGet, "/v1/library/search")

				q := r.URL.Query()
				if got := q.Get("sort_by"); got != "" {
					return nil, fmt.Errorf("expected no sort_by param, got %q", got)
				}

				if got := q["system_predicates[]"]; len(got) != 0 {
					return nil, fmt.Errorf("expected no system_predicates[], got %v", got)
				}

				return jsonResponse(t, r, http.StatusOK, RecentPage{}), nil
			}),
		)
		client.sessionKey = "session-omit-opts"

		_, err := client.SearchByTags(context.Background(), []string{"creator:alice"}, 0, 10)
		if err != nil {
			t.Fatalf("SearchByTags() error = %v", err)
		}
	})
}

func TestClientGetPendingCount(t *testing.T) {
	t.Run("fetches pending count with service key using session auth", func(t *testing.T) {
		serviceKey := coreptrsync.DaemonServiceKeyHex()

		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("a", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodGet, "/manage_services/pending_counts")
				assertHeader(t, r, "Hydrus-Client-API-Access-Key", "")
				assertHeader(t, r, "Hydrus-Client-API-Session-Key", "session-pending")
				assertQueryValue(t, r.URL, "service_key", serviceKey)

				return jsonResponse(t, r, http.StatusOK, coreptrsync.PendingInfo{
					ServiceKey:   serviceKey,
					PendingCount: 17,
				}), nil
			}),
		)
		client.sessionKey = "session-pending"

		info, err := client.GetPendingCount(context.Background(), serviceKey)
		if err != nil {
			t.Fatalf("GetPendingCount() error = %v", err)
		}

		if info.ServiceKey != serviceKey {
			t.Fatalf("info.ServiceKey = %q, want %q", info.ServiceKey, serviceKey)
		}

		if info.PendingCount != 17 {
			t.Fatalf("info.PendingCount = %d, want 17", info.PendingCount)
		}
	})

	t.Run("omits service_key query param when blank", func(t *testing.T) {
		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("b", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodGet, "/manage_services/pending_counts")
				assertHeader(t, r, "Hydrus-Client-API-Session-Key", "session-pending-blank")

				if got := r.URL.Query().Get("service_key"); got != "" {
					return nil, fmt.Errorf("expected no service_key param, got %q", got)
				}

				return jsonResponse(t, r, http.StatusOK, coreptrsync.PendingInfo{
					ServiceKey:   "server-default",
					PendingCount: 3,
				}), nil
			}),
		)
		client.sessionKey = "session-pending-blank"

		info, err := client.GetPendingCount(context.Background(), "")
		if err != nil {
			t.Fatalf("GetPendingCount() error = %v", err)
		}

		if info.PendingCount != 3 {
			t.Fatalf("info.PendingCount = %d, want 3", info.PendingCount)
		}
	})

	t.Run("omits service_key for whitespace-only input", func(t *testing.T) {
		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("c", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodGet, "/manage_services/pending_counts")

				if got := r.URL.Query().Get("service_key"); got != "" {
					return nil, fmt.Errorf("expected no service_key param, got %q", got)
				}

				return jsonResponse(t, r, http.StatusOK, coreptrsync.PendingInfo{PendingCount: 0}), nil
			}),
		)
		client.sessionKey = "session-pending-ws"

		_, err := client.GetPendingCount(context.Background(), "   ")
		if err != nil {
			t.Fatalf("GetPendingCount() error = %v", err)
		}
	})

	t.Run("surfaces daemon error on non-2xx response", func(t *testing.T) {
		client := newClientWithRoundTripper(
			t,
			"http://daemon.test",
			strings.Repeat("d", 64),
			roundTripFunc(func(r *http.Request) (*http.Response, error) {
				assertMethodAndPath(t, r, http.MethodGet, "/manage_services/pending_counts")
				return textResponse(r, http.StatusNotFound, "service not found"), nil
			}),
		)
		client.sessionKey = "session-pending-err"

		_, err := client.GetPendingCount(context.Background(), "nonexistent")
		if err == nil {
			t.Fatal("GetPendingCount() error = nil, want error")
		}

		if !strings.Contains(err.Error(), "daemon returned HTTP 404") {
			t.Fatalf("GetPendingCount() error = %v, want HTTP 404 error", err)
		}
	})
}

func TestDBIntegrityCheck(t *testing.T) {
	client := newClientWithRoundTripper(
		t,
		"http://daemon.test",
		strings.Repeat("f", 64),
		roundTripFunc(func(r *http.Request) (*http.Response, error) {
			assertMethodAndPath(t, r, http.MethodPost, "/manage_database/integrity_check")
			assertHeader(t, r, "Hydrus-Client-API-Session-Key", "session-db")

			response := DBIntegrityResponse{
				Integrity: DBIntegrityResult{
					Passed:  true,
					Results: []string{"ok"},
				},
			}
			return jsonResponse(t, r, http.StatusOK, response), nil
		}),
	)
	client.sessionKey = "session-db"

	res, err := client.TriggerDBIntegrityCheck(context.Background())
	if err != nil {
		t.Fatalf("TriggerDBIntegrityCheck() error = %v", err)
	}

	if !res.Integrity.Passed {
		t.Fatal("expected integrity check to pass")
	}

	if len(res.Integrity.Results) != 1 || res.Integrity.Results[0] != "ok" {
		t.Fatalf("res.Integrity.Results = %v, want [ok]", res.Integrity.Results)
	}
}
