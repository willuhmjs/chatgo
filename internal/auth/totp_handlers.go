package auth

// HTTP surface for 2FA (BE-SEC). Integrator wiring (internal/auth owns these
// handlers; docs/FEATURES-V2.md routes):
//
//	POST /api/me/2fa/setup   {password}     → {secret, otpauth_uri}
//	POST /api/me/2fa/enable  {code}         → {ok:true}
//	POST /api/me/2fa/disable {password}     → {ok:true}
//	POST /api/auth/totp      {totp_token, code} → session cookie + user
//
// The first three ride AuthPolicy's /api/* Require branch (session needed);
// /api/auth/totp sits on the public /api/auth/* default branch because the
// caller has no session yet — that is exactly why it is rate-limited and
// statelessly authenticated by the totp_token.

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"

	"chatgo/internal/api/httpx"
	"chatgo/internal/audit"
	"chatgo/internal/store"
	"chatgo/internal/totp"
)

// Register2FA mounts the four 2FA routes on the shared mux.
func (a *Auth) Register2FA(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/me/2fa/setup", a.Handle2FASetup)
	mux.HandleFunc("POST /api/me/2fa/enable", a.Handle2FAEnable)
	mux.HandleFunc("POST /api/me/2fa/disable", a.Handle2FADisable)
	mux.HandleFunc("POST /api/auth/totp", a.HandleTOTPVerify)
}

// sessionUser is the defense-in-depth caller lookup every /api/me/2fa/*
// handler runs (AuthPolicy's Require already guarantees one).
func (a *Auth) sessionUser(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	u, ok := a.UserFrom(r.Context())
	if !ok {
		httpx.ErrorJSON(w, http.StatusUnauthorized, "authentication required")
		return nil, false
	}
	return u, true
}

// checkUserPassword runs bcrypt under the shared semaphore against the
// caller's own hash. False also covers OIDC-only accounts (no hash).
func (a *Auth) checkUserPassword(r *http.Request, u *store.User, password string) (bool, error) {
	if !u.HasPassword() {
		return false, nil
	}
	release, err := a.AcquireBcrypt(r.Context())
	if err != nil {
		return false, err
	}
	defer release()
	return VerifyPassword(*u.PasswordHash, password), nil
}

