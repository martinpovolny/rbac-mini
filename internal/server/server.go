package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"rbac-go/internal/access"
	"rbac-go/internal/group"
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
	s.mux.HandleFunc("GET /api/rbac/v1/roles/{uuid}/access/", s.handleRoleAccess)
	s.mux.HandleFunc("GET /api/rbac/v1/groups/", s.handleGroupsList)
	s.mux.HandleFunc("POST /api/rbac/v1/groups/", s.handleGroupCreate)
	s.mux.HandleFunc("GET /api/rbac/v1/groups/{uuid}/", s.handleGroupDetail)
	s.mux.HandleFunc("PUT /api/rbac/v1/groups/{uuid}/", s.handleGroupUpdate)
	s.mux.HandleFunc("DELETE /api/rbac/v1/groups/{uuid}/", s.handleGroupDelete)
	s.mux.HandleFunc("GET /api/rbac/v1/groups/{uuid}/principals/", s.handleGroupPrincipals)
	s.mux.HandleFunc("GET /api/rbac/v1/groups/{uuid}/roles/", s.handleGroupRoles)
	s.mux.HandleFunc("POST /api/rbac/v1/groups/{uuid}/roles/", s.handleGroupRolesAssign)
	s.mux.HandleFunc("DELETE /api/rbac/v1/groups/{uuid}/roles/", s.handleGroupRolesRemove)
	s.mux.HandleFunc("POST /api/rbac/v1/groups/{uuid}/principals/", s.handleGroupPrincipalsAdd)
	s.mux.HandleFunc("DELETE /api/rbac/v1/groups/{uuid}/principals/", s.handleGroupPrincipalsRemove)
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
	filter, err := principalFilter(q, limit, offset)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	items, total, err := principal.List(s.db, user.OrgID, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unexpected error")
		return
	}
	if filter.UsernameOnly {
		only := make([]map[string]string, 0, len(items))
		for _, item := range items {
			only = append(only, map[string]string{"username": item.Username})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"meta":  map[string]any{"count": total, "limit": limit, "offset": offset},
			"links": principalsLinks(r, limit, offset, total),
			"data":  only,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"meta":  map[string]any{"count": total, "limit": limit, "offset": offset},
		"links": principalsLinks(r, limit, offset, total),
		"data":  items,
	})
}

// principalFilter validates the GET /principals/ query parameters the RBAC
// UI sends. Defaults mirror the Django view: type=user, status=enabled,
// match_criteria=exact, sort_order=asc.
func principalFilter(q url.Values, limit, offset int) (principal.Filter, error) {
	f := principal.Filter{Limit: limit, Offset: offset}
	var err error
	if f.Type, err = queryEnum(q, "type", []string{"user", "service-account", "all"}, "user"); err != nil {
		return f, err
	}
	if f.MatchCriteria, err = queryEnum(q, "match_criteria", []string{"partial", "exact"}, "exact"); err != nil {
		return f, err
	}
	if f.Status, err = queryEnum(q, "status", []string{"enabled", "disabled", "all"}, "enabled"); err != nil {
		return f, err
	}
	sortOrder, err := queryEnum(q, "sort_order", []string{"asc", "desc"}, "asc")
	if err != nil {
		return f, err
	}
	f.SortDesc = sortOrder == "desc"
	adminOnly, err := queryBool(q, "admin_only")
	if err != nil {
		return f, err
	}
	if f.UsernameOnly, err = queryBool(q, "username_only"); err != nil {
		return f, err
	}
	for _, name := range strings.Split(strings.ReplaceAll(q.Get("usernames"), " ", ""), ",") {
		if name != "" {
			f.Usernames = append(f.Usernames, name)
		}
	}
	f.Email = strings.TrimSpace(q.Get("email"))
	// Django only forwards admin_only on unfiltered requests; the UI never
	// combines it with a username or email filter.
	if len(f.Usernames) == 0 && f.Email == "" {
		f.AdminOnly = adminOnly
	}
	return f, nil
}

