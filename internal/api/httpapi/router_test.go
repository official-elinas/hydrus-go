package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/official-elinas/hydrus-go/internal/buildinfo"
	"github.com/official-elinas/hydrus-go/internal/core/fileassets"
	"github.com/official-elinas/hydrus-go/internal/core/fileimport"
	"github.com/official-elinas/hydrus-go/internal/core/filemetadata"
	"github.com/official-elinas/hydrus-go/internal/core/filetrash"
	"github.com/official-elinas/hydrus-go/internal/core/librarybrowse"
	"github.com/official-elinas/hydrus-go/internal/core/services"
)

func TestPublicEndpoints(t *testing.T) {
	handler := newTestHandler(t)

	tests := []struct {
		name       string
		path       string
		wantStatus int
		check      func(t *testing.T, rr *httptest.ResponseRecorder)
	}{
		{
			name:       "root welcome",
			path:       "/",
			wantStatus: http.StatusOK,
			check: func(t *testing.T, rr *httptest.ResponseRecorder) {
				var payload map[string]any
				decodeJSON(t, rr.Body.Bytes(), &payload)

				if payload["name"] != "hydrus-go" {
					t.Fatalf("name = %v, want hydrus-go", payload["name"])
				}
			},
		},
		{
			name:       "health endpoint",
			path:       "/healthz",
			wantStatus: http.StatusOK,
			check: func(t *testing.T, rr *httptest.ResponseRecorder) {
				var payload map[string]any
				decodeJSON(t, rr.Body.Bytes(), &payload)

				if payload["status"] != "ok" {
					t.Fatalf("status = %v, want ok", payload["status"])
				}
			},
		},
		{
			name:       "api version",
			path:       "/api_version",
			wantStatus: http.StatusOK,
			check: func(t *testing.T, rr *httptest.ResponseRecorder) {
				var payload map[string]any
				decodeJSON(t, rr.Body.Bytes(), &payload)

				if int(payload["version"].(float64)) != buildinfo.ClientAPIVersion {
					t.Fatalf(
						"version = %v, want %d",
						payload["version"],
						buildinfo.ClientAPIVersion,
					)
				}

				if int(payload["hydrus_version"].(float64)) != buildinfo.ReferenceHydrusVersion {
					t.Fatalf(
						"hydrus_version = %v, want %d",
						payload["hydrus_version"],
						buildinfo.ReferenceHydrusVersion,
					)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rr.Code, tt.wantStatus)
			}

			if rr.Header().Get("Allow") != http.MethodGet {
				t.Fatalf("Allow = %q, want %q", rr.Header().Get("Allow"), http.MethodGet)
			}

			tt.check(t, rr)
		})
	}
}

