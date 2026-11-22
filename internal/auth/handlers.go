package auth

import (
	"errors"
	"net/http"
	"time"

	"chatgo/internal/api/httpx"
	"chatgo/internal/store"
)

// HTTP handlers for the /api/auth/* surface. apimgr.registerAuth wires these
// onto frozen paths; keeping the logic here lets internal/auth test the whole
// surface without an import cycle through internal/apimgr.

// userView is the JSON shape returned by login/register/me.
type userView struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	AvatarURL string `json:"avatar_url,omitempty"`
}

func viewUser(u *store.User) userView {
	return userView{ID: u.ID, Email: u.Email, Name: u.Name, Role: u.Role, AvatarURL: u.AvatarURL}
}

// HandleLogin POST /api/auth/login {email,password}.
func (a *Auth) HandleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := httpx.Decode(r, &body); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	u, err := a.Login(r.Context(), RequestIP(r), body.Email, body.Password)
	switch {
	case errors.Is(err, ErrRateLimited):
		httpx.ErrorJSON(w, http.StatusTooManyRequests, "too many login attempts, try again later")
	case errors.Is(err, ErrInvalidCredentials):
		httpx.ErrorJSON(w, http.StatusUnauthorized, "invalid email or password")
	case err != nil:
		httpx.InternalError(w, r, err)
	default:
		a.finishLogin(w, r, u)
	}
}

// HandleRegister POST /api/auth/register {email,password,name}, per the
// Registration policy. "approve" answers 201 + pending user (no session);
// "open" logs the new user straight in.
func (a *Auth) HandleRegister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Name     string `json:"name"`
	}
	if err := httpx.Decode(r, &body); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	u, err := a.Register(r.Context(), body.Email, body.Password, body.Name)
	switch {
	case errors.Is(err, ErrRegistrationClosed):
		httpx.ErrorJSON(w, http.StatusForbidden, "registration is closed")
	case errors.Is(err, ErrEmailExists):
		httpx.ErrorJSON(w, http.StatusConflict, "an account with that email already exists")
	case errors.Is(err, ErrWeakPassword):
		httpx.ErrorJSON(w, http.StatusBadRequest, "password must be 8-72 characters")
	case errors.Is(err, ErrInvalidEmail):
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid email address")
	case err != nil:
		httpx.InternalError(w, r, err)
	case u.Role == store.RolePending:
		httpx.JSON(w, http.StatusCreated, viewUser(u))
	default:
		a.finishLogin(w, r, u)
	}
}

// HandleLogout POST /api/auth/logout — deletes the session row when attached,
// always clears the cookie (tolerant of anonymous, contract §AuthPolicy).
func (a *Auth) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if s, ok := a.SessionFrom(r.Context()); ok {
		if err := a.Store.Sessions.Delete(r.Context(), s.Token); err != nil {
			httpx.InternalError(w, r, err)
			return
		}
	}
	a.ClearSessionCookie(w)
	httpx.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// HandleConfig GET /api/auth/config — public capabilities for the login page.
func (a *Auth) HandleConfig(w http.ResponseWriter, r *http.Request) {
	name := a.Cfg.OIDC.DisplayName
	if name == "" {
		name = "SSO"
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"oidc_enabled": a.OIDCEnabled(),
		"oidc_name":    name,
		"registration": a.Cfg.Registration,
	})
}

// HandleMe GET /api/auth/me — identity probe; Require attached (and throttled
// the last_active touch) before this runs.
func (a *Auth) HandleMe(w http.ResponseWriter, r *http.Request) {
	u, ok := a.UserFrom(r.Context())
	if !ok {
		httpx.ErrorJSON(w, http.StatusUnauthorized, "authentication required")
		return
	}
	httpx.JSON(w, http.StatusOK, viewUser(u))
}

// finishLogin issues a session, sets the cookie and returns the user.
func (a *Auth) finishLogin(w http.ResponseWriter, r *http.Request, u *store.User) {
	sess, err := a.CreateSession(r.Context(), u.ID, r.UserAgent(), RequestIP(r))
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	a.SetSessionCookie(w, sess.Token, time.Unix(sess.ExpiresAt, 0))
	httpx.JSON(w, http.StatusOK, viewUser(u))
}
