package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/deploy"
)

// The Git form of a new app offers Docker Compose under Build with. What
// it makes is a stack: a service from Git, read from the Compose file the
// form names under its base directory.
func TestGitFormMakesAStack(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	envPath := "/projects/" + projectID + "/env/" + env.ID
	page := envPath + "/app/new?source=git"

	_, form := a.get(page)
	wantAll(t, "the form", form,
		`data-value="compose"`, "Docker Compose",
		// The folder is looked at for a Compose file too, and a name typed
		// into the field is sent along so it can be left alone.
		`name="stack" value="1"`, `hx-include="#git-where, #compose_path"`,
		// An app's fields and a stack's, each in a fieldset: the stack's
		// start disabled, so its required field does not stop the form.
		`data-when="build_pack" data-is="dockerfile static nixpacks railpack">`,
		`data-when="build_pack" data-is="compose" hidden disabled>`,
		`id="compose_path"`, `value="docker-compose.yml"`, `id="variables"`, `name="connect_env"`,
	)

	stack := func(name string, change url.Values) url.Values {
		f := url.Values{
			"source": {"git"}, "name": {name}, "access": {"public"},
			"repo": {"https://github.com/Acme/Shop"}, "branch": {"main"}, "auto_deploy": {"1"},
			"build_pack": {"compose"}, "base_dir": {"deploy"}, "compose_path": {"compose.yaml"},
			"variables": {"GREETING=hello"}, "connect_env": {"1"},
			// What the app's fields would send if they were not disabled.
			"port": {"3000"}, "domain": {"x.example.com"},
		}
		for k, v := range change {
			f[k] = v
		}
		return f
	}

	res, body := a.post(page, envPath+"/app", stack("shop", nil))
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d\n%s", res.StatusCode, body)
	}
	id := createdID(res, db.KindService)
	svc, err := a.db.ServiceByID(ctx, id)
	if err != nil {
		t.Fatalf("the form made no service: %v (led to %s)", err, res.Header.Get("Location"))
	}
	if !svc.FromGit() || svc.ComposePath != "deploy/compose.yaml" || svc.RepoName != "acme/shop" || svc.Branch != "main" || !svc.AutoDeploy || !svc.ConnectEnv || svc.Compose != "" {
		t.Errorf("the service: %+v", svc)
	}
	if vars, err := a.server.Deploy.ServiceVariables(svc); err != nil || vars["GREETING"] != "hello" {
		t.Errorf("its variables: %v, %v", vars, err)
	}
	// No app was made beside it, and no domain.
	var apps, domains int
	a.db.QueryRow(`SELECT COUNT(*) FROM apps`).Scan(&apps)
	a.db.QueryRow(`SELECT COUNT(*) FROM domains`).Scan(&domains)
	if apps != 0 || domains != 0 {
		t.Errorf("%d apps and %d domains beside the stack", apps, domains)
	}
	// Deploy was not ticked: nothing is queued, and the page says how.
	var queued int
	a.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE kind = ?`, deploy.JobService).Scan(&queued)
	if queued != 0 {
		t.Errorf("%d deployments queued without Deploy", queued)
	}
	_, overview := a.get(res.Header.Get("Location"))
	wantAll(t, "the stack's page", overview, "Service created", "acme/shop @ main")

	// Ticked, it is deployed.
	res, body = a.post(page, envPath+"/app", stack("shop-two", url.Values{"deploy": {"1"}, "base_dir": {""}}))
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("create and deploy: %d\n%s", res.StatusCode, body)
	}
	second, err := a.db.ServiceByID(ctx, createdID(res, db.KindService))
	if err != nil || second.ComposePath != "compose.yaml" {
		t.Fatalf("the second service: %+v, %v", second, err)
	}
	a.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE kind = ?`, deploy.JobService).Scan(&queued)
	if queued != 1 {
		t.Errorf("%d deployments queued, want 1", queued)
	}

	// What is refused comes back on the app's form, with Docker Compose
	// still chosen and the stack's fields as they were sent.
	for what, c := range map[string]struct {
		change url.Values
		want   string
	}{
		"a path out of the repository":    {url.Values{"compose_path": {"../compose.yaml"}}, "Enter the file&#39;s path from the base directory"},
		"no path":                         {url.Values{"compose_path": {""}}, "Enter the file&#39;s path from the base directory"},
		"a base directory out of it":      {url.Values{"base_dir": {"../.."}}, "Enter a path inside the repository"},
		"no repository":                   {url.Values{"repo": {"not a repository"}}, `id="repo-error"`},
		"a taken name":                    {url.Values{"name": {"shop"}}, "already has an app, database or service called shop"},
		"a generated variable":            {url.Values{"variables": {"SERVICE_PASSWORD_DB=mine"}}, "is filled in by musdash"},
		"a source the form did not offer": {url.Values{"access": {"source:nobodys"}}, "Choose how the repository is read"},
		// Each part is a path; together they must still be one, and name a
		// file.
		"a path that is the folder itself": {url.Values{"compose_path": {"."}}, "Enter the file&#39;s path from the base directory"},
		"two parts too long together":      {url.Values{"base_dir": {strings.Repeat("a", 250)}, "compose_path": {strings.Repeat("b", 250)}}, "Enter the file&#39;s path from the base directory"},
	} {
		res, body := a.post(page, envPath+"/app", stack("other", c.change))
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, c.want) {
			t.Errorf("%s: %d, want 422 with %q", what, res.StatusCode, c.want)
			continue
		}
		wantAll(t, "the form after "+what, body,
			"New app from a Git repository", `name="build_pack" value="compose"`,
			`data-when="build_pack" data-is="compose">`,
			`data-when="build_pack" data-is="dockerfile static nixpacks railpack" hidden disabled>`,
		)
	}
	var services int
	a.db.QueryRow(`SELECT COUNT(*) FROM services`).Scan(&services)
	if services != 2 {
		t.Errorf("%d services after the refusals, want 2", services)
	}

	// An app that exists does not become a stack: its Source form has no
	// such build, and does not ask for one to be looked for.
	app := a.newGitApp(projectID, env, "web", nil)
	_, settings := a.get(envPath + "/app/" + app.ID + "/settings")
	if strings.Contains(settings, `data-value="compose"`) || strings.Contains(settings, `name="stack"`) || !strings.Contains(settings, `hx-include="#git-where"`) {
		t.Error("an app's Source form offers Docker Compose")
	}
	res, _ = a.post(envPath+"/app/"+app.ID+"/settings", envPath+"/app/"+app.ID+"/source", url.Values{
		"access": {"public"}, "repo": {"https://github.com/Acme/Shop"}, "branch": {"main"}, "build_pack": {"compose"},
	})
	wantStatus(t, res, http.StatusUnprocessableEntity)
}