// Handle2FASetup POST /api/me/2fa/setup {password}.
// Local accounts only (OIDC-only → 400, per spec), rejected when 2FA is
// already enabled (409). The fresh secret lives ONLY in the in-memory
// pending map (single slot per user, 10-min TTL) until /2fa/enable.
func (a *Auth) Handle2FASetup(w http.ResponseWriter, r *http.Request) {
	u, ok := a.sessionUser(w, r)
	if !ok {
		return
	}
	if !a.Allow("2fa:setup:"+u.ID, totpConfigPerMin) {
		httpx.ErrorJSON(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := httpx.Decode(r, &body); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !u.HasPassword() {
		httpx.ErrorJSON(w, http.StatusBadRequest, "this account signs in through single sign-on; a local password is required")
		return
	}
	if enabled, err := a.totpEnabled(r.Context(), u.ID); err != nil {
		httpx.InternalError(w, r, err)
		return
	} else if enabled {
		httpx.ErrorJSON(w, http.StatusConflict, "two-factor authentication is already enabled")
		return
	}
	okPW, err := a.checkUserPassword(r, u, body.Password)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if !okPW {
		httpx.ErrorJSON(w, http.StatusUnauthorized, "password incorrect")
		return
	}
	secret, err := totp.GenerateSecret()
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if err := a.setPendingTOTP(u.ID, secret); err != nil {
		if err == errPendingFull {
			httpx.ErrorJSON(w, http.StatusTooManyRequests, "too many pending 2FA setups, try again later")
			return
		}
		httpx.InternalError(w, r, err)
		return
	}
	audit.Record(r.Context(), a.Store.DB, u, audit.Action2FASetup, "", a.RequestIP(r))
	httpx.JSON(w, http.StatusOK, map[string]string{
		"secret":      secret,
		"otpauth_uri": totp.OTPAuthURL(totpIssuer, u.Email, secret),
	})
}

// Handle2FAEnable POST /api/me/2fa/enable {code} — commits the pending
// secret after proving the authenticator works. A wrong code keeps the
// pending entry alive (retry within the TTL); success seals the secret to
// users.totp_secret and clears the slot.
func (a *Auth) Handle2FAEnable(w http.ResponseWriter, r *http.Request) {
	u, ok := a.sessionUser(w, r)
	if !ok {
		return
	}
	if !a.Allow("2fa:enable:"+u.ID, totpEnablePerMin) {
		httpx.ErrorJSON(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := httpx.Decode(r, &body); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	secret, ok := a.pendingTOTPSecret(u.ID)
	if !ok {
		httpx.ErrorJSON(w, http.StatusBadRequest, "no pending 2FA setup; call POST /api/me/2fa/setup first")
		return
	}
	// Validate against the pending secret WITHOUT the replay memory (the
	// feature is not committed yet; enable is throttled 10/min).
	if !totp.Validate(secret, body.Code, time.Now()) {
		audit.Record(r.Context(), a.Store.DB, u, audit.Action2FAEnable, "failed", a.RequestIP(r))
		httpx.ErrorJSON(w, http.StatusUnauthorized, "invalid code")
		return
	}
	if err := a.setTOTPSecret(r.Context(), u.ID, secret); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	a.clearPendingTOTP(u.ID)
	a.forgetTOTPState(u.ID)
	audit.Record(r.Context(), a.Store.DB, u, audit.Action2FAEnable, "", a.RequestIP(r))
	httpx.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Handle2FADisable POST /api/me/2fa/disable {password} — password-gated
// (an attacker with a stolen session still cannot turn 2FA off).
func (a *Auth) Handle2FADisable(w http.ResponseWriter, r *http.Request) {
	u, ok := a.sessionUser(w, r)
	if !ok {
		return
	}
	if !a.Allow("2fa:disable:"+u.ID, totpConfigPerMin) {
		httpx.ErrorJSON(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := httpx.Decode(r, &body); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if enabled, err := a.totpEnabled(r.Context(), u.ID); err != nil {
		httpx.InternalError(w, r, err)
		return
	} else if !enabled {
		httpx.ErrorJSON(w, http.StatusConflict, "two-factor authentication is not enabled")
		return
	}
	okPW, err := a.checkUserPassword(r, u, body.Password)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if !okPW {
		audit.Record(r.Context(), a.Store.DB, u, audit.Action2FADisable, "failed", a.RequestIP(r))
		httpx.ErrorJSON(w, http.StatusUnauthorized, "password incorrect")
		return
	}
	if err := a.clearTOTPSecret(r.Context(), u.ID); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	a.forgetTOTPState(u.ID)
	audit.Record(r.Context(), a.Store.DB, u, audit.Action2FADisable, "", a.RequestIP(r))
	httpx.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// HandleTOTPVerify POST /api/auth/totp {totp_token, code}.
// Public path (the caller has no session yet), so the token signature,
// 10-min expiry, single-use nonce and the ±window + monotonic-step code
// check all have to hold. On success the session is issued through the SAME
// finishLogin path (cookie + login.success audit) as a password login.
func (a *Auth) HandleTOTPVerify(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TOTPToken string `json:"totp_token"`
		Code      string `json:"code"`
	}
	if err := httpx.Decode(r, &body); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.TOTPToken == "" || body.Code == "" {
		httpx.ErrorJSON(w, http.StatusBadRequest, "totp_token and code are required")
		return
	}
	// 10/min per IP+token: one theft victim cannot be brute-forced while a
	// spray against other tokens keeps its own budget.
	limKey := "totp:" + a.RequestIP(r) + ":" + shortHMACKey(body.TOTPToken)
	if !a.Allow(limKey, totpVerifyPerMin) {
		httpx.ErrorJSON(w, http.StatusTooManyRequests, "too many verification attempts, try again later")
		return
	}
	fail := func(u *store.User, reason string) {
		if u != nil {
			audit.Record(r.Context(), a.Store.DB, u, audit.ActionTOTPVerifyFail, reason, a.RequestIP(r))
		} else {
			audit.RecordIdentity(r.Context(), a.Store.DB, "", "", audit.ActionTOTPVerifyFail, reason, a.RequestIP(r))
		}
		httpx.ErrorJSON(w, http.StatusUnauthorized, "invalid or expired verification token")
	}

	userID, nonce, err := a.parseTOTPToken(body.TOTPToken)
	if err != nil {
		fail(nil, "bad_token")
		return
	}
	u, err := a.Store.Users.GetByID(r.Context(), userID)
	if err != nil {
		fail(nil, "unknown_user")
		return
	}
	if !a.consumeTOTPCode(r.Context(), u.ID, body.Code) {
		fail(u, "bad_code")
		return
	}
	// Consume the token only on SUCCESS: an honest mistype may retry inside
	// the TTL (rate-limiter bounded), but the token can never buy two
	// sessions — the nonce goes into the used set right here.
	if !a.totpMarkNonceUsed(nonce) {
		fail(u, "replayed_token")
		return
	}
	audit.Record(r.Context(), a.Store.DB, u, audit.ActionTOTPVerifyOK, "", a.RequestIP(r))
	a.finishLogin(w, r, u, "totp")
}

// shortHMACKey squeezes a token into a stable rate-limit key fragment (the
// raw token is long and must never land in a map key readable elsewhere).
func shortHMACKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8])
}
