// Command totpcode prints the current 6-digit RFC 6238 TOTP code for a
// base32 secret, so bench/smoke_v2.sh can drive the 2FA enable/login flow
// without an interactive authenticator app.
//
// It is a deliberately INDEPENDENT implementation (HMAC-SHA1, 30s period,
// ±0 here — we always emit the current step) of bench/../internal/totp: a
// separate code path catches bugs that a shared library would hide. Stdlib
// only, per docs/FEATURES-V2.md §RAM discipline (go.mod is frozen anyway —
// bench code lives in the same module).
//
// Usage:
//
//	go run ./bench/totpcode <base32-secret>        # current code
//	go run ./bench/totpcode -t 59 <base32-secret>  # code at a fixed unix time
//	go run ./bench/totpcode -selftest              # RFC 6238 Appendix B vectors
package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

// RFC 6238 Appendix B test key: the ASCII string "12345678901234567890"
// (20 bytes = 160-bit SHA1 recommendation) base32-encoded.
//
//	rfcSecret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
//
// Expected 8-digit SHA-1 codes from the RFC table (we emit the low 6 digits,
// matching the app's frozen 6-digit Digits parameter):
//
//	T=59          → 94287082 → 287082
//	T=1111111109  → 07081804 → 081804
//	T=1111111111  → 14050471 → 050471
//	T=1234567890  → 89005924 → 005924
//	T=2000000000  → 69279037 → 279037
//	T=20000000000 → 65353130 → 353130
const rfcSecret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

const period = 30

// code computes the 6-digit code for one unix timestamp.
func code(secret string, unix int64) (string, error) {
	s := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '-':
			return -1 // tolerate pasted-formatted secrets
		}
		return r
	}, strings.ToUpper(strings.TrimSpace(secret)))
	if s == "" {
		return "", fmt.Errorf("empty secret")
	}
	if p := len(s) % 8; p != 0 {
		s += strings.Repeat("=", 8-p) // restore padding authenticator apps drop
	}
	key, err := base32.StdEncoding.DecodeString(s)
	if err != nil {
		return "", fmt.Errorf("invalid base32 secret: %w", err)
	}

	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(unix/period))
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)

	// RFC 4226 dynamic truncation → 31-bit int → mod 10^6, zero-padded.
	off := sum[len(sum)-1] & 0x0f
	bin := int32(sum[off]&0x7f)<<24 | int32(sum[off+1])<<16 |
		int32(sum[off+2])<<8 | int32(sum[off+3])
	return fmt.Sprintf("%06d", bin%1000000), nil
}

func selfTest() int {
	vectors := []struct {
		unix int64
		want string // 6-digit projection of the RFC 8-digit value
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
		{20000000000, "353130"},
	}
	bad := 0
	for _, v := range vectors {
		got, err := code(rfcSecret, v.unix)
		if err != nil {
			fmt.Printf("FAIL totp-vector T=%d: %v\n", v.unix, err)
			bad++
			continue
		}
		if got != v.want {
			fmt.Printf("FAIL totp-vector T=%d: got %s want %s\n", v.unix, got, v.want)
			bad++
			continue
		}
		fmt.Printf("PASS totp-vector T=%d → %s\n", v.unix, got)
	}
	return bad
}

func main() {
	tFlag := flag.Int64("t", -1, "unix timestamp to compute for (default: now)")
	self := flag.Bool("selftest", false, "run embedded RFC 6238 Appendix B vectors and exit")
	flag.Parse()

	if *self {
		if n := selfTest(); n > 0 {
			fmt.Printf("totpcode: %d vector(s) FAILED\n", n)
			os.Exit(1)
		}
		fmt.Println("totpcode: all RFC 6238 vectors PASS")
		return
	}

	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: totpcode [-t unix] [-selftest] <base32-secret>")
		os.Exit(2)
	}
	now := time.Now().Unix()
	if *tFlag >= 0 {
		now = *tFlag
	}
	c, err := code(flag.Arg(0), now)
	if err != nil {
		fmt.Fprintln(os.Stderr, "totpcode:", err)
		os.Exit(1)
	}
	fmt.Println(c)
}
