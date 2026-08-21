package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/SakuraOpenSource/virtualis/internal/auth"
	"github.com/SakuraOpenSource/virtualis/internal/config"
	"github.com/SakuraOpenSource/virtualis/internal/httpx"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"github.com/SakuraOpenSource/virtualis/internal/service"
)

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
// Captcha is verified when the login scene is enabled.
func (h *Handler) Login(c *gin.Context) {
	var req LoginRequest
	if !bindJSON(c, &req) {
		return
	}
	if err := h.captchaSvc().Verify(service.CaptchaSceneLogin, req.CaptchaID, req.CaptchaCode); err != nil {
		respond(c, nil, err)
		return
	}
	user, err := h.users().Login(req.Identifier, req.Password)
	if err != nil {
		respond(c, nil, err)
		return
	}
	if err := h.setSession(c, user); err != nil {
		respond(c, nil, err)
		return
	}
	OK(c, gin.H{"user": user})
}

// Logout clears authentication cookies.
func (h *Handler) Logout(c *gin.Context) {
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
	token, _, err := auth.GenerateToken(h.rt.JWTSecret(), u.ID, u.Role)
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