func TestProtectedEndpoints(t *testing.T) {
	access, handler := newAccessControlledHandler(t)

	t.Run("missing access key is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/get_services", nil)
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
		}
	})

	t.Run("invalid session key returns 419", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/verify_access_key", nil)
		req.Header.Set("Hydrus-Client-API-Session-Key", strings.Repeat("c", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != expiredSessionStatusCode {
			t.Fatalf("status = %d, want %d", rr.Code, expiredSessionStatusCode)
		}
	})

	t.Run("verify access key returns permissions", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/verify_access_key", nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", access.AccessKey())
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}

		var payload map[string]any
		decodeJSON(t, rr.Body.Bytes(), &payload)

		if payload["name"] != "test-client" {
			t.Fatalf("name = %v, want test-client", payload["name"])
		}

		permissions, ok := payload["basic_permissions"].([]any)
		if !ok {
			t.Fatalf("basic_permissions type = %T, want []any", payload["basic_permissions"])
		}

		if len(permissions) != 2 {
			t.Fatalf("len(basic_permissions) = %d, want 2", len(permissions))
		}

		if int(permissions[0].(float64)) != int(PermissionSearchAndFetchFiles) {
			t.Fatalf("basic_permissions[0] = %v, want %d", permissions[0], PermissionSearchAndFetchFiles)
		}

		if int(permissions[1].(float64)) != int(PermissionImportAndDeleteFiles) {
			t.Fatalf(
				"basic_permissions[1] = %v, want %d",
				permissions[1],
				PermissionImportAndDeleteFiles,
			)
		}
	})

	t.Run("session key can authenticate service discovery", func(t *testing.T) {
		sessionReq := httptest.NewRequest(http.MethodGet, "/session_key", nil)
		sessionReq.Header.Set("Hydrus-Client-API-Access-Key", access.AccessKey())
		sessionRR := httptest.NewRecorder()

		handler.ServeHTTP(sessionRR, sessionReq)

		if sessionRR.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", sessionRR.Code, http.StatusOK)
		}

		var sessionPayload map[string]any
		decodeJSON(t, sessionRR.Body.Bytes(), &sessionPayload)

		sessionKey, ok := sessionPayload["session_key"].(string)
		if !ok || sessionKey == "" {
			t.Fatalf("session_key = %v, want non-empty string", sessionPayload["session_key"])
		}

		servicesReq := httptest.NewRequest(http.MethodGet, "/get_services", nil)
		servicesReq.Header.Set("Hydrus-Client-API-Session-Key", sessionKey)
		servicesRR := httptest.NewRecorder()

		handler.ServeHTTP(servicesRR, servicesReq)

		if servicesRR.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", servicesRR.Code, http.StatusOK)
		}

		var servicesPayload map[string]any
		decodeJSON(t, servicesRR.Body.Bytes(), &servicesPayload)

		if _, ok := servicesPayload["services_v2"]; !ok {
			t.Fatal("services_v2 missing from response")
		}

		if _, ok := servicesPayload["local_files"]; !ok {
			t.Fatal("local_files missing from response")
		}

		assertDefaultServiceDiscoveryPayload(t, servicesPayload)
	})

	t.Run("get service by key returns service", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			"/get_service?service_key=7265706f7369746f72792075706461746573",
			nil,
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", access.AccessKey())
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}

		var payload map[string]any
		decodeJSON(t, rr.Body.Bytes(), &payload)

		service, ok := payload["service"].(map[string]any)
		if !ok {
			t.Fatalf("service type = %T, want map[string]any", payload["service"])
		}

		if service["name"] != "repository updates" {
			t.Fatalf("service.name = %v, want repository updates", service["name"])
		}
	})

	t.Run("file metadata requires DB-backed store", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			"/get_files/file_metadata?hashes=%5B%22"+strings.Repeat("a", 64)+"%22%5D&only_return_identifiers=true",
			nil,
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", access.AccessKey())
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotImplemented {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotImplemented)
		}
	})

	t.Run("recent local browse requires DB-backed store", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/library/recent", nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", access.AccessKey())
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotImplemented {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotImplemented)
		}
	})

	t.Run("file content requires DB-backed store", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/files/content?file_id=1", nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", access.AccessKey())
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotImplemented {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotImplemented)
		}
	})

	t.Run("local import requires DB-backed write store", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodPost,
			"/v1/import/local_file",
			strings.NewReader(`{"path":"/tmp/example.png"}`),
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", access.AccessKey())
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotImplemented {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotImplemented)
		}
	})

	t.Run("file trash requires DB-backed write store", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodPost,
			"/v1/files/trash",
			strings.NewReader(`{"file_id":1}`),
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", access.AccessKey())
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotImplemented {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotImplemented)
		}
	})

	t.Run("trash requires import and delete permission", func(t *testing.T) {
		access, err := NewAccessControl(
			strings.Repeat("e", 64),
			"search-only",
			[]Permission{PermissionSearchAndFetchFiles},
		)
		if err != nil {
			t.Fatalf("NewAccessControl() error = %v", err)
		}

		handler := NewHandler(
			slog.New(slog.NewTextHandler(io.Discard, nil)),
			access,
			services.DefaultProvider(),
			nil,
			nil,
			nil,
			nil,
			&fakeMetadataStore{},
			false,
		)

		req := httptest.NewRequest(
			http.MethodPost,
			"/v1/files/trash",
			strings.NewReader(`{"file_id":1}`),
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", access.AccessKey())
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
		}
	})
}

