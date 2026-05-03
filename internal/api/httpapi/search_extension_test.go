package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/official-elinas/hydrus-go/internal/core/librarybrowse"
	"github.com/official-elinas/hydrus-go/internal/core/services"
)

func TestSearchFilesExtension(t *testing.T) {
	t.Run("passes sort_by to store", func(t *testing.T) {
		var capturedReq librarybrowse.SearchRequest
		store := &fakeMetadataStore{
			searchByTagsHandle: func(req librarybrowse.SearchRequest) (librarybrowse.Page, error) {
				capturedReq = req
				return librarybrowse.Page{Items: []librarybrowse.Item{}}, nil
			},
		}

		handler := newHandlerWithDeps(t, services.DefaultProvider(), store, false)
		req := httptest.NewRequest(http.MethodGet, "/v1/library/search?sort_by=size_desc&limit=10", nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}

		if capturedReq.SortBy != librarybrowse.SortBySizeDesc {
			t.Fatalf("SortBy = %q, want %q", capturedReq.SortBy, librarybrowse.SortBySizeDesc)
		}
	})

	t.Run("rejects unknown sort_by value", func(t *testing.T) {
		store := &fakeMetadataStore{}
		handler := newHandlerWithDeps(t, services.DefaultProvider(), store, false)
		req := httptest.NewRequest(http.MethodGet, "/v1/library/search?sort_by=banana&limit=10", nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
		}
	})

	t.Run("passes system_predicates to store", func(t *testing.T) {
		var capturedReq librarybrowse.SearchRequest
		store := &fakeMetadataStore{
			searchByTagsHandle: func(req librarybrowse.SearchRequest) (librarybrowse.Page, error) {
				capturedReq = req
				return librarybrowse.Page{Items: []librarybrowse.Item{}}, nil
			},
		}

		handler := newHandlerWithDeps(t, services.DefaultProvider(), store, false)
		req := httptest.NewRequest(
			http.MethodGet,
			"/v1/library/search?system_predicates[]=size>=1000&system_predicates[]=width<800&limit=10",
			nil,
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}

		if len(capturedReq.SystemPredicates) != 2 {
			t.Fatalf("len(SystemPredicates) = %d, want 2", len(capturedReq.SystemPredicates))
		}

		p0 := capturedReq.SystemPredicates[0]
		if p0.Field != librarybrowse.PredicateFieldSize || p0.Op != librarybrowse.PredicateOpGTE || p0.Value != 1000 {
			t.Fatalf("SystemPredicates[0] = %+v, want {size >= 1000}", p0)
		}

		p1 := capturedReq.SystemPredicates[1]
		if p1.Field != librarybrowse.PredicateFieldWidth || p1.Op != librarybrowse.PredicateOpLT || p1.Value != 800 {
			t.Fatalf("SystemPredicates[1] = %+v, want {width < 800}", p1)
		}
	})

	t.Run("rejects malformed system predicate", func(t *testing.T) {
		store := &fakeMetadataStore{}
		handler := newHandlerWithDeps(t, services.DefaultProvider(), store, false)
		req := httptest.NewRequest(
			http.MethodGet,
			"/v1/library/search?system_predicates[]=notapredicate&limit=10",
			nil,
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
		}
	})

	t.Run("rejects unknown predicate field", func(t *testing.T) {
		store := &fakeMetadataStore{}
		handler := newHandlerWithDeps(t, services.DefaultProvider(), store, false)
		req := httptest.NewRequest(
			http.MethodGet,
			"/v1/library/search?system_predicates[]=duration>=5&limit=10",
			nil,
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
		}
	})

	t.Run("response echoes sort_by and system_predicates", func(t *testing.T) {
		store := &fakeMetadataStore{
			searchByTagsHandle: func(req librarybrowse.SearchRequest) (librarybrowse.Page, error) {
				return librarybrowse.Page{Items: []librarybrowse.Item{}}, nil
			},
		}

		handler := newHandlerWithDeps(t, services.DefaultProvider(), store, false)
		req := httptest.NewRequest(
			http.MethodGet,
			"/v1/library/search?sort_by=size_asc&system_predicates[]=height>=100&limit=10",
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

		if payload["sort_by"] != "size_asc" {
			t.Fatalf("sort_by = %v, want size_asc", payload["sort_by"])
		}

		preds, ok := payload["system_predicates"].([]any)
		if !ok || len(preds) != 1 {
			t.Fatalf("system_predicates = %v, want 1-element slice", payload["system_predicates"])
		}
	})

	t.Run("tags and sort_by and predicates all forwarded together", func(t *testing.T) {
		var capturedReq librarybrowse.SearchRequest
		store := &fakeMetadataStore{
			searchByTagsHandle: func(req librarybrowse.SearchRequest) (librarybrowse.Page, error) {
				capturedReq = req
				return librarybrowse.Page{Items: []librarybrowse.Item{}}, nil
			},
		}

		handler := newHandlerWithDeps(t, services.DefaultProvider(), store, false)
		url := fmt.Sprintf(
			"/v1/library/search?tags=%s&sort_by=import_oldest&system_predicates[]=size>500&limit=10",
			"character%3Abob",
		)
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}

		if len(capturedReq.Tags) != 1 || capturedReq.Tags[0] != "character:bob" {
			t.Fatalf("Tags = %v, want [character:bob]", capturedReq.Tags)
		}

		if capturedReq.SortBy != librarybrowse.SortByImportOldest {
			t.Fatalf("SortBy = %q, want import_oldest", capturedReq.SortBy)
		}

		if len(capturedReq.SystemPredicates) != 1 || capturedReq.SystemPredicates[0].Value != 500 {
			t.Fatalf("SystemPredicates = %v, want [{size > 500}]", capturedReq.SystemPredicates)
		}
	})
}

