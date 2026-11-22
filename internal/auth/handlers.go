package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"chatgo/internal/api/httpx"
	"chatgo/internal/audit"
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
// Local password success for a user with 2FA enabled issues NO cookie: the
// answer is {requires_totp, totp_token} and the session comes from
// POST /api/auth/totp (docs/FEATURES-V2.md §BE-SEC).
func (a *Auth) HandleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := httpx.Decode(r, &body); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ip := a.RequestIP(r)
	// Shape-check the email BEFORE any store query, bcrypt burn or audit
	// write (L8-M2): the failed-login row below carries this email whole, so
	// an unvalidated megabyte string used to bloat audit_log on every probe.
	// validEmail is the same gate Register and the OIDC claim path enforce
	// (≤254B + addr-spec), so no storable account email can ever 400 here —
	// rejecting on shape leaks nothing about existence, and such garbage
	// never reaches the DB or the audit table at all.
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if !validEmail(email) {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid email address")
		return
	}
	u, err := a.Login(r.Context(), ip, email, body.Password)
	switch {
	case errors.Is(err, ErrRateLimited):
		httpx.ErrorJSON(w, http.StatusTooManyRequests, "too many login attempts, try again later")
	case errors.Is(err, ErrInvalidCredentials):
		// Identity lands in the email column, never in detail (PII rule);
		// unknown-user vs wrong-password are indistinguishable by design.
		audit.RecordIdentity(r.Context(), a.Store.DB, "", email,
			audit.ActionLoginFail, "invalid credentials", ip)
		httpx.ErrorJSON(w, http.StatusUnauthorized, "invalid email or password")
	case err != nil:
		httpx.InternalError(w, r, err)
	default:
		enabled, err := a.totpEnabled(r.Context(), u.ID)
		if err != nil {
			httpx.InternalError(w, r, err)
			return
		}
		if enabled {
			a.totpChallenge(w, r, u)
			return
		}
		a.finishLogin(w, r, u, "password")
	}
}

// totpChallenge answers a passed password check for a 2FA user: stateless
// handoff token, explicitly no session cookie.
func (a *Auth) totpChallenge(w http.ResponseWriter, r *http.Request, u *store.User) {
	token, err := a.issueTOTPToken(u.ID)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	audit.Record(r.Context(), a.Store.DB, u, audit.ActionTOTPChallenge, "", a.RequestIP(r))
	httpx.JSON(w, http.StatusOK, map[string]any{
		"requires_totp": true,
		"totp_token":    token,
	})
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
	u, err := a.Register(r.Context(), a.RequestIP(r), body.Email, body.Password, body.Name)
	switch {
	case errors.Is(err, ErrRegistrationClosed):
		httpx.ErrorJSON(w, http.StatusForbidden, "registration is closed")
	case errors.Is(err, ErrRateLimited):
		httpx.ErrorJSON(w, http.StatusTooManyRequests, "too many registration attempts, try again later")
	case errors.Is(err, ErrEmailExists):
		httpx.ErrorJSON(w, http.StatusConflict, "an account with that email already exists")
	case errors.Is(err, ErrWeakPassword):
		httpx.ErrorJSON(w, http.StatusBadRequest, "password must be 8-72 bytes")
	case errors.Is(err, ErrInvalidEmail):
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid email address")
	case err != nil:
		httpx.InternalError(w, r, err)
	case u.Role == store.RolePending:
		httpx.JSON(w, http.StatusCreated, viewUser(u))
	default:
		a.finishLogin(w, r, u, "register")
	}
}

// HandleLogout POST /api/auth/logout — deletes the session row when attached,
// always clears the cookie (tolerant of anonymous, contract §AuthPolicy).
func (a *Auth) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if u, ok := a.UserFrom(r.Context()); ok {
		audit.Record(r.Context(), a.Store.DB, u, audit.ActionLogout, "", a.RequestIP(r))
	}
	if s, ok := a.SessionFrom(r.Context()); ok {
		if err := a.Store.Sessions.Delete(r.Context(), s.Token); err != nil {
			httpx.InternalError(w, r, err)
			return
		}
	}
	a.ClearSessionCookie(w, r)
	httpx.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// SettingFirstRunDone is the settings key stamped by the first-user
// bootstrap transaction (docs/AUTHGATE-CONTRACT.md §2). Value "true" once an
// instance has had its first account; instances upgraded from before the
// stamp have users but no setting, so isFirstRun falls back to a user COUNT.
const SettingFirstRunDone = "first_run_done"

// HandleConfig GET /api/auth/config — public capabilities for the login page.
// first_run is additive (AUTHGATE §5): the FE renders the create-admin card
// while it is true.
func (a *Auth) HandleConfig(w http.ResponseWriter, r *http.Request) {
	name := a.Cfg.OIDC.DisplayName
	if name == "" {
		name = "SSO"
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"oidc_enabled": a.OIDCEnabled(),
		"oidc_name":    name,
		"registration": a.Cfg.Registration,
		"first_run":    a.isFirstRun(r.Context()),
	})
}

// HandleStatus GET /api/auth/status — public auth-gate probe
// (docs/AUTHGATE-CONTRACT.md §2): authenticated = a live session cookie is
// present (same check as the SPA shell guard); first_run = the instance still
// awaits its admin (SettingFirstRunDone unset AND zero users, see isFirstRun).
//
// Integrator wiring (apimgr/auth.go is BE-apichat-owned):
// mux.HandleFunc("GET /api/auth/status", d.Auth.HandleStatus)
// — api.AuthPolicy's /api/auth/* default branch already keeps it public.
func (a *Auth) HandleStatus(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]any{
		"authenticated": a.HasValidSession(r),
		"first_run":     a.isFirstRun(r.Context()),
	})
}

// isFirstRun reports whether the admin bootstrap is still pending — i.e. the
// instance is unclaimed. The SettingFirstRunDone stamp is only a fast path:
// upgraded instances predate it, so a missing setting alone must not report
// first_run while users already exist (the FE would bounce a signed-in admin
// to /welcome). Authoritative signal: setting=="true" → false; otherwise the
// instance is in first run only while it has zero users (same COUNT probe the
// Register bootstrap uses). A store failure degrades to false: first_run only
// drives FE copy, and the actual register-bootstrap gate is the first-user
// transaction, so a read error can never widen access.
func (a *Auth) isFirstRun(ctx context.Context) bool {
	v, err := a.Store.Settings.Get(ctx, SettingFirstRunDone)
	if err != nil {
		slog.Warn("auth: first_run settings read failed", "key", SettingFirstRunDone, "err", err)
		return false
	}
	if v != nil && *v == "true" {
		return false
	}
	n, err := a.userCount(ctx)
	if err != nil {
		slog.Warn("auth: first_run user count read failed", "err", err)
		return false
	}
	return n == 0
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

// finishLogin issues a session, sets the cookie and returns the user. Every
// successful local login funnels through here (password, TOTP-verified, and
// open-registration auto-login), which is also where the login.success audit
// row is written — method records which path issued the session.
func (a *Auth) finishLogin(w http.ResponseWriter, r *http.Request, u *store.User, method string) {
	sess, err := a.CreateSession(r.Context(), u.ID, r.UserAgent(), a.RequestIP(r))
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	audit.Record(r.Context(), a.Store.DB, u, audit.ActionLoginSuccess, "method="+method, a.RequestIP(r))
	a.SetSessionCookie(w, r, sess.Token, time.Unix(sess.ExpiresAt, 0))
	httpx.JSON(w, http.StatusOK, viewUser(u))
}
