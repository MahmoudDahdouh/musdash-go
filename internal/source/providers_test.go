package source

import (
	"encoding/hex"
	"strings"
	"testing"
)

// The bodies below follow the hosts' documented payloads, cut down to what
// surrounds the values that are read.

const gitlabPush = `{"object_kind":"push","event_name":"push","before":"95790bf891e76fee5e1747ab589903a6a1f80f22",
	"after":"da1560886d4f094c3e6c9ef40349f7d38b5d27d7","ref":"refs/heads/main","ref_protected":true,
	"checkout_sha":"da1560886d4f094c3e6c9ef40349f7d38b5d27d7","user_id":4,"project_id":15,
	"project":{"id":15,"name":"Shop","web_url":"https://gitlab.example.com/acme/team/shop","namespace":"team",
		"path_with_namespace":"acme/team/shop","default_branch":"main"},
	"repository":{"name":"Shop","url":"git@gitlab.example.com:acme/team/shop.git","homepage":"https://gitlab.example.com/acme/team/shop"},
	"commits":[{"id":"b6568db1bc1dcd7f8b4d5a946b0b91f9dacd7327","message":"x","author":{"name":"a"}}],"total_commits_count":1}`

func gitlabMR(action, oldrev, source, target string) string {
	old := ""
	if oldrev != "" {
		old = `"oldrev":"` + oldrev + `",`
	}
	return `{"object_kind":"merge_request","event_type":"merge_request","user":{"id":1,"name":"Sam"},
	"project":{"id":1,"name":"Shop","path_with_namespace":"acme/shop"},
	"repository":{"name":"Shop","url":"ssh://git@example.com/acme/shop.git"},
	"object_attributes":{"id":99,"iid":7,"target_branch":"main","source_branch":"feature/login","source_project_id":14,
		"target_project_id":14,"state":"opened","title":"MS-Viewport",` + old + `
		"source":{"name":"Shop","path_with_namespace":"` + source + `"},
		"target":{"name":"Shop","path_with_namespace":"` + target + `"},
		"last_commit":{"id":"da1560886d4f094c3e6c9ef40349f7d38b5d27d7","message":"fixed readme","author":{"name":"a"}},
		"action":"` + action + `"},
	"labels":[],"changes":{"updated_at":{"previous":"x","current":"y"}}}`
}

const bitbucketPush = `{"actor":{"display_name":"Sam"},"repository":{"type":"repository","full_name":"acme/shop","name":"shop",
	"owner":{"display_name":"acme","nickname":"acme"}},
	"push":{"changes":[
		{"new":{"type":"branch","name":"main","target":{"type":"commit","hash":"709d658dc5b6d6afcd46049c2f332ee3f515a67d","author":{"raw":"x"}}},
		 "old":{"type":"branch","name":"main","target":{"hash":"1e65c05c1d5171631d92438a13901ca7dae9618c"}},
		 "created":false,"forced":false,"closed":false,"commits":[{"hash":"709d658dc5b6d6afcd46049c2f332ee3f515a67d","type":"commit"}]},
		{"new":{"type":"tag","name":"v1","target":{"hash":"aaaa658dc5b6d6afcd46049c2f332ee3f515a67d"}},"old":null},
		{"new":null,"old":{"type":"branch","name":"gone","target":{"hash":"bbbb658dc5b6d6afcd46049c2f332ee3f515a67d"}},"closed":true},
		{"new":{"type":"branch","name":"release/2","target":{"hash":"cccc658dc5b6d6afcd46049c2f332ee3f515a67d"}},"old":null,"created":true}
	]}}`

func bitbucketPR(state, source, destination string) string {
	src := `null`
	if source != "" {
		src = `{"full_name":"` + source + `","name":"shop"}`
	}
	return `{"actor":{"display_name":"Sam"},
	"pullrequest":{"id":12,"title":"Login","state":"` + state + `","author":{"display_name":"Sam"},
		"source":{"branch":{"name":"feature/login"},"commit":{"hash":"d3022fc0ca3d"},"repository":` + src + `},
		"destination":{"branch":{"name":"main"},"commit":{"hash":"ce5965ddd289"},"repository":{"full_name":"` + destination + `","name":"shop"}},
		"merge_commit":null,"participants":[],"reviewers":[]},
	"repository":{"full_name":"` + destination + `","name":"shop"}}`
}

func readSigned(t *testing.T, body string) Event {
	t.Helper()
	secret := []byte("s3cret")
	ev, ok := ReadHook(strings.NewReader(body), secret, Proof{Hub256: Sign(secret, []byte(body))})
	if !ok {
		t.Fatalf("a signed body was refused: %s", body)
	}
	return ev
}