func TestSearchFilesFavoriteFilter(t *testing.T) {
	t.Run("favorite shorthand sets FavoriteFilter true", func(t *testing.T) {
		var capturedReq librarybrowse.SearchRequest
		store := &fakeMetadataStore{
			searchByTagsHandle: func(req librarybrowse.SearchRequest) (librarybrowse.Page, error) {
				capturedReq = req
				return librarybrowse.Page{Items: []librarybrowse.Item{}}, nil
			},
		}

		handler := newHandlerWithDeps(t, services.DefaultProvider(), store, false)
		req := httptest.NewRequest(http.MethodGet, "/v1/library/search?system_predicates[]=favorite&limit=10", nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}

		if capturedReq.FavoriteFilter == nil || !*capturedReq.FavoriteFilter {
			t.Fatalf("FavoriteFilter = %v, want *true", capturedReq.FavoriteFilter)
		}

		if len(capturedReq.SystemPredicates) != 0 {
			t.Fatalf("SystemPredicates = %v, want empty", capturedReq.SystemPredicates)
		}
	})

	t.Run("favourite=false sets FavoriteFilter false", func(t *testing.T) {
		var capturedReq librarybrowse.SearchRequest
		store := &fakeMetadataStore{
			searchByTagsHandle: func(req librarybrowse.SearchRequest) (librarybrowse.Page, error) {
				capturedReq = req
				return librarybrowse.Page{Items: []librarybrowse.Item{}}, nil
			},
		}

		handler := newHandlerWithDeps(t, services.DefaultProvider(), store, false)
		req := httptest.NewRequest(http.MethodGet, "/v1/library/search?system_predicates[]=favourite%3Dfalse&limit=10", nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}

		if capturedReq.FavoriteFilter == nil || *capturedReq.FavoriteFilter {
			t.Fatalf("FavoriteFilter = %v, want *false", capturedReq.FavoriteFilter)
		}
	})

	t.Run("favorite=invalid returns bad request", func(t *testing.T) {
		store := &fakeMetadataStore{}
		handler := newHandlerWithDeps(t, services.DefaultProvider(), store, false)
		req := httptest.NewRequest(http.MethodGet, "/v1/library/search?system_predicates[]=favorite%3Dmaybe&limit=10", nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
		}
	})

	t.Run("no FavoriteFilter when not specified", func(t *testing.T) {
		var capturedReq librarybrowse.SearchRequest
		store := &fakeMetadataStore{
			searchByTagsHandle: func(req librarybrowse.SearchRequest) (librarybrowse.Page, error) {
				capturedReq = req
				return librarybrowse.Page{Items: []librarybrowse.Item{}}, nil
			},
		}

		handler := newHandlerWithDeps(t, services.DefaultProvider(), store, false)
		req := httptest.NewRequest(http.MethodGet, "/v1/library/search?limit=10", nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}

		if capturedReq.FavoriteFilter != nil {
			t.Fatalf("FavoriteFilter = %v, want nil", capturedReq.FavoriteFilter)
		}
	})
}

