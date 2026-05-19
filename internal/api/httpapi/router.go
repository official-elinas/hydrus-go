package httpapi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/official-elinas/hydrus-go/internal/buildinfo"
	"github.com/official-elinas/hydrus-go/internal/core/clientapi"
	"github.com/official-elinas/hydrus-go/internal/core/clientsessions"
	coredownloader "github.com/official-elinas/hydrus-go/internal/core/downloader"
	"github.com/official-elinas/hydrus-go/internal/core/fileassets"
	"github.com/official-elinas/hydrus-go/internal/core/fileimport"
	"github.com/official-elinas/hydrus-go/internal/core/filemetadata"
	"github.com/official-elinas/hydrus-go/internal/core/filetrash"
	"github.com/official-elinas/hydrus-go/internal/core/librarybrowse"
	coreptrsync "github.com/official-elinas/hydrus-go/internal/core/ptrsync"
	"github.com/official-elinas/hydrus-go/internal/core/services"
)

type Server struct {
	logger          *slog.Logger
	access          *AccessControl
	services        services.Provider
	metadataStore   filemetadata.Store
	browseStore     librarybrowse.Store
	assetStore      fileassets.Store
	clientAPIStore  clientapi.Store
	downloaderStore coredownloader.Store
	importStore     fileimport.Store
	trashStore      filetrash.Store
	ptrStore        coreptrsync.Store
	sessionStore    clientsessions.Store
	enableCORS      bool
}

