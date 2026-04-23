package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
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
	coreptrsync "github.com/official-elinas/hydrus-go/internal/core/ptrsync"
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

	t.Run("get service by visible name returns service", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			"/get_service?service_name=repository%20updates",
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

	t.Run("get service by visible name is case insensitive", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			"/get_service?service_name=RePoSiToRy%20UpDaTeS",
			nil,
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", access.AccessKey())
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}
	})

	t.Run("ptr status returns daemon-owned sync status", func(t *testing.T) {
		ptrStore := stubPTRStore{
			status: coreptrsync.Status{
				Enabled:                  true,
				Configured:               true,
				ServiceName:              "public tag repository",
				ServiceKey:               "7075626c696320746167207265706f7369746f7279",
				Host:                     "ptr.hydrus.network",
				Port:                     45871,
				AccountMode:              coreptrsync.AccountModeSharedReadOnly,
				Phase:                    coreptrsync.PhaseIdle,
				MetadataSlice:            7,
				DownloadedUpdateCount:    11,
				ProcessedDefinitionCount: 5,
				ProcessedContentCount:    6,
				UpdatedAtMS:              1700000000123,
			},
		}

		handler := newHandlerWithPTRStore(t, ptrStore)
		req := httptest.NewRequest(http.MethodGet, "/service/ptr/status", nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", access.AccessKey())
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}

		var payload map[string]any
		decodeJSON(t, rr.Body.Bytes(), &payload)

		ptr, ok := payload["ptr"].(map[string]any)
		if !ok {
			t.Fatalf("ptr payload type = %T, want map[string]any", payload["ptr"])
		}

		if ptr["service_name"] != "public tag repository" {
			t.Fatalf("service_name = %v, want public tag repository", ptr["service_name"])
		}

		if ptr["account_mode"] != coreptrsync.AccountModeSharedReadOnly {
			t.Fatalf("account_mode = %v, want %s", ptr["account_mode"], coreptrsync.AccountModeSharedReadOnly)
		}

		if ptr["phase"] != coreptrsync.PhaseIdle {
			t.Fatalf("phase = %v, want %s", ptr["phase"], coreptrsync.PhaseIdle)
		}
	})

	t.Run("ptr status returns not implemented without a store", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/service/ptr/status", nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", access.AccessKey())
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotImplemented {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotImplemented)
		}
	})

	t.Run("ptr status returns internal error when store fails", func(t *testing.T) {
		ptrHandler := newHandlerWithPTRStore(t, stubPTRStore{err: errors.New("boom")})
		req := httptest.NewRequest(http.MethodGet, "/service/ptr/status", nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", access.AccessKey())
		rr := httptest.NewRecorder()

		ptrHandler.ServeHTTP(rr, req)

		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusInternalServerError)
		}
	})

	t.Run("ptr trigger returns immediate daemon-owned status", func(t *testing.T) {
		ptrStore := stubPTRStore{
			triggerStatus: coreptrsync.Status{
				Enabled:     true,
				Configured:  true,
				ServiceName: "public tag repository",
				Phase:       coreptrsync.PhaseSyncing,
				IsRunning:   true,
			},
		}

		handler := newHandlerWithPTRStore(t, ptrStore)
		req := httptest.NewRequest(http.MethodPost, "/service/ptr/sync", nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", access.AccessKey())
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}

		var payload map[string]any
		decodeJSON(t, rr.Body.Bytes(), &payload)

		ptr, ok := payload["ptr"].(map[string]any)
		if !ok {
			t.Fatalf("ptr payload type = %T, want map[string]any", payload["ptr"])
		}

		if ptr["phase"] != coreptrsync.PhaseSyncing {
			t.Fatalf("phase = %v, want %s", ptr["phase"], coreptrsync.PhaseSyncing)
		}

		if ptr["is_running"] != true {
			t.Fatalf("is_running = %v, want true", ptr["is_running"])
		}
	})

	t.Run("ptr trigger returns status payload when disabled", func(t *testing.T) {
		ptrStore := stubPTRStore{
			triggerStatus: coreptrsync.Status{
				Enabled:   false,
				Phase:     coreptrsync.PhaseDisabled,
				IsRunning: false,
			},
			triggerErr: coreptrsync.ErrSyncDisabled,
		}

		handler := newHandlerWithPTRStore(t, ptrStore)
		req := httptest.NewRequest(http.MethodPost, "/service/ptr/sync", nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", access.AccessKey())
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
		}

		var payload map[string]any
		decodeJSON(t, rr.Body.Bytes(), &payload)

		ptr := payload["ptr"].(map[string]any)
		if ptr["phase"] != coreptrsync.PhaseDisabled {
			t.Fatalf("phase = %v, want %s", ptr["phase"], coreptrsync.PhaseDisabled)
		}
	})

	t.Run("ptr trigger returns not implemented without a store", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/service/ptr/sync", nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", access.AccessKey())
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotImplemented {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotImplemented)
		}
	})

	t.Run("ptr trigger requires import and delete permission", func(t *testing.T) {
		searchOnlyAccess, err := NewAccessControl(
			strings.Repeat("f", 64),
			"search-only",
			[]Permission{PermissionSearchAndFetchFiles},
		)
		if err != nil {
			t.Fatalf("NewAccessControl() error = %v", err)
		}

		handler := NewHandler(
			slog.New(slog.NewTextHandler(io.Discard, nil)),
			searchOnlyAccess,
			services.DefaultProvider(),
			nil,
			nil,
			nil,
			nil,
			nil,
			stubPTRStore{},
			false,
		)

		req := httptest.NewRequest(http.MethodPost, "/service/ptr/sync", nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", searchOnlyAccess.AccessKey())
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
		}
	})

	t.Run("hidden service key returns unavailable", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			"/get_service?service_key=636c69656e7420617069",
			nil,
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", access.AccessKey())
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
		}

		if got := strings.TrimSpace(rr.Body.String()); got != "service exists but is not available through this endpoint" {
			t.Fatalf("body = %q, want unavailable message", got)
		}
	})

	t.Run("hidden service name is masked as not found", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			"/get_service?service_name=client%20api",
			nil,
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", access.AccessKey())
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
		}

		if got := strings.TrimSpace(rr.Body.String()); got != "service not found" {
			t.Fatalf("body = %q, want service not found", got)
		}
	})

	t.Run("hidden service mixed case name stays masked", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			"/get_service?service_name=ClIeNt%20ApI",
			nil,
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", access.AccessKey())
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
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

	t.Run("upload import requires DB-backed write store", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/import/upload", nil)
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
			nil,
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

	t.Run("upload import returns imported file payload", func(t *testing.T) {
		store := &fakeMetadataStore{
			importUploadHandle: func(request fileimport.UploadRequest) (fileimport.Result, error) {
				if request.Filename != "example.png" {
					t.Fatalf("request.Filename = %q, want example.png", request.Filename)
				}

				if request.LocalFileServiceKey != "service-key" {
					t.Fatalf(
						"request.LocalFileServiceKey = %q, want service-key",
						request.LocalFileServiceKey,
					)
				}

				if request.FileModifiedAtMS == nil || *request.FileModifiedAtMS != 1234567890 {
					t.Fatalf("request.FileModifiedAtMS = %v, want 1234567890", request.FileModifiedAtMS)
				}

				payload, err := os.ReadFile(request.StagedPath)
				if err != nil {
					t.Fatalf("ReadFile(staged upload) error = %v", err)
				}

				if !bytes.Equal(payload, []byte("png-bytes")) {
					t.Fatalf("staged payload = %q, want png-bytes", string(payload))
				}

				return fileimport.Result{
					FileID:                    52,
					Hash:                      strings.Repeat("9", 64),
					AlreadyImported:           false,
					ManagedFileAlreadyPresent: false,
				}, nil
			},
		}

		handler := newHandlerWithDeps(t, provider, store, false)
		req := newMultipartFormRequest(
			t,
			"/v1/import/upload",
			map[string]string{
				uploadFormLocalServiceKeyField:  "service-key",
				uploadFormFileModifiedAtMSField: "1234567890",
			},
			uploadFormFileField,
			"example.png",
			[]byte("png-bytes"),
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}

		var payload map[string]any
		decodeJSON(t, rr.Body.Bytes(), &payload)

		if int(payload["file_id"].(float64)) != 52 {
			t.Fatalf("file_id = %v, want 52", payload["file_id"])
		}

		if payload["content_url"] != "/v1/files/content?file_id=52" {
			t.Fatalf("content_url = %v, want /v1/files/content?file_id=52", payload["content_url"])
		}
	})

	t.Run("upload import maps request errors", func(t *testing.T) {
		store := &fakeMetadataStore{
			importUploadHandle: func(fileimport.UploadRequest) (fileimport.Result, error) {
				return fileimport.Result{}, &fileimport.RequestError{Message: "bad upload"}
			},
		}

		handler := newHandlerWithDeps(t, provider, store, false)
		req := newMultipartFormRequest(
			t,
			"/v1/import/upload",
			nil,
			uploadFormFileField,
			"example.png",
			[]byte("png-bytes"),
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
		}
	})

	t.Run("upload import rejects missing file field", func(t *testing.T) {
		store := &fakeMetadataStore{}
		handler := newHandlerWithDeps(t, provider, store, false)
		req := newMultipartFormRequest(
			t,
			"/v1/import/upload",
			map[string]string{uploadFormLocalServiceKeyField: "service-key"},
			"",
			"",
			nil,
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
		}
	})

	t.Run("upload import rejects malformed multipart requests", func(t *testing.T) {
		store := &fakeMetadataStore{}
		handler := newHandlerWithDeps(t, provider, store, false)
		req := httptest.NewRequest(
			http.MethodPost,
			"/v1/import/upload",
			strings.NewReader("not-a-multipart-request"),
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
		}
	})

	t.Run("upload import rejects oversized bodies", func(t *testing.T) {
		originalLimit := uploadRequestBodyLimitBytes
		uploadRequestBodyLimitBytes = 32
		defer func() {
			uploadRequestBodyLimitBytes = originalLimit
		}()

		store := &fakeMetadataStore{}
		handler := newHandlerWithDeps(t, provider, store, false)
		req := newMultipartFormRequest(
			t,
			"/v1/import/upload",
			nil,
			uploadFormFileField,
			"example.png",
			[]byte("png-bytes"),
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusRequestEntityTooLarge)
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

func TestGetService_HiddenBootstrapOnlyServices(t *testing.T) {
	access, handler := newAccessControlledHandler(t)

	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "deleted from anywhere by key is unavailable",
			path:       "/get_service?service_key=616c6c2064656c657465642066696c6573",
			wantStatus: http.StatusBadRequest,
			wantBody:   "service exists but is not available through this endpoint",
		},
		{
			name:       "deleted from anywhere by name is masked",
			path:       "/get_service?service_name=deleted%20from%20anywhere",
			wantStatus: http.StatusNotFound,
			wantBody:   "service not found",
		},
		{
			name:       "local notes by key is unavailable",
			path:       "/get_service?service_key=6c6f63616c206e6f746573",
			wantStatus: http.StatusBadRequest,
			wantBody:   "service exists but is not available through this endpoint",
		},
		{
			name:       "local notes by name is masked",
			path:       "/get_service?service_name=local%20notes",
			wantStatus: http.StatusNotFound,
			wantBody:   "service not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Header.Set("Hydrus-Client-API-Access-Key", access.AccessKey())
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rr.Code, tt.wantStatus)
			}

			if got := strings.TrimSpace(rr.Body.String()); got != tt.wantBody {
				t.Fatalf("body = %q, want %q", got, tt.wantBody)
			}
		})
	}
}

