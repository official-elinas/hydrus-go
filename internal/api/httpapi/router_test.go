package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/official-elinas/hydrus-go/internal/buildinfo"
	"github.com/official-elinas/hydrus-go/internal/core/filemetadata"
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

		if len(permissions) != 1 || int(permissions[0].(float64)) != int(PermissionSearchAndFetchFiles) {
			t.Fatalf("basic_permissions = %v, want [%d]", permissions, PermissionSearchAndFetchFiles)
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
		[]Permission{PermissionSearchAndFetchFiles},
	)
	if err != nil {
		t.Fatalf("NewAccessControl() error = %v", err)
	}

	return NewHandler(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		access,
		services.DefaultProvider(),
		nil,
		false,
	)
}

func newAccessControlledHandler(t *testing.T) (*AccessControl, http.Handler) {
	t.Helper()

	access, err := NewAccessControl(
		strings.Repeat("b", 64),
		"test-client",
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
		false,
	)

	return access, handler
}

func newCORSEnabledHandler(t *testing.T) http.Handler {
	t.Helper()

	access, err := NewAccessControl(
		strings.Repeat("d", 64),
		"test-client",
		[]Permission{PermissionSearchAndFetchFiles},
	)
	if err != nil {
		t.Fatalf("NewAccessControl() error = %v", err)
	}

	return NewHandler(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		access,
		services.DefaultProvider(),
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
		[]Permission{PermissionSearchAndFetchFiles},
	)
	if err != nil {
		t.Fatalf("NewAccessControl() error = %v", err)
	}

	return NewHandler(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		access,
		provider,
		store,
		enableCORS,
	)
}

type fakeMetadataStore struct {
	rows        []filemetadata.Row
	err         error
	handle      func(filemetadata.Request) ([]filemetadata.Row, error)
	lastRequest *filemetadata.Request
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

func decodeJSON(t *testing.T, raw []byte, target any) {
	t.Helper()

	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
}
