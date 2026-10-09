package server

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/SakuraOpenSource/virtualis/internal/auth"
	"github.com/SakuraOpenSource/virtualis/internal/config"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"github.com/SakuraOpenSource/virtualis/internal/runtime"
	"github.com/SakuraOpenSource/virtualis/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type apiFixture struct {
	db                  *gorm.DB
	eng                 *gin.Engine
	admin, owner, other model.User
	inst                model.Instance
	key, readonly       string
	rt                  *runtime.Runtime
}

func newAPIFixture(t *testing.T) apiFixture {
	t.Helper()
	dir := t.TempDir()
	db, err := gorm.Open(sqlite.Open(filepath.Join(dir, "api.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw, _ := db.DB(); _ = raw.Close() })
	admin := model.User{Username: "admin", Email: "a@test.local", Role: "admin", Status: "active"}
	owner := model.User{Username: "owner", Email: "o@test.local", Role: "user", Status: "active"}
	other := model.User{Username: "other", Email: "x@test.local", Role: "user", Status: "active"}
	for _, u := range []*model.User{&admin, &owner, &other} {
		if err = db.Create(u).Error; err != nil {
			t.Fatal(err)
		}
	}
	inst := model.Instance{Name: "api-guest", OwnerID: &owner.ID, Driver: "qemu", Status: "stopped"}
	if err = db.Create(&inst).Error; err != nil {
		t.Fatal(err)
	}
	key, readonly := "lvs_write-test", "lvs_read-test"
	for secret, scopes := range map[string]model.ScopeList{key: model.ScopeList(model.AllScopes()), readonly: {model.ScopeInstanceRead}} {
		if err = db.Create(&model.APIKey{UserID: admin.ID, Name: secret, Prefix: secret, KeyHash: service.HashAPIKey(secret), Scopes: scopes, Status: "active"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	rt := runtime.New(dir)
	rt.Activate(&config.Config{JWTSecret: "tests-only-jwt-secret"}, db)
	eng, closeFn := New(rt, false)
	t.Cleanup(closeFn)
	return apiFixture{db: db, eng: eng, admin: admin, owner: owner, other: other, inst: inst, key: key, readonly: readonly, rt: rt}
}
func (f apiFixture) request(t *testing.T, method, path, body, key string, user *model.User) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if user != nil {
		token, _, err := auth.GenerateToken(f.rt.JWTSecret(), user.ID, user.Role, user.SessionVersion)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(&http.Cookie{Name: auth.CookieToken, Value: token})
		req.AddCookie(&http.Cookie{Name: auth.CookieCSRF, Value: "test-csrf"})
		req.Header.Set(auth.HeaderCSRF, "test-csrf")
	}
	rec := httptest.NewRecorder()
	f.eng.ServeHTTP(rec, req)
	return rec
}
func TestRecoveryRoutesPermissionAndShapes(t *testing.T) {
	f := newAPIFixture(t)
	snapshot := model.Snapshot{InstanceID: f.inst.ID, Name: "base", Status: "available"}
	if err := f.db.Create(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"/api", "/api/v1"} {
		key := ""
		var user *model.User = &f.admin
		if prefix == "/api/v1" {
			key = f.key
			user = nil
		}
		res := f.request(t, "GET", fmt.Sprintf("%s/instances/%d/snapshots", prefix, f.inst.ID), "", key, user)
		if res.Code != 200 || !bytes.Contains(res.Body.Bytes(), []byte(`"items"`)) {
			t.Fatalf("%s: %d %s", prefix, res.Code, res.Body.String())
		}
	}
	res := f.request(t, "GET", fmt.Sprintf("/api/instances/%d/snapshots", f.inst.ID), "", "", &f.other)
	if res.Code != 403 {
		t.Fatalf("ownership: %d %s", res.Code, res.Body.String())
	}
	res = f.request(t, "POST", fmt.Sprintf("/api/v1/instances/%d/snapshots", f.inst.ID), `{"name":"test"}`, f.readonly, nil)
	if res.Code != 403 {
		t.Fatalf("scope: %d %s", res.Code, res.Body.String())
	}
	res = f.request(t, "POST", fmt.Sprintf("/api/instances/%d/snapshots", f.inst.ID), `{"name":"test"}`, "", &f.owner)
	if res.Code != 403 {
		t.Fatalf("session destructive admin: %d", res.Code)
	}
}
