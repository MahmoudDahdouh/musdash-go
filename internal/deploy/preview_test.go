package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/source"
)

// previewEnv is an environment whose app is built from Git and gives its
// pull requests previews.
func previewEnv(t *testing.T) (*env, *gitEnvRecorder) {
	t.Helper()
	e := newEnv(t)
	rec := &gitEnvRecorder{}
	e.fake.Handle = rec.handle
	e.gitApp(nil)
	if err := e.db.SetAppPreviews(context.Background(), e.team, e.app.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	return e, rec
}

// pull is a pull request of the test app's repository into its branch.
func pull(number int, branch string) source.PullRequest {
	return source.PullRequest{Repo: "acme/shop", Number: number, Action: "opened", Branch: branch,
		Commit: testCommit, HeadRepo: "acme/shop", BaseBranch: "main"}
}

// last waits for an app's newest deployment to finish.
func (e *env) last(appID string) db.Deployment {
	e.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		list, _ := e.db.ListDeployments(context.Background(), appID, 1)
		if len(list) == 1 && (list[0].Status == db.DeploySuccess || list[0].Status == db.DeployFailed) {
			return list[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.t.Fatal("no deployment finished")
	return db.Deployment{}
}

// gone waits for an app to be removed.
func (e *env) gone(appID string) {
	e.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := e.db.AppByID(context.Background(), appID); errors.Is(err, db.ErrNotFound) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.t.Fatalf("app %s was not removed", appID)
}

func TestAPullRequestGetsAPreview(t *testing.T) {
	e, rec := previewEnv(t)
	ctx := context.Background()
	seal := func(v string) string { s, _ := e.d.Box.SealString(v); return s }
	e.db.ReplaceEnvVars(ctx, db.KindApp, e.app.ID, []db.EnvVar{
		{Key: "DATABASE_URL", Value: seal("postgres://prod")},
		{Key: "NPM_TOKEN", Value: seal("npm_s3cret"), BuildTime: true},
		// A parent that sets these itself does not get to say a preview is
		// not one.
		{Key: "MUSDASH_PREVIEW", Value: seal("0")},
	})
	e.db.AddStorage(ctx, db.Storage{ResourceKind: db.KindApp, ResourceID: e.app.ID, Kind: db.StorageFile, Target: "/etc/app/config.yml", Content: seal("mode: x")})
	e.db.AddStorage(ctx, db.Storage{ResourceKind: db.KindApp, ResourceID: e.app.ID, Kind: db.StorageVolume, Source: "uploads", Target: "/data"})
	e.db.AddStorage(ctx, db.Storage{ResourceKind: db.KindApp, ResourceID: e.app.ID, Kind: db.StorageBind, Source: "/srv/shared", Target: "/shared"})
	// What makes the parent reachable from outside is not handed down.
	e.db.Exec(`UPDATE apps SET webhook_secret = 'sealed-secret', deploy_token_hash = 'hash', auto_deploy = 1 WHERE id = ?`, e.app.ID)
	parent := e.reload()

	child, err := e.d.SyncPreview(ctx, parent, pull(12, "feature/login"))
	if err != nil {
		t.Fatal(err)
	}
	dep := e.last(child.ID)
	if dep.Status != db.DeploySuccess || dep.Trigger != TriggerPullRequest {
		t.Fatalf("%s %q (%s)\n%s", dep.Status, dep.Error, dep.Trigger, e.log(dep))
	}
	child, _ = e.db.AppByID(ctx, child.ID)
	if child.PreviewOf != parent.ID || child.PRNumber != 12 || child.Name != "web-pr-12" || child.Branch != "feature/login" {
		t.Fatalf("the preview: %+v", child)
	}
	if child.AutoDeploy || child.WebhookSecret != "" || child.DeployTokenHash != "" || child.Previews {
		t.Fatalf("a preview can be reached or deployed from outside as its parent can: %+v", child)
	}
	if child.ServerID != parent.ServerID || child.EnvironmentID != parent.EnvironmentID || child.RepoURL != parent.RepoURL || child.BuildPack != parent.BuildPack || child.Port != parent.Port {
		t.Fatalf("the preview does not have its parent's settings: %+v", child)
	}

	// Built from the pull request's branch, with the parent's build
	// variables.
	calls := e.fake.Calls()
	clone := calls[indexOf(calls, "git clone")]
	if !strings.Contains(clone, " feature/login ") {
		t.Fatalf("cloned another branch: %s", clone)
	}
	if env := strings.Join(rec.envs["docker build"], "\n"); !strings.Contains(env, "NPM_TOKEN=npm_s3cret") {
		t.Fatalf("the build did not get the parent's build variables: %q", env)
	}

	// Its own address, routed to its own container.
	domains, _ := e.db.ListDomains(ctx, db.KindApp, child.ID)
	if len(domains) != 1 || !strings.HasPrefix(domains[0].Host, "pr-12-web-") || !strings.HasSuffix(domains[0].Host, ".sslip.io") || domains[0].TLS {
		t.Fatalf("address: %+v", domains)
	}
	routed := false
	for _, r := range e.routes().Routes {
		if r.Host == domains[0].Host && r.Target == "127.0.0.1:"+strconv.Itoa(child.HostPort) {
			routed = true
		}
	}
	if !routed || child.HostPort == 0 {
		t.Fatalf("the preview's address is not routed: %+v", e.routes().Routes)
	}

	// The parent's variables, and the two that say it is a preview.
	r, _ := e.d.Runners.Runner(ctx, e.server)
	envFile, _, ok := e.fake.File(e.d.at(r).AppDir(child.ID) + "/env")
	if !ok {
		t.Fatal("no env file for the preview")
	}
	for _, want := range []string{"DATABASE_URL=postgres://prod", "MUSDASH_PREVIEW=1", "MUSDASH_PULL_REQUEST=12"} {
		if !strings.Contains(envFile, want) {
			t.Errorf("the preview's variables lack %s:\n%s", want, envFile)
		}
	}
	if strings.Contains(envFile, "MUSDASH_PREVIEW=0") || strings.Contains(envFile, "NPM_TOKEN") || strings.Count(envFile, "MUSDASH_PREVIEW=") != 1 {
		t.Errorf("the preview's variables:\n%s", envFile)
	}

	// The parent's files, and nothing that holds production's data.
	run := ""
	for _, c := range calls {
		if strings.HasPrefix(c, "docker run") && strings.Contains(c, ContainerName(child.ID, dep.ID)) {
			run = c
		}
	}
	if !strings.Contains(run, "/etc/app/config.yml") {
		t.Errorf("the preview did not get the parent's file:\n%s", run)
	}
	for _, never := range []string{"/srv/shared", "uploads", ":/data"} {
		if strings.Contains(run, never) {
			t.Errorf("the preview was given %q of its parent:\n%s", never, run)
		}
	}

	// Listed with its parent, not among the environment's apps, and a push
	// to its branch is not what deploys it.
	if apps, _ := e.db.ListApps(ctx, parent.EnvironmentID); len(apps) != 1 || apps[0].ID != parent.ID {
		t.Fatalf("the environment lists %d apps", len(apps))
	}
	if list, _ := e.db.Previews(ctx, parent.ID); len(list) != 1 || list[0].ID != child.ID {
		t.Fatalf("previews of the parent: %+v", list)
	}
	e.db.Exec(`UPDATE apps SET auto_deploy = 1 WHERE id = ?`, child.ID)
	if apps, _ := e.db.AppsForPush(ctx, "", "acme/shop", "feature/login"); len(apps) != 0 {
		t.Fatal("a push would deploy the preview as well as its pull request event")
	}

	// A push to the pull request deploys the same preview again.
	again, err := e.d.SyncPreview(ctx, parent, pull(12, "feature/login"))
	if err != nil || again.ID != child.ID {
		t.Fatalf("a second event: %v, app %s", err, again.ID)
	}
	second := e.last(child.ID)
	for second.ID == dep.ID {
		time.Sleep(5 * time.Millisecond)
		second = e.last(child.ID)
	}
	if second.Status != db.DeploySuccess {
		t.Fatalf("the redeployment: %s %q", second.Status, second.Error)
	}
	if list, _ := e.db.Previews(ctx, parent.ID); len(list) != 1 {
		t.Fatalf("%d previews after a second event", len(list))
	}
	// The parent itself is no preview, whatever it deploys.
	e.deploy()
	parentEnv, _, _ := e.fake.File(e.d.at(r).AppDir(parent.ID) + "/env")
	if strings.Contains(parentEnv, "MUSDASH_PULL_REQUEST") || !strings.Contains(parentEnv, "MUSDASH_PREVIEW=0") {
		t.Fatalf("the parent's own variables:\n%s", parentEnv)
	}
}

// The rule that makes previews safe: only code that is in the app's own
// repository, where only people who may push can put it, is built and run
// with the app's variables.
func TestWhatGetsNoPreview(t *testing.T) {
	e, _ := previewEnv(t)
	ctx := context.Background()
	parent := e.reload()
	change := func(mutate func(*source.PullRequest)) source.PullRequest {
		pr := pull(12, "feature/login")
		mutate(&pr)
		return pr
	}
	refused := map[string]source.PullRequest{
		"from a fork":                  change(func(p *source.PullRequest) { p.HeadRepo = "mallory/shop" }),
		"from a fork that was deleted": change(func(p *source.PullRequest) { p.HeadRepo = "" }),
		"of another repository":        change(func(p *source.PullRequest) { p.Repo, p.HeadRepo = "acme/other", "acme/other" }),
		"with no repository named":     change(func(p *source.PullRequest) { p.Repo = "" }),
		"into another branch":          change(func(p *source.PullRequest) { p.BaseBranch = "develop" }),
		"without a number":             change(func(p *source.PullRequest) { p.Number = 0 }),
		"with a branch that is a flag": change(func(p *source.PullRequest) { p.Branch = "--upload-pack=touch /tmp/x" }),
		"with a branch that climbs":    change(func(p *source.PullRequest) { p.Branch = "../../etc" }),
		"with no branch":               change(func(p *source.PullRequest) { p.Branch = "" }),
	}
	before := len(e.fake.Calls())
	for name, pr := range refused {
		if _, err := e.d.SyncPreview(ctx, parent, pr); !errors.Is(err, ErrNoPreview) {
			t.Errorf("a pull request %s: %v, want ErrNoPreview", name, err)
		}
	}
	// And the app's own side of it.
	off := parent
	off.Previews = false
	image := parent
	image.Source = db.SourceImage
	itself := parent
	itself.PreviewOf = "someapp"
	for name, app := range map[string]db.App{"with previews off": off, "deployed from an image": image, "that is a preview": itself} {
		if _, err := e.d.SyncPreview(ctx, app, pull(12, "feature/login")); !errors.Is(err, ErrNoPreview) {
			t.Errorf("an app %s: %v, want ErrNoPreview", name, err)
		}
	}
	if list, _ := e.db.Previews(ctx, parent.ID); len(list) != 0 {
		t.Fatalf("%d previews were made", len(list))
	}
	var jobs int
	e.db.QueryRowContext(ctx, `SELECT count(*) FROM jobs`).Scan(&jobs)
	if jobs != 0 || len(e.fake.Calls()) != before {
		t.Fatalf("refused pull requests queued %d jobs and ran %d commands", jobs, len(e.fake.Calls())-before)
	}
	// The same repository in another case is the same repository.
	if _, err := e.d.SyncPreview(ctx, parent, change(func(p *source.PullRequest) { p.Repo, p.HeadRepo = "Acme/Shop", "ACME/shop" })); err != nil {
		t.Fatalf("the repository's name in another case: %v", err)
	}
}

func TestPreviewsAreLimited(t *testing.T) {
	e, _ := previewEnv(t)
	ctx := context.Background()
	parent := e.reload()
	for n := 1; n <= db.MaxPreviews; n++ {
		if _, err := e.db.CreatePreview(ctx, parent, n, PreviewName(parent.Name, n), "b"+strconv.Itoa(n)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.d.SyncPreview(ctx, parent, pull(99, "one-too-many")); !errors.Is(err, db.ErrPreviewLimit) {
		t.Fatalf("an eleventh preview: %v", err)
	}
	// One that exists is still deployed, and a closed one makes room.
	if _, err := e.d.SyncPreview(ctx, parent, pull(3, "b3")); err != nil {
		t.Fatalf("an existing preview at the limit: %v", err)
	}
	gone, _ := e.db.Preview(ctx, parent.ID, 5)
	if err := e.d.ClosePreview(ctx, gone); err != nil {
		t.Fatal(err)
	}
	e.gone(gone.ID)
	if _, err := e.d.SyncPreview(ctx, parent, pull(99, "now-there-is-room")); err != nil {
		t.Fatalf("after one was closed: %v", err)
	}
	// A preview's name can be taken by something else in the environment.
	other, err := e.db.CreatePreview(ctx, db.App{ID: parent.ID, EnvironmentID: parent.EnvironmentID}, 500, "web", "x")
	if !errors.Is(err, db.ErrNameTaken) && !errors.Is(err, db.ErrPreviewLimit) {
		t.Fatalf("a preview named like its parent: %+v %v", other, err)
	}
}

func TestClosingAPullRequestRemovesItsPreview(t *testing.T) {
	e, rec := previewEnv(t)
	ctx := context.Background()
	// What Docker lists as the images of an app.
	rec.images = testCommit[:12] + "\n"
	e.deploy()
	parent := e.reload()
	child, err := e.d.SyncPreview(ctx, parent, pull(12, "feature/login"))
	if err != nil {
		t.Fatal(err)
	}
	dep := e.last(child.ID)
	child, _ = e.db.AppByID(ctx, child.ID)
	domains, _ := e.db.ListDomains(ctx, db.KindApp, child.ID)
	if dep.Status != db.DeploySuccess || len(domains) != 1 {
		t.Fatalf("%s %q, %d domains", dep.Status, dep.Error, len(domains))
	}

	before := len(e.fake.Calls())
	if err := e.d.ClosePreview(ctx, child); err != nil {
		t.Fatal(err)
	}
	e.gone(child.ID)
	calls := strings.Join(e.since(before), "\n")
	for _, want := range []string{child.Container, "docker rmi " + ImageRepository(child.ID) + ":"} {
		if !strings.Contains(calls, want) {
			t.Errorf("closing did not touch %q:\n%s", want, calls)
		}
	}
	if left, _ := e.db.ListDomains(ctx, db.KindApp, child.ID); len(left) != 0 {
		t.Fatal("the preview's address was kept")
	}
	for _, r := range e.routes().Routes {
		if r.Host == domains[0].Host {
			t.Fatal("the preview's address is still routed")
		}
	}
	// The parent is untouched and still serving.
	if app := e.reload(); app.Container == "" || app.Status != db.AppRunning {
		t.Fatalf("the parent after its preview was closed: %+v", app)
	}
	if strings.Contains(calls, ImageRepository(parent.ID)+":") || strings.Contains(calls, parent.Container) {
		t.Fatalf("closing a preview touched its parent:\n%s", calls)
	}

	// Closing only ever removes a preview. The parent's id, handed to the
	// same job, removes nothing.
	if err := e.d.ClosePreview(ctx, parent); !errors.Is(err, ErrNoPreview) {
		t.Fatalf("closing an app that is no preview: %v", err)
	}
	raw, _ := json.Marshal(closePayload{AppID: parent.ID})
	if err := e.d.runClosePreview(ctx, raw); err == nil {
		t.Fatal("the close job took an ordinary app")
	}
	if _, err := e.db.AppByID(ctx, parent.ID); err != nil {
		t.Fatalf("the parent was removed: %v", err)
	}
	// One that is gone already is not an error: events are redelivered.
	raw, _ = json.Marshal(closePayload{AppID: child.ID})
	if err := e.d.runClosePreview(ctx, raw); err != nil {
		t.Fatalf("closing a preview twice: %v", err)
	}
}

func TestDeletingAnAppRemovesItsPreviews(t *testing.T) {
	e, _ := previewEnv(t)
	ctx := context.Background()
	parent := e.reload()
	var children []db.App
	for _, n := range []int{7, 8} {
		child, err := e.d.SyncPreview(ctx, parent, pull(n, "b"+strconv.Itoa(n)))
		if err != nil {
			t.Fatal(err)
		}
		if dep := e.last(child.ID); dep.Status != db.DeploySuccess {
			t.Fatalf("%s %q", dep.Status, dep.Error)
		}
		children = append(children, child)
	}
	// The row cannot go while they exist: a preview without a parent would
	// be a running app that no page lists.
	if err := e.db.DeleteApp(ctx, parent.ID); !errors.Is(err, db.ErrHasPreviews) {
		t.Fatalf("deleting the row of an app with previews: %v", err)
	}
	if err := e.d.Destroy(ctx, parent.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{parent.ID, children[0].ID, children[1].ID} {
		if _, err := e.db.AppByID(ctx, id); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("app %s is still there: %v", id, err)
		}
	}
	var domains int
	e.db.QueryRowContext(ctx, `SELECT count(*) FROM domains`).Scan(&domains)
	if domains != 0 {
		t.Fatalf("%d domains were left", domains)
	}
}

// fakeComments stands in for GitHub's pull request comments.
type fakeComments struct {
	mu    sync.Mutex
	calls []string
	fail  error
}

func (f *fakeComments) CommentOnPullRequest(_ context.Context, appID int64, _ []byte, owner, repo string, number int, commentID int64, text string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join([]string{strconv.FormatInt(appID, 10), owner + "/" + repo, strconv.Itoa(number), strconv.FormatInt(commentID, 10), text}, "|"))
	if f.fail != nil {
		return 0, f.fail
	}
	if commentID > 0 {
		return commentID, nil
	}
	return 4711, nil
}

func (f *fakeComments) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func TestAPreviewTellsItsPullRequestWhereItIs(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.fake.Handle = (&gitEnvRecorder{}).handle
	e.d.Tokens = &stubTokens{token: "ghs_token"}
	comments := &fakeComments{}
	e.d.Comments = comments
	src, _ := e.db.StartGitSource(ctx, e.team, "musdash-test", "state1")
	src.AppID, src.Slug = 777, "musdash-test"
	src.PrivateKey, _ = e.d.Box.SealString("-----BEGIN RSA PRIVATE KEY-----\nMII\n-----END RSA PRIVATE KEY-----\n")
	if err := e.db.FinishGitSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	e.gitApp(func(a *db.App) { a.GitSourceID = src.ID })
	e.db.SetAppPreviews(ctx, e.team, e.app.ID, true, "preview.example.com")
	parent := e.reload()

	// The parent's own deployments say nothing to anyone.
	e.deploy()
	if n := len(comments.all()); n != 0 {
		t.Fatalf("%d comments for an app that is no preview", n)
	}

	child, err := e.d.SyncPreview(ctx, parent, pull(12, "feature/login"))
	if err != nil {
		t.Fatal(err)
	}
	first := e.last(child.ID)
	if first.Status != db.DeploySuccess {
		t.Fatalf("%s %q", first.Status, first.Error)
	}
	// Under the app's preview domain, over HTTPS.
	domains, _ := e.db.ListDomains(ctx, db.KindApp, child.ID)
	if len(domains) != 1 || domains[0].Host != "pr-12-web.preview.example.com" || !domains[0].TLS {
		t.Fatalf("address: %+v", domains)
	}
	wait := func(n int) []string {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for len(comments.all()) < n && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		got := comments.all()
		if len(got) != n {
			t.Fatalf("%d comments, want %d: %q", len(got), n, got)
		}
		return got
	}
	got := wait(1)
	if !strings.HasPrefix(got[0], "777|acme/shop|12|0|") || !strings.Contains(got[0], "https://pr-12-web.preview.example.com") || !strings.Contains(got[0], testCommit[:7]) {
		t.Fatalf("the comment: %q", got[0])
	}
	if app, _ := e.db.AppByID(ctx, child.ID); app.PRCommentID != 4711 {
		t.Fatalf("comment id %d was recorded", app.PRCommentID)
	}

	// The next deployment rewrites that comment.
	if _, err := e.d.SyncPreview(ctx, parent, pull(12, "feature/login")); err != nil {
		t.Fatal(err)
	}
	if got = wait(2); !strings.HasPrefix(got[1], "777|acme/shop|12|4711|") {
		t.Fatalf("the second comment: %q", got[1])
	}

	// GitHub refusing the comment (an App that may not write to pull
	// requests) does not fail the deployment.
	comments.mu.Lock()
	comments.fail = errors.New("GitHub answered 403: Resource not accessible by integration")
	comments.mu.Unlock()
	if _, err := e.d.SyncPreview(ctx, parent, pull(12, "feature/login")); err != nil {
		t.Fatal(err)
	}
	wait(3)
	if dep := e.last(child.ID); dep.Status != db.DeploySuccess {
		t.Fatalf("a refused comment failed the deployment: %s %q", dep.Status, dep.Error)
	}
	comments.mu.Lock()
	comments.fail = nil
	comments.mu.Unlock()

	// Closed: the comment says so.
	child, _ = e.db.AppByID(ctx, child.ID)
	if err := e.d.ClosePreview(ctx, child); err != nil {
		t.Fatal(err)
	}
	e.gone(child.ID)
	if got = wait(4); !strings.HasPrefix(got[3], "777|acme/shop|12|4711|") || !strings.Contains(got[3], "removed") {
		t.Fatalf("the closing comment: %q", got[3])
	}
}

func TestPreviewNames(t *testing.T) {
	if got := PreviewName("web", 12); got != "web-pr-12" {
		t.Fatal(got)
	}
	app := db.App{Name: "web", PreviewDomain: "preview.example.com"}
	if got := PreviewHost(app, db.Server{}, 12); got != "pr-12-web.preview.example.com" {
		t.Fatal(got)
	}
	app.PreviewDomain = ""
	got := PreviewHost(app, db.Server{IP: "203.0.113.7"}, 12)
	if !strings.HasPrefix(got, "pr-12-web-") || !strings.HasSuffix(got, ".203.0.113.7.sslip.io") {
		t.Fatal(got)
	}
	// Two of them are not the same address.
	if got == PreviewHost(app, db.Server{IP: "203.0.113.7"}, 12) {
		t.Fatal("generated preview addresses repeat")
	}
}
