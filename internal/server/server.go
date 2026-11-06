package server

import (
	"database/sql"
	"net/http"
)

// Server wires the v1 endpoints.
type Server struct {
	db  *sql.DB
	mux *http.ServeMux
}

// New returns the HTTP handler. No routes are registered yet.
func New(db *sql.DB) *Server {
	s := &Server{db: db, mux: http.NewServeMux()}
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}