func TestGetServiceByNameUsesDiscoveryVisibleCatalog(t *testing.T) {
	visibleService := services.Service{
		Name:       "client api",
		ServiceKey: "76697369626c6520636c69656e7420617069",
		Type:       services.TypeLocalFileDomain,
		TypePretty: services.TypePretty(services.TypeLocalFileDomain),
	}
	hiddenService := services.Service{
		Name:       "client api",
		ServiceKey: "636c69656e7420617069",
		Type:       services.TypeClientAPIService,
		TypePretty: services.TypePretty(services.TypeClientAPIService),
	}

	provider := services.NewStaticProviderWithLookupCatalog(
		services.Catalog{visibleService},
		services.Catalog{hiddenService, visibleService},
	)
	handler := newHandlerWithDeps(t, provider, nil, false)

	req := httptest.NewRequest(
		http.MethodGet,
		"/get_service?service_name=client%20api",
		nil,
	)
	req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
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

	if service["service_key"] != visibleService.ServiceKey {
		t.Fatalf("service.service_key = %v, want %q", service["service_key"], visibleService.ServiceKey)
	}
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

	t.Run("services objects include hidden services referenced by metadata", func(t *testing.T) {
		allowsZero := true
		minStars := 0
		maxStars := 7
		hiddenRepoRating := services.Service{
			Name:       "repo stars",
			ServiceKey: "7265706f2d7374617273",
			Type:       services.TypeRatingNumericalRepository,
			TypePretty: services.TypePretty(services.TypeRatingNumericalRepository),
			StarShape:  "circle",
			AllowsZero: &allowsZero,
			MinStars:   &minStars,
			MaxStars:   &maxStars,
		}

		repoProvider := services.NewStaticProviderWithLookupCatalog(
			services.DefaultCatalog(),
			append(services.BootstrapCatalog(), hiddenRepoRating),
		)
		repoStore := &fakeMetadataStore{
			handle: func(request filemetadata.Request) ([]filemetadata.Row, error) {
				return []filemetadata.Row{{
					"file_id": int64(1),
					"hash":    strings.Repeat("a", 64),
					"ratings": map[string]any{
						hiddenRepoRating.ServiceKey: 6,
					},
				}}, nil
			},
		}

		handler := newHandlerWithDeps(t, repoProvider, repoStore, false)
		req := httptest.NewRequest(
			http.MethodGet,
			"/get_files/file_metadata?file_ids=%5B1%5D",
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

		servicesByKey, ok := payload["services"].(map[string]any)
		if !ok {
			t.Fatalf("services type = %T, want map[string]any", payload["services"])
		}

		if _, ok := servicesByKey[hiddenRepoRating.ServiceKey]; !ok {
			t.Fatalf("services missing hidden repo rating %q", hiddenRepoRating.ServiceKey)
		}

		servicesValue, ok := payload["services_v2"].([]any)
		if !ok {
			t.Fatalf("services_v2 type = %T, want []any", payload["services_v2"])
		}

		var matched map[string]any
		for _, item := range servicesValue {
			service, ok := item.(map[string]any)
			if !ok {
				t.Fatalf("service item type = %T, want map[string]any", item)
			}

			if service["service_key"] == hiddenRepoRating.ServiceKey {
				matched = service
				break
			}
		}

		if matched == nil {
			t.Fatalf("services_v2 missing hidden repo rating %q", hiddenRepoRating.ServiceKey)
		}

		if got := matched["max_stars"]; got != float64(7) {
			t.Fatalf("hidden repo rating max_stars = %v, want 7", got)
		}

		if got := matched["allows_zero"]; got != true {
			t.Fatalf("hidden repo rating allows_zero = %v, want true", got)
		}

		if got := matched["star_shape"]; got != "circle" {
			t.Fatalf("hidden repo rating star_shape = %v, want circle", got)
		}
	})

	t.Run("passes through tags payload and hides legacy maps by default", func(t *testing.T) {
		tagStore := &fakeMetadataStore{
			handle: func(request filemetadata.Request) ([]filemetadata.Row, error) {
				if request.IncludeLegacyServiceKeysTags {
					t.Fatal("IncludeLegacyServiceKeysTags = true, want false by default")
				}

				return []filemetadata.Row{{
					"file_id": int64(1),
					"hash":    strings.Repeat("a", 64),
					"tags": map[string]map[string]any{
						"74616773": {
							"name":        "my tags",
							"type":        services.TypeLocalTag,
							"type_pretty": services.TypePretty(services.TypeLocalTag),
							"storage_tags": map[string][]string{
								"0": {"creator:alice"},
							},
							"display_tags": map[string][]string{
								"0": {"creator:alice"},
							},
						},
					},
				}}, nil
			},
		}

		handler := newHandlerWithDeps(t, provider, tagStore, false)
		req := httptest.NewRequest(
			http.MethodGet,
			"/get_files/file_metadata?file_ids=%5B1%5D&include_services_object=false",
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
		if !ok || len(metadata) != 1 {
			t.Fatalf("metadata = %v, want one row", payload["metadata"])
		}

		row, ok := metadata[0].(map[string]any)
		if !ok {
			t.Fatalf("metadata[0] type = %T, want map[string]any", metadata[0])
		}

		tags, ok := row["tags"].(map[string]any)
		if !ok {
			t.Fatalf("metadata[0][tags] type = %T, want map[string]any", row["tags"])
		}

		if _, ok := tags["74616773"]; !ok {
			t.Fatal("metadata[0][tags] missing expected service entry")
		}

		if _, ok := row["service_keys_to_statuses_to_tags"]; ok {
			t.Fatal("legacy service_keys_to_statuses_to_tags unexpectedly present")
		}

		if _, ok := row["service_keys_to_statuses_to_display_tags"]; ok {
			t.Fatal("legacy service_keys_to_statuses_to_display_tags unexpectedly present")
		}
	})

	t.Run("can include legacy tag maps when hide_service_keys_tags is false", func(t *testing.T) {
		tagStore := &fakeMetadataStore{
			handle: func(request filemetadata.Request) ([]filemetadata.Row, error) {
				if !request.IncludeLegacyServiceKeysTags {
					t.Fatal("IncludeLegacyServiceKeysTags = false, want true when hide_service_keys_tags=false")
				}

				return []filemetadata.Row{{
					"file_id": int64(1),
					"hash":    strings.Repeat("a", 64),
					"tags": map[string]map[string]any{
						"74616773": {
							"name":        "my tags",
							"type":        services.TypeLocalTag,
							"type_pretty": services.TypePretty(services.TypeLocalTag),
							"storage_tags": map[string][]string{
								"0": {"creator:alice"},
							},
							"display_tags": map[string][]string{
								"0": {"creator:alice"},
							},
						},
					},
					"service_keys_to_statuses_to_tags": map[string]map[string][]string{
						"74616773": {
							"0": {"creator:alice"},
						},
					},
					"service_keys_to_statuses_to_display_tags": map[string]map[string][]string{
						"74616773": {
							"0": {"creator:alice"},
						},
					},
				}}, nil
			},
		}

		handler := newHandlerWithDeps(t, provider, tagStore, false)
		req := httptest.NewRequest(
			http.MethodGet,
			"/get_files/file_metadata?file_ids=%5B1%5D&include_services_object=false&hide_service_keys_tags=false",
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
		if !ok || len(metadata) != 1 {
			t.Fatalf("metadata = %v, want one row", payload["metadata"])
		}

		row, ok := metadata[0].(map[string]any)
		if !ok {
			t.Fatalf("metadata[0] type = %T, want map[string]any", metadata[0])
		}

		if _, ok := row["service_keys_to_statuses_to_tags"].(map[string]any); !ok {
			t.Fatalf("metadata[0][service_keys_to_statuses_to_tags] type = %T, want map[string]any", row["service_keys_to_statuses_to_tags"])
		}

		if _, ok := row["service_keys_to_statuses_to_display_tags"].(map[string]any); !ok {
			t.Fatalf("metadata[0][service_keys_to_statuses_to_display_tags] type = %T, want map[string]any", row["service_keys_to_statuses_to_display_tags"])
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

	t.Run("parses full mode flags and legacy tag compatibility toggle", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			"/get_files/file_metadata?file_ids=%5B1%5D&include_milliseconds=true&include_notes=true&detailed_url_information=true&create_new_file_ids=true&include_services_object=false&hide_service_keys_tags=false",
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

		if !store.lastRequest.CreateNewFileIDs {
			t.Fatal("CreateNewFileIDs = false, want true")
		}

		if store.lastRequest.IncludeServicesObject {
			t.Fatal("IncludeServicesObject = true, want false")
		}

		if !store.lastRequest.IncludeLegacyServiceKeysTags {
			t.Fatal("IncludeLegacyServiceKeysTags = false, want true")
		}
	})

	t.Run("passes through notes payload when requested", func(t *testing.T) {
		notesStore := &fakeMetadataStore{
			handle: func(request filemetadata.Request) ([]filemetadata.Row, error) {
				if !request.IncludeNotes {
					t.Fatal("IncludeNotes = false, want true")
				}

				return []filemetadata.Row{{
					"file_id": int64(1),
					"hash":    strings.Repeat("a", 64),
					"notes": map[string]string{
						"artist commentary": "hello from hydrus-go",
						"translation":       "line one\nline two",
					},
				}}, nil
			},
		}
		handler := newHandlerWithDeps(t, provider, notesStore, false)

		req := httptest.NewRequest(
			http.MethodGet,
			"/get_files/file_metadata?file_ids=%5B1%5D&include_notes=true&include_services_object=false",
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
		if !ok || len(metadata) != 1 {
			t.Fatalf("metadata = %v, want one row", payload["metadata"])
		}

		row, ok := metadata[0].(map[string]any)
		if !ok {
			t.Fatalf("metadata[0] type = %T, want map[string]any", metadata[0])
		}

		notes, ok := row["notes"].(map[string]any)
		if !ok {
			t.Fatalf("metadata[0][notes] type = %T, want map[string]any", row["notes"])
		}

		if got := notes["artist commentary"]; got != "hello from hydrus-go" {
			t.Fatalf("metadata[0][notes][artist commentary] = %v, want hello from hydrus-go", got)
		}

		if got := notes["translation"]; got != "line one\nline two" {
			t.Fatalf("metadata[0][notes][translation] = %v, want line one\\nline two", got)
		}
	})

	t.Run("passes through detailed known URLs payload when requested", func(t *testing.T) {
		detailedURLStore := &fakeMetadataStore{
			handle: func(request filemetadata.Request) ([]filemetadata.Row, error) {
				if !request.DetailedURLInformation {
					t.Fatal("DetailedURLInformation = false, want true")
				}

				return []filemetadata.Row{{
					"file_id": int64(1),
					"hash":    strings.Repeat("a", 64),
					"detailed_known_urls": []map[string]any{
						{
							"normalised_url":      "https://img.weirdbooru.com/images/ab/cd/abcdblahblahblah.jpg",
							"url_type":            5,
							"url_type_string":     "unknown url",
							"match_name":          "unknown url",
							"can_parse":           false,
							"cannot_parse_reason": "unknown url class",
						},
						{
							"normalised_url":      "https://otherbooru.org/index.php?id=123456&page=post&s=view",
							"url_type":            0,
							"url_type_string":     "post url",
							"match_name":          "otherbooru file page",
							"can_parse":           false,
							"cannot_parse_reason": "Could not find a parser for otherbooru file page URL Class!",
						},
					},
				}}, nil
			},
		}
		handler := newHandlerWithDeps(t, provider, detailedURLStore, false)

		req := httptest.NewRequest(
			http.MethodGet,
			"/get_files/file_metadata?file_ids=%5B1%5D&detailed_url_information=true&include_services_object=false",
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
		if !ok || len(metadata) != 1 {
			t.Fatalf("metadata = %v, want one row", payload["metadata"])
		}

		row, ok := metadata[0].(map[string]any)
		if !ok {
			t.Fatalf("metadata[0] type = %T, want map[string]any", metadata[0])
		}

		detailedKnownURLs, ok := row["detailed_known_urls"].([]any)
		if !ok {
			t.Fatalf("metadata[0][detailed_known_urls] type = %T, want []any", row["detailed_known_urls"])
		}

		if len(detailedKnownURLs) != 2 {
			t.Fatalf("len(metadata[0][detailed_known_urls]) = %d, want 2", len(detailedKnownURLs))
		}

		first, ok := detailedKnownURLs[0].(map[string]any)
		if !ok {
			t.Fatalf("metadata[0][detailed_known_urls][0] type = %T, want map[string]any", detailedKnownURLs[0])
		}

		if got := first["normalised_url"]; got != "https://img.weirdbooru.com/images/ab/cd/abcdblahblahblah.jpg" {
			t.Fatalf("metadata[0][detailed_known_urls][0][normalised_url] = %v, want weirdbooru image URL", got)
		}

		second, ok := detailedKnownURLs[1].(map[string]any)
		if !ok {
			t.Fatalf("metadata[0][detailed_known_urls][1] type = %T, want map[string]any", detailedKnownURLs[1])
		}

		if got := second["normalised_url"]; got != "https://otherbooru.org/index.php?id=123456&page=post&s=view" {
			t.Fatalf("metadata[0][detailed_known_urls][1][normalised_url] = %v, want normalised otherbooru URL", got)
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
		nil,
		enableCORS,
	)
}

func newHandlerWithPTRStore(t *testing.T, ptrStore coreptrsync.Store) http.Handler {
	t.Helper()

	access, err := NewAccessControl(
		strings.Repeat("b", 64),
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
		ptrStore,
		false,
	)
}

type stubPTRStore struct {
	status        coreptrsync.Status
	err           error
	triggerStatus coreptrsync.Status
	triggerErr    error
}

func (s stubPTRStore) Status(context.Context) (coreptrsync.Status, error) {
	return s.status, s.err
}

func (s stubPTRStore) Trigger(context.Context) (coreptrsync.Status, error) {
	return s.triggerStatus, s.triggerErr
}

type fakeMetadataStore struct {
	rows                   []filemetadata.Row
	err                    error
	handle                 func(filemetadata.Request) ([]filemetadata.Row, error)
	listRecentHandle       func(librarybrowse.Request) (librarybrowse.Page, error)
	resolveContentHandle   func(int64) (fileassets.Descriptor, error)
	resolveThumbnailHandle func(int64) (fileassets.Descriptor, error)
	importLocalHandle      func(fileimport.Request) (fileimport.Result, error)
	importUploadHandle     func(fileimport.UploadRequest) (fileimport.Result, error)
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

func (s *fakeMetadataStore) ImportUpload(
	_ context.Context,
	request fileimport.UploadRequest,
) (fileimport.Result, error) {
	if s.importUploadHandle != nil {
		return s.importUploadHandle(request)
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

func newMultipartFormRequest(
	t *testing.T,
	path string,
	fields map[string]string,
	fileFieldName string,
	fileName string,
	payload []byte,
) *http.Request {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatalf("WriteField(%q) error = %v", name, err)
		}
	}

	if fileFieldName != "" {
		part, err := writer.CreateFormFile(fileFieldName, fileName)
		if err != nil {
			t.Fatalf("CreateFormFile() error = %v", err)
		}

		if _, err := part.Write(payload); err != nil {
			t.Fatalf("multipart file write error = %v", err)
		}
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("multipart writer Close() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
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