func TestThinClientEndpoints(t *testing.T) {
	provider := services.NewStaticProvider(services.DefaultCatalog())

	t.Run("recent local browse returns page payload", func(t *testing.T) {
		store := &fakeMetadataStore{
			listRecentHandle: func(request librarybrowse.Request) (librarybrowse.Page, error) {
				if request.Offset != 2 || request.Limit != 1 {
					t.Fatalf("request = %+v, want offset=2 limit=1", request)
				}

				width := int64(640)
				height := int64(480)
				importedAtMS := int64(123456)
				return librarybrowse.Page{
					Items: []librarybrowse.Item{
						{
							FileID:       5,
							Hash:         strings.Repeat("a", 64),
							MIME:         "image/jpeg",
							Width:        &width,
							Height:       &height,
							ImportedAtMS: &importedAtMS,
							HasThumbnail: true,
						},
					},
					HasMore: true,
				}, nil
			},
		}

		handler := newHandlerWithDeps(t, provider, store, false)
		req := httptest.NewRequest(http.MethodGet, "/v1/library/recent?offset=2&limit=1", nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}

		var payload map[string]any
		decodeJSON(t, rr.Body.Bytes(), &payload)

		if int(payload["offset"].(float64)) != 2 || int(payload["limit"].(float64)) != 1 {
			t.Fatalf("offset/limit = %v/%v, want 2/1", payload["offset"], payload["limit"])
		}

		if payload["has_more"] != true {
			t.Fatalf("has_more = %v, want true", payload["has_more"])
		}

		items, ok := payload["items"].([]any)
		if !ok || len(items) != 1 {
			t.Fatalf("items = %v, want single item", payload["items"])
		}

		item := items[0].(map[string]any)
		if item["content_url"] != "/v1/files/content?file_id=5" {
			t.Fatalf("content_url = %v, want /v1/files/content?file_id=5", item["content_url"])
		}

		if item["thumbnail_url"] != "/v1/files/thumbnail?file_id=5" {
			t.Fatalf(
				"thumbnail_url = %v, want /v1/files/thumbnail?file_id=5",
				item["thumbnail_url"],
			)
		}
	})

	t.Run("file content streams managed bytes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "content.jpg")
		if err := os.WriteFile(path, []byte("jpeg bytes"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		store := &fakeMetadataStore{
			resolveContentHandle: func(fileID int64) (fileassets.Descriptor, error) {
				if fileID != 7 {
					t.Fatalf("fileID = %d, want 7", fileID)
				}

				return fileassets.Descriptor{
					FileID:   7,
					Hash:     strings.Repeat("c", 64),
					Path:     path,
					Filename: "file.jpg",
					MIME:     "image/jpeg",
				}, nil
			},
		}

		handler := newHandlerWithDeps(t, provider, store, false)
		req := httptest.NewRequest(http.MethodGet, "/v1/files/content?file_id=7", nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}

		if rr.Header().Get("Content-Type") != "image/jpeg" {
			t.Fatalf("Content-Type = %q, want image/jpeg", rr.Header().Get("Content-Type"))
		}

		if rr.Body.String() != "jpeg bytes" {
			t.Fatalf("body = %q, want jpeg bytes", rr.Body.String())
		}
	})

	t.Run("thumbnail endpoint sniffs content type when needed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "thumb.thumbnail")
		pngBytes := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0x00, 0x00, 0x00}
		if err := os.WriteFile(path, pngBytes, 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		store := &fakeMetadataStore{
			resolveThumbnailHandle: func(fileID int64) (fileassets.Descriptor, error) {
				return fileassets.Descriptor{
					FileID:   fileID,
					Hash:     strings.Repeat("d", 64),
					Path:     path,
					Filename: "thumb.thumbnail",
				}, nil
			},
		}

		handler := newHandlerWithDeps(t, provider, store, false)
		req := httptest.NewRequest(http.MethodGet, "/v1/files/thumbnail?file_id=8", nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}

		if rr.Header().Get("Content-Type") != "image/png" {
			t.Fatalf("Content-Type = %q, want image/png", rr.Header().Get("Content-Type"))
		}
	})

	t.Run("local import returns imported file payload", func(t *testing.T) {
		store := &fakeMetadataStore{
			importLocalHandle: func(request fileimport.Request) (fileimport.Result, error) {
				if request.Path != "/tmp/example.png" {
					t.Fatalf("request.Path = %q, want /tmp/example.png", request.Path)
				}

				return fileimport.Result{
					FileID:                    42,
					Hash:                      strings.Repeat("f", 64),
					AlreadyImported:           false,
					ManagedFileAlreadyPresent: false,
				}, nil
			},
		}

		handler := newHandlerWithDeps(t, provider, store, false)
		req := httptest.NewRequest(
			http.MethodPost,
			"/v1/import/local_file",
			strings.NewReader(`{"path":"/tmp/example.png"}`),
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}

		var payload map[string]any
		decodeJSON(t, rr.Body.Bytes(), &payload)

		if int(payload["file_id"].(float64)) != 42 {
			t.Fatalf("file_id = %v, want 42", payload["file_id"])
		}

		if payload["content_url"] != "/v1/files/content?file_id=42" {
			t.Fatalf("content_url = %v, want /v1/files/content?file_id=42", payload["content_url"])
		}
	})

	t.Run("local import maps request and not found errors", func(t *testing.T) {
		requestStore := &fakeMetadataStore{
			importLocalHandle: func(fileimport.Request) (fileimport.Result, error) {
				return fileimport.Result{}, &fileimport.RequestError{Message: "bad path"}
			},
		}

		handler := newHandlerWithDeps(t, provider, requestStore, false)
		req := httptest.NewRequest(
			http.MethodPost,
			"/v1/import/local_file",
			strings.NewReader(`{"path":"/tmp/example.png"}`),
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("request error status = %d, want %d", rr.Code, http.StatusBadRequest)
		}

		notFoundStore := &fakeMetadataStore{
			importLocalHandle: func(fileimport.Request) (fileimport.Result, error) {
				return fileimport.Result{}, &fileimport.NotFoundError{Message: "missing"}
			},
		}

		handler = newHandlerWithDeps(t, provider, notFoundStore, false)
		req = httptest.NewRequest(
			http.MethodPost,
			"/v1/import/local_file",
			strings.NewReader(`{"path":"/tmp/example.png"}`),
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr = httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Fatalf("not found status = %d, want %d", rr.Code, http.StatusNotFound)
		}
	})

	t.Run("local import rejects malformed JSON", func(t *testing.T) {
		store := &fakeMetadataStore{}
		handler := newHandlerWithDeps(t, provider, store, false)
		req := httptest.NewRequest(
			http.MethodPost,
			"/v1/import/local_file",
			strings.NewReader(`{"path":`),
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
		}
	})

	t.Run("trash returns trashed payload", func(t *testing.T) {
		store := &fakeMetadataStore{
			trashFileHandle: func(request filetrash.Request) (filetrash.Result, error) {
				if request.FileID != 42 {
					t.Fatalf("request.FileID = %d, want 42", request.FileID)
				}

				return filetrash.Result{
					FileID:            42,
					Trashed:           true,
					RemovedFromRecent: true,
				}, nil
			},
		}

		handler := newHandlerWithDeps(t, provider, store, false)
		req := httptest.NewRequest(
			http.MethodPost,
			"/v1/files/trash",
			strings.NewReader(`{"file_id":42}`),
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}

		var payload map[string]any
		decodeJSON(t, rr.Body.Bytes(), &payload)

		if int(payload["file_id"].(float64)) != 42 {
			t.Fatalf("file_id = %v, want 42", payload["file_id"])
		}

		if payload["state"] != "trashed" {
			t.Fatalf("state = %v, want trashed", payload["state"])
		}
	})

	t.Run("trash maps request and not found errors", func(t *testing.T) {
		requestStore := &fakeMetadataStore{
			trashFileHandle: func(filetrash.Request) (filetrash.Result, error) {
				return filetrash.Result{}, &filetrash.RequestError{Message: "bad file_id"}
			},
		}

		handler := newHandlerWithDeps(t, provider, requestStore, false)
		req := httptest.NewRequest(
			http.MethodPost,
			"/v1/files/trash",
			strings.NewReader(`{"file_id":42}`),
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("request error status = %d, want %d", rr.Code, http.StatusBadRequest)
		}

		notFoundStore := &fakeMetadataStore{
			trashFileHandle: func(filetrash.Request) (filetrash.Result, error) {
				return filetrash.Result{}, &filetrash.NotFoundError{Message: "missing"}
			},
		}

		handler = newHandlerWithDeps(t, provider, notFoundStore, false)
		req = httptest.NewRequest(
			http.MethodPost,
			"/v1/files/trash",
			strings.NewReader(`{"file_id":42}`),
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr = httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Fatalf("not found status = %d, want %d", rr.Code, http.StatusNotFound)
		}
	})

	t.Run("trash rejects malformed JSON", func(t *testing.T) {
		store := &fakeMetadataStore{}
		handler := newHandlerWithDeps(t, provider, store, false)
		req := httptest.NewRequest(
			http.MethodPost,
			"/v1/files/trash",
			strings.NewReader(`{"file_id":`),
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
		}
	})
}

