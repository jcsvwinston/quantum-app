package warehouse

import (
	"net/http"
	"strconv"

	"github.com/jcsvwinston/nucleus/pkg/auth"
	"github.com/jcsvwinston/nucleus/pkg/nucleus"
	"github.com/jcsvwinston/quark"
)

const (
	sessionUserIDKey = "warehouse:user_id"
	sessionEmailKey  = "warehouse:email"
)

// login validates credentials against the app_users table and establishes a
// server-side session (Redis-backed per the session_store config). RenewToken
// before writing identity defends against session fixation.
func (m *module) login(c *nucleus.Context) error {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := c.BindJSON(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
	}
	if in.Email == "" || in.Password == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "email and password are required"})
	}

	ctx := c.Request.Context()
	user, err := quark.For[AppUser](ctx, m.bridgedPG).Where("email", "=", in.Email).First()
	if err != nil || !auth.CheckPassword(in.Password, user.PasswordHash) {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
	}

	if err := m.sess.RenewToken(ctx); err != nil {
		return err
	}
	m.sess.Put(ctx, sessionUserIDKey, strconv.FormatInt(user.ID, 10))
	m.sess.Put(ctx, sessionEmailKey, user.Email)

	return c.JSON(http.StatusOK, map[string]any{"id": user.ID, "email": user.Email, "name": user.Name})
}

// logout destroys the server-side session (the Redis key is deleted).
func (m *module) logout(c *nucleus.Context) error {
	if err := m.sess.Destroy(c.Request.Context()); err != nil {
		return err
	}
	return c.NoContent()
}

// me reports the authenticated identity, or 401 without a session.
func (m *module) me(c *nucleus.Context) error {
	ctx := c.Request.Context()
	id := m.sess.GetString(ctx, sessionUserIDKey)
	if id == "" {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
	}
	return c.JSON(http.StatusOK, map[string]string{
		"user_id": id,
		"email":   m.sess.GetString(ctx, sessionEmailKey),
	})
}

// requireUser guards endpoints that mutate state or expose PII (all product
// mutations, the datasheet upload/delete, and the order reads): true when a
// session identity exists, otherwise it writes the 401 and the caller
// returns nil.
func (m *module) requireUser(c *nucleus.Context) bool {
	if m.sess.GetString(c.Request.Context(), sessionUserIDKey) == "" {
		_ = c.JSON(http.StatusUnauthorized, map[string]string{"error": "login required"})
		return false
	}
	return true
}
