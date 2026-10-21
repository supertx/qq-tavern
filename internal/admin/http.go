// Package admin provides authenticated configuration for a single instance.
// Management credentials are independent from internal service credentials.
package admin

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/supertx/qq-tavern/internal/domain"
	"github.com/supertx/qq-tavern/internal/state"
)

const Prefix = "/api/admin/v1"

type api struct {
	store *state.Store
	bot   string
}
type endpoint func(http.ResponseWriter, *http.Request) error

func NewHandler(store *state.Store, bot, token string) http.Handler {
	mux := http.NewServeMux()
	a := &api{store: store, bot: bot}
	register := func(pattern string, handler endpoint) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if err := handler(w, r); err != nil {
				writeError(w, err)
			}
		})
	}
	register("GET "+Prefix+"/instance", a.instance)
	register("GET "+Prefix+"/command-policy", a.getPolicy)
	register("PUT "+Prefix+"/command-policy", a.putPolicy)
	register("GET "+Prefix+"/scopes", a.scopes)
	register("GET "+Prefix+"/scopes/{scope}/roles", a.roles)
	register("POST "+Prefix+"/scopes/{scope}/roles", a.createRole)
	register("PUT "+Prefix+"/scopes/{scope}/active-role", a.selectRole)
	register("GET "+Prefix+"/scopes/{scope}/roles/{role}/config", a.getConfig)
	register("PATCH "+Prefix+"/scopes/{scope}/roles/{role}/config", a.patchConfig)
	register("GET "+Prefix+"/scopes/{scope}/roles/{role}/status-schema", a.getSchema)
	register("PUT "+Prefix+"/scopes/{scope}/roles/{role}/status-schema", a.putSchema)
	register("POST "+Prefix+"/scopes/{scope}/imports/preview", a.preview)
	register("POST "+Prefix+"/scopes/{scope}/imports/{import}/commit", a.commit)
	register("DELETE "+Prefix+"/scopes/{scope}/imports/{import}", a.cancel)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 404, map[string]any{"error": map[string]string{"code": "not_found", "message": "endpoint not found"}})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if len(token) < 32 {
			writeJSON(w, 404, map[string]any{"error": map[string]string{"code": "disabled", "message": "management API is disabled"}})
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeJSON(w, 401, map[string]any{"error": map[string]string{"code": "unauthorized", "message": "valid instance token required"}})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	status, code, message := 400, "invalid_request", err.Error()
	var tooLarge *http.MaxBytesError
	var databaseError interface{ Code() int }
	switch {
	case errors.Is(err, state.ErrNotFound):
		status, code = 404, "not_found"
	case errors.Is(err, state.ErrConflict):
		status, code = 409, "version_conflict"
	case errors.Is(err, state.ErrExpiredImport):
		status, code = 410, "import_expired"
	case errors.Is(err, state.ErrForbidden):
		status, code = 403, "forbidden"
	case errors.As(err, &tooLarge):
		status, code, message = 413, "too_large", "request exceeds 8 MiB"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status, code, message = 503, "unavailable", "request timed out"
	case errors.As(err, &databaseError):
		status, code, message = 500, "internal_error", "database operation failed"
	}
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func body(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if ct := strings.Split(r.Header.Get("Content-Type"), ";")[0]; ct != "application/json" {
		return nil, errors.New("Content-Type must be application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, state.MaxImportBytes)
	return io.ReadAll(r.Body)
}
func decode(w http.ResponseWriter, r *http.Request, value any) error {
	data, err := body(w, r)
	if err != nil {
		return err
	}
	return state.DecodeJSON(data, value)
}
func expected(value *int64) (int64, error) {
	if value == nil || *value < 0 {
		return 0, errors.New("expected_version is required and must be nonnegative")
	}
	return *value, nil
}
func (a *api) scope(r *http.Request) (domain.Scope, error) {
	return a.store.ResolveScope(r.Context(), a.bot, r.PathValue("scope"))
}
func (a *api) role(r *http.Request) (*state.Store, error) {
	scope, err := a.scope(r)
	if err != nil {
		return nil, err
	}
	id, err := strconv.ParseInt(r.PathValue("role"), 10, 64)
	if err != nil || id < 1 {
		return nil, state.ErrNotFound
	}
	return a.store.BindRoleID(r.Context(), scope, id)
}
func (a *api) instance(w http.ResponseWriter, r *http.Request) error {
	id, err := a.store.InstanceID(r.Context())
	if err == nil {
		writeJSON(w, 200, map[string]any{"instance_id": id, "bot_id": a.bot, "schema_version": 4})
	}
	return err
}
func (a *api) getPolicy(w http.ResponseWriter, r *http.Request) error {
	p, err := a.store.Policy(r.Context())
	if err == nil {
		writeJSON(w, 200, map[string]any{"groups": p.Groups, "version": p.Version, "descriptions": state.CommandGroups()})
	}
	return err
}
func (a *api) putPolicy(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Expected *int64                      `json:"expected_version"`
		Groups   map[state.CommandGroup]bool `json:"groups"`
	}
	if err := decode(w, r, &in); err != nil {
		return err
	}
	v, err := expected(in.Expected)
	if err != nil {
		return err
	}
	p, err := a.store.SetPolicy(r.Context(), v, in.Groups)
	if err == nil {
		writeJSON(w, 200, p)
	}
	return err
}
func (a *api) scopes(w http.ResponseWriter, r *http.Request) error {
	scopes, err := a.store.Scopes(r.Context(), a.bot)
	if err == nil {
		writeJSON(w, 200, map[string]any{"scopes": scopes})
	}
	return err
}
func roleJSON(role state.Role) any {
	return map[string]any{"id": role.ID, "name": role.Name, "active": role.Active}
}
func (a *api) roles(w http.ResponseWriter, r *http.Request) error {
	scope, err := a.scope(r)
	if err != nil {
		return err
	}
	roles, err := a.store.Roles(r.Context(), scope)
	if err != nil {
		return err
	}
	items := []any{}
	for _, role := range roles {
		items = append(items, roleJSON(role))
	}
	writeJSON(w, 200, map[string]any{"roles": items})
	return nil
}
func (a *api) createRole(w http.ResponseWriter, r *http.Request) error {
	scope, err := a.scope(r)
	if err != nil {
		return err
	}
	var in struct {
		Name string `json:"name"`
	}
	if err = decode(w, r, &in); err != nil {
		return err
	}
	role, err := a.store.AdminCreateRole(r.Context(), scope, in.Name)
	if err == nil {
		writeJSON(w, 201, roleJSON(role))
	}
	return err
}
func (a *api) selectRole(w http.ResponseWriter, r *http.Request) error {
	scope, err := a.scope(r)
	if err != nil {
		return err
	}
	var in struct {
		RoleID   int64  `json:"role_id"`
		Expected *int64 `json:"expected_version"`
	}
	if err = decode(w, r, &in); err != nil {
		return err
	}
	v, err := expected(in.Expected)
	if err != nil {
		return err
	}
	if in.RoleID < 1 {
		return errors.New("role_id must be positive")
	}
	err = a.store.AdminSelectRole(r.Context(), scope, in.RoleID, v)
	if err == nil {
		writeJSON(w, 200, map[string]any{"active_role_id": in.RoleID, "version": v + 1})
	}
	return err
}
func (a *api) getConfig(w http.ResponseWriter, r *http.Request) error {
	view, err := a.role(r)
	if err != nil {
		return err
	}
	c, err := view.RoleConfig(r.Context())
	if err == nil {
		writeJSON(w, 200, c)
	}
	return err
}
func (a *api) patchConfig(w http.ResponseWriter, r *http.Request) error {
	view, err := a.role(r)
	if err != nil {
		return err
	}
	var in struct {
		Expected *int64 `json:"expected_version"`
		state.ConfigPatch
	}
	if err = decode(w, r, &in); err != nil {
		return err
	}
	v, err := expected(in.Expected)
	if err != nil {
		return err
	}
	if len(in.Fields()) == 0 {
		return errors.New("no settings provided")
	}
	c, err := view.AdminPatchConfig(r.Context(), v, in.ConfigPatch)
	if err == nil {
		writeJSON(w, 200, c)
	}
	return err
}
func (a *api) getSchema(w http.ResponseWriter, r *http.Request) error {
	view, err := a.role(r)
	if err != nil {
		return err
	}
	schema, err := view.RoleSchema(r.Context())
	if err == nil {
		writeJSON(w, 200, schema)
	}
	return err
}
func (a *api) putSchema(w http.ResponseWriter, r *http.Request) error {
	view, err := a.role(r)
	if err != nil {
		return err
	}
	var in struct {
		Expected    *int64                    `json:"expected_version"`
		Definitions *[]state.StatusDefinition `json:"definitions"`
	}
	if err = decode(w, r, &in); err != nil {
		return err
	}
	v, err := expected(in.Expected)
	if err != nil {
		return err
	}
	if in.Definitions == nil {
		return errors.New("definitions is required")
	}
	schema, err := view.AdminSetSchema(r.Context(), v, *in.Definitions)
	if err == nil {
		writeJSON(w, 200, schema)
	}
	return err
}
func (a *api) preview(w http.ResponseWriter, r *http.Request) error {
	scope, err := a.scope(r)
	if err != nil {
		return err
	}
	data, err := body(w, r)
	if err != nil {
		return err
	}
	preview, err := a.store.PreviewImport(r.Context(), scope, "admin-api", "admin", data)
	if err == nil {
		writeJSON(w, 201, preview)
	}
	return err
}
func (a *api) commit(w http.ResponseWriter, r *http.Request) error {
	scope, err := a.scope(r)
	if err != nil {
		return err
	}
	receipt, err := a.store.CommitImport(r.Context(), scope, "admin-api", "admin", r.PathValue("import"))
	if err == nil {
		writeJSON(w, 200, receipt)
	}
	return err
}
func (a *api) cancel(w http.ResponseWriter, r *http.Request) error {
	scope, err := a.scope(r)
	if err != nil {
		return err
	}
	err = a.store.CancelImport(r.Context(), scope, "admin-api", "admin", r.PathValue("import"))
	if err == nil {
		w.WriteHeader(204)
	}
	return err
}
