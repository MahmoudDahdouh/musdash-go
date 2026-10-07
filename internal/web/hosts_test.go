package web

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/source"
)

// hookAs sends a webhook the way a Git host other than GitHub does: with
// that host's own headers.
func (a *app) hookAs(path string, headers map[string]string, body string) (*http.Response, string) {
	a.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, a.url+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return a.do(&http.Client{}, req)
}

func gitlabPushBody(project, ref string) string {
	return `{"object_kind":"push","ref":"` + ref + `","after":"da1560886d4f094c3e6c9ef40349f7d38b5d27d7",
		"project":{"path_with_namespace":"` + project + `"},"repository":{"name":"shop"}}`
}

func bitbucketPushBody(repo string, branches ...string) string {
	var changes []string
	for _, b := range branches {
		changes = append(changes, `{"new":{"type":"branch","name":"`+b+`","target":{"hash":"709d658dc5b6d6afcd46049c2f332ee3f515a67d"}}}`)
	}
	return `{"repository":{"full_name":"` + repo + `"},"push":{"changes":[` + strings.Join(changes, ",") + `]}}`
}

func bitbucketPRBody(state, source, commit string, number string) string {
	return `{"pullrequest":{"id":` + number + `,"state":"` + state + `",
		"source":{"branch":{"name":"feature"},"commit":{"hash":"` + commit + `"},"repository":{"full_name":"` + source + `"}},
		"destination":{"branch":{"name":"main"},"repository":{"full_name":"acme/shop"}}},"repository":{"full_name":"acme/shop"}}`
}

// webhookSecret creates the app's webhook secret and reads it as a person
// would: the Keys page lists it hidden, and Show asks for it.
func (a *app) webhookSecret(appID string) string {
	a.t.Helper()
	res, _ := a.post("/keys/tokens", a.appPath(appID)+"/webhook-secret", nil)
	wantRedirect(a.t, res, "/keys/tokens")
	_, page := a.get("/keys/tokens")
	if !strings.Contains(page, "/webhooks/git/"+appID) || !strings.Contains(page, `hx-get="`+a.appPath(appID)+`/webhook-secret"`) {
		a.t.Fatal("the Keys page does not list the webhook address with a way to show its secret")
	}
	if regexp.MustCompile(`[0-9a-f]{48}`).MatchString(page) {
		a.t.Fatal("the Keys page holds a webhook secret that was not asked for")
	}
	_, cell := a.get(a.appPath(appID) + "/webhook-secret")
	m := regexp.MustCompile(`data-copy="([0-9a-f]{48})"`).FindStringSubmatch(cell)
	if m == nil {
		a.t.Fatal("Show does not answer with the webhook secret")
	}
	if _, hidden := a.get(a.appPath(appID) + "/webhook-secret?hide=1"); strings.Contains(hidden, m[1]) {
		a.t.Fatal("Hide answers with the secret")
	}
	return m[1]
}

