package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/supertx/qq-tavern/internal/domain"
	"github.com/supertx/qq-tavern/internal/state"
)

func testAPI(t *testing.T, token string) (*state.Store, http.Handler, domain.Scope, int64) {
	t.Helper()
	s, err := state.Open(filepath.Join(t.TempDir(), "instance.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	scope := domain.Scope{BotID: "bot", Kind: "group", ID: "g"}
	view, err := s.BindActive(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	return s, NewHandler(s, "bot", token), scope, view.Role().ID
}

func call(t *testing.T, h http.Handler, token, method, path, data string, want int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, Prefix+path, strings.NewReader(data))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: status %d, expected %d: %s", method, path, w.Code, want, w.Body.String())
	}
	return w
}

func TestIndependentInstancesAuthenticationPolicyAndData(t *testing.T) {
	aToken, bToken := strings.Repeat("a", 40), strings.Repeat("b", 40)
	a, ah, scope, roleID := testAPI(t, aToken)
	b, bh, _, _ := testAPI(t, bToken)
	call(t, bh, aToken, "GET", "/instance", "", 401)
	call(t, ah, "", "GET", "/instance", "", 401)
	call(t, NewHandler(a, "bot", ""), aToken, "GET", "/instance", "", 404)
	ia := call(t, ah, aToken, "GET", "/instance", "", 200).Body.String()
	ib := call(t, bh, bToken, "GET", "/instance", "", 200).Body.String()
	if ia == ib || strings.Contains(ia, aToken) {
		t.Fatal("instances share identity or disclose credential")
	}
	call(t, ah, aToken, "PUT", "/command-policy", `{"expected_version":0,"groups":{"world_edit":true}}`, 200)
	call(t, ah, aToken, "PUT", "/command-policy", `{"expected_version":0,"groups":{"role_edit":true}}`, 409)
	call(t, ah, aToken, "PUT", "/command-policy", `{"groups":{"role_edit":true}}`, 400)
	call(t, ah, aToken, "PUT", "/command-policy", `{"expected_version":1,"groups":{"made_up":true}}`, 400)
	p, err := b.Policy(context.Background())
	if err != nil || p.Version != 0 || p.Groups[state.WorldEdit] {
		t.Fatal("policy leaked across instances", p, err)
	}
	base := "/scopes/" + scope.Key()
	configPath := fmt.Sprintf("%s/roles/%d/config", base, roleID)
	call(t, ah, aToken, "PATCH", configPath, `{"expected_version":0,"character":{"name":"A","description":"API 管理角色"},"note":"测试规则"}`, 200)
	c := call(t, ah, aToken, "GET", configPath, "", 200).Body.String()
	if !strings.Contains(c, "API 管理角色") {
		t.Fatal(c)
	}
	call(t, ah, aToken, "PATCH", configPath, `{"expected_version":0,"note":"不应覆盖"}`, 409)
	call(t, ah, aToken, "PATCH", configPath, `{"expected_version":1,"note":null}`, 400)
	call(t, ah, aToken, "PATCH", configPath, `{"expected_version":1,"note":"a","note":"b"}`, 400)
	bc := call(t, bh, bToken, "GET", configPath, "", 200).Body.String()
	if strings.Contains(bc, "API 管理角色") {
		t.Fatal("config leaked across instances")
	}
	var n int
	if err = a.DB().QueryRow(`SELECT count(*) FROM tavern_admin_audit WHERE actor='admin-api'`).Scan(&n); err != nil || n != 2 {
		t.Fatal("missing policy/config audit", n, err)
	}
}

func TestScopeRoleAndPrivateAdministration(t *testing.T) {
	token := strings.Repeat("t", 40)
	s, h, scope, roleID := testAPI(t, token)
	ctx := context.Background()
	foreign := domain.Scope{BotID: "another-bot", Kind: "group", ID: "g"}
	_, err := s.BindActive(ctx, foreign)
	if err != nil {
		t.Fatal(err)
	}
	call(t, h, token, "GET", "/scopes/"+foreign.Key()+"/roles", "", 404)
	scopes := call(t, h, token, "GET", "/scopes", "", 200).Body.String()
	if strings.Contains(scopes, foreign.Key()) {
		t.Fatal("foreign bot disclosed")
	}
	base := "/scopes/" + scope.Key()
	w := call(t, h, token, "POST", base+"/roles", `{"name":"B"}`, 201)
	var role struct {
		ID int64 `json:"id"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &role); err != nil || role.ID == 0 {
		t.Fatal(err)
	}
	call(t, h, token, "POST", base+"/roles", `{"name":"B"}`, 409)
	call(t, h, token, "PUT", base+"/active-role", fmt.Sprintf(`{"role_id":%d,"expected_version":0}`, role.ID), 200)
	call(t, h, token, "PUT", base+"/active-role", fmt.Sprintf(`{"role_id":%d,"expected_version":0}`, roleID), 409)
	// Switching back must not make an old selection version valid again.
	call(t, h, token, "PUT", base+"/active-role", fmt.Sprintf(`{"role_id":%d,"expected_version":1}`, roleID), 200)
	call(t, h, token, "PUT", base+"/active-role", fmt.Sprintf(`{"role_id":%d,"expected_version":0}`, role.ID), 409)
	priv := domain.Scope{BotID: "bot", Kind: "private", ID: "member"}
	view, err := s.BindActive(ctx, priv)
	if err != nil {
		t.Fatal(err)
	}
	privateBase := fmt.Sprintf("/scopes/%s/roles/%d", priv.Key(), view.Role().ID)
	call(t, h, token, "PATCH", privateBase+"/config", `{"expected_version":0,"character":{"name":"私聊角色"}}`, 200)
	call(t, h, token, "GET", fmt.Sprintf("%s/roles/%d/config", base, view.Role().ID), "", 404)
	var actor string
	if err = s.DB().QueryRow(`SELECT actor FROM tavern_revision WHERE scope=? ORDER BY id DESC LIMIT 1`, view.Role().Key).Scan(&actor); err != nil || actor != "admin-api" {
		t.Fatal("admin impersonated private user", actor, err)
	}
	schema := `{"expected_version":0,"definitions":[{"name":"信任","kind":"number","owner":"member","initial":10,"min":0,"max":100,"max_delta":2,"rule":"信守承诺增加","enabled":true}]}`
	call(t, h, token, "PUT", privateBase+"/status-schema", schema, 200)
	call(t, h, token, "PUT", privateBase+"/status-schema", schema, 409)
	call(t, h, token, "GET", privateBase+"/status-schema", "", 200)
}

func TestAPIImportPreviewCommitAndLimits(t *testing.T) {
	token := strings.Repeat("t", 40)
	_, h, scope, _ := testAPI(t, token)
	base := "/scopes/" + scope.Key() + "/imports"
	data := `{"format":"qq-tavern.bundle","version":1,"roles":[{"name":"导入角色","worldbook":{"entries":[{"title":"世界","content":"测试世界","constant":true}]}}]}`
	w := call(t, h, token, "POST", base+"/preview", data, 201)
	var p state.ImportPreview
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	first := call(t, h, token, "POST", base+"/"+p.ID+"/commit", "", 200).Body.String()
	again := call(t, h, token, "POST", base+"/"+p.ID+"/commit", "", 200).Body.String()
	if first != again {
		t.Fatal("duplicate commit changed result")
	}
	call(t, h, token, "POST", base+"/preview", strings.Repeat("x", state.MaxImportBytes+1), 413)
	call(t, h, token, "POST", base+"/preview", `{"format":"wrong","version":1,"roles":[{"name":"A"}]}`, 400)
}
