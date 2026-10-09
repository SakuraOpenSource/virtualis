package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/SakuraOpenSource/virtualis/internal/auth"
	"github.com/SakuraOpenSource/virtualis/internal/config"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"github.com/SakuraOpenSource/virtualis/internal/runtime"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// sharedDBFixture builds two independent engines (two Handler instances =
// two replicas) over ONE database file, which is the deployment contract for
// durable logout: a revocation written by replica A must be enforced by
// replica B and by any process that starts later.
func sharedDBFixture(t *testing.T) (a, b apiFixture) {
	t.Helper()
	dir := t.TempDir()
	dsn := filepath.Join(dir, "shared.db")
	open := func() *gorm.DB {
		db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		if err = db.AutoMigrate(model.AllModels()...); err != nil {
			t.Fatal(err)
		}
		return db
	}
	// One shared admin row: the two replicas share the database, so the user
	// is created exactly once on the first connection.
	seed := func(db *gorm.DB) model.User {
		var admin model.User
		if err := db.First(&admin, "username = ?", "admin").Error; err != nil {
			admin = model.User{Username: "admin", Email: "a@test.local", Role: "admin", Status: "active"}
			if err := db.Create(&admin).Error; err != nil {
				t.Fatal(err)
			}
		}
		return admin
	}
	mk := func() apiFixture {
		db := open()
		admin := seed(db)
		t.Cleanup(func() { raw, _ := db.DB(); _ = raw.Close() })
		rt := runtime.New(dir)
		rt.Activate(&config.Config{JWTSecret: "tests-only-jwt-secret"}, db)
		eng, closeFn := New(rt, false)
		t.Cleanup(closeFn)
		return apiFixture{db: db, eng: eng, admin: admin, rt: rt}
	}
	return mk(), mk()
}

// REV-AUTH-LOGOUT: logout must be durable and shared. Revoking on one
// handler (replica) has to be enforced by a different handler over the same
// database, and by a fresh process started afterwards (restart). The old
// in-memory list made logout reversible exactly when an operator restarted
// or a second replica served the request.
func TestLogoutPersistsAcrossReplicasAndRestart(t *testing.T) {
	f, peer := sharedDBFixture(t)
	setAdminPassword(t, f, "original-pass-123")
	token, csrf := loginSession(t, f, "admin", "original-pass-123")

	// Sanity: the peer replica accepts the token before logout.
	rec := doWithSession(t, peer, http.MethodGet, "/api/me", "", token, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("peer rejected live token: %d %s", rec.Code, rec.Body.String())
	}

	// Logout on the first replica.
	rec = doWithSession(t, f, http.MethodPost, "/api/auth/logout", "", token, csrf)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout: %d %s", rec.Code, rec.Body.String())
	}

	// Same replica rejects it.
	rec = doWithSession(t, f, http.MethodGet, "/api/me", "", token, csrf)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("local replica accepted logged-out token: %d", rec.Code)
	}
	// The OTHER replica must reject it too (shared durable storage).
	rec = doWithSession(t, peer, http.MethodGet, "/api/me", "", token, csrf)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("peer replica accepted logged-out token: %d", rec.Code)
	}
	// A fresh process (restart: new revoker, warm from the database) must
	// reject it as well.
	rt := runtime.New(t.TempDir())
	rt.Activate(&config.Config{JWTSecret: "tests-only-jwt-secret"}, peer.db)
	eng, closeFn := New(rt, false)
	t.Cleanup(closeFn)
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieToken, Value: token})
	req.AddCookie(&http.Cookie{Name: auth.CookieCSRF, Value: csrf})
	rec2 := httptest.NewRecorder()
	eng.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("restart resurrected logged-out token: %d", rec2.Code)
	}
}

// REV-AUTH-LOGOUT (legacy tokens): a token without jti (pre-upgrade format)
// cannot be identified for precise revocation, so logout must fall back to
// invalidating the whole session family — bumping session_version — instead
// of silently leaving the token valid.
func TestLogoutLegacyTokenWithoutJTIGloballyInvalidates(t *testing.T) {
	f, peer := sharedDBFixture(t)
	setAdminPassword(t, f, "original-pass-123")

	restore := auth.SetPasswordCost(4)
	defer restore()
	legacy, _, err := auth.GenerateTokenWithTTL(f.rt.JWTSecret(), f.admin.ID, f.admin.Role, auth.TokenTTL, 0)
	if err != nil {
		t.Fatal(err)
	}
	csrf := "test-csrf"

	rec := doWithSession(t, f, http.MethodGet, "/api/me", "", legacy, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy token rejected before logout: %d %s", rec.Code, rec.Body.String())
	}

	// Logout with the legacy token in the cookie.
	rec = doWithSession(t, f, http.MethodPost, "/api/auth/logout", "", legacy, csrf)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout: %d %s", rec.Code, rec.Body.String())
	}

	// The same legacy token must now be refused — here and on the peer.
	rec = doWithSession(t, f, http.MethodGet, "/api/me", "", legacy, csrf)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("legacy token survived logout on same replica: %d", rec.Code)
	}
	rec = doWithSession(t, peer, http.MethodGet, "/api/me", "", legacy, csrf)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("legacy token survived logout on peer replica: %d", rec.Code)
	}
}
