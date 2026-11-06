package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"rbac-go/internal/access"
	"rbac-go/internal/identity"
	"rbac-go/internal/permission"
	"rbac-go/internal/principal"
	"rbac-go/internal/role"
	"rbac-go/internal/store"
)

// Server wires the v1 endpoints.
type Server struct {
	db       *store.DB
	mux      *http.ServeMux
	requests *prometheus.CounterVec
}

// New returns the HTTP handler with the v1 routes registered. Metrics use a
// per-Server registry so test servers stay isolated.
func New(db *store.DB) *Server {
	s := &Server{db: db, mux: http.NewServeMux()}
	s.requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "rbac_api_requests_total",
		Help: "Total v1 API requests by endpoint (url_name analogue).",
	}, []string{"endpoint", "method", "code"})
	reg := prometheus.NewRegistry()
	reg.MustRegister(s.requests)
	s.mux.HandleFunc("GET /api/rbac/v1/status/", s.handleStatus)
	s.mux.HandleFunc("GET /api/rbac/v1/ready/", s.handleReady)
	s.mux.HandleFunc("GET /api/rbac/v1/access/", s.handleAccess)
	s.mux.HandleFunc("GET /api/rbac/v1/principals/", s.handlePrincipals)
	s.mux.HandleFunc("GET /api/rbac/v1/permissions/", s.handlePermissions)
	s.mux.HandleFunc("GET /api/rbac/v1/permissions/options/", s.handlePermissionOptions)
	s.mux.HandleFunc("POST /api/rbac/v1/roles/", s.handleRoleCreate)
	s.mux.HandleFunc("GET /api/rbac/v1/roles/", s.handleRolesList)
	s.mux.HandleFunc("PUT /api/rbac/v1/roles/{uuid}/", s.handleRoleUpdate)
	s.mux.HandleFunc("PATCH /api/rbac/v1/roles/{uuid}/", s.handleRolePatch)
	s.mux.HandleFunc("DELETE /api/rbac/v1/roles/{uuid}/", s.handleRoleDelete)
	s.mux.HandleFunc("GET /api/rbac/v1/roles/{uuid}/", s.handleRoleDetail)
	s.mux.Handle("GET /metrics/", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec := &codeRecorder{ResponseWriter: w, code: http.StatusOK}
	s.mux.ServeHTTP(rec, r)
	s.requests.WithLabelValues(endpointName(r.URL.Path), r.Method, strconv.Itoa(rec.code)).Inc()
}

type codeRecorder struct {
	http.ResponseWriter
	code int
}

func (c *codeRecorder) WriteHeader(code int) {
	c.code = code
	c.ResponseWriter.WriteHeader(code)
}

// endpointName maps a request path to the metric endpoint label (the Django
// url_name analogue used by dashboards/).
func endpointName(path string) string {
	switch {
	case strings.HasPrefix(path, "/api/rbac/v1/access/"):
		return "access"
	case strings.HasPrefix(path, "/api/rbac/v1/status/"):
		return "status"
	case strings.HasPrefix(path, "/api/rbac/v1/ready/"):
		return "ready"
	case strings.HasPrefix(path, "/metrics/"):
		return "metrics"
	default:
		return "other"
	}
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

// handlePrincipals serves GET /api/rbac/v1/principals/ (Users page).
func (s *Server) handlePrincipals(w http.ResponseWriter, r *http.Request) {
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
	limit, offset := 10, 0
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
	items, total, err := principal.List(s.db, user.OrgID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unexpected error")
		return
	}
	link := func(off int, present bool) *string {
		if !present {
			return nil
		}
		v, _ := url.ParseQuery(r.URL.RawQuery)
		v.Set("limit", strconv.Itoa(limit))
		v.Set("offset", strconv.Itoa(off))
		s := r.URL.Path + "?" + v.Encode()
		return &s
	}
	lastOff := 0
	if total > 0 {
		lastOff = (total - 1) / limit * limit
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"meta": map[string]any{"count": total, "limit": limit, "offset": offset},
		"links": map[string]any{
			"first":    link(0, true),
			"next":     link(offset+limit, offset+limit < total),
			"previous": link(ifGE0(offset-limit), offset-limit >= 0),
			"last":     link(lastOff, true),
		},
		"data": items,
	})
}

