// Package totp implements RFC 6238 TOTP (HMAC-SHA1, 30-second steps,
// 6 digits, ±1-step skew window) on the RFC 4226 HOTP core, plus otpauth://
// URI building. Stdlib only (crypto/hmac, crypto/sha1, encoding/base32) per
// docs/FEATURES-V2.md §RAM discipline: NO new dependencies.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Frozen parameters (contract: SHA1 / 30s / 6 digits / ±1 window).
const (
	// Digits is the OTP length.
	Digits = 6
	// Period is the step duration in seconds.
	Period = 30
	// Window is the number of ±steps accepted around "now" (RFC 6238
	// recommends allowing for transmission delay; replay control lives in
	// the caller's last-used-step memory, not here).
	Window = 1
	// secretBytes is the RFC 6238 SHA1 recommendation (160 bits, matching
	// the HMAC-SHA1 output size); "12345678901234567890" from the RFC.
	secretBytes = 20
)

// ErrBadSecret reports a secret that is not valid base32.
var ErrBadSecret = errors.New("totp: invalid base32 secret")

// GenerateSecret returns a fresh 160-bit shared secret, base32-encoded
// without padding (the format authenticator apps and otpauth:// expect).
func GenerateSecret() (string, error) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("totp: generate secret: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), nil
}

// DecodeSecret accepts the secret as users paste it (spaces, dashes, lower
// case, missing "=" padding all tolerated) and returns the raw key bytes.
func DecodeSecret(secret string) ([]byte, error) {
	s := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '-', '\u00a0':
			return -1 // drop separators authenticators sometimes insert
		}
		return r
	}, strings.ToUpper(strings.TrimSpace(secret)))
	if s == "" {
		return nil, ErrBadSecret
	}
	if p := len(s) % 8; p != 0 {
		s += strings.Repeat("=", 8-p) // restore base32 padding
	}
	key, err := base32.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, ErrBadSecret
	}
	if len(key) == 0 {
		return nil, ErrBadSecret
	}
	return key, nil
}

// hotp is the RFC 4226 dynamic-truncation code for one counter value.
func hotp(key []byte, counter int64) string {
	var buf [8]byte
	// #nosec G115 -- counter is unixSeconds/30 (RFC 6238): non-negative for every
	// real timestamp; negative input would signal a clock before 1970.
	binary.BigEndian.PutUint64(buf[:], uint64(counter))
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)

	off := sum[len(sum)-1] & 0x0f
	val := uint32(sum[off]&0x7f)<<24 |
		uint32(sum[off+1])<<16 |
		uint32(sum[off+2])<<8 |
		uint32(sum[off+3])
	return fmt.Sprintf("%0*d", Digits, val%1000000)
}

// Code returns the 6-digit TOTP for secret at time now.
func Code(secret string, now time.Time) (string, error) {
	key, err := DecodeSecret(secret)
	if err != nil {
		return "", err
	}
	return hotp(key, now.Unix()/Period), nil
}

// Validate reports whether code is current for secret within ±Window steps.
func Validate(secret, code string, now time.Time) bool {
	_, ok := ValidateStep(secret, code, now)
	return ok
}

// ValidateStep is Validate plus the matching step number, so callers can
// enforce monotonic consumption (remember-last-used-step replay guard,
// docs/FEATURES-V2.md §BE-SEC). Returns (0, false) when nothing matches.
// Comparison is constant-time per candidate step.
func ValidateStep(secret, code string, now time.Time) (int64, bool) {
	key, err := DecodeSecret(secret)
	if err != nil {
		return 0, false
	}
	want := normalizeCode(code)
	if len(want) != Digits {
		return 0, false
	}
	cur := now.Unix() / Period
	for off := -int64(Window); off <= Window; off++ {
		step := cur + off
		if subtle.ConstantTimeCompare([]byte(hotp(key, step)), want) == 1 {
			return step, true
		}
	}
	return 0, false
}

func normalizeCode(code string) []byte {
	c := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '\t':
			return -1
		}
		return r
	}, strings.TrimSpace(code))
	return []byte(c)
}

// OTPAuthURL builds the provisioning URI every authenticator app understands:
// otpauth://totp/{issuer}:{account}?secret=…&issuer=… . Only manual entry +
// this URI are shown (QR rendering is out of scope, docs/FEATURES-V2.md).
func OTPAuthURL(issuer, account, secret string) string {
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) +
		"?secret=" + url.QueryEscape(secret) + "&issuer=" + url.QueryEscape(issuer)
}