func TestGetFileMetadata(t *testing.T) {
	provider := services.NewStaticProvider(services.DefaultCatalog())
	store := &fakeMetadataStore{
		rows: []filemetadata.Row{
			{
				"file_id":         int64(1),
				"hash":            strings.Repeat("a", 64),
				"size":            int64(123),
				"mime":            "image/jpeg",
				"filetype_human":  "jpeg",
				"filetype_enum":   1,
				"ext":             ".jpg",
				"width":           int64(640),
				"height":          int64(480),
				"duration":        nil,
				"num_frames":      nil,
				"num_words":       nil,
				"has_audio":       false,
				"filetype_forced": false,
				"original_mime":   nil,
				"blurhash":        "LKO2?U%2Tw=w]~RBVZRi};RPxuwH",
			},
			filemetadata.MissingHashRow(strings.Repeat("b", 64)),
		},
	}

	handler := newHandlerWithDeps(t, provider, store, false)
	accessKey := strings.Repeat("b", 64)

	t.Run("returns metadata with services objects by default", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			"/get_files/file_metadata?hashes=%5B%22"+strings.Repeat("a", 64)+"%22%2C%22"+strings.Repeat("b", 64)+"%22%5D&only_return_basic_information=true&include_blurhash=true",
			nil,
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", accessKey)
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}

		var payload map[string]any
		decodeJSON(t, rr.Body.Bytes(), &payload)

		metadata, ok := payload["metadata"].([]any)
		if !ok {
			t.Fatalf("metadata type = %T, want []any", payload["metadata"])
		}

		if len(metadata) != 2 {
			t.Fatalf("len(metadata) = %d, want 2", len(metadata))
		}

		if _, ok := payload["services"]; !ok {
			t.Fatal("services missing from response")
		}

		if _, ok := payload["services_v2"]; !ok {
			t.Fatal("services_v2 missing from response")
		}
	})

	t.Run("can suppress services objects", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			"/get_files/file_metadata?file_ids=%5B1%5D&only_return_identifiers=true&include_services_object=false",
			nil,
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", accessKey)
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}

		var payload map[string]any
		decodeJSON(t, rr.Body.Bytes(), &payload)

		if _, ok := payload["services"]; ok {
			t.Fatal("services unexpectedly present")
		}

		if _, ok := payload["services_v2"]; ok {
			t.Fatal("services_v2 unexpectedly present")
		}
	})

	t.Run("invalid hash is rejected", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			"/get_files/file_metadata?hashes=%5B%22deadbeef%22%5D&only_return_identifiers=true",
			nil,
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", accessKey)
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
		}
	})

	t.Run("parses full mode flags", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			"/get_files/file_metadata?file_ids=%5B1%5D&include_milliseconds=true&include_notes=true&detailed_url_information=true&include_services_object=false",
			nil,
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", accessKey)
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}

		if store.lastRequest == nil {
			t.Fatal("lastRequest = nil, want parsed request")
		}

		if store.lastRequest.OnlyReturnIdentifiers {
			t.Fatal("OnlyReturnIdentifiers = true, want false")
		}

		if store.lastRequest.OnlyReturnBasicInformation {
			t.Fatal("OnlyReturnBasicInformation = true, want false")
		}

		if !store.lastRequest.IncludeMilliseconds {
			t.Fatal("IncludeMilliseconds = false, want true")
		}

		if !store.lastRequest.IncludeNotes {
			t.Fatal("IncludeNotes = false, want true")
		}

		if !store.lastRequest.DetailedURLInformation {
			t.Fatal("DetailedURLInformation = false, want true")
		}

		if store.lastRequest.IncludeServicesObject {
			t.Fatal("IncludeServicesObject = true, want false")
		}
	})

	t.Run("unsupported full mode flags return not implemented", func(t *testing.T) {
		rejectingStore := &fakeMetadataStore{
			handle: func(request filemetadata.Request) ([]filemetadata.Row, error) {
				switch {
				case request.IncludeNotes:
					return nil, &filemetadata.UnsupportedError{Message: "include_notes=true is not implemented yet"}
				case request.DetailedURLInformation:
					return nil, &filemetadata.UnsupportedError{Message: "detailed_url_information=true is not implemented yet"}
				default:
					return []filemetadata.Row{}, nil
				}
			},
		}
		handler := newHandlerWithDeps(t, provider, rejectingStore, false)

		req := httptest.NewRequest(
			http.MethodGet,
			"/get_files/file_metadata?file_ids=%5B1%5D&include_notes=true",
			nil,
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", accessKey)
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotImplemented {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotImplemented)
		}

		req = httptest.NewRequest(
			http.MethodGet,
			"/get_files/file_metadata?file_ids=%5B1%5D&detailed_url_information=true",
			nil,
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", accessKey)
		rr = httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotImplemented {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotImplemented)
		}
	})

	t.Run("typed store errors map to HTTP status codes", func(t *testing.T) {
		handler := newHandlerWithDeps(
			t,
			provider,
			&fakeMetadataStore{err: &filemetadata.NotFoundError{Message: "missing"}},
			false,
		)

		req := httptest.NewRequest(
			http.MethodGet,
			"/get_files/file_metadata?file_ids=%5B1%5D&only_return_identifiers=true",
			nil,
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", accessKey)
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
		}

		handler = newHandlerWithDeps(
			t,
			provider,
			&fakeMetadataStore{err: io.ErrUnexpectedEOF},
			false,
		)

		req = httptest.NewRequest(
			http.MethodGet,
			"/get_files/file_metadata?file_ids=%5B1%5D&only_return_identifiers=true",
			nil,
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", accessKey)
		rr = httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusInternalServerError)
		}
	})
}

