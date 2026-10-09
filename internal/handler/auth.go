package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/SakuraOpenSource/virtualis/internal/auth"
	"github.com/SakuraOpenSource/virtualis/internal/config"
	"github.com/SakuraOpenSource/virtualis/internal/httpx"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"github.com/SakuraOpenSource/virtualis/internal/service"
)

// CodeTooManyRequests marks the 429 produced by the login failure limiter.
const CodeTooManyRequests = "TOO_MANY_REQUESTS"

// Bootstrap exposes installation status and site info for the frontend.
func (h *Handler) Bootstrap(c *gin.Context) {
	OK(c, h.install.Bootstrap())
}

// TestDatabase checks database connectivity without persisting anything.
func (h *Handler) TestDatabase(c *gin.Context) {
	if h.rt.Installed() {
		Conflict(c, "already installed")
		return
	}
	var req config.Database
	if !bindJSON(c, &req) {
		return
	}
	if err := h.install.TestDatabase(req); err != nil {
		respond(c, nil, err)
		return
	}
	OK(c, gin.H{"ok": true})
}

// Install performs first-time setup, creates admin account and activates runtime.
func (h *Handler) Install(c *gin.Context) {
	var req service.InstallRequest
	if !bindJSON(c, &req) {
		return
	}
	if err := h.install.Install(req); err != nil {
		respond(c, nil, err)
		return
	}
	// Auto-login admin to spare one round trip.
	user, err := h.users().Login(req.AdminUsername, req.AdminPassword)
	if err != nil {
		OK(c, gin.H{"ok": true})
		return
	}
	if err := h.setSession(c, user); err != nil {
		respond(c, nil, err)
		return
	}
	OK(c, gin.H{"ok": true, "user": user})
}

// LoginRequest is the login payload.
type LoginRequest struct {
	Identifier  string `json:"identifier"`
	Password    string `json:"password"`
	CaptchaID   string `json:"captcha_id"`
	CaptchaCode string `json:"captcha_code"`
}

// Login authenticates admin and issues session cookies.
// Captcha is verified when the login scene is enabled. Failure throttling
// (account + source IP) runs unconditionally — captcha stays optional by
// default, but the limiter must always bound password guessing attempts and
// the bcrypt CPU they consume.
func (h *Handler) Login(c *gin.Context) {
	var req LoginRequest
	if !bindJSON(c, &req) {
		return
	}
	ip := c.ClientIP()
	// The limiter check precedes captcha and password verification so locked
	// out requests end here without spending bcrypt CPU.
	if !h.allowLogin(c, loginScopeLogin, req.Identifier, ip) {
		return
	}
	if err := h.captchaSvc().Verify(service.CaptchaSceneLogin, req.CaptchaID, req.CaptchaCode); err != nil {
		respond(c, nil, err)
		return
	}
	user, err := h.users().Login(req.Identifier, req.Password)
	if err != nil {
		h.loginTracker.RecordFailure(loginScopeLogin, req.Identifier, ip)
		respond(c, nil, err)
		return
	}
	h.loginTracker.RecordSuccess(loginScopeLogin, req.Identifier)
	if err := h.setSession(c, user); err != nil {
		respond(c, nil, err)
		return
	}
	OK(c, gin.H{"user": user})
}

// allowLogin checks the failure limiter and writes 429 + Retry-After when the
// attempt is not allowed. The message does not reveal which dimension
// (account or source) is locked, to avoid telling probers where to pivot.
func (h *Handler) allowLogin(c *gin.Context, scope, identifier, ip string) bool {
	if ok, wait := h.loginTracker.Check(scope, identifier, ip); !ok {
		seconds := int(wait.Seconds())
		if seconds < 1 {
			seconds = 1
		}
		c.Header("Retry-After", strconv.Itoa(seconds))
		Fail(c, http.StatusTooManyRequests, CodeTooManyRequests, "too many attempts, retry later")
		return false
	}
	return true
}

