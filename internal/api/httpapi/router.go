package httpapi

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/official-elinas/hydrus-go/internal/buildinfo"
	"github.com/official-elinas/hydrus-go/internal/core/filemetadata"
	"github.com/official-elinas/hydrus-go/internal/core/services"
)

type Server struct {
	logger        *slog.Logger
	access        *AccessControl
	services      services.Provider
	metadataStore filemetadata.Store
	enableCORS    bool
}

// NewHandler constructs the bootstrap hydrus-go HTTP API handler.
func NewHandler(
	logger *slog.Logger,
	access *AccessControl,
	serviceProvider services.Provider,
	metadataStore filemetadata.Store,
	enableCORS bool,
) http.Handler {
	server := &Server{
		logger:        logger,
		access:        access,
		services:      serviceProvider,
		metadataStore: metadataStore,
		enableCORS:    enableCORS,
	}

	mux := http.NewServeMux()
	mux.Handle("/", server.get("/", server.handleWelcome))
	mux.Handle("/healthz", server.get("/healthz", server.handleHealthz))
	mux.Handle("/api_version", server.get("/api_version", server.handleAPIVersion))
	mux.Handle("/verify_access_key", server.get("/verify_access_key", server.handleVerifyAccessKey))
	mux.Handle("/session_key", server.get("/session_key", server.handleSessionKey))
	mux.Handle("/get_services", server.get("/get_services", server.handleGetServices))
	mux.Handle("/get_service", server.get("/get_service", server.handleGetService))
	mux.Handle(
		"/get_files/file_metadata",
		server.get("/get_files/file_metadata", server.handleGetFileMetadata),
	)

	return server.withGlobalMiddleware(mux)
}

func (s *Server) get(path string, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			writeError(w, http.StatusNotFound, "not found")
			return
		}

		w.Header().Set("Allow", http.MethodGet)

		switch r.Method {
		case http.MethodGet:
			s.writeCORSHeaders(w, r)
			next(w, r)
		case http.MethodOptions:
			if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
				if !s.enableCORS {
					writeError(w, http.StatusForbidden, "CORS is disabled")
					return
				}

				s.writeCORSHeaders(w, r)
				w.Header().Set("Access-Control-Allow-Methods", http.MethodGet)
			}

			w.WriteHeader(http.StatusOK)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	})
}

func (s *Server) withGlobalMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}

		headerValue := buildinfo.ServerHeader()
		recorder.Header().Set("Hydrus-Server", headerValue)
		recorder.Header().Set("Server", headerValue)

		next.ServeHTTP(recorder, r)

		s.logger.Debug(
			"served request",
			"method",
			r.Method,
			"path",
			r.URL.Path,
			"status",
			recorder.statusCode,
			"duration",
			time.Since(start),
		)
	})
}

func (s *Server) handleWelcome(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name":    "hydrus-go",
		"message": "hydrus-go headless daemon is running",
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleAPIVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) handleVerifyAccessKey(w http.ResponseWriter, r *http.Request) {
	principal, statusCode, err := s.access.Authorize(r)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"name":               principal.Name,
		"permits_everything": principal.PermitsEverything,
		"basic_permissions":  permissionInts(principal.BasicPermissions),
		"human_description":  permissionDescription(principal.BasicPermissions),
	})
}

func (s *Server) handleSessionKey(w http.ResponseWriter, r *http.Request) {
	principal, statusCode, err := s.access.Authorize(r)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}

	sessionKey, err := s.access.NewSession(principal.AccessKey)
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"session_key": sessionKey})
}

func (s *Server) handleGetServices(w http.ResponseWriter, r *http.Request) {
	_, statusCode, err := s.access.Authorize(
		r,
		PermissionImportAndDeleteFiles,
		PermissionEditFileTags,
		PermissionEditFileNotes,
		PermissionEditFileRelationships,
		PermissionEditFileRatings,
		PermissionManagePages,
		PermissionSearchAndFetchFiles,
	)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}

	catalog, err := s.services.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load services")
		return
	}

	body := map[string]any{
		"services":    catalog.LegacyMap(),
		"services_v2": catalog,
	}

	for category, list := range catalog.Grouped() {
		body[category] = list
	}

	writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleGetService(w http.ResponseWriter, r *http.Request) {
	_, statusCode, err := s.access.Authorize(
		r,
		PermissionImportAndDeleteFiles,
		PermissionEditFileTags,
		PermissionEditFileNotes,
		PermissionEditFileRelationships,
		PermissionEditFileRatings,
		PermissionManagePages,
		PermissionSearchAndFetchFiles,
	)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}

	service, statusCode, err := s.lookupService(r)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}

	if !services.IsDiscoveryAllowed(service.Type) {
		writeError(
			w,
			http.StatusBadRequest,
			"service exists but is not available through this endpoint",
		)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"service": map[string]any{
			"name":        service.Name,
			"service_key": service.ServiceKey,
			"type":        service.Type,
			"type_pretty": service.TypePretty,
		},
	})
}

func (s *Server) lookupService(r *http.Request) (services.Service, int, error) {
	serviceKey := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("service_key")))
	serviceName := strings.TrimSpace(r.URL.Query().Get("service_name"))

	if serviceKey == "" && serviceName == "" {
		return services.Service{}, http.StatusBadRequest, fmt.Errorf(
			"service_key or service_name is required",
		)
	}

	if serviceKey != "" {
		if _, err := hex.DecodeString(serviceKey); err != nil {
			return services.Service{}, http.StatusBadRequest, fmt.Errorf(
				"invalid service_key: %w",
				err,
			)
		}

		service, ok, err := s.services.ByKey(r.Context(), serviceKey)
		if err != nil {
			return services.Service{}, http.StatusInternalServerError, fmt.Errorf(
				"load service by key: %w",
				err,
			)
		}

		if !ok {
			return services.Service{}, http.StatusNotFound, fmt.Errorf("service not found")
		}

		return service, http.StatusOK, nil
	}

	service, ok, err := s.services.ByName(r.Context(), serviceName)
	if err != nil {
		return services.Service{}, http.StatusInternalServerError, fmt.Errorf(
			"load service by name: %w",
			err,
		)
	}

	if !ok {
		return services.Service{}, http.StatusNotFound, fmt.Errorf("service not found")
	}

	return service, http.StatusOK, nil
}

func writeJSON(w http.ResponseWriter, statusCode int, body map[string]any) {
	payload := map[string]any{}
	for key, value := range body {
		payload[key] = value
	}

	if _, ok := payload["version"]; !ok {
		payload["version"] = buildinfo.ClientAPIVersion
	}

	if _, ok := payload["hydrus_version"]; !ok {
		payload["hydrus_version"] = buildinfo.ReferenceHydrusVersion
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		writeError(
			w,
			http.StatusInternalServerError,
			"could not encode JSON response",
		)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	_, _ = w.Write(encoded)
}

func writeError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(statusCode)
	_, _ = io.WriteString(w, message)
}

func (s *Server) writeCORSHeaders(w http.ResponseWriter, r *http.Request) {
	if !s.enableCORS {
		return
	}

	if strings.TrimSpace(r.Header.Get("Origin")) == "" {
		return
	}

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "*")
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(statusCode int) {
	r.statusCode = statusCode
	r.ResponseWriter.WriteHeader(statusCode)
}