func TestOptionsRequest(t *testing.T) {
	handler := newTestHandler(t)
	req := httptest.NewRequest(http.MethodOptions, "/api_version", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	if rr.Header().Get("Allow") != http.MethodGet {
		t.Fatalf(
			"Allow = %q, want %q",
			rr.Header().Get("Allow"),
			http.MethodGet,
		)
	}

	if rr.Header().Get("Access-Control-Allow-Methods") != "" {
		t.Fatalf(
			"Access-Control-Allow-Methods = %q, want empty",
			rr.Header().Get("Access-Control-Allow-Methods"),
		)
	}
}

func TestOptionsRequest_WithOriginAllowedWhenCORSEnabled(t *testing.T) {
	handler := newCORSEnabledHandler(t)
	req := httptest.NewRequest(http.MethodOptions, "/api_version", nil)
	req.Header.Set("Origin", "https://example.com")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	if rr.Header().Get("Access-Control-Allow-Methods") != http.MethodGet {
		t.Fatalf(
			"Access-Control-Allow-Methods = %q, want %q",
			rr.Header().Get("Access-Control-Allow-Methods"),
			http.MethodGet,
		)
	}

	if rr.Header().Get("Access-Control-Allow-Headers") != "*" {
		t.Fatalf(
			"Access-Control-Allow-Headers = %q, want *",
			rr.Header().Get("Access-Control-Allow-Headers"),
		)
	}

	if rr.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf(
			"Access-Control-Allow-Origin = %q, want *",
			rr.Header().Get("Access-Control-Allow-Origin"),
		)
	}
}

