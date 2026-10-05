package auth

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"net/url"
	"strings"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
)

// The second step is TOTP as authenticator apps speak it (RFC 6238):
// HMAC-SHA1 over the number of thirty-second steps since 1970, six digits.
// SHA-1 here is not a weakness: HMAC does not rest on its collision
// resistance, and it is the one variant every app implements.
const (
	totpPeriod = 30
	totpDigits = 6
	// totpWindow is how many steps either side of now are accepted, for a
	// phone whose clock is a little off and a code typed at the last second.
	totpWindow = 1
)

var totpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPKey returns a new 160-bit key in base32, the form apps take.
func NewTOTPKey() string {
	return totpEncoding.EncodeToString(secret.RandomBytes(20))
}

// TOTPLink is the otpauth:// address of a key, which an authenticator app
// or a password manager on the same device opens.
func TOTPLink(issuer, account, key string) string {
	q := url.Values{"secret": {key}, "issuer": {issuer}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + q.Encode()
}

// totpAt is the code of one step.
func totpAt(key []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	// Dynamic truncation: the low four bits of the last byte say where the
	// four bytes of the number start.
	at := sum[len(sum)-1] & 0x0f
	n := binary.BigEndian.Uint32(sum[at:at+4]) & 0x7fffffff
	code := make([]byte, totpDigits)
	for i := totpDigits - 1; i >= 0; i-- {
		code[i] = byte('0' + n%10)
		n /= 10
	}
	return string(code)
}

// TOTPCheck reports whether code is right for key at the given time, and
// which step it belongs to. Only a step later than after is accepted: the
// caller stores the step of each code it takes, so a code works once.
func TOTPCheck(key, code string, now time.Time, after int64) (step int64, ok bool) {
	raw, err := totpEncoding.DecodeString(strings.ToUpper(strings.TrimSpace(key)))
	if err != nil || len(raw) == 0 {
		return 0, false
	}
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits {
		return 0, false
	}
	current := now.Unix() / totpPeriod
	// Every step of the window is compared, whichever matches.
	for s := current - totpWindow; s <= current+totpWindow; s++ {
		if subtle.ConstantTimeCompare([]byte(totpAt(raw, s)), []byte(code)) == 1 && s > after && !ok {
			step, ok = s, true
		}
	}
	return step, ok
}

// TOTPNow is the code an app would show at a time. Tests and the command
// line use it; signing in never does.
func TOTPNow(key string, now time.Time) string {
	raw, err := totpEncoding.DecodeString(strings.ToUpper(strings.TrimSpace(key)))
	if err != nil {
		return ""
	}
	return totpAt(raw, now.Unix()/totpPeriod)
}

// RecoveryCodes is how many codes for a lost phone a person is given.
const RecoveryCodes = 10

var recoveryEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// NewRecoveryCode returns a code such as "k3fa-x2mq-7hzb": twelve
// characters, sixty bits. That is too many to guess, which is why a plain
// SHA-256 of it can be stored: there is nothing for a work factor to slow.
func NewRecoveryCode() string {
	s := recoveryEncoding.EncodeToString(secret.RandomBytes(8))[:12]
	return s[:4] + "-" + s[4:8] + "-" + s[8:]
}

// NormalRecoveryCode brings a typed recovery code to the form it was made
// in, without its dashes: what is hashed and compared.
func NormalRecoveryCode(typed string) string {
	typed = strings.ToLower(strings.TrimSpace(typed))
	typed = strings.ReplaceAll(typed, "-", "")
	return strings.ReplaceAll(typed, " ", "")
}

// IsTOTPCode reports whether what was typed has the shape of a code from
// an app rather than a recovery code.
func IsTOTPCode(typed string) bool {
	typed = strings.ReplaceAll(strings.TrimSpace(typed), " ", "")
	if len(typed) != totpDigits {
		return false
	}
	for _, c := range typed {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
