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
	// IfChanged is set when the event says only that the pull request is
	// open, not what happened to it (Bitbucket says that in a header, and
	// headers are not signed). Such an event deploys only when Commit is
	// not what was deployed last: a comment must not start a build.
	IfChanged bool
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
//
// Nor is it asked which host sent it, for the same reason. GitHub's shape
// (Gitea and Forgejo use it too), GitLab's and Bitbucket's are all looked
// for in the one pass over the body.
type Event struct {
	// Push is the first branch the event says was pushed.
	Push Push
	// Pushes are all of them: one push to Bitbucket can move several
	// branches. At most maxPushes are kept.
	Pushes []Push
	PR     PullRequest
}

// maxPushes is how many branches of one event are looked at.
const maxPushes = 16

// Proof is what a webhook request offers to show that its sender knows the
// secret. Hosts differ in how they do it; a request is accepted when any
// one of them holds.
type Proof struct {
	// Hub256 is X-Hub-Signature-256, "sha256=<hex>": the HMAC-SHA256 of
	// the body. GitHub, Gitea and Forgejo send it.
	Hub256 string
	// Hub is X-Hub-Signature in the same form, as Bitbucket sends it.
	// GitHub's own value of this header is a SHA-1 and is not accepted.
	Hub string
	// Bare is the same HMAC as bare hex: X-Gitea-Signature and
	// X-Gogs-Signature.
	Bare string
	// Token is X-Gitlab-Token: the secret itself. It shows who is
	// sending, and says nothing about the body: GitLab does not sign with
	// a secret that the receiving side chose.
	Token string
}

// holds reports whether any part of the proof matches. sum is the
// HMAC-SHA256 of the body under the secret. Every part is compared, in
// constant time, whether or not an earlier one matched.
func (p Proof) holds(secret, sum []byte) bool {
	ok := false
	hub256, _ := strings.CutPrefix(p.Hub256, "sha256=")
	if hub256 == p.Hub256 {
		hub256 = ""
	}
	hub, _ := strings.CutPrefix(p.Hub, "sha256=")
	if hub == p.Hub {
		hub = ""
	}
	for _, given := range []string{hub256, hub, p.Bare} {
		want, err := hex.DecodeString(given)
		if err == nil && len(want) == sha256.Size && hmac.Equal(want, sum) {
			ok = true
		}
	}
	if p.Token != "" {
		// Hashed first, so that neither length tells anything.
		given, want := sha256.Sum256([]byte(p.Token)), sha256.Sum256(secret)
		if hmac.Equal(given[:], want[:]) {
			ok = true
		}
	}
	return ok
}

// ReadPush is ReadEvent for a caller that only acts on pushes.
func ReadPush(body io.Reader, secret []byte, header string) (p Push, signed bool) {
	ev, signed := ReadEvent(body, secret, header)
	return ev.Push, signed
}

// ReadEvent reads a webhook body, checking it against the
// X-Hub-Signature-256 header ("sha256=<hex>") with the shared secret: the
// one proof a GitHub App's webhook offers.
func ReadEvent(body io.Reader, secret []byte, header string) (e Event, signed bool) {
	return ReadHook(body, secret, Proof{Hub256: header})
}

// ReadHook reads a webhook body and checks the proof that came with it.
// signed is false when nothing of the proof holds, and then the Event is
// empty. An empty secret verifies nothing: a webhook with no secret
// configured is refused, not trusted.
//
// The body is not collected in memory. It is walked token by token while
// the same bytes feed the MAC, and only the few values a deployment needs
// are kept, so the memory used is bounded by the body's largest single
// value rather than by its size. The caller bounds the body itself. It is
// read to its end whatever the proof looks like, so that how long a
// request takes says nothing about the secret.
func ReadHook(body io.Reader, secret []byte, proof Proof) (e Event, signed bool) {
	mac := hmac.New(sha256.New, secret)
	tee := io.TeeReader(body, mac)
	ev, decodeErr := decodeEvent(json.NewDecoder(tee))
	// Whatever the decoder did not consume still belongs to the signed body.
	if _, err := io.Copy(io.Discard, tee); err != nil {
		return Event{}, false
	}
	if held := proof.holds(secret, mac.Sum(nil)); len(secret) == 0 || !held {
		return Event{}, false
	}
	if decodeErr != nil {
		return Event{}, true
	}
	e = Event{Pushes: ev.pushes(), PR: ev.pullRequest()}
	if len(e.Pushes) > 0 {
		e.Push = e.Pushes[0]
	} else {
		e.Push = Push{Repo: ev.repository()}
	}
	return e, true
}