func TestOptionsRequest_WithOriginRejectedWhenCORSSDisabled(t *testing.T) {
	handler := newTestHandler(t)
	req := httptest.NewRequest(http.MethodOptions, "/api_version", nil)
	req.Header.Set("Origin", "https://example.com")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}

	if rr.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf(
			"Access-Control-Allow-Origin = %q, want empty",
			rr.Header().Get("Access-Control-Allow-Origin"),
		)
	}
}

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()

	access, err := NewAccessControl(
		strings.Repeat("a", 64),
		"test-client",
		[]Permission{PermissionSearchAndFetchFiles, PermissionImportAndDeleteFiles},
	)
	if err != nil {
		t.Fatalf("NewAccessControl() error = %v", err)
	}

	return NewHandler(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		access,
		services.DefaultProvider(),
		nil,
		nil,
		nil,
		nil,
		nil,
		false,
	)
}

func newAccessControlledHandler(t *testing.T) (*AccessControl, http.Handler) {
	t.Helper()

	access, err := NewAccessControl(
		strings.Repeat("b", 64),
		"test-client",
		[]Permission{PermissionSearchAndFetchFiles, PermissionImportAndDeleteFiles},
	)
	if err != nil {
		t.Fatalf("NewAccessControl() error = %v", err)
	}

	handler := NewHandler(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		access,
		services.DefaultProvider(),
		nil,
		nil,
		nil,
		nil,
		nil,
		false,
	)

	return access, handler
}

