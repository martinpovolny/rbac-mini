package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
)

// Server wires the v1 endpoints.
type Server struct {
	db  *sql.DB
	mux *http.ServeMux
}

// New returns the HTTP handler with the v1 routes registered.
func New(db *sql.DB) *Server {
	s := &Server{db: db, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /api/rbac/v1/status/", s.handleStatus)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// handleStatus is the operator probe target (see SPEC §2).
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"api_version": 1})
}
