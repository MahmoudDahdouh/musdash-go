package source

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
)

// Sign returns the X-Hub-Signature-256 value for a body. Tests and the
// documentation's curl example use it.
func Sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Push is what a push webhook says changed.
type Push struct {
	Repo string // "acme/shop"; empty when the event names no repository
	// Branch is the pushed branch. It is empty for anything that should not
	// start a deployment: a tag push, a deleted branch, another kind of
	// event, a body that is not JSON.
	Branch string
	Commit string // the new head commit
}

// ReadPush reads a webhook body, checking it against the
// X-Hub-Signature-256 header ("sha256=<hex>") with the shared secret.
// signed is false when the signature does not match, and then the Push is
// empty. An empty secret verifies nothing: a webhook with no secret
// configured is refused, not trusted.
//
// The body is not collected in memory. It is walked token by token while
// the same bytes feed the MAC, and only the few values a deployment needs
// are kept, so the memory used is bounded by the body's largest single
// value rather than by its size. The caller bounds the body itself.
func ReadPush(body io.Reader, secret []byte, header string) (p Push, signed bool) {
	given, hasPrefix := strings.CutPrefix(header, "sha256=")
	want, err := hex.DecodeString(given)
	if len(secret) == 0 || !hasPrefix || err != nil || len(want) != sha256.Size {
		return Push{}, false
	}
	mac := hmac.New(sha256.New, secret)
	tee := io.TeeReader(body, mac)
	ev, decodeErr := decodePush(json.NewDecoder(tee))
	// Whatever the decoder did not consume still belongs to the signed body.
	if _, err := io.Copy(io.Discard, tee); err != nil {
		return Push{}, false
	}
	if !hmac.Equal(mac.Sum(nil), want) {
		return Push{}, false
	}
	if decodeErr != nil {
		return Push{}, true
	}
	return ev.push(), true
}

type pushEvent struct {
	ref, after, repo string
	deleted          bool
}

func (ev pushEvent) push() Push {
	p := Push{Repo: ev.repo}
	branch, isBranch := strings.CutPrefix(ev.ref, "refs/heads/")
	// A deleted ref is also signalled by an all-zero "after".
	if !isBranch || ev.deleted || strings.Trim(ev.after, "0") == "" {
		return p
	}
	p.Branch, p.Commit = branch, ev.after
	return p
}

// decodePush reads the top level of a push event, skipping everything but
// ref, after, deleted and repository.full_name.
func decodePush(dec *json.Decoder) (pushEvent, error) {
	var ev pushEvent
	err := eachKey(dec, func(key string) error {
		switch key {
		case "ref":
			return readValue(dec, &ev.ref)
		case "after":
			return readValue(dec, &ev.after)
		case "deleted":
			return readValue(dec, &ev.deleted)
		case "repository":
			return eachKey(dec, func(key string) error {
				if key == "full_name" {
					return readValue(dec, &ev.repo)
				}
				return readValue(dec, nil)
			})
		}
		return readValue(dec, nil)
	})
	return ev, err
}

// eachKey reads one JSON value. For an object it calls fn for every key,
// and fn must consume that key's value; any other value is skipped.
func eachKey(dec *json.Decoder, fn func(key string) error) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok != json.Delim('{') {
		return skipRest(dec, tok)
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := tok.(string)
		if err := fn(key); err != nil {
			return err
		}
	}
	_, err = dec.Token() // the closing brace
	return err
}

// readValue reads one JSON value. A string or boolean is stored in dst when
// dst points at that type; anything else, of any size, is skipped.
func readValue(dec *json.Decoder, dst any) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch d := dst.(type) {
	case *string:
		if s, ok := tok.(string); ok {
			*d = s
		}
	case *bool:
		if b, ok := tok.(bool); ok {
			*d = b
		}
	}
	return skipRest(dec, tok)
}

// skipRest consumes the remainder of the value that began with tok.
func skipRest(dec *json.Decoder, tok json.Token) error {
	if d, ok := tok.(json.Delim); !ok || (d != '{' && d != '[') {
		return nil
	}
	for depth := 1; depth > 0; {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			if d == '{' || d == '[' {
				depth++
			} else {
				depth--
			}
		}
	}
	return nil
}
