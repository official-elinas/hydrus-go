package httpapi

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.json
var openapiSpec []byte

func (s *Server) handleGetOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(openapiSpec)
}
