package server

import (
	"net/http"
	"testing"

	"github.com/SakuraOpenSource/virtualis/internal/auth"
	"github.com/SakuraOpenSource/virtualis/internal/model"
)

// REV-AUTH-SAME-SECOND: a session issued in the SAME second as a password
// change must be rejected immediately. Comparing JWT iat (second precision)
// against a truncated password_changed_at cannot distinguish tokens issued
// within that second from the change itself, so a token copied before the
// change keeps admin access until natural expiry. The regression must NOT
// sleep across a second boundary to pass — immediate revocation is the
// security property.
func TestPasswordChangeSameSecondRevokesOlderSessionImmediately(t *testing.T) {
	f := newAPIFixture(t)
	setAdminPassword(t, f, "original-pass-123")
	oldToken, oldCsrf := loginSession(t, f, "admin", "original-pass-123")

	// Change the password right away: whatever wall-clock second this lands
	// in, it is the same second as the login above with high probability,
	// and even when it is not, the assertion below must still hold.
	rec := doWithSession(t, f, http.MethodPost, "/api/me/password", `{"old_password":"original-pass-123","new_password":"replacement-pass-456"}`, oldToken, oldCsrf)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("change password: %d %s", rec.Code, rec.Body.String())
	}

	// The pre-change session must be dead on the very next request.
	rec = doWithSession(t, f, http.MethodGet, "/api/me", "", oldToken, oldCsrf)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("pre-change token survived same-second password rotation: %d %s", rec.Code, rec.Body.String())
	}

	// The session re-issued by the change response keeps working: the new
	// token carries the bumped session version.
	newToken, newCsrf := loginSession(t, f, "admin", "replacement-pass-456")
	rec = doWithSession(t, f, http.MethodGet, "/api/me", "", newToken, newCsrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("post-change token rejected: %d %s", rec.Code, rec.Body.String())
	}
}

// REV-AUTH-SAME-SECOND (upgraded-row semantics): a legacy token without a
// session version claim is refused once the account has a nonzero session
// version — otherwise pre-upgrade tokens could never be invalidated by a
// password change at all.
func TestPasswordChangeRejectsLegacyTokenWithoutSessionVersion(t *testing.T) {
	f := newAPIFixture(t)
	setAdminPassword(t, f, "original-pass-123")

	// Legacy token: signed by the same secret, same shape, but no sess_ver
	// claim (as issued by binaries before this fix).
	restore := auth.SetPasswordCost(4)
	defer restore()
	legacy, _, err := auth.GenerateTokenWithTTL(f.rt.JWTSecret(), f.admin.ID, f.admin.Role, auth.TokenTTL, 0)
	if err != nil {
		t.Fatal(err)
	}

	// No password change yet: the account still has session_version 0, so
	// the legacy token is accepted (upgrade path must not force a global
	// re-login of untouched accounts).
	rec := doWithSession(t, f, http.MethodGet, "/api/me", "", legacy, "test-csrf")
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy token rejected before any password change: %d %s", rec.Code, rec.Body.String())
	}

	// Bump the session version the way a password change does.
	if err := f.db.Model(&model.User{}).Where("id = ?", f.admin.ID).Update("session_version", 1).Error; err != nil {
		t.Fatal(err)
	}
	rec = doWithSession(t, f, http.MethodGet, "/api/me", "", legacy, "test-csrf")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("legacy token survived version bump: %d %s", rec.Code, rec.Body.String())
	}
}