func TestSearchFilesResolutionPredicate(t *testing.T) {
	t.Run("resolution>=WxH lowers into width and height predicates", func(t *testing.T) {
		var capturedReq librarybrowse.SearchRequest
		store := &fakeMetadataStore{
			searchByTagsHandle: func(req librarybrowse.SearchRequest) (librarybrowse.Page, error) {
				capturedReq = req
				return librarybrowse.Page{Items: []librarybrowse.Item{}}, nil
			},
		}

		handler := newHandlerWithDeps(t, services.DefaultProvider(), store, false)
		req := httptest.NewRequest(http.MethodGet, "/v1/library/search?system_predicates[]=resolution%3E%3D1280x720&limit=10", nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}

		if len(capturedReq.SystemPredicates) != 2 {
			t.Fatalf("len(SystemPredicates) = %d, want 2", len(capturedReq.SystemPredicates))
		}

		pw := capturedReq.SystemPredicates[0]
		if pw.Field != librarybrowse.PredicateFieldWidth || pw.Op != librarybrowse.PredicateOpGTE || pw.Value != 1280 {
			t.Fatalf("SystemPredicates[0] = %+v, want {width >= 1280}", pw)
		}

		ph := capturedReq.SystemPredicates[1]
		if ph.Field != librarybrowse.PredicateFieldHeight || ph.Op != librarybrowse.PredicateOpGTE || ph.Value != 720 {
			t.Fatalf("SystemPredicates[1] = %+v, want {height >= 720}", ph)
		}
	})

	t.Run("resolution=WxH lowers into exact width and height predicates", func(t *testing.T) {
		var capturedReq librarybrowse.SearchRequest
		store := &fakeMetadataStore{
			searchByTagsHandle: func(req librarybrowse.SearchRequest) (librarybrowse.Page, error) {
				capturedReq = req
				return librarybrowse.Page{Items: []librarybrowse.Item{}}, nil
			},
		}

		handler := newHandlerWithDeps(t, services.DefaultProvider(), store, false)
		req := httptest.NewRequest(http.MethodGet, "/v1/library/search?system_predicates[]=resolution%3D1920x1080&limit=10", nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}

		if len(capturedReq.SystemPredicates) != 2 {
			t.Fatalf("len(SystemPredicates) = %d, want 2", len(capturedReq.SystemPredicates))
		}

		if capturedReq.SystemPredicates[0].Value != 1920 || capturedReq.SystemPredicates[1].Value != 1080 {
			t.Fatalf("SystemPredicates = %+v, want width=1920 height=1080", capturedReq.SystemPredicates)
		}
	})

	t.Run("resolution with missing x separator returns bad request", func(t *testing.T) {
		store := &fakeMetadataStore{}
		handler := newHandlerWithDeps(t, services.DefaultProvider(), store, false)
		req := httptest.NewRequest(http.MethodGet, "/v1/library/search?system_predicates[]=resolution%3E%3D1280&limit=10", nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
		}
	})
}