// A folder with a Compose file and nothing that builds an app is a stack:
// Build with is set to Docker Compose and the field takes the file's name,
// on the form that offers it and nowhere else.
func TestAStackIsWorkedOut(t *testing.T) {
	a := newApp(t, false)
	a.server.GitHub = fakeGitHubRepos(t, `[]`)
	a.setup()
	src := a.githubSource(firstTeam(t, a), "acme")
	detect := func(extra url.Values) string {
		t.Helper()
		q := url.Values{"access": {"source:" + src.ID}, "repo": {"https://github.com/acme/shop"}, "branch": {"main"}}
		for k, v := range extra {
			q[k] = v
		}
		res, body := a.get("/sources/detect?" + q.Encode())
		wantStatus(t, res, http.StatusOK)
		return body
	}
	const field = `id="compose_path"`

	// Found, and the field, which holds the form's own default, takes it.
	got := detect(url.Values{"stack": {"1"}, "base_dir": {"stack"}, "compose_path": {"docker-compose.yml"}})
	wantAll(t, "a stack", got, `data-pick="build_pack"`, `data-value="compose"`, ">compose.yaml<", "deployed as a stack",
		field, `value="compose.yaml"`, `hx-swap-oob="true"`)
	// An empty field takes it too.
	if got := detect(url.Values{"stack": {"1"}, "base_dir": {"stack"}}); !strings.Contains(got, field) {
		t.Errorf("an empty field was not filled:\n%s", got)
	}
	// A path a person typed is theirs.
	got = detect(url.Values{"stack": {"1"}, "base_dir": {"stack"}, "compose_path": {"deploy/prod.yaml"}})
	if !strings.Contains(got, `data-value="compose"`) || strings.Contains(got, field) {
		t.Errorf("a typed path was replaced:\n%s", got)
	}
	// A Dockerfile beside the Compose file: an app.
	got = detect(url.Values{"stack": {"1"}, "base_dir": {"both"}})
	if !strings.Contains(got, `data-value="dockerfile"`) || strings.Contains(got, field) {
		t.Errorf("a folder with a Dockerfile:\n%s", got)
	}
	// A form that does not offer a stack is not told of one.
	got = detect(url.Values{"base_dir": {"stack"}})
	if strings.Contains(got, "compose") {
		t.Errorf("an app's Source form was told of a stack:\n%s", got)
	}
}
