package source

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// VerifySignature checks a webhook body against the X-Hub-Signature-256
// header ("sha256=<hex>") using the shared secret. An empty secret verifies
// nothing: a webhook with no secret configured is refused, not trusted.
func VerifySignature(secret, body []byte, header string) bool {
	if len(secret) == 0 {
		return false
	}
	given, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	want, err := hex.DecodeString(given)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}

// Sign returns the X-Hub-Signature-256 value for a body. Tests and the
// documentation's curl example use it.
func Sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Push is what a push webhook says changed.
type Push struct {
	Repo   string // "acme/shop"
	Branch string // "main"
	Commit string // the new head commit
}

// ParsePush reads a GitHub (or GitHub-compatible) push event. It reports
// false for anything that should not start a deployment: a tag push, a
// deleted branch, or a body that is not a push event.
func ParsePush(body []byte) (Push, bool) {
	var ev struct {
		Ref        string `json:"ref"`
		After      string `json:"after"`
		Deleted    bool   `json:"deleted"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		return Push{}, false
	}
	branch, isBranch := strings.CutPrefix(ev.Ref, "refs/heads/")
	if !isBranch || branch == "" || ev.Deleted || ev.Repository.FullName == "" {
		return Push{}, false
	}
	// A deleted ref is also signalled by an all-zero "after".
	if strings.Trim(ev.After, "0") == "" {
		return Push{}, false
	}
	return Push{Repo: ev.Repository.FullName, Branch: branch, Commit: ev.After}, true
}
