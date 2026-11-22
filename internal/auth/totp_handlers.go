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
// caller has no session yet. That endpoint carries the C6 throttle wall
// (coarse per-IP pre-cap → token parse → per-USERID bucket) and burns the
// challenge nonce on EVERY attempt, so one minted token buys exactly one
// code guess and a sustained attack must re-buy guesses through the
// password login limiter (5/min/IP, auth.go loginPerMin).

import (
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
// Probe ordering (L1-L1): the body decodes BEFORE the limiter bucket (a
// bodiless POST used to burn setup budget) and the password is checked
// BEFORE the enrollment probe (409 must not answer before 401).
func (a *Auth) Handle2FASetup(w http.ResponseWriter, r *http.Request) {
	u, ok := a.sessionUser(w, r)
	if !ok {
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := httpx.Decode(r, &body); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !a.Allow("2fa:setup:"+u.ID, totpConfigPerMin) {
		httpx.ErrorJSON(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	if !u.HasPassword() {
		httpx.ErrorJSON(w, http.StatusBadRequest, "this account signs in through single sign-on; a local password is required")
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
	if enabled, err := a.totpEnabled(r.Context(), u.ID); err != nil {
		httpx.InternalError(w, r, err)
		return
	} else if enabled {
		httpx.ErrorJSON(w, http.StatusConflict, "two-factor authentication is already enabled")
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
	var body struct {
		Code string `json:"code"`
	}
	if err := httpx.Decode(r, &body); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !a.Allow("2fa:enable:"+u.ID, totpEnablePerMin) {
		httpx.ErrorJSON(w, http.StatusTooManyRequests, "too many attempts, try again later")
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
// (an attacker with a stolen session still cannot turn 2FA off). Ordering
// matches setup (L1-L1): decode before the bucket, password before the
// enrollment-state 409.
func (a *Auth) Handle2FADisable(w http.ResponseWriter, r *http.Request) {
	u, ok := a.sessionUser(w, r)
	if !ok {
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := httpx.Decode(r, &body); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !a.Allow("2fa:disable:"+u.ID, totpConfigPerMin) {
		httpx.ErrorJSON(w, http.StatusTooManyRequests, "too many attempts, try again later")
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
	if enabled, err := a.totpEnabled(r.Context(), u.ID); err != nil {
		httpx.InternalError(w, r, err)
		return
	} else if !enabled {
		httpx.ErrorJSON(w, http.StatusConflict, "two-factor authentication is not enabled")
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
// 10-min expiry, code window and monotonic step all have to hold. On
// success the session is issued through the SAME finishLogin path (cookie +
// login.success audit) as a password login.
//
// Brute-force wall (C6 / L1-H1 + L1-M1 + L6-H1, NIST SP 800-63B §5.2.2) —
// checks ordered cheapest-attacker-cost first:
//
//  1. coarse per-IP pre-cap (30/min) before ANY parse or attacker-keyed
//     state: garbage-token floods get a 429 without minting buckets or
//     audit rows (pre-fix: 40k req/5s, zero 429, +17.5 MB/5s);
//  2. parse (HMAC verify) — structurally invalid tokens: 401, NO audit
//     insert (they were the unauthenticated DB-write amplifier);
//  3. per-USERID bucket (10/min): the aggregate wall a token spray can
//     never widen, however many tokens or IPs the attacker holds;
//  4. challenge-kill check: a consecutive-failure lockout has invalidated
//     everything minted before it;
//  5. BURN THE NONCE ON EVERY ATTEMPT, pass or fail — one minted challenge
//     = one code guess. A wrong answer kills the token, so every further
//     guess costs a full POST /api/auth/login, itself throttled 5/min/IP
//     (loginPerMin, auth.go). Pre-fix a token survived its whole 10-min
//     TTL (~110 tries); the measured one-IP budget of ~119 guesses/min
//     collapses to at most 5/min/IP — one per password login.
//
// 15 consecutive bad codes on live challenges (noteTOTPFailure) invalidate
// every outstanding challenge for the user and land an audit alert.
func (a *Auth) HandleTOTPVerify(w http.ResponseWriter, r *http.Request) {
	// 1. Coarse per-IP pre-cap — before body parse and before any state
	// keyed on attacker content (L1-H1 memory-DoS guard).
	if !a.Allow("totp:ip:"+a.RequestIP(r), totpVerifyIPPerMin) {
		httpx.ErrorJSON(w, http.StatusTooManyRequests, "too many verification attempts, try again later")
		return
	}
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
	// 2. Parse first so the limiter below can key on the TARGET USER, and so
	// forged/expired garbage never reaches audit or any per-token state.
	userID, nonce, expiresAt, err := a.parseTOTPToken(body.TOTPToken)
	if err != nil {
		httpx.ErrorJSON(w, http.StatusUnauthorized, "invalid or expired verification token")
		return
	}
	u, err := a.Store.Users.GetByID(r.Context(), userID)
	if err != nil {
		httpx.ErrorJSON(w, http.StatusUnauthorized, "invalid or expired verification token")
		return
	}
	// 3. Per-victim aggregate wall, independent of source IP and token
	// count: at most totpVerifyPerMin code checks/min can EVER reach this
	// user's secret.
	if !a.Allow("totp:uid:"+u.ID, totpVerifyPerMin) {
		httpx.ErrorJSON(w, http.StatusTooManyRequests, "too many verification attempts, try again later")
		return
	}
	fail := func(reason string) {
		audit.Record(r.Context(), a.Store.DB, u, audit.ActionTOTPVerifyFail, reason, a.RequestIP(r))
		httpx.ErrorJSON(w, http.StatusUnauthorized, "invalid or expired verification token")
	}
	// 4. Dead by lockout: minted at or before a consecutive-failure trip.
	// Not counted toward the counter (it cannot buy a guess anyway).
	if a.totpChallengeKilled(u.ID, totpTokenIssuedAt(expiresAt)) {
		fail("superseded")
		return
	}
	// 5. One-use nonce, burned on EVERY attempt (pass or fail). A repeat on
	// the same token is a replay of a spent challenge, not a new guess, so
	// it does not feed the failure counter either.
	if !a.totpMarkNonceUsed(nonce) {
		fail("replayed_token")
		return
	}
	// Constant-time ±window + monotonic-step check — semantics unchanged
	// (Lens 8 "exemplary" verdict preserved).
	if !a.consumeTOTPCode(r.Context(), u.ID, body.Code) {
		if a.noteTOTPFailure(u.ID) {
			// Threshold reached: every challenge minted up to now is dead
			// and the alert lands in the trail admins read.
			audit.Record(r.Context(), a.Store.DB, u, audit.ActionTOTPVerifyFail, "lockout", a.RequestIP(r))
		}
		fail("bad_code")
		return
	}
	a.clearTOTPVerifyState(u.ID) // success ends any failure streak
	audit.Record(r.Context(), a.Store.DB, u, audit.ActionTOTPVerifyOK, "", a.RequestIP(r))
	a.finishLogin(w, r, u, "totp")
}
