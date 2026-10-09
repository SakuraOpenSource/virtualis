package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SakuraOpenSource/virtualis/internal/auth"
	"github.com/SakuraOpenSource/virtualis/internal/model"
)

// loginSession performs a login and returns the issued token and csrf cookies.
func loginSession(t *testing.T, f apiFixture, identifier, password string) (token, csrf string) {
	t.Helper()
	body := fmt.Sprintf(`{"identifier":%q,"password":%q}`, identifier, password)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: auth.CookieCSRF, Value: "seed"})
	req.Header.Set(auth.HeaderCSRF, "seed")
	rec := httptest.NewRecorder()
	f.eng.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
	}
	for _, ck := range rec.Result().Cookies() {
		switch ck.Name {
		case auth.CookieToken:
			token = ck.Value
		case auth.CookieCSRF:
			csrf = ck.Value
		}
	}
	if token == "" || csrf == "" {
		t.Fatal("login did not issue session cookies")
	}
	return token, csrf
}

func doWithSession(t *testing.T, f apiFixture, method, path, body, token, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: auth.CookieToken, Value: token})
	req.AddCookie(&http.Cookie{Name: auth.CookieCSRF, Value: csrf})
	req.Header.Set(auth.HeaderCSRF, csrf)
	rec := httptest.NewRecorder()
	f.eng.ServeHTTP(rec, req)
	return rec
}

func setAdminPassword(t *testing.T, f apiFixture, password string) {
	t.Helper()
	restore := auth.SetPasswordCost(4)
	defer restore()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.User{}).Where("id = ?", f.admin.ID).Update("password_hash", hash).Error; err != nil {
		t.Fatal(err)
	}
}

// P-AUTH-1: logging out must invalidate the issued token, not only drop cookies.
func TestAuditLogoutRevokesCopiedToken(t *testing.T) {
	f := newAPIFixture(t)
	setAdminPassword(t, f, "original-pass-123")
	token, csrf := loginSession(t, f, "admin", "original-pass-123")

	rec := doWithSession(t, f, http.MethodPost, "/api/auth/logout", "", token, csrf)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout: %d %s", rec.Code, rec.Body.String())
	}
	// A copied token must be rejected even though the JWT itself is unexpired.
	rec = doWithSession(t, f, http.MethodGet, "/api/me", "", token, csrf)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("copied token survived logout: %d %s", rec.Code, rec.Body.String())
	}
}

// P-AUTH-1: changing the password must invalidate sessions issued before it.
func TestAuditPasswordChangeRevokesOlderTokens(t *testing.T) {
	f := newAPIFixture(t)
	setAdminPassword(t, f, "original-pass-123")
	oldToken, oldCsrf := loginSession(t, f, "admin", "original-pass-123")

	// Cross a whole-second boundary so the JWT iat (second precision) is
	// strictly before password_changed_at even after its second truncation.
	time.Sleep(1100 * time.Millisecond)
	rec := doWithSession(t, f, http.MethodPost, "/api/me/password", `{"old_password":"original-pass-123","new_password":"replacement-pass-456"}`, oldToken, oldCsrf)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("change password: %d %s", rec.Code, rec.Body.String())
	}
	rec = doWithSession(t, f, http.MethodGet, "/api/me", "", oldToken, oldCsrf)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("pre-change token survived password rotation: %d %s", rec.Code, rec.Body.String())
	}
	// The freshly issued session (from the change response) keeps working.
	newToken, newCsrf := loginSession(t, f, "admin", "replacement-pass-456")
	rec = doWithSession(t, f, http.MethodGet, "/api/me", "", newToken, newCsrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("new credentials rejected: %d %s", rec.Code, rec.Body.String())
	}
}

// P-AUTH-3: repeated failed password attempts must be throttled with 429 + Retry-After.
func TestAuditLoginThrottlesRepeatedFailures(t *testing.T) {
	f := newAPIFixture(t)
	setAdminPassword(t, f, "original-pass-123")
	statuses := []int{}
	for i := 0; i < 6; i++ {
		body := `{"identifier":"admin","password":"totally-wrong"}`
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: auth.CookieCSRF, Value: "seed"})
		req.Header.Set(auth.HeaderCSRF, "seed")
		rec := httptest.NewRecorder()
		f.eng.ServeHTTP(rec, req)
		statuses = append(statuses, rec.Code)
		if i == 5 {
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("want 429 on 6th attempt, statuses=%v body=%s", statuses, rec.Body.String())
			}
			if rec.Header().Get("Retry-After") == "" {
				t.Fatal("429 response missing Retry-After header")
			}
		}
	}
}