func TestWebhooksOfOtherGitHosts(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	app := a.newGitApp(projectID, env, "web", nil)
	other := a.newGitApp(projectID, env, "api", nil)
	hookPath := "/webhooks/git/" + app.ID
	secret, otherSecret := a.webhookSecret(app.ID), a.webhookSecret(other.ID)

	// The pages say where each host wants the address and the secret.
	_, page := a.get("/keys/tokens")
	for _, want := range []string{"GitLab", "Secret token", "Bitbucket", "Gitea"} {
		if !strings.Contains(page, want) {
			t.Errorf("the Keys page does not mention %q", want)
		}
	}

	// GitLab shows the secret itself.
	refused := map[string]map[string]string{
		"no token":             {},
		"a wrong token":        {"X-Gitlab-Token": strings.Repeat("0", 48)},
		"another app's token":  {"X-Gitlab-Token": otherSecret},
		"the token, shortened": {"X-Gitlab-Token": secret[:40]},
		"the token as a MAC":   {"X-Hub-Signature-256": "sha256=" + secret, "X-Gitea-Signature": secret},
	}
	for name, headers := range refused {
		if res, _ := a.hookAs(hookPath, headers, gitlabPushBody("acme/shop", "refs/heads/main")); res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", name, res.StatusCode)
		}
	}
	if a.deployJobs() != 0 {
		t.Fatal("a refused webhook queued a deployment")
	}
	gitlab := map[string]string{"X-Gitlab-Token": secret, "X-Gitlab-Event": "Push Hook", "Idempotency-Key": "f5e5f430-f57b-4e6e-9fac-d9128cd7232f"}
	if res, body := a.hookAs(hookPath, gitlab, gitlabPushBody("acme/shop", "refs/heads/other")); res.StatusCode != http.StatusOK || a.deployJobs() != 0 {
		t.Fatalf("a GitLab push to another branch: %d %s", res.StatusCode, body)
	}
	gitlab["Idempotency-Key"] = "0a0a0a0a-f57b-4e6e-9fac-d9128cd7232f"
	if res, body := a.hookAs(hookPath, gitlab, gitlabPushBody("someone/else", "refs/heads/main")); res.StatusCode != http.StatusOK || a.deployJobs() != 0 {
		t.Fatalf("a GitLab push to another project: %d %s", res.StatusCode, body)
	}
	gitlab["Idempotency-Key"] = "1b1b1b1b-f57b-4e6e-9fac-d9128cd7232f"
	res, body := a.hookAs(hookPath, gitlab, gitlabPushBody("Acme/Shop", "refs/heads/main"))
	if res.StatusCode != http.StatusOK || !strings.Contains(body, `"deployments":1`) || a.deployJobs() != 1 {
		t.Fatalf("a GitLab push: %d %s", res.StatusCode, body)
	}
	// GitLab sends a delivery again under the same key.
	a.waitDeployed(app.ID)
	if _, body := a.hookAs(hookPath, gitlab, gitlabPushBody("acme/shop", "refs/heads/main")); !strings.Contains(body, "already handled") || a.deployJobs() != 1 {
		t.Fatalf("a GitLab delivery sent again: %s", body)
	}

	// Bitbucket signs the body, and one push can move several branches.
	two := bitbucketPushBody("acme/shop", "release/2", "main")
	bitbucket := func(body, delivery string) map[string]string {
		return map[string]string{"X-Hub-Signature": source.Sign([]byte(secret), []byte(body)), "X-Event-Key": "repo:push", "X-Request-UUID": delivery}
	}
	if res, body := a.hookAs(hookPath, bitbucket(two, "bb-1"), two); res.StatusCode != http.StatusOK || !strings.Contains(body, `"deployments":1`) {
		t.Fatalf("a Bitbucket push of two branches: %d %s", res.StatusCode, body)
	}
	a.waitDeployed(app.ID)
	elsewhere := bitbucketPushBody("acme/shop", "release/2", "release/3")
	if _, body := a.hookAs(hookPath, bitbucket(elsewhere, "bb-2"), elsewhere); !strings.Contains(body, `"deployments":0`) {
		t.Fatalf("a Bitbucket push of other branches: %s", body)
	}
	if res, _ := a.hookAs(hookPath, bitbucket(two, "bb-3"), elsewhere); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a Bitbucket signature over another body: %d", res.StatusCode)
	}

	// Gitea before it sent GitHub's header: the bare MAC.
	gitea := pushBody("acme/shop", "refs/heads/main")
	bare := strings.TrimPrefix(source.Sign([]byte(secret), []byte(gitea)), "sha256=")
	if res, body := a.hookAs(hookPath, map[string]string{"X-Gitea-Signature": bare, "X-Gitea-Delivery": "g-1"}, gitea); res.StatusCode != http.StatusOK || !strings.Contains(body, `"deployments":1`) {
		t.Fatalf("a Gitea push: %d %s", res.StatusCode, body)
	}
}