func newCORSEnabledHandler(t *testing.T) http.Handler {
	t.Helper()

	access, err := NewAccessControl(
		strings.Repeat("d", 64),
		"test-client",
		[]Permission{PermissionSearchAndFetchFiles, PermissionImportAndDeleteFiles},
	)
	if err != nil {
		t.Fatalf("NewAccessControl() error = %v", err)
	}

	return NewHandler(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		access,
		services.DefaultProvider(),
		nil,
		nil,
		nil,
		nil,
		nil,
		true,
	)
}

func newHandlerWithDeps(
	t *testing.T,
	provider services.Provider,
	store filemetadata.Store,
	enableCORS bool,
) http.Handler {
	t.Helper()

	access, err := NewAccessControl(
		strings.Repeat("b", 64),
		"test-client",
		[]Permission{PermissionSearchAndFetchFiles, PermissionImportAndDeleteFiles},
	)
	if err != nil {
		t.Fatalf("NewAccessControl() error = %v", err)
	}

	var browseStore librarybrowse.Store
	var assetStore fileassets.Store
	var importStore fileimport.Store
	var trashStore filetrash.Store
	if store != nil {
		if candidate, ok := store.(librarybrowse.Store); ok {
			browseStore = candidate
		}

		if candidate, ok := store.(fileassets.Store); ok {
			assetStore = candidate
		}

		if candidate, ok := store.(fileimport.Store); ok {
			importStore = candidate
		}

		if candidate, ok := store.(filetrash.Store); ok {
			trashStore = candidate
		}
	}

	return NewHandler(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		access,
		provider,
		store,
		browseStore,
		assetStore,
		importStore,
		trashStore,
		enableCORS,
	)
}

type fakeMetadataStore struct {
	rows                   []filemetadata.Row
	err                    error
	handle                 func(filemetadata.Request) ([]filemetadata.Row, error)
	listRecentHandle       func(librarybrowse.Request) (librarybrowse.Page, error)
	resolveContentHandle   func(int64) (fileassets.Descriptor, error)
	resolveThumbnailHandle func(int64) (fileassets.Descriptor, error)
	importLocalHandle      func(fileimport.Request) (fileimport.Result, error)
	trashFileHandle        func(filetrash.Request) (filetrash.Result, error)
	lastRequest            *filemetadata.Request
}

func (s *fakeMetadataStore) GetMetadata(
	_ context.Context,
	request filemetadata.Request,
) ([]filemetadata.Row, error) {
	copy := request
	s.lastRequest = &copy
	if s.handle != nil {
		return s.handle(request)
	}
	return s.rows, s.err
}

func (s *fakeMetadataStore) ListRecent(
	_ context.Context,
	request librarybrowse.Request,
) (librarybrowse.Page, error) {
	if s.listRecentHandle != nil {
		return s.listRecentHandle(request)
	}

	return librarybrowse.Page{}, nil
}

func (s *fakeMetadataStore) ResolveFileContent(
	_ context.Context,
	fileID int64,
) (fileassets.Descriptor, error) {
	if s.resolveContentHandle != nil {
		return s.resolveContentHandle(fileID)
	}

	return fileassets.Descriptor{}, nil
}

func (s *fakeMetadataStore) ResolveThumbnail(
	_ context.Context,
	fileID int64,
) (fileassets.Descriptor, error) {
	if s.resolveThumbnailHandle != nil {
		return s.resolveThumbnailHandle(fileID)
	}

	return fileassets.Descriptor{}, nil
}