// NewHandler constructs the bootstrap hydrus-go HTTP API handler.
func NewHandler(
	logger *slog.Logger,
	access *AccessControl,
	serviceProvider services.Provider,
	metadataStore filemetadata.Store,
	browseStore librarybrowse.Store,
	assetStore fileassets.Store,
	clientAPIStore clientapi.Store,
	downloaderStore coredownloader.Store,
	importStore fileimport.Store,
	trashStore filetrash.Store,
	ptrStore coreptrsync.Store,
	sessionStore clientsessions.Store,
	enableCORS bool,
) http.Handler {
	server := &Server{
		logger:          logger,
		access:          access,
		services:        serviceProvider,
		metadataStore:   metadataStore,
		browseStore:     browseStore,
		assetStore:      assetStore,
		clientAPIStore:  clientAPIStore,
		downloaderStore: downloaderStore,
		importStore:     importStore,
		trashStore:      trashStore,
		ptrStore:        ptrStore,
		sessionStore:    sessionStore,
		enableCORS:      enableCORS,
	}

	mux := http.NewServeMux()
	mux.Handle("/", server.get("/", server.handleWelcome))
	mux.Handle("/healthz", server.get("/healthz", server.handleHealthz))
	mux.Handle("/api_version", server.get("/api_version", server.handleAPIVersion))
	mux.Handle("/verify_access_key", server.get("/verify_access_key", server.handleVerifyAccessKey))
	mux.Handle("/session_key", server.get("/session_key", server.handleSessionKey))
	mux.Handle("/get_services", server.get("/get_services", server.handleGetServices))
	mux.Handle("/get_service", server.get("/get_service", server.handleGetService))
	mux.Handle("/v1/tags/autocomplete", server.get("/v1/tags/autocomplete", server.handleGetTagAutocomplete))
	mux.Handle("/add_files/add_file", server.post("/add_files/add_file", server.handleHydrusAddFile))
	mux.Handle("/add_urls/associate_url", server.post("/add_urls/associate_url", server.handleHydrusAssociateURL))
	mux.Handle("/add_notes/set_notes", server.post("/add_notes/set_notes", server.handleHydrusSetNotes))
	mux.Handle("/edit_times/set_time", server.post("/edit_times/set_time", server.handleHydrusSetTime))
	mux.Handle("/v1/downloader/status", server.get("/v1/downloader/status", server.handleGetDownloaderStatus))
	mux.Handle("/v1/downloader/downloaders", server.get("/v1/downloader/downloaders", server.handleGetDownloaderDownloaders))
	mux.Handle("/v1/downloader/url", server.post("/v1/downloader/url", server.handlePostDownloaderURL))
	mux.Handle("/v1/downloader/subscription", server.post("/v1/downloader/subscription", server.handlePostDownloaderSubscription))
	mux.Handle(
		"/get_files/file_metadata",
		server.get("/get_files/file_metadata", server.handleGetFileMetadata),
	)
	mux.Handle("/v1/library/recent", server.get("/v1/library/recent", server.handleListRecentFiles))
	mux.Handle("/v1/library/search", server.get("/v1/library/search", server.handleSearchFiles))
	mux.Handle("/v1/files/content", server.get("/v1/files/content", server.handleGetFileContent))
	mux.Handle("/v1/files/thumbnail", server.get("/v1/files/thumbnail", server.handleGetFileThumbnail))
	mux.Handle("/v1/files/trash", server.post("/v1/files/trash", server.handleTrashFile))
	mux.Handle("/v1/import/local_file", server.post("/v1/import/local_file", server.handleImportLocalFile))
	mux.Handle("/v1/import/url", server.post("/v1/import/url", server.handleImportURL))
	mux.Handle("/v1/import/upload", server.post("/v1/import/upload", server.handleImportUpload))
	mux.Handle("/add_tags/add_tags", server.post("/add_tags/add_tags", server.handlePostAddTags))
	mux.Handle("/manage_database/force_commit", server.post("/manage_database/force_commit", server.handlePostDatabaseForceCommit))
	mux.Handle("/manage_database/integrity_check", server.post("/manage_database/integrity_check", server.handlePostDatabaseIntegrityCheck))
	mux.Handle("/manage_services/commit_pending", server.post("/manage_services/commit_pending", server.handlePostCommitPending))
	mux.Handle("/manage_services/pending_counts", server.get("/manage_services/pending_counts", server.handleGetPendingCounts))
	mux.Handle("/service/ptr/status", server.get("/service/ptr/status", server.handleGetPTRStatus))
	mux.Handle("/service/ptr/sync", server.post("/service/ptr/sync", server.handlePostPTRSync))
	mux.HandleFunc("GET /v1/sessions", server.handleListSessions)
	mux.HandleFunc("POST /v1/sessions", server.handleCreateSession)
	mux.HandleFunc("PATCH /v1/sessions/{id}", server.handleUpdateSession)
	mux.HandleFunc("DELETE /v1/sessions/{id}", server.handleDeleteSession)

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

func (s *Server) post(path string, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			writeError(w, http.StatusNotFound, "not found")
			return
		}

		w.Header().Set("Allow", http.MethodPost)

		switch r.Method {
		case http.MethodPost:
			s.writeCORSHeaders(w, r)
			next(w, r)
		case http.MethodOptions:
			if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
				if !s.enableCORS {
					writeError(w, http.StatusForbidden, "CORS is disabled")
					return
				}

				s.writeCORSHeaders(w, r)
				w.Header().Set("Access-Control-Allow-Methods", http.MethodPost)
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

	request, statusCode, err := parseServiceLookupRequest(r)
	if err != nil {
		writeError(w, statusCode, err.Error())
		return
	}

	var service services.Service
	if request.serviceKey != "" {
		service, statusCode, err = s.lookupServiceByKey(r.Context(), request.serviceKey)
	} else {
		service, statusCode, err = s.lookupPublicServiceByName(r.Context(), request.serviceName)
	}
	if err != nil {
		writeError(w, statusCode, err.Error())
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

type serviceLookupRequest struct {
	serviceKey  string
	serviceName string
}

func parseServiceLookupRequest(r *http.Request) (serviceLookupRequest, int, error) {
	serviceKey := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("service_key")))
	serviceName := strings.TrimSpace(r.URL.Query().Get("service_name"))

	if serviceKey == "" && serviceName == "" {
		return serviceLookupRequest{}, http.StatusBadRequest, fmt.Errorf(
			"service_key or service_name is required",
		)
	}

	if serviceKey != "" {
		if _, err := hex.DecodeString(serviceKey); err != nil {
			return serviceLookupRequest{}, http.StatusBadRequest, fmt.Errorf(
				"invalid service_key: %w",
				err,
			)
		}
	}

	return serviceLookupRequest{
		serviceKey:  serviceKey,
		serviceName: serviceName,
	}, http.StatusOK, nil
}

func (s *Server) lookupServiceByKey(
	ctx context.Context,
	serviceKey string,
) (services.Service, int, error) {
	service, ok, err := s.services.ByKey(ctx, serviceKey)
	if err != nil {
		return services.Service{}, http.StatusInternalServerError, fmt.Errorf(
			"load service by key: %w",
			err,
		)
	}

	if !ok {
		return services.Service{}, http.StatusNotFound, fmt.Errorf("service not found")
	}

	if !services.IsDiscoveryAllowed(service.Type) {
		return services.Service{}, http.StatusBadRequest, fmt.Errorf(
			"service exists but is not available through this endpoint",
		)
	}

	return service, http.StatusOK, nil
}

func (s *Server) lookupPublicServiceByName(
	ctx context.Context,
	serviceName string,
) (services.Service, int, error) {
	// Public service-name lookup intentionally resolves only against the
	// discovery-visible catalog so hidden bootstrap-only services stay masked as
	// 404 even when direct provider lookups can resolve them.
	catalog, err := s.services.List(ctx)
	if err != nil {
		return services.Service{}, http.StatusInternalServerError, fmt.Errorf(
			"load service catalog: %w",
			err,
		)
	}

	service, ok := catalog.ByName(serviceName)

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