func queryEnum(q url.Values, key string, allowed []string, def string) (string, error) {
	value := q.Get(key)
	if value == "" {
		return def, nil
	}
	for _, candidate := range allowed {
		if value == candidate {
			return value, nil
		}
	}
	return "", fmt.Errorf("invalid %s", key)
}

func queryBool(q url.Values, key string) (bool, error) {
	v, err := queryBoolPtr(q, key)
	if err != nil || v == nil {
		return false, err
	}
	return *v, nil
}

// queryBoolPtr is queryBool for filters that must distinguish absent from
// false (Django only applies a filter when its parameter is present).
func queryBoolPtr(q url.Values, key string) (*bool, error) {
	switch q.Get(key) {
	case "":
		return nil, nil
	case "true":
		v := true
		return &v, nil
	case "false":
		v := false
		return &v, nil
	default:
		return nil, fmt.Errorf("invalid %s", key)
	}
}

func principalsLinks(r *http.Request, limit, offset, total int) map[string]any {
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
	return map[string]any{
		"first":    link(0, true),
		"next":     link(offset+limit, offset+limit < total),
		"previous": link(ifGE0(offset-limit), offset-limit >= 0),
		"last":     link(lastOff, true),
	}
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

	entries, err := access.Query(s.db, user.OrgID, principal, apps, user.Admin)
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
	var page []access.Entry
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
	filter := role.Filter{Name: q.Get("name"), DisplayName: q.Get("display_name"), OrderBy: "name"}
	if filter.Name != "" || filter.DisplayName != "" {
		if filter.NameMatch, err = queryEnum(q, "name_match",
			[]string{"partial", "exact"}, "partial"); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if filter.System, err = queryBoolPtr(q, "system"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	orderBy := q.Get("order_by")
	if strings.HasPrefix(orderBy, "-") {
		filter.Desc = true
		orderBy = strings.TrimPrefix(orderBy, "-")
	}
	switch orderBy {
	case "", "name":
	case "display_name", "policyCount", "modified":
		filter.OrderBy = orderBy
	default:
		writeError(w, http.StatusBadRequest, "invalid order_by")
		return
	}
	items, total, err := role.List(s.db, user.OrgID, filter, limit, offset)
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

// handleGroupRolesAssign serves POST /api/rbac/v1/groups/{uuid}/roles/.
func (s *Server) handleGroupRolesAssign(w http.ResponseWriter, r *http.Request) {
	user, err := identity.Parse(r.Header.Get("X-Rh-Identity"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var body struct {
		Roles []string `json:"roles"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := group.AssignRoles(s.db, user.OrgID, r.PathValue("uuid"), body.Roles); err != nil {
		groupRolesWriteError(w, err)
		return
	}
	items, _, err := group.Roles(s.db, user.OrgID, r.PathValue("uuid"), 1000, 0)
	if err != nil {
		groupRolesWriteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items})
}

// handleGroupRolesRemove serves DELETE /api/rbac/v1/groups/{uuid}/roles/?roles=.
func (s *Server) handleGroupRolesRemove(w http.ResponseWriter, r *http.Request) {
	user, err := identity.Parse(r.Header.Get("X-Rh-Identity"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var uuids []string
	if raw := strings.TrimSpace(r.URL.Query().Get("roles")); raw != "" {
		uuids = strings.Split(raw, ",")
	}
	if err := group.RemoveRoles(s.db, user.OrgID, r.PathValue("uuid"), uuids); err != nil {
		groupRolesWriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func groupRolesWriteError(w http.ResponseWriter, err error) {
	var unknown group.ErrUnknownRole
	if errors.As(err, &unknown) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.Contains(err.Error(), "cannot be modified") {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "unexpected error")
}

// handleGroupRoles serves GET /api/rbac/v1/groups/{uuid}/roles/.
func (s *Server) handleGroupRoles(w http.ResponseWriter, r *http.Request) {
	user, err := identity.Parse(r.Header.Get("X-Rh-Identity"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
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
	items, total, err := group.Roles(s.db, user.OrgID, r.PathValue("uuid"), limit, offset)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
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

// groupMembersBody parses {usernames: [...]} member payloads.
func groupMembersBody(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	var body struct {
		Usernames []string `json:"usernames"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return nil, false
	}
	return body.Usernames, true
}

func groupMembersError(w http.ResponseWriter, err error) {
	var unknown group.ErrUnknownPrincipals
	var protected group.ErrProtected
	if errors.As(err, &unknown) || errors.As(err, &protected) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "unexpected error")
}

// handleGroupPrincipalsAdd serves POST /api/rbac/v1/groups/{uuid}/principals/.
func (s *Server) handleGroupPrincipalsAdd(w http.ResponseWriter, r *http.Request) {
	user, err := identity.Parse(r.Header.Get("X-Rh-Identity"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	usernames, ok := groupMembersBody(w, r)
	if !ok {
		return
	}
	if err := group.AddMembers(s.db, user.OrgID, r.PathValue("uuid"), usernames); err != nil {
		groupMembersError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"usernames": usernames})
}

// handleGroupPrincipalsRemove serves DELETE /api/rbac/v1/groups/{uuid}/principals/.
func (s *Server) handleGroupPrincipalsRemove(w http.ResponseWriter, r *http.Request) {
	user, err := identity.Parse(r.Header.Get("X-Rh-Identity"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	usernames, ok := groupMembersBody(w, r)
	if !ok {
		return
	}
	if err := group.RemoveMembers(s.db, user.OrgID, r.PathValue("uuid"), usernames); err != nil {
		groupMembersError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"usernames": usernames})
}

// handleGroupPrincipals serves GET /api/rbac/v1/groups/{uuid}/principals/.
func (s *Server) handleGroupPrincipals(w http.ResponseWriter, r *http.Request) {
	user, err := identity.Parse(r.Header.Get("X-Rh-Identity"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
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
	filter := principal.Filter{Limit: limit, Offset: offset, Status: "enabled"}
	switch order := q.Get("order_by"); order {
	case "":
	case "username":
	case "-username":
		filter.SortDesc = true
	default:
		writeError(w, http.StatusBadRequest, "invalid order_by")
		return
	}
	if filter.UsernameOnly, err = queryBool(q, "username_only"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if filter.AdminOnly, err = queryBool(q, "admin_only"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if filter.Type, err = queryEnum(q, "principal_type",
		[]string{"user", "service-account", "all"}, "user"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	items, total, err := group.Members(s.db, user.OrgID, r.PathValue("uuid"), filter)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	data := any(items)
	if filter.UsernameOnly {
		only := make([]map[string]string, 0, len(items))
		for _, item := range items {
			only = append(only, map[string]string{"username": item.Username})
		}
		data = only
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"meta": map[string]any{"count": total, "limit": limit, "offset": offset},
		"links": map[string]any{
			"first": linkURL(r, 0, limit),
			"next":  linkURLOrNil(r, offset+limit, limit, offset+limit < total),
			"last":  linkURL(r, lastOffset(total, limit), limit),
		},
		"data": data,
	})
}

// groupWriteError maps group domain errors to v1 responses.
func groupWriteError(w http.ResponseWriter, err error) {
	var dup group.ErrDuplicate
	var reserved group.ErrReservedName
	var protected group.ErrProtected
	switch {
	case errors.As(err, &dup), errors.As(err, &reserved), errors.As(err, &protected):
		writeError(w, http.StatusBadRequest, err.Error())
	case err == sql.ErrNoRows:
		writeError(w, http.StatusNotFound, "not found")
	default:
		msg := err.Error()
		if strings.Contains(msg, "required") || strings.Contains(msg, "too long") ||
			strings.Contains(msg, "cannot be modified") {
			writeError(w, http.StatusBadRequest, msg)
		} else {
			writeError(w, http.StatusInternalServerError, "unexpected error")
		}
	}
}

// handleGroupUpdate serves PUT /api/rbac/v1/groups/{uuid}/.
func (s *Server) handleGroupUpdate(w http.ResponseWriter, r *http.Request) {
	user, err := identity.Parse(r.Header.Get("X-Rh-Identity"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var body struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Name == nil && body.Description == nil {
		writeError(w, http.StatusBadRequest, "nothing to update")
		return
	}
	name, desc := "", ""
	setDesc := body.Description != nil
	if body.Name != nil {
		name = *body.Name
	} else {
		// Name unchanged: read current name so the guard checks run on it.
		current, err := group.Get(s.db, user.OrgID, r.PathValue("uuid"))
		if err != nil {
			groupWriteError(w, err)
			return
		}
		name = current.Name
	}
	if setDesc {
		desc = *body.Description
	}
	updated, err := group.Update(s.db, user.OrgID, r.PathValue("uuid"), name, desc, setDesc)
	if err != nil {
		groupWriteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// handleGroupDelete serves DELETE /api/rbac/v1/groups/{uuid}/.
func (s *Server) handleGroupDelete(w http.ResponseWriter, r *http.Request) {
	user, err := identity.Parse(r.Header.Get("X-Rh-Identity"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := group.Delete(s.db, user.OrgID, r.PathValue("uuid")); err != nil {
		var protected group.ErrProtected
		if errors.As(err, &protected) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "unexpected error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGroupDetail serves GET /api/rbac/v1/groups/{uuid}/.
func (s *Server) handleGroupDetail(w http.ResponseWriter, r *http.Request) {
	user, err := identity.Parse(r.Header.Get("X-Rh-Identity"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	g, err := group.Get(s.db, user.OrgID, r.PathValue("uuid"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// handleGroupCreate serves POST /api/rbac/v1/groups/.
func (s *Server) handleGroupCreate(w http.ResponseWriter, r *http.Request) {
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
		Description string `json:"description"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	created, err := group.Create(s.db, user.OrgID, body.Name, body.Description)
	if err != nil {
		var dup group.ErrDuplicate
		var reserved group.ErrReservedName
		if errors.As(err, &dup) || errors.As(err, &reserved) ||
			strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "too long") {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "unexpected error")
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// handleGroupsList serves GET /api/rbac/v1/groups/.
func (s *Server) handleGroupsList(w http.ResponseWriter, r *http.Request) {
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
	filter := group.Filter{Name: q.Get("name"), NameMatch: "partial"}
	if filter.Name != "" {
		if filter.NameMatch, err = queryEnum(q, "name_match",
			[]string{"partial", "exact"}, "partial"); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if filter.PlatformDefault, err = queryBoolPtr(q, "platform_default"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if filter.AdminDefault, err = queryBoolPtr(q, "admin_default"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if filter.System, err = queryBoolPtr(q, "system"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	orderBy := q.Get("order_by")
	if strings.HasPrefix(orderBy, "-") {
		filter.Desc = true
		orderBy = strings.TrimPrefix(orderBy, "-")
	}
	switch orderBy {
	case "", "name":
	case "modified", "principalCount", "policyCount":
		filter.OrderBy = orderBy
	default:
		writeError(w, http.StatusBadRequest, "invalid order_by")
		return
	}
	items, total, err := group.List(s.db, user.OrgID, filter, limit, offset)
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

// handleRoleAccess serves GET /api/rbac/v1/roles/{uuid}/access/.
func (s *Server) handleRoleAccess(w http.ResponseWriter, r *http.Request) {
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
	total := len(role.Access)
	var page []access.Entry
	if offset < total {
		end := offset + limit
		if end > total {
			end = total
		}
		page = role.Access[offset:end]
	} else {
		page = []access.Entry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"meta": map[string]any{"count": total, "limit": limit, "offset": offset},
		"links": map[string]any{
			"first": linkURL(r, 0, limit),
			"next":  linkURLOrNil(r, offset+limit, limit, offset+limit < total),
			"last":  linkURL(r, lastOffset(total, limit), limit),
		},
		"data": page,
	})
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
