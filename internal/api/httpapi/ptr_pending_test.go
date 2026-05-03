package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	coreptrsync "github.com/official-elinas/hydrus-go/internal/core/ptrsync"
)

type pendingCountStub struct {
	info coreptrsync.PendingInfo
	err  error
}

func (s pendingCountStub) Status(context.Context) (coreptrsync.Status, error) {
	return coreptrsync.Status{}, nil
}

func (s pendingCountStub) Trigger(context.Context) (coreptrsync.Status, error) {
	return coreptrsync.Status{}, nil
}

func (s pendingCountStub) AddPendingMappings(
	_ context.Context,
	_ coreptrsync.PendingMappingsRequest,
) (coreptrsync.PendingMappingsResult, error) {
	return coreptrsync.PendingMappingsResult{}, nil
}

func (s pendingCountStub) CommitPending(
	_ context.Context,
	_ coreptrsync.CommitPendingRequest,
) (coreptrsync.CommitPendingResult, error) {
	return coreptrsync.CommitPendingResult{}, nil
}

func (s pendingCountStub) PendingMappingCount(
	_ context.Context,
	_ coreptrsync.PendingCountRequest,
) (coreptrsync.PendingInfo, error) {
	return s.info, s.err
}

func TestGetPendingCounts(t *testing.T) {
	t.Run("returns not implemented without ptr store", func(t *testing.T) {
		handler := newHandlerWithPTRStore(t, nil)
		req := httptest.NewRequest(http.MethodGet, "/manage_services/pending_counts", nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotImplemented {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotImplemented)
		}
	})

	t.Run("requires edit file tags permission", func(t *testing.T) {
		handler := newHandlerWithPTRStore(t, pendingCountStub{})
		req := httptest.NewRequest(http.MethodGet, "/manage_services/pending_counts", nil)
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
		}
	})

	t.Run("returns zero count for default service key", func(t *testing.T) {
		stub := pendingCountStub{
			info: coreptrsync.PendingInfo{
				ServiceKey:   coreptrsync.DaemonServiceKeyHex(),
				PendingCount: 0,
			},
		}
		handler := newHandlerWithPTRStore(t, stub)
		req := httptest.NewRequest(http.MethodGet, "/manage_services/pending_counts", nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}

		var payload map[string]any
		decodeJSON(t, rr.Body.Bytes(), &payload)

		if got, _ := payload["pending_count"].(float64); int64(got) != 0 {
			t.Fatalf("pending_count = %v, want 0", payload["pending_count"])
		}

		if got, _ := payload["service_key"].(string); got != coreptrsync.DaemonServiceKeyHex() {
			t.Fatalf("service_key = %q, want %q", got, coreptrsync.DaemonServiceKeyHex())
		}
	})

	t.Run("returns non-zero count", func(t *testing.T) {
		stub := pendingCountStub{
			info: coreptrsync.PendingInfo{
				ServiceKey:   coreptrsync.DaemonServiceKeyHex(),
				PendingCount: 42,
			},
		}
		handler := newHandlerWithPTRStore(t, stub)
		req := httptest.NewRequest(http.MethodGet, "/manage_services/pending_counts", nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}

		var payload map[string]any
		decodeJSON(t, rr.Body.Bytes(), &payload)

		if got, _ := payload["pending_count"].(float64); int64(got) != 42 {
			t.Fatalf("pending_count = %v, want 42", payload["pending_count"])
		}
	})

	t.Run("returns not found for unknown service key", func(t *testing.T) {
		stub := pendingCountStub{
			err: coreptrsync.ErrPTRServiceNotFound,
		}
		handler := newHandlerWithPTRStore(t, stub)
		req := httptest.NewRequest(http.MethodGet, "/manage_services/pending_counts?service_key=deadbeef", nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
		}
	})

	t.Run("returns service unavailable when sync is disabled", func(t *testing.T) {
		stub := pendingCountStub{
			err: coreptrsync.ErrSyncDisabled,
		}
		handler := newHandlerWithPTRStore(t, stub)
		req := httptest.NewRequest(http.MethodGet, "/manage_services/pending_counts", nil)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
		}
	})

	t.Run("passes service_key query param to store", func(t *testing.T) {
		var capturedKey string
		stub := captureKeyStub{
			capture: func(req coreptrsync.PendingCountRequest) {
				capturedKey = req.ServiceKey
			},
			info: coreptrsync.PendingInfo{
				ServiceKey:   coreptrsync.DaemonServiceKeyHex(),
				PendingCount: 0,
			},
		}
		handler := newHandlerWithPTRStore(t, stub)
		req := httptest.NewRequest(
			http.MethodGet,
			"/manage_services/pending_counts?service_key="+coreptrsync.DaemonServiceKeyHex(),
			nil,
		)
		req.Header.Set("Hydrus-Client-API-Access-Key", strings.Repeat("b", 64))
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}

		if capturedKey != coreptrsync.DaemonServiceKeyHex() {
			t.Fatalf("captured service_key = %q, want %q", capturedKey, coreptrsync.DaemonServiceKeyHex())
		}
	})
}

type captureKeyStub struct {
	capture func(coreptrsync.PendingCountRequest)
	info    coreptrsync.PendingInfo
	err     error
}

func (s captureKeyStub) Status(context.Context) (coreptrsync.Status, error) {
	return coreptrsync.Status{}, nil
}

func (s captureKeyStub) Trigger(context.Context) (coreptrsync.Status, error) {
	return coreptrsync.Status{}, nil
}

func (s captureKeyStub) AddPendingMappings(
	_ context.Context,
	_ coreptrsync.PendingMappingsRequest,
) (coreptrsync.PendingMappingsResult, error) {
	return coreptrsync.PendingMappingsResult{}, nil
}

func (s captureKeyStub) CommitPending(
	_ context.Context,
	_ coreptrsync.CommitPendingRequest,
) (coreptrsync.CommitPendingResult, error) {
	return coreptrsync.CommitPendingResult{}, nil
}

func (s captureKeyStub) PendingMappingCount(
	_ context.Context,
	req coreptrsync.PendingCountRequest,
) (coreptrsync.PendingInfo, error) {
	if s.capture != nil {
		s.capture(req)
	}
	return s.info, s.err
}