// Logout revokes the current token server-side and clears cookies.
//
// Dropping cookies only affects the browser that still holds them; a copied
// token keeps working until natural expiry. The revocation is persisted in
// the shared database (revoked_tokens) BEFORE the 204 is returned, so every
// replica enforces it and it survives restarts. Tokens without a jti
// (pre-upgrade format) cannot be identified for precise revocation; for
// those, logout bumps the account's session_version, which invalidates the
// whole session family on every replica at once.
func (h *Handler) Logout(c *gin.Context) {
	if token, err := c.Cookie(auth.CookieToken); err == nil && token != "" {
		if claims, err := auth.ParseToken(h.rt.JWTSecret(), token); err == nil {
			if claims.ID != "" && claims.ExpiresAt != nil {
				// Fail closed: a logout whose revocation could not be
				// persisted must not report success, otherwise the client
				// believes the session is dead while the token still
				// authenticates on the next replica.
				if err := h.revoker.Revoke(claims.ID, claims.ExpiresAt.Time); err != nil {
					Internal(c, "logout unavailable, try again")
					return
				}
			} else {
				// Legacy token (no jti): retire the entire session family
				// via the version counter — precise per-token revocation is
				// impossible without an identifier.
				if err := h.db().Model(&model.User{}).Where("id = ?", claims.UserID).
					Update("session_version", gorm.Expr("session_version + 1")).Error; err != nil {
					Internal(c, "logout unavailable, try again")
					return
				}
			}
		}
	}
	h.dropCookie(c, auth.CookieToken, true)
	h.dropCookie(c, auth.CookieCSRF, false)
	noContent(c)
}

// Me returns current authenticated user.
func (h *Handler) Me(c *gin.Context) {
	OK(c, gin.H{"user": httpx.CurrentUser(c)})
}

// UpdateEmailRequest updates email.
type UpdateEmailRequest struct {
	Password string `json:"password"`
	Email    string `json:"email"`
}

// UpdateEmail changes the current user's email.
func (h *Handler) UpdateEmail(c *gin.Context) {
	var req UpdateEmailRequest
	if !bindJSON(c, &req) {
		return
	}
	uid := httpx.CurrentUserID(c)
	if err := h.users().ChangeEmail(uid, req.Password, req.Email); err != nil {
		respond(c, nil, err)
		return
	}
	user, err := h.users().Get(uid)
	respond(c, gin.H{"user": user}, err)
}

// UpdatePasswordRequest changes password.
type UpdatePasswordRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

// UpdatePassword changes current user's password and reissues token.
func (h *Handler) UpdatePassword(c *gin.Context) {
	var req UpdatePasswordRequest
	if !bindJSON(c, &req) {
		return
	}
	uid := httpx.CurrentUserID(c)
	if err := h.users().ChangePassword(uid, req.OldPassword, req.NewPassword); err != nil {
		respond(c, nil, err)
		return
	}
	user, err := h.users().Get(uid)
	if err != nil {
		respond(c, nil, err)
		return
	}
	if err := h.setSession(c, user); err != nil {
		respond(c, nil, err)
		return
	}
	noContent(c)
}

func (h *Handler) setSession(c *gin.Context, u *model.User) error {
	// Sign under the CURRENT session_version read with the user row: after a
	// password change the handler re-reads the user, so this token carries
	// the incremented version and survives the equality check.
	token, _, err := auth.GenerateToken(h.rt.JWTSecret(), u.ID, u.Role, u.SessionVersion)
	if err != nil {
		return err
	}
	csrf, err := auth.GenerateCSRFToken()
	if err != nil {
		return err
	}
	secure := h.isSecure(c)
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(auth.CookieToken, token, int(auth.TokenTTL.Seconds()), "/", "", secure, true)
	c.SetCookie(auth.CookieCSRF, csrf, int(auth.TokenTTL.Seconds()), "/", "", secure, false)
	return nil
}

func (h *Handler) dropCookie(c *gin.Context, name string, httpOnly bool) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(name, "", -1, "/", "", h.isSecure(c), httpOnly)
}

func (h *Handler) isSecure(c *gin.Context) bool {
	if c.Request.TLS != nil {
		return true
	}
	return strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https")
}