type pushEvent struct {
	ref, after, repo string
	deleted          bool

	// Of a pull request event.
	action                              string
	number                              int
	hasPR                               bool
	headRef, headSHA, headRepo, baseRef string

	// GitLab: "object_kind" says what the event is, "project" where, and
	// "object_attributes" holds a merge request.
	kind, project string
	mr            struct {
		iid                                 int
		action, oldrev, source, target, sha string
		sourceRepo, targetRepo              string
	}

	// Bitbucket: the branches a push moved, and a pull request.
	changes []Push
	bb      struct {
		has                                      bool
		id                                       int
		state, branch, sha, repo, base, baseRepo string
	}
}

// repository is the repository the event is about.
func (ev pushEvent) repository() string {
	if ev.repo != "" {
		return ev.repo
	}
	return ev.project
}

// pullRequest is the event read as one about a pull request: GitHub's
// "pull_request", GitLab's merge request or Bitbucket's "pullrequest".
// Without one of those objects it is not one, whatever else the body holds.
func (ev pushEvent) pullRequest() PullRequest {
	switch {
	case ev.hasPR && ev.number > 0:
		return PullRequest{Repo: ev.repo, Number: ev.number, Action: ev.action,
			Branch: ev.headRef, Commit: ev.headSHA, HeadRepo: ev.headRepo, BaseBranch: ev.baseRef}
	case ev.kind == "merge_request" && ev.mr.iid > 0:
		pr := PullRequest{Repo: ev.mr.targetRepo, Number: ev.mr.iid,
			Branch: ev.mr.source, Commit: ev.mr.sha, HeadRepo: ev.mr.sourceRepo, BaseBranch: ev.mr.target}
		if pr.Repo == "" {
			pr.Repo = ev.project
		}
		switch ev.mr.action {
		case "open":
			pr.Action = "opened"
		case "reopen":
			pr.Action = "reopened"
		case "close", "merge":
			pr.Action = "closed"
		case "update":
			// GitLab calls every change an update: a new title, a label,
			// a review. Only one that names the commit it replaced
			// brought new code.
			if ev.mr.oldrev != "" {
				pr.Action = "synchronize"
			}
		}
		return pr
	case ev.bb.has && ev.bb.id > 0:
		pr := PullRequest{Repo: ev.bb.baseRepo, Number: ev.bb.id,
			Branch: ev.bb.branch, Commit: ev.bb.sha, HeadRepo: ev.bb.repo, BaseBranch: ev.bb.base}
		if pr.Repo == "" {
			pr.Repo = ev.repo
		}
		switch ev.bb.state {
		case "OPEN":
			pr.Action, pr.IfChanged = "synchronize", true
		case "MERGED", "DECLINED", "SUPERSEDED":
			pr.Action = "closed"
		}
		return pr
	}
	return PullRequest{}
}