func (s *fakeMetadataStore) ImportLocalPath(
	_ context.Context,
	request fileimport.Request,
) (fileimport.Result, error) {
	if s.importLocalHandle != nil {
		return s.importLocalHandle(request)
	}

	return fileimport.Result{}, nil
}

func (s *fakeMetadataStore) TrashFile(
	_ context.Context,
	request filetrash.Request,
) (filetrash.Result, error) {
	if s.trashFileHandle != nil {
		return s.trashFileHandle(request)
	}

	return filetrash.Result{}, nil
}

func assertDefaultServiceDiscoveryPayload(t *testing.T, payload map[string]any) {
	t.Helper()

	servicesValue, ok := payload["services_v2"].([]any)
	if !ok {
		t.Fatalf("services_v2 type = %T, want []any", payload["services_v2"])
	}

	serviceByName := map[string]map[string]any{}
	for _, item := range servicesValue {
		service, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("service item type = %T, want map[string]any", item)
		}

		name, ok := service["name"].(string)
		if !ok {
			t.Fatalf("service name type = %T, want string", service["name"])
		}

		serviceByName[name] = service
	}

	localTagsValue, ok := payload["local_tags"].([]any)
	if !ok {
		t.Fatalf("local_tags type = %T, want []any", payload["local_tags"])
	}

	if len(localTagsValue) != 2 {
		t.Fatalf("len(local_tags) = %d, want 2", len(localTagsValue))
	}

	if _, ok := serviceByName["downloader tags"]; !ok {
		t.Fatal("downloader tags missing from discovery payload")
	}

	favourites, ok := serviceByName["favourites"]
	if !ok {
		t.Fatal("favourites missing from discovery payload")
	}

	if _, ok := payload["local_ratings"]; ok {
		t.Fatal("local_ratings unexpectedly present in grouped discovery payload")
	}

	if got, _ := favourites["star_shape"].(string); got != "fat star" {
		t.Fatalf("favourites star_shape = %q, want %q", got, "fat star")
	}

	showInThumbnail, ok := favourites["show_in_thumbnail"].(bool)
	if !ok || showInThumbnail {
		t.Fatalf("favourites show_in_thumbnail = %v, want explicit false", favourites["show_in_thumbnail"])
	}

	showInThumbnailEvenWhenNull, ok := favourites["show_in_thumbnail_even_when_null"].(bool)
	if !ok || showInThumbnailEvenWhenNull {
		t.Fatalf(
			"favourites show_in_thumbnail_even_when_null = %v, want explicit false",
			favourites["show_in_thumbnail_even_when_null"],
		)
	}

	colours, ok := favourites["colours"].(map[string]any)
	if !ok {
		t.Fatalf("favourites colours type = %T, want map[string]any", favourites["colours"])
	}

	likeColour, ok := colours["like"].(map[string]any)
	if !ok {
		t.Fatalf("favourites like colour type = %T, want map[string]any", colours["like"])
	}

	if got, _ := likeColour["brush"].(string); got != "#F0F041" {
		t.Fatalf("favourites like brush = %q, want %q", got, "#F0F041")
	}

	dislikeColour, ok := colours["dislike"].(map[string]any)
	if !ok {
		t.Fatalf("favourites dislike colour type = %T, want map[string]any", colours["dislike"])
	}

	if got, _ := dislikeColour["brush"].(string); got != "#C85078" {
		t.Fatalf("favourites dislike brush = %q, want %q", got, "#C85078")
	}

	nullColour, ok := colours["null"].(map[string]any)
	if !ok {
		t.Fatalf("favourites null colour type = %T, want map[string]any", colours["null"])
	}

	if got, _ := nullColour["brush"].(string); got != "#BFBFBF" {
		t.Fatalf("favourites null brush = %q, want %q", got, "#BFBFBF")
	}

	mixedColour, ok := colours["mixed"].(map[string]any)
	if !ok {
		t.Fatalf("favourites mixed colour type = %T, want map[string]any", colours["mixed"])
	}

	if got, _ := mixedColour["brush"].(string); got != "#5F5F5F" {
		t.Fatalf("favourites mixed brush = %q, want %q", got, "#5F5F5F")
	}
}

func decodeJSON(t *testing.T, raw []byte, target any) {
	t.Helper()

	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
}
