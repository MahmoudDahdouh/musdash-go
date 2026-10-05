package auth

import (
	"strings"
	"testing"
	"time"
)

// The key of RFC 6238's test vectors: the ASCII bytes "12345678901234567890".
const rfcKey = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

func TestTOTPMatchesRFC6238(t *testing.T) {
	// The RFC lists eight-digit codes; six digits are their last six.
	for unix, want := range map[int64]string{
		59:          "287082",
		1111111109:  "081804",
		1111111111:  "050471",
		1234567890:  "005924",
		2000000000:  "279037",
		20000000000: "353130",
	} {
		if got := TOTPNow(rfcKey, time.Unix(unix, 0)); got != want {
			t.Errorf("at %d: %s, want %s", unix, got, want)
		}
		step, ok := TOTPCheck(rfcKey, want, time.Unix(unix, 0), 0)
		if !ok || step != unix/30 {
			t.Errorf("at %d: the right code was refused (step %d)", unix, step)
		}
	}
}

func TestTOTPWindowAndReuse(t *testing.T) {
	now := time.Unix(1700000000, 0)
	code := TOTPNow(rfcKey, now)

	// One step either side is accepted, two are not.
	for _, off := range []time.Duration{-30 * time.Second, 0, 30 * time.Second} {
		if _, ok := TOTPCheck(rfcKey, code, now.Add(off), 0); !ok {
			t.Errorf("a code %v away was refused", off)
		}
	}
	for _, off := range []time.Duration{-61 * time.Second, 61 * time.Second} {
		if _, ok := TOTPCheck(rfcKey, code, now.Add(off), 0); ok {
			t.Errorf("a code %v away was accepted", off)
		}
	}

	// A code is not accepted for the step it was used at, or an earlier one.
	step, ok := TOTPCheck(rfcKey, code, now, 0)
	if !ok {
		t.Fatal("refused")
	}
	if _, ok := TOTPCheck(rfcKey, code, now, step); ok {
		t.Fatal("a used code was accepted again")
	}
	if _, ok := TOTPCheck(rfcKey, TOTPNow(rfcKey, now.Add(-30*time.Second)), now, step); ok {
		t.Fatal("the code before a used one was accepted")
	}
	if _, ok := TOTPCheck(rfcKey, TOTPNow(rfcKey, now.Add(30*time.Second)), now, step); !ok {
		t.Fatal("the next code must still work")
	}

	// Shapes that are never right.
	for _, bad := range []string{"", "12345", "1234567", "abcdef", code + "0", "-" + code[1:]} {
		if _, ok := TOTPCheck(rfcKey, bad, now, 0); ok && bad != code {
			t.Errorf("%q was accepted", bad)
		}
	}
	if _, ok := TOTPCheck("not base32 !", code, now, 0); ok {
		t.Fatal("a broken key accepted a code")
	}
	if _, ok := TOTPCheck("", "000000", now, 0); ok {
		t.Fatal("an empty key accepted a code")
	}
	// As people type them.
	if _, ok := TOTPCheck(strings.ToLower(rfcKey), " "+code[:3]+" "+code[3:]+" ", now, 0); !ok {
		t.Fatal("a code with spaces was refused")
	}
}

func TestTOTPKeyAndLink(t *testing.T) {
	a, b := NewTOTPKey(), NewTOTPKey()
	if a == b || len(a) != 32 {
		t.Fatalf("keys: %q %q", a, b)
	}
	if _, ok := TOTPCheck(a, TOTPNow(a, time.Now()), time.Now(), 0); !ok {
		t.Fatal("a new key does not check its own code")
	}
	link := TOTPLink("musdash", "sam+test@example.com", a)
	if !strings.HasPrefix(link, "otpauth://totp/musdash:sam+test@example.com?") && !strings.HasPrefix(link, "otpauth://totp/musdash:sam%2Btest@example.com?") {
		t.Fatalf("link: %s", link)
	}
	for _, part := range []string{"secret=" + a, "issuer=musdash", "digits=6", "period=30", "algorithm=SHA1"} {
		if !strings.Contains(link, part) {
			t.Errorf("link lacks %s: %s", part, link)
		}
	}
}

func TestRecoveryCodes(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		c := NewRecoveryCode()
		if len(c) != 14 || c[4] != '-' || c[9] != '-' || seen[c] {
			t.Fatalf("code %q", c)
		}
		seen[c] = true
		if IsTOTPCode(c) {
			t.Fatalf("%q looks like an app's code", c)
		}
		if got := NormalRecoveryCode("  " + strings.ToUpper(c) + " "); got != strings.ReplaceAll(c, "-", "") {
			t.Fatalf("normalised %q", got)
		}
	}
	if !IsTOTPCode(" 123 456 ") || IsTOTPCode("12345a") || IsTOTPCode("1234567") {
		t.Fatal("IsTOTPCode")
	}
}