func TestPreviewsFromGitLabAndBitbucket(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	app := a.newGitApp(projectID, env, "web", nil)
	settings, hookPath := a.appPath(app.ID)+"/settings", "/webhooks/git/"+app.ID
	res, _ := a.post(settings, a.appPath(app.ID)+"/previews", url.Values{"previews": {"1"}, "preview_domain": {"preview.example.com"}})
	wantRedirect(t, res, settings+"#previews")
	secret := a.webhookSecret(app.ID)
	gitlab := map[string]string{"X-Gitlab-Token": secret}
	mr := func(action, oldrev, sourceProject string) string {
		old := ""
		if oldrev != "" {
			old = `"oldrev":"` + oldrev + `",`
		}
		return `{"object_kind":"merge_request","project":{"path_with_namespace":"acme/shop"},
			"object_attributes":{"iid":5,"action":"` + action + `",` + old + `"source_branch":"feature","target_branch":"main",
			"last_commit":{"id":"da1560886d4f094c3e6c9ef40349f7d38b5d27d7"},
			"source":{"path_with_namespace":"` + sourceProject + `"},"target":{"path_with_namespace":"acme/shop"}}}`
	}

	// A merge request from a fork is somebody else's code.
	if _, answer := a.hookAs(hookPath, gitlab, mr("open", "", "mallory/shop")); !strings.Contains(answer, `"previews":0`) {
		t.Fatalf("a merge request from a fork: %s", answer)
	}
	if _, answer := a.hookAs(hookPath, gitlab, mr("open", "", "acme/shop")); !strings.Contains(answer, `"previews":1`) {
		t.Fatalf("a merge request: %s", answer)
	}
	previews := a.previewsOf(app.ID)
	if len(previews) != 1 || previews[0].PRNumber != 5 || previews[0].Branch != "feature" {
		t.Fatalf("previews: %+v", previews)
	}
	a.waitDeployed(previews[0].ID)
	// A new title is an update too, and deploys nothing.
	if _, answer := a.hookAs(hookPath, gitlab, mr("update", "", "acme/shop")); !strings.Contains(answer, `"previews":0`) {
		t.Fatalf("an edited merge request: %s", answer)
	}
	if _, answer := a.hookAs(hookPath, gitlab, mr("update", "e59094b8de0f2f91abbe4760a52d9137260252d8", "acme/shop")); !strings.Contains(answer, `"previews":1`) {
		t.Fatalf("new commits: %s", answer)
	}
	if list, _ := a.db.ListDeployments(ctx, previews[0].ID, 5); len(list) != 2 {
		t.Fatalf("%d deployments of the preview after new commits", len(list))
	}
	if _, answer := a.hookAs(hookPath, gitlab, mr("merge", "", "acme/shop")); !strings.Contains(answer, `"previews":1`) {
		t.Fatalf("merged: %s", answer)
	}
	a.waitGone(previews[0].ID)

	// Bitbucket says only that a pull request is open. It is deployed when
	// its commit is not the one deployed last.
	signed := func(body string) map[string]string {
		return map[string]string{"X-Hub-Signature": source.Sign([]byte(secret), []byte(body))}
	}
	opened := bitbucketPRBody("OPEN", "acme/shop", "d3022fc0ca3d", "9")
	if _, answer := a.hookAs(hookPath, signed(opened), opened); !strings.Contains(answer, `"previews":1`) {
		t.Fatalf("a Bitbucket pull request: %s", answer)
	}
	previews = a.previewsOf(app.ID)
	if len(previews) != 1 || previews[0].PRNumber != 9 {
		t.Fatalf("previews: %+v", previews)
	}
	a.waitDeployed(previews[0].ID)
	a.db.Exec(`UPDATE deployments SET status = 'success', commit_sha = 'd3022fc0ca3d4d1f0e1e8a7c9d2b5f6a7b8c9d0e' WHERE app_id = ?`, previews[0].ID)
	// A comment on it: the same body again.
	a.hookAs(hookPath, signed(opened), opened)
	if list, _ := a.db.ListDeployments(ctx, previews[0].ID, 5); len(list) != 1 {
		t.Fatalf("%d deployments after an event that brought no commit", len(list))
	}
	// Nor does a comment start again a build that failed, or pile a second
	// build on one that is running.
	for _, status := range []string{"failed", "running"} {
		a.db.Exec(`UPDATE deployments SET status = ? WHERE app_id = ?`, status, previews[0].ID)
		a.hookAs(hookPath, signed(opened), opened)
		if list, _ := a.db.ListDeployments(ctx, previews[0].ID, 5); len(list) != 1 {
			t.Fatalf("%d deployments after a comment on a build that is %s", len(list), status)
		}
	}
	a.db.Exec(`UPDATE deployments SET status = 'success' WHERE app_id = ?`, previews[0].ID)
	pushed := bitbucketPRBody("OPEN", "acme/shop", "ffff2fc0ca3d", "9")
	a.hookAs(hookPath, signed(pushed), pushed)
	if list, _ := a.db.ListDeployments(ctx, previews[0].ID, 5); len(list) != 2 {
		t.Fatalf("%d deployments after a new commit", len(list))
	}
	fork := bitbucketPRBody("OPEN", "mallory/shop", "abcd2fc0ca3d", "10")
	if _, answer := a.hookAs(hookPath, signed(fork), fork); !strings.Contains(answer, `"previews":0`) {
		t.Fatalf("a Bitbucket pull request from a fork: %s", answer)
	}
	declined := bitbucketPRBody("DECLINED", "acme/shop", "ffff2fc0ca3d", "9")
	if _, answer := a.hookAs(hookPath, signed(declined), declined); !strings.Contains(answer, `"previews":1`) {
		t.Fatalf("declined: %s", answer)
	}
	a.waitGone(previews[0].ID)
}