// P-AUTH-2: Create must honor requested scopes and expiry instead of forcing all scopes.
func TestAuditAPIKeyCreateHonorsRequestedScopeAndExpiry(t *testing.T) {
	f := newAPIFixture(t)
	// The site key model allows exactly one active row; retire the seeded ones.
	f.db.Model(&model.APIKey{}).Where("status = ?", model.APIKeyActive).Update("status", model.APIKeyRevoked)
	res := f.request(t, http.MethodPost, "/api/api-keys", `{"name":"ci read-only","scopes":["instance:read"],"expires_in_days":1}`, "", &f.admin)
	if res.Code != http.StatusOK {
		t.Fatalf("create: %d %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"scopes":["instance:read"]`) {
		t.Fatalf("requested scopes ignored: %s", res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"expires_at"`) || strings.Contains(res.Body.String(), `"expires_at":null`) {
		t.Fatalf("requested expiry ignored: %s", res.Body.String())
	}
	// Unknown scopes are rejected rather than silently widened.
	res = f.request(t, http.MethodPost, "/api/api-keys", `{"name":"bad","scopes":["root:everything"]}`, "", &f.admin)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("invalid scope accepted: %d %s", res.Code, res.Body.String())
	}
}

// P-AUTH-2: a GET list must be read-only: no scope elevation, no revoking other rows.
func TestAuditAPIKeyListDoesNotElevateOrRevoke(t *testing.T) {
	f := newAPIFixture(t)
	oldKey := model.APIKey{UserID: f.admin.ID, Name: "old", Prefix: "lvs_old", KeyHash: "h-old", Scopes: model.ScopeList(model.AllScopes()), Status: model.APIKeyActive}
	if err := f.db.Create(&oldKey).Error; err != nil {
		t.Fatal(err)
	}
	newKey := model.APIKey{UserID: f.admin.ID, Name: "new", Prefix: "lvs_new", KeyHash: "h-new", Scopes: model.ScopeList{model.ScopeInstanceRead}, Status: model.APIKeyActive}
	if err := f.db.Create(&newKey).Error; err != nil {
		t.Fatal(err)
	}
	res := f.request(t, http.MethodGet, "/api/api-keys", "", "", &f.admin)
	if res.Code != http.StatusOK {
		t.Fatalf("list: %d %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"scopes":["instance:read"]`) {
		t.Fatalf("listing elevated limited key: %s", res.Body.String())
	}
	var stored model.APIKey
	if err := f.db.First(&stored, oldKey.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != model.APIKeyActive {
		t.Fatal("read-only listing revoked another credential row")
	}
}

// VIR-CORE-02: a rejected reinstall (busy fence) must not mutate image_id.
func TestAuditReinstallRejectedByFenceKeepsImage(t *testing.T) {
	f := newAPIFixture(t)
	imageA := model.Image{Name: "base-a", Driver: "qemu", Type: "vm"}
	imageB := model.Image{Name: "base-b", Driver: "qemu", Type: "vm"}
	if err := f.db.Create(&imageA).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&imageB).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&f.inst).Updates(map[string]any{"image_id": imageA.ID, "busy_operation": "foreign-worker"}).Error; err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"action":"reinstall","image_id":%d}`, imageB.ID)
	res := f.request(t, http.MethodPost, fmt.Sprintf("/api/instances/%d/power", f.inst.ID), body, "", &f.admin)
	if res.Code != http.StatusConflict {
		t.Fatalf("expected busy conflict, got %d %s", res.Code, res.Body.String())
	}
	var inst model.Instance
	if err := f.db.First(&inst, f.inst.ID).Error; err != nil {
		t.Fatal(err)
	}
	if inst.ImageID == nil || *inst.ImageID != imageA.ID {
		t.Fatalf("rejected reinstall mutated image_id: %+v", inst.ImageID)
	}
}

// X-Levis-Operation-ID: v1 mutating routes must persist the caller correlation id.
func TestAuditV1ResizePersistsLevisOperationID(t *testing.T) {
	f := newAPIFixture(t)
	if err := f.db.Model(&f.inst).Update("spec", `{"cpu":1,"memory_mb":1024,"disk_gb":20,"arch":"x86_64"}`).Error; err != nil {
		t.Fatal(err)
	}
	body := `{"spec":{"cpu":1,"memory_mb":1024,"disk_gb":20}}`
	req := httptest.NewRequest(http.MethodPatch, fmt.Sprintf("/api/v1/instances/%d/spec", f.inst.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.key)
	req.Header.Set("X-Levis-Operation-ID", "levis-op-42")
	rec := httptest.NewRecorder()
	f.eng.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatal("resize unexpectedly succeeded without an agent")
	}
	res := f.request(t, http.MethodGet, fmt.Sprintf("/api/v1/instances/%d/logs", f.inst.ID), "", f.key, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("logs: %d %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "levis-op-42") {
		t.Fatalf("correlation id not persisted in operation logs: %s", res.Body.String())
	}
}

// VIR-CORE-06: trashed instances keep read-only log access for diagnostics.
func TestAuditTrashedInstanceLogsRemainReadable(t *testing.T) {
	f := newAPIFixture(t)
	now := f.inst.CreatedAt
	if err := f.db.Model(&f.inst).Update("trashed_at", now).Error; err != nil {
		t.Fatal(err)
	}
	res := f.request(t, http.MethodGet, fmt.Sprintf("/api/instances/%d/logs", f.inst.ID), "", "", &f.admin)
	if res.Code != http.StatusOK {
		t.Fatalf("trashed logs: %d %s", res.Code, res.Body.String())
	}
}
