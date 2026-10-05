package source

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
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

// PullRequest is what a pull request webhook says happened.
type PullRequest struct {
	// Repo is the repository the pull request was opened in, "acme/shop".
	Repo string
	// Number is the pull request's number. Zero means the event was not
	// about a pull request, or could not be read.
	Number int
	// Action is what happened: "opened", "synchronize" (new commits),
	// "reopened", "closed", and others that change nothing here.
	Action string
	// Branch and Commit are the head of the pull request: what would be
	// merged. HeadRepo is the repository that branch is in, which is
	// another one than Repo when the pull request comes from a fork.
	Branch   string
	Commit   string
	HeadRepo string
	// BaseBranch is the branch the pull request would be merged into.
	BaseBranch string
}

// FromFork reports whether the pull request's code is in another
// repository than the one it was opened in, or in one that is not known
// (a fork that was deleted). Such code was written by someone who may have
// no right to push to the repository, and is not run.
func (p PullRequest) FromFork() bool {
	return p.HeadRepo == "" || !strings.EqualFold(p.HeadRepo, p.Repo)
}

// Event is what a webhook body says, read as a push and as a pull request.
// The body does not say which of the two it is (a header does, and the
// header is not signed), so both are read and the caller takes the one the
// body turned out to be: a push has a Branch, a pull request a Number.
type Event struct {
	Push Push
	PR   PullRequest
}

// ReadPush is ReadEvent for a caller that only acts on pushes.
func ReadPush(body io.Reader, secret []byte, header string) (p Push, signed bool) {
	ev, signed := ReadEvent(body, secret, header)
	return ev.Push, signed
}

// ReadEvent reads a webhook body, checking it against the
// X-Hub-Signature-256 header ("sha256=<hex>") with the shared secret.
// signed is false when the signature does not match, and then the Event is
// empty. An empty secret verifies nothing: a webhook with no secret
// configured is refused, not trusted.
//
// The body is not collected in memory. It is walked token by token while
// the same bytes feed the MAC, and only the few values a deployment needs
// are kept, so the memory used is bounded by the body's largest single
// value rather than by its size. The caller bounds the body itself.
func ReadEvent(body io.Reader, secret []byte, header string) (e Event, signed bool) {
	given, hasPrefix := strings.CutPrefix(header, "sha256=")
	want, err := hex.DecodeString(given)
	if len(secret) == 0 || !hasPrefix || err != nil || len(want) != sha256.Size {
		return Event{}, false
	}
	mac := hmac.New(sha256.New, secret)
	tee := io.TeeReader(body, mac)
	ev, decodeErr := decodeEvent(json.NewDecoder(tee))
	// Whatever the decoder did not consume still belongs to the signed body.
	if _, err := io.Copy(io.Discard, tee); err != nil {
		return Event{}, false
	}
	if !hmac.Equal(mac.Sum(nil), want) {
		return Event{}, false
	}
	if decodeErr != nil {
		return Event{}, true
	}
	return Event{Push: ev.push(), PR: ev.pullRequest()}, true
}

type pushEvent struct {
	ref, after, repo string
	deleted          bool

	// Of a pull request event.
	action                              string
	number                              int
	hasPR                               bool
	headRef, headSHA, headRepo, baseRef string
}

// pullRequest is the event read as one about a pull request. Without the
// "pull_request" object it is not one, whatever else the body holds.
func (ev pushEvent) pullRequest() PullRequest {
	if !ev.hasPR || ev.number <= 0 {
		return PullRequest{}
	}
	return PullRequest{Repo: ev.repo, Number: ev.number, Action: ev.action,
		Branch: ev.headRef, Commit: ev.headSHA, HeadRepo: ev.headRepo, BaseBranch: ev.baseRef}
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

// decodeEvent reads the top level of a push or pull request event, skipping
// everything but the values named here.
func decodeEvent(dec *json.Decoder) (pushEvent, error) {
	var ev pushEvent
	// The repository of one end of a pull request: {"ref", "sha", "repo":
	// {"full_name"}}. A deleted fork has "repo": null.
	side := func(ref, sha, repo *string) error {
		return eachKey(dec, func(key string) error {
			switch key {
			case "ref":
				return readValue(dec, ref)
			case "sha":
				return readValue(dec, sha)
			case "repo":
				return eachKey(dec, func(key string) error {
					if key == "full_name" {
						return readValue(dec, repo)
					}
					return readValue(dec, nil)
				})
			}
			return readValue(dec, nil)
		})
	}
	err := eachKey(dec, func(key string) error {
		switch key {
		case "ref":
			return readValue(dec, &ev.ref)
		case "after":
			return readValue(dec, &ev.after)
		case "deleted":
			return readValue(dec, &ev.deleted)
		case "action":
			return readValue(dec, &ev.action)
		case "number":
			return readValue(dec, &ev.number)
		case "pull_request":
			ev.hasPR = true
			var ignored string
			return eachKey(dec, func(key string) error {
				switch key {
				case "head":
					return side(&ev.headRef, &ev.headSHA, &ev.headRepo)
				case "base":
					return side(&ev.baseRef, &ignored, &ignored)
				}
				return readValue(dec, nil)
			})
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
	case *int:
		// A whole number that fits; anything else leaves dst as it was.
		if f, ok := tok.(float64); ok && f == math.Trunc(f) && f >= 0 && f < 1<<31 {
			*d = int(f)
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