// pushes are the branches the event says were pushed. A tag, a deleted
// branch and any other kind of event give none.
func (ev pushEvent) pushes() []Push {
	var out []Push
	repo := ev.repository()
	branch, isBranch := strings.CutPrefix(ev.ref, "refs/heads/")
	// A deleted ref is also signalled by an all-zero "after". GitLab names
	// its events; one that is not a push has no branch to deploy even if
	// it carries a "ref".
	if isBranch && !ev.deleted && strings.Trim(ev.after, "0") != "" && (ev.kind == "" || ev.kind == "push") {
		out = append(out, Push{Repo: repo, Branch: branch, Commit: ev.after})
	}
	for _, c := range ev.changes {
		out = append(out, Push{Repo: repo, Branch: c.Branch, Commit: c.Commit})
	}
	return out
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

		// GitLab.
		case "object_kind":
			return readValue(dec, &ev.kind)
		case "project":
			return eachKey(dec, named(dec, "path_with_namespace", &ev.project))
		case "object_attributes":
			return eachKey(dec, func(key string) error {
				switch key {
				case "iid":
					return readValue(dec, &ev.mr.iid)
				case "action":
					return readValue(dec, &ev.mr.action)
				case "oldrev":
					return readValue(dec, &ev.mr.oldrev)
				case "source_branch":
					return readValue(dec, &ev.mr.source)
				case "target_branch":
					return readValue(dec, &ev.mr.target)
				case "last_commit":
					return eachKey(dec, func(key string) error {
						if key == "id" {
							return readValue(dec, &ev.mr.sha)
						}
						return readValue(dec, nil)
					})
				case "source":
					return eachKey(dec, named(dec, "path_with_namespace", &ev.mr.sourceRepo))
				case "target":
					return eachKey(dec, named(dec, "path_with_namespace", &ev.mr.targetRepo))
				}
				return readValue(dec, nil)
			})

		// Bitbucket.
		case "push":
			return eachKey(dec, func(key string) error {
				if key != "changes" {
					return readValue(dec, nil)
				}
				return eachItem(dec, func() error {
					var kind, name, hash string
					err := eachKey(dec, func(key string) error {
						// "new" is null for a branch that was deleted.
						if key != "new" {
							return readValue(dec, nil)
						}
						return eachKey(dec, func(key string) error {
							switch key {
							case "type":
								return readValue(dec, &kind)
							case "name":
								return readValue(dec, &name)
							case "target":
								return eachKey(dec, named(dec, "hash", &hash))
							}
							return readValue(dec, nil)
						})
					})
					if err == nil && kind == "branch" && name != "" && hash != "" && len(ev.changes) < maxPushes {
						ev.changes = append(ev.changes, Push{Branch: name, Commit: hash})
					}
					return err
				})
			})
		case "pullrequest":
			ev.bb.has = true
			end := func(branch, sha, repo *string) error {
				return eachKey(dec, func(key string) error {
					switch key {
					case "branch":
						return eachKey(dec, named(dec, "name", branch))
					case "commit":
						return eachKey(dec, named(dec, "hash", sha))
					case "repository":
						return eachKey(dec, named(dec, "full_name", repo))
					}
					return readValue(dec, nil)
				})
			}
			var ignored string
			return eachKey(dec, func(key string) error {
				switch key {
				case "id":
					return readValue(dec, &ev.bb.id)
				case "state":
					return readValue(dec, &ev.bb.state)
				case "source":
					return end(&ev.bb.branch, &ev.bb.sha, &ev.bb.repo)
				case "destination":
					return end(&ev.bb.base, &ignored, &ev.bb.baseRepo)
				}
				return readValue(dec, nil)
			})
		}
		return readValue(dec, nil)
	})
	return ev, err
}

// named reads the one key of an object that is wanted into dst.
func named(dec *json.Decoder, want string, dst *string) func(string) error {
	return func(key string) error {
		if key == want {
			return readValue(dec, dst)
		}
		return readValue(dec, nil)
	}
}

// eachItem reads one JSON value. For an array it calls fn for every item,
// and fn must consume that item; any other value is skipped.
func eachItem(dec *json.Decoder, fn func() error) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok != json.Delim('[') {
		return skipRest(dec, tok)
	}
	for dec.More() {
		if err := fn(); err != nil {
			return err
		}
	}
	_, err = dec.Token() // the closing bracket
	return err
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