func TestGitLabEvents(t *testing.T) {
	ev := readSigned(t, gitlabPush)
	want := Push{Repo: "acme/team/shop", Branch: "main", Commit: "da1560886d4f094c3e6c9ef40349f7d38b5d27d7"}
	if ev.Push != want || len(ev.Pushes) != 1 || ev.PR.Number != 0 {
		t.Fatalf("%+v", ev)
	}
	// Other events of GitLab carry a "ref" too. None of them is a push.
	for name, body := range map[string]string{
		"a tag":           strings.Replace(strings.Replace(gitlabPush, `"object_kind":"push"`, `"object_kind":"tag_push"`, 1), "refs/heads/main", "refs/tags/v1", 1),
		"a pipeline":      strings.Replace(gitlabPush, `"object_kind":"push"`, `"object_kind":"pipeline"`, 1),
		"a deleted ref":   strings.Replace(gitlabPush, `"after":"da1560886d4f094c3e6c9ef40349f7d38b5d27d7"`, `"after":"0000000000000000000000000000000000000000"`, 1),
		"a merge request": gitlabMR("open", "", "acme/shop", "acme/shop"),
	} {
		if ev := readSigned(t, body); len(ev.Pushes) != 0 || ev.Push.Branch != "" {
			t.Errorf("%s was read as a push: %+v", name, ev.Pushes)
		}
	}

	pr := readSigned(t, gitlabMR("open", "", "acme/shop", "acme/shop")).PR
	if pr != (PullRequest{Repo: "acme/shop", Number: 7, Action: "opened", Branch: "feature/login",
		Commit: "da1560886d4f094c3e6c9ef40349f7d38b5d27d7", HeadRepo: "acme/shop", BaseBranch: "main"}) {
		t.Fatalf("%+v", pr)
	}
	if pr.FromFork() {
		t.Fatal("a branch of the project itself counts as a fork")
	}
	for action, want := range map[string]string{"reopen": "reopened", "close": "closed", "merge": "closed", "approved": "", "unapproval": ""} {
		if got := readSigned(t, gitlabMR(action, "", "acme/shop", "acme/shop")).PR.Action; got != want {
			t.Errorf("action %s read as %q, want %q", action, got, want)
		}
	}
	// An update is new code only when it names the commit it replaced.
	if got := readSigned(t, gitlabMR("update", "", "acme/shop", "acme/shop")).PR.Action; got != "" {
		t.Errorf("an edited title was read as %q", got)
	}
	if got := readSigned(t, gitlabMR("update", "e59094b8de0f2f91abbe4760a52d9137260252d8", "acme/shop", "acme/shop")).PR.Action; got != "synchronize" {
		t.Errorf("new commits were read as %q", got)
	}
	if pr := readSigned(t, gitlabMR("open", "", "mallory/shop", "acme/shop")).PR; !pr.FromFork() || pr.Repo != "acme/shop" {
		t.Fatalf("a merge request from a fork: %+v", pr)
	}
	// An issue has "object_attributes" with an iid and an action as well.
	issue := strings.Replace(gitlabMR("open", "", "acme/shop", "acme/shop"), `"object_kind":"merge_request"`, `"object_kind":"issue"`, 1)
	if pr := readSigned(t, issue).PR; pr.Number != 0 {
		t.Fatalf("an issue was read as merge request %d", pr.Number)
	}
}

