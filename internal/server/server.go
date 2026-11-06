package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"rbac-go/internal/access"
	"rbac-go/internal/identity"
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
	s.mux.HandleFunc("GET /api/rbac/v1/ready/", s.handleReady)
	s.mux.HandleFunc("GET /api/rbac/v1/access/", s.handleAccess)
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

// handleReady pings the SQL DB: 200 when reachable, 503 otherwise.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := s.db.Ping(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unreachable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ready": true})
}

func writeError(w http.ResponseWriter, code int, detail string) {
	writeJSON(w, code, map[string]any{
		"errors": []map[string]any{{"detail": detail, "status": strconv.Itoa(code)}},
	})
}

// ensureTenant creates the tenant row on first sight (bootstrap behavior,
// SPEC §4). Idempotent and portable (works on SQLite and Postgres).
func ensureTenant(db *sql.DB, orgID string) error {
	_, err := db.Exec(
		`INSERT INTO tenants(org_id, tenant_name, ready) VALUES(?,?,1)
		 ON CONFLICT(org_id) DO NOTHING`, orgID, orgID)
	return err
}

var validOrderBy = map[string]bool{
	"application": true, "resource_type": true, "verb": true,
}

var validStatus = map[string]bool{
	"enabled": true, "disabled": true, "all": true, "": true,
}

// handleAccess serves GET /api/rbac/v1/access/ (SPEC §2, openapi subset).
func (s *Server) handleAccess(w http.ResponseWriter, r *http.Request) {
	user, err := identity.Parse(r.Header.Get("X-Rh-Identity"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := ensureTenant(s.db, user.OrgID); err != nil {
		writeError(w, http.StatusInternalServerError, "unexpected error")
		return
	}

	q := r.URL.Query()

	var apps []string
	if raw := strings.TrimSpace(q.Get("application")); raw != "" {
		for _, a := range strings.Split(raw, ",") {
			if a = strings.TrimSpace(a); a != "" {
				apps = append(apps, a)
			}
		}
	}

	// Admin username override takes precedence over the identity header.
	principal := user.Username
	if override := strings.ToLower(strings.TrimSpace(q.Get("username"))); override != "" && user.Admin {
		principal = override
	}

	orderBy := q.Get("order_by")
	field := strings.TrimPrefix(orderBy, "-")
	if !validOrderBy[field] && orderBy != "" {
		writeError(w, http.StatusBadRequest, "invalid order_by")
		return
	}
	if !validStatus[q.Get("status")] {
		writeError(w, http.StatusBadRequest, "invalid status")
		return
	}

	limit := -1 // absent means all, like the Django view's default_limit
	offset := 0
	if raw := q.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 1000 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
	}
	if raw := q.Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 {
			writeError(w, http.StatusBadRequest, "invalid offset")
			return
		}
	}

	entries, err := access.Query(s.db, user.OrgID, principal, apps)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unexpected error")
		return
	}
	if orderBy != "" {
		reverse := strings.HasPrefix(orderBy, "-")
		idx := map[string]int{"application": 0, "resource_type": 1, "verb": 2}[field]
		sort.SliceStable(entries, func(i, j int) bool {
			pi, pj := parts(entries[i].Permission), parts(entries[j].Permission)
			if reverse {
				return pi[idx] > pj[idx]
			}
			return pi[idx] < pj[idx]
		})
	}

	total := len(entries)
	effectiveLimit := total
	if limit >= 0 {
		effectiveLimit = limit
	}
	page := entries
	if offset < total {
		end := offset + effectiveLimit
		if end > total {
			end = total
		}
		page = entries[offset:end]
	} else {
		page = []access.Entry{}
	}

	links := map[string]*string{}
	link := func(off int) *string {
		v, _ := url.ParseQuery(r.URL.RawQuery)
		v.Set("offset", strconv.Itoa(off))
		v.Set("limit", strconv.Itoa(effectiveLimit))
		s := r.URL.Path + "?" + v.Encode()
		return &s
	}
	zero := 0
	links["first"] = link(zero)
	if offset+effectiveLimit < total {
		links["next"] = link(offset + effectiveLimit)
	}
	if offset > 0 {
		prev := offset - effectiveLimit
		if prev < 0 {
			prev = 0
		}
		links["previous"] = link(prev)
	}
	if total > 0 {
		links["last"] = link((total - 1) / effectiveLimit * effectiveLimit)
	} else {
		links["last"] = link(zero)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"meta":  map[string]any{"count": total},
		"links": links,
		"data":  page,
	})
}

func parts(permission string) [3]string {
	var out [3]string
	segs := strings.SplitN(permission, ":", 3)
	copy(out[:], segs)
	return out
}
