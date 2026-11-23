package totp

import (
	"strings"
	"testing"
	"time"
)

// rfcSecret is the ASCII string "12345678901234567890" (the RFC 4226/6238
// test key) in base32.
const rfcSecret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

// TestRFCVectors checks against the RFC 6238 appendix B table (SHA-1 row).
// The RFC prints 8-digit codes; ours are 6 digits = the low-order 6 digits
// of the same truncated value (mod 10^6).
func TestRFCVectors(t *testing.T) {
	vectors := []struct {
		unix int64
		want string // 8-digit RFC value
		code string // 6-digit projection we must emit
	}{
		{59, "94287082", "287082"},
		{1111111109, "07081804", "081804"},
		{1111111111, "14050471", "050471"},
		{1234567890, "89005924", "005924"},
		{2000000000, "69279037", "279037"},
		{20000000000, "65353130", "353130"},
	}
	for _, v := range vectors {
		now := time.Unix(v.unix, 0)
		got, err := Code(rfcSecret, now)
		if err != nil {
			t.Fatalf("Code(%d): %v", v.unix, err)
		}
		if got != v.code {
			t.Errorf("T=%d: Code = %q, want %q (RFC 8-digit %s)", v.unix, got, v.code, v.want)
		}
		if !Validate(rfcSecret, got, now) {
			t.Errorf("T=%d: Validate rejected its own code", v.unix)
		}
	}
}

func TestValidateWindow(t *testing.T) {
	now := time.Unix(1111111111, 0) // step 37037037 (RFC vector T)
	cur, err := Code(rfcSecret, now)
	if err != nil {
		t.Fatal(err)
	}
	prev, _ := Code(rfcSecret, now.Add(-Period*time.Second))
	next, _ := Code(rfcSecret, now.Add(Period*time.Second))
	far := time.Unix(now.Unix()+2*Period, 0)
	farPrev, _ := Code(rfcSecret, now.Add(-2*Period*time.Second))

	tests := []struct {
		name   string
		code   string
		at     time.Time
		want   bool
		stepOK bool
	}{
		{"current", cur, now, true, true},
		{"one step back accepted", prev, now, true, false},
		{"one step forward accepted", next, now, true, false},
		{"two steps back rejected", farPrev, now, false, false},
		{"future code beyond window rejected", cur, far, false, false},
		{"wrong code rejected", "000000", now, false, false}, // RFC key never yields 000000 here
		{"garbage rejected", "abcdef", now, false, false},
		{"short code rejected", "12345", now, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			step, ok := ValidateStep(rfcSecret, tc.code, tc.at)
			if ok != tc.want {
				t.Fatalf("ValidateStep(%q, T=%d) ok = %v, want %v", tc.code, tc.at.Unix(), ok, tc.want)
			}
			if ok && tc.stepOK && step != now.Unix()/Period {
				t.Errorf("step = %d, want %d", step, now.Unix()/Period)
			}
			if got := Validate(rfcSecret, tc.code, tc.at); got != ok {
				t.Errorf("Validate/ValidateStep disagree: %v vs %v", got, ok)
			}
		})
	}
}

func TestValidateToleratesFormatting(t *testing.T) {
	now := time.Unix(1111111111, 0)
	// lower-case, spaces and dashes must all decode identically.
	if !Validate(strings.ToLower(rfcSecret), "05 04-71", now) {
		t.Error("lower-case secret / spaced code rejected")
	}
	// invalid base32 (digit 1/0 are not in the alphabet) fails closed.
	if Validate("!!!not-base32!!!", "050471", now) {
		t.Error("invalid secret accepted")
	}
}

func TestGenerateSecretRoundTrips(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		s, err := GenerateSecret()
		if err != nil {
			t.Fatal(err)
		}
		if len(s) != 32 { // 20 bytes → 32 unpadded base32 chars
			t.Fatalf("secret %q: len = %d, want 32", s, len(s))
		}
		if seen[s] {
			t.Fatalf("duplicate secret generated: %q", s)
		}
		seen[s] = true
		if _, err := Code(s, time.Now()); err != nil {
			t.Fatalf("generated secret does not decode: %v", err)
		}
	}
}

func TestOTPAuthURL(t *testing.T) {
	got := OTPAuthURL("ChatGo", "alice@example.com", "JBSWY3DPEHPK3PXP")
	want := "otpauth://totp/ChatGo:alice@example.com?secret=JBSWY3DPEHPK3PXP&issuer=ChatGo"
	if got != want {
		t.Errorf("OTPAuthURL = %q\nwant       %q", got, want)
	}
	// Account names needing escapes must not break the URI structure.
	got = OTPAuthURL("Chat Go", "a b@example.com", "ABC")
	for _, frag := range []string{"otpauth://totp/", "secret=ABC", "issuer=Chat+Go"} {
		if !strings.Contains(got, frag) {
			t.Errorf("OTPAuthURL %q missing %q", got, frag)
		}
	}
}