func TestBitbucketEvents(t *testing.T) {
	ev := readSigned(t, bitbucketPush)
	want := []Push{
		{Repo: "acme/shop", Branch: "main", Commit: "709d658dc5b6d6afcd46049c2f332ee3f515a67d"},
		{Repo: "acme/shop", Branch: "release/2", Commit: "cccc658dc5b6d6afcd46049c2f332ee3f515a67d"},
	}
	if len(ev.Pushes) != 2 || ev.Pushes[0] != want[0] || ev.Pushes[1] != want[1] || ev.Push != want[0] {
		t.Fatalf("%+v", ev.Pushes)
	}
	// A push of very many branches is cut off, not kept whole.
	var many strings.Builder
	many.WriteString(`{"repository":{"full_name":"acme/shop"},"push":{"changes":[`)
	for i := 0; i < 500; i++ {
		if i > 0 {
			many.WriteByte(',')
		}
		many.WriteString(`{"new":{"type":"branch","name":"b` + strings.Repeat("x", i%7) + `","target":{"hash":"abc"}}}`)
	}
	many.WriteString(`]}}`)
	if got := len(readSigned(t, many.String()).Pushes); got != maxPushes {
		t.Fatalf("%d branches kept, want %d", got, maxPushes)
	}
	for name, body := range map[string]string{
		"changes that is no list": `{"push":{"changes":{"new":{"type":"branch","name":"main","target":{"hash":"abc"}}}}}`,
		"a change that is text":   `{"push":{"changes":["main"]}}`,
		"no hash":                 `{"push":{"changes":[{"new":{"type":"branch","name":"main","target":{}}}]}}`,
		"push that is text":       `{"push":"main"}`,
	} {
		if ev := readSigned(t, body); len(ev.Pushes) != 0 {
			t.Errorf("%s: read as a push: %+v", name, ev.Pushes)
		}
	}

	pr := readSigned(t, bitbucketPR("OPEN", "acme/shop", "acme/shop")).PR
	if pr != (PullRequest{Repo: "acme/shop", Number: 12, Action: "synchronize", Branch: "feature/login",
		Commit: "d3022fc0ca3d", HeadRepo: "acme/shop", BaseBranch: "main", IfChanged: true}) {
		t.Fatalf("%+v", pr)
	}
	for state, want := range map[string]string{"MERGED": "closed", "DECLINED": "closed", "SUPERSEDED": "closed", "DRAFT": ""} {
		if got := readSigned(t, bitbucketPR(state, "acme/shop", "acme/shop")).PR; got.Action != want || got.IfChanged {
			t.Errorf("state %s read as %+v", state, got)
		}
	}
	for name, body := range map[string]string{
		"a fork":                  bitbucketPR("OPEN", "mallory/shop", "acme/shop"),
		"a fork that was deleted": bitbucketPR("OPEN", "", "acme/shop"),
	} {
		if pr := readSigned(t, body).PR; pr.Number == 0 || !pr.FromFork() {
			t.Errorf("%s: not seen as a fork: %+v", name, pr)
		}
	}
}

func TestProofs(t *testing.T) {
	secret, body := []byte("0123456789abcdef0123456789abcdef0123456789abcdef"), gitlabPush
	mac := Sign(secret, []byte(body))
	bare := strings.TrimPrefix(mac, "sha256=")
	accepted := map[string]Proof{
		"GitHub's header":    {Hub256: mac},
		"Bitbucket's header": {Hub: mac},
		"Gitea's header":     {Bare: bare},
		"GitLab's token":     {Token: string(secret)},
		// One that holds is enough: Gitea sends two headers, and GitHub
		// sends a SHA-1 next to its SHA-256.
		"a SHA-1 beside it": {Hub256: mac, Hub: "sha1=" + bare[:40]},
	}
	for name, p := range accepted {
		if ev, ok := ReadHook(strings.NewReader(body), secret, p); !ok || ev.Push.Branch != "main" {
			t.Errorf("%s was refused", name)
		}
	}
	other := Sign([]byte("another secret"), []byte(body))
	refused := map[string]Proof{
		"nothing":                    {},
		"the MAC of another secret":  {Hub256: other, Hub: other, Bare: strings.TrimPrefix(other, "sha256=")},
		"the MAC of another body":    {Hub: Sign(secret, []byte("{}"))},
		"a SHA-1":                    {Hub: "sha1=" + bare[:40]},
		"the bare MAC with a prefix": {Bare: mac},
		"the prefixed MAC, bare":     {Hub256: bare, Hub: bare},
		"a wrong token":              {Token: string(secret[:47]) + "0"},
		"a token that is a prefix":   {Token: string(secret[:20])},
		"the token in hex":           {Token: hex.EncodeToString(secret)},
		"the MAC as the token":       {Token: bare},
		"the token as the MAC":       {Bare: string(secret), Hub256: "sha256=" + string(secret)},
	}
	for name, p := range refused {
		if ev, ok := ReadHook(strings.NewReader(body), secret, p); ok || len(ev.Pushes) != 0 || ev.Push != (Push{}) {
			t.Errorf("%s was accepted", name)
		}
	}
	// No secret configured: nothing verifies, not an empty token either.
	for _, p := range []Proof{{Token: ""}, {Hub256: Sign(nil, []byte(body))}, {Bare: strings.TrimPrefix(Sign(nil, []byte(body)), "sha256=")}} {
		if _, ok := ReadHook(strings.NewReader(body), nil, p); ok {
			t.Fatalf("an empty secret verified %+v", p)
		}
	}
	// The body is read to its end whatever the proof is.
	r := strings.NewReader(body)
	ReadHook(r, secret, Proof{})
	if r.Len() != 0 {
		t.Fatalf("%d bytes of a refused body were left unread", r.Len())
	}
}