func ifGE0(v int) int {
	if v < 0 {
		return 0
	}
	return v
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
func ensureTenant(db *store.DB, orgID string) error {
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

// permissionFilter builds the query filter shared by list + options.
func permissionFilter(r *http.Request) (permission.Filter, bool) {
	q := r.URL.Query()
	split := func(key string) []string {
		raw := strings.TrimSpace(q.Get(key))
		if raw == "" {
			return nil
		}
		return strings.Split(raw, ",")
	}
	allowedOnly := q.Get("allowed_only") == "true"
	var allowList []string
	if allowedOnly {
		allowList = strings.Split(os.Getenv("ROLE_CREATE_ALLOW_LIST"), ",")
	}
	return permission.Filter{
		Applications:   split("application"),
		ResourceTypes:  split("resource_type"),
		Verbs:          split("verb"),
		ExcludeGlobals: q.Get("exclude_globals") == "true",
		AllowedOnly:    allowedOnly,
		AllowList:      allowList,
	}, true
}

// handlePermissions serves GET /api/rbac/v1/permissions/ (role wizard).
func (s *Server) handlePermissions(w http.ResponseWriter, r *http.Request) {
	user, err := identity.Parse(r.Header.Get("X-Rh-Identity"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := ensureTenant(s.db, user.OrgID); err != nil {
		writeError(w, http.StatusInternalServerError, "unexpected error")
		return
	}
	filter, _ := permissionFilter(r)
	limit, offset := 10, 0
	q := r.URL.Query()
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
	items, total, err := permission.List(s.db, user.OrgID, filter, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unexpected error")
		return
	}
	link := func(off int, present bool) *string {
		if !present {
			return nil
		}
		v, _ := url.ParseQuery(r.URL.RawQuery)
		v.Set("limit", strconv.Itoa(limit))
		v.Set("offset", strconv.Itoa(off))
		s := r.URL.Path + "?" + v.Encode()
		return &s
	}
	lastOff := 0
	if total > 0 {
		lastOff = (total - 1) / limit * limit
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"meta": map[string]any{"count": total, "limit": limit, "offset": offset},
		"links": map[string]any{
			"first":    link(0, true),
			"next":     link(offset+limit, offset+limit < total),
			"previous": link(ifGE0(offset-limit), offset-limit >= 0),
			"last":     link(lastOff, true),
		},
		"data": items,
	})
}

// handlePermissionOptions serves GET /api/rbac/v1/permissions/options/.
func (s *Server) handlePermissionOptions(w http.ResponseWriter, r *http.Request) {
	user, err := identity.Parse(r.Header.Get("X-Rh-Identity"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := ensureTenant(s.db, user.OrgID); err != nil {
		writeError(w, http.StatusInternalServerError, "unexpected error")
		return
	}
	field := r.URL.Query().Get("field")
	if field == "" {
		field = "application"
	}
	filter, _ := permissionFilter(r)
	values, err := permission.Options(s.db, user.OrgID, field, filter)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid field")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": values})
}

// handleRoleCreate serves POST /api/rbac/v1/roles/ (wizard Step 4).
func (s *Server) handleRoleCreate(w http.ResponseWriter, r *http.Request) {
	user, err := identity.Parse(r.Header.Get("X-Rh-Identity"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := ensureTenant(s.db, user.OrgID); err != nil {
		writeError(w, http.StatusInternalServerError, "unexpected error")
		return
	}
	var body struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		Description string `json:"description"`
		Access      []struct {
			Permission          string                      `json:"permission"`
			ResourceDefinitions []access.ResourceDefinition `json:"resourceDefinitions"`
		} `json:"access"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	grants := make([]role.AccessIn, 0, len(body.Access))
	for _, a := range body.Access {
		grants = append(grants, role.AccessIn{Permission: a.Permission, ResourceDefinitions: a.ResourceDefinitions})
	}
	created, err := role.Create(s.db, user.OrgID, body.Name, body.DisplayName, body.Description,
		grants, strings.Split(os.Getenv("ROLE_CREATE_ALLOW_LIST"), ","))
	if err != nil {
		var notAllowed *role.ErrAppNotAllowed
		var unknown *role.ErrUnknownPermission
		if errors.As(err, &notAllowed) || errors.As(err, &unknown) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "required") {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "unexpected error")
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// linkURL builds a same-path pagination link preserving the query string.
func linkURL(r *http.Request, off, limit int) *string {
	v, _ := url.ParseQuery(r.URL.RawQuery)
	v.Set("offset", strconv.Itoa(off))
	v.Set("limit", strconv.Itoa(limit))
	s := r.URL.Path + "?" + v.Encode()
	return &s
}

func linkURLOrNil(r *http.Request, off, limit int, present bool) *string {
	if !present {
		return nil
	}
	return linkURL(r, off, limit)
}

func lastOffset(total, limit int) int {
	if total == 0 {
		return 0
	}
	return (total - 1) / limit * limit
}

// handleRolesList serves GET /api/rbac/v1/roles/.
func (s *Server) handleRolesList(w http.ResponseWriter, r *http.Request) {
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
	limit, offset := 10, 0
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
	items, total, err := role.List(s.db, user.OrgID, q.Get("name"), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unexpected error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"meta": map[string]any{"count": total, "limit": limit, "offset": offset},
		"links": map[string]any{
			"first": linkURL(r, 0, limit),
			"next":  linkURLOrNil(r, offset+limit, limit, offset+limit < total),
			"last":  linkURL(r, lastOffset(total, limit), limit),
		},
		"data": items,
	})
}

// roleWriteError maps domain errors to v1 responses.
func roleWriteError(w http.ResponseWriter, err error) {
	var notAllowed *role.ErrAppNotAllowed
	var unknown *role.ErrUnknownPermission
	var sys role.ErrSystemProtected
	switch {
	case errors.As(err, &notAllowed), errors.As(err, &unknown), errors.As(err, &sys):
		writeError(w, http.StatusBadRequest, err.Error())
	case err == sql.ErrNoRows:
		writeError(w, http.StatusNotFound, "not found")
	default:
		msg := err.Error()
		if strings.Contains(msg, "already exists") || strings.Contains(msg, "required") ||
			strings.Contains(msg, "not supported") || strings.Contains(msg, "UNIQUE") ||
			strings.Contains(msg, "unique") {
			writeError(w, http.StatusBadRequest, msg)
		} else {
			writeError(w, http.StatusInternalServerError, "unexpected error")
		}
	}
}

// handleRoleUpdate serves PUT /api/rbac/v1/roles/{uuid}/ (full replace).
func (s *Server) handleRoleUpdate(w http.ResponseWriter, r *http.Request) {
	user, err := identity.Parse(r.Header.Get("X-Rh-Identity"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var body struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		Description string `json:"description"`
		Access      []struct {
			Permission          string                      `json:"permission"`
			ResourceDefinitions []access.ResourceDefinition `json:"resourceDefinitions"`
		} `json:"access"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	grants := make([]role.AccessIn, 0, len(body.Access))
	for _, a := range body.Access {
		grants = append(grants, role.AccessIn{Permission: a.Permission, ResourceDefinitions: a.ResourceDefinitions})
	}
	updated, err := role.Update(s.db, user.OrgID, r.PathValue("uuid"), body.Name,
		body.DisplayName, body.Description, grants, strings.Split(os.Getenv("ROLE_CREATE_ALLOW_LIST"), ","))
	if err != nil {
		roleWriteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// handleRolePatch serves PATCH /api/rbac/v1/roles/{uuid}/.
func (s *Server) handleRolePatch(w http.ResponseWriter, r *http.Request) {
	user, err := identity.Parse(r.Header.Get("X-Rh-Identity"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var raw map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	fields := map[string]string{}
	for k, v := range raw {
		s, ok := v.(string)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		fields[k] = s
	}
	patched, err := role.Patch(s.db, user.OrgID, r.PathValue("uuid"), fields)
	if err != nil {
		roleWriteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, patched)
}

// handleRoleDelete serves DELETE /api/rbac/v1/roles/{uuid}/.
func (s *Server) handleRoleDelete(w http.ResponseWriter, r *http.Request) {
	user, err := identity.Parse(r.Header.Get("X-Rh-Identity"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := role.Delete(s.db, user.OrgID, r.PathValue("uuid")); err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		var sys role.ErrSystemProtected
		if errors.As(err, &sys) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "unexpected error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRoleDetail serves GET /api/rbac/v1/roles/{uuid}/.
func (s *Server) handleRoleDetail(w http.ResponseWriter, r *http.Request) {
	user, err := identity.Parse(r.Header.Get("X-Rh-Identity"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	role, err := role.Get(s.db, user.OrgID, r.PathValue("uuid"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, role)
}

func parts(permission string) [3]string {
	var out [3]string
	segs := strings.SplitN(permission, ":", 3)
	copy(out[:], segs)
	return out
}
