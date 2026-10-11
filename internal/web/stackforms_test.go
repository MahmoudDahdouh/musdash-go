package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/deploy"
)

// needsGreeting is a stack whose file requires a variable.
const needsGreeting = `services:
  web:
    image: nginx:alpine
    environment:
      GREETING: ${GREETING:?say what the page greets with}
      TOKEN: ${TOKEN:?the token}
      REGION: ${REGION:-eu}
`

// A stack whose pasted file requires a variable that has no value is not
// deployed: its deployment could only fail.
func TestAStackIsNotDeployedWithoutItsVariables(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	envPath := "/projects/" + projectID + "/env/" + env.ID
	page := envPath + "/service/new?template=custom"
	queued := func() (n int) {
		t.Helper()
		if err := a.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE kind = ?`, deploy.JobService).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	form := func(change url.Values) url.Values {
		f := url.Values{"template": {db.TemplateCustom}, "name": {"greeter"}, "compose": {needsGreeting}, "variables": {"GREETING=hello"}, "deploy": {"1"}}
		for k, v := range change {
			f[k] = v
		}
		return f
	}

	// The form says which, where the value is given, and keeps what was
	// typed.
	res, body := a.post(page, envPath+"/service", form(nil))
	wantStatus(t, res, http.StatusUnprocessableEntity)
	wantAll(t, "the refusal", body, "The file needs a value for TOKEN.", "untick Deploy", "GREETING=hello", "say what the page greets with")
	res, body = a.post(page, envPath+"/service", form(url.Values{"variables": {""}}))
	wantStatus(t, res, http.StatusUnprocessableEntity)
	wantAll(t, "the refusal of two", body, "The file needs a value for GREETING and TOKEN.")
	if queued() != 0 {
		t.Fatal("a refused form queued a deployment")
	}

	// Without Deploy it is made, to be given its values later.
	res, _ = a.post(page, envPath+"/service", form(url.Values{"deploy": {""}}))
	wantStatus(t, res, http.StatusSeeOther)
	base := envPath + "/service/" + createdID(res, db.KindService)

	// Deploy leads to where the value is entered, and queues nothing.
	res, _ = a.post(base, base+"/deploy", nil)
	wantStatus(t, res, http.StatusSeeOther)
	if got := res.Header.Get("Location"); got != base+"/compose" {
		t.Errorf("Deploy led to %s", got)
	}
	_, tab := a.get(base + "/compose")
	wantAll(t, "the note", tab, "Not deployed: the Compose file needs a value for TOKEN.")
	if queued() != 0 {
		t.Fatal("Deploy queued a deployment that can only fail")
	}
	// Save and deploy saves, and does the same.
	res, _ = a.post(base+"/compose", base+"/compose", url.Values{"compose": {needsGreeting}, "variables": {"GREETING=hi"}, "deploy": {"1"}})
	wantStatus(t, res, http.StatusSeeOther)
	if got := res.Header.Get("Location"); got != base+"/compose" || queued() != 0 {
		t.Errorf("Save and deploy led to %s with %d queued", got, queued())
	}
	// With every value it is deployed.
	res, _ = a.post(base+"/compose", base+"/compose", url.Values{"compose": {needsGreeting}, "variables": {"GREETING=hi\nTOKEN=t"}, "deploy": {"1"}})
	wantStatus(t, res, http.StatusSeeOther)
	if got := res.Header.Get("Location"); got != base || queued() != 1 {
		t.Errorf("with its values: led to %s with %d queued", got, queued())
	}
}

// The Compose tab hides stored variables until they are asked for. With
// none stored there is nothing to hide, and the box is there to type into.
func TestTheVariablesBoxIsShownWhenNothingIsStored(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	envPath := "/projects/" + projectID + "/env/" + env.ID
	plain := "services:\n  web:\n    image: nginx:alpine\n"

	empty := a.newService(projectID, env, "empty", url.Values{"compose": {plain}, "variables": {""}})
	_, tab := a.get(envPath + "/service/" + empty.ID + "/compose")
	if !strings.Contains(tab, `<textarea id="variables"`) || strings.Contains(tab, `name="variables_kept"`) || strings.Contains(tab, "Show and edit") {
		t.Error("a stack with no variables does not show the box")
	}

	held := a.newService(projectID, env, "held", url.Values{"compose": {plain}, "variables": {"API_KEY=k-123"}})
	_, tab = a.get(envPath + "/service/" + held.ID + "/compose")
	if strings.Contains(tab, `<textarea id="variables"`) || !strings.Contains(tab, `name="variables_kept" value="1"`) || strings.Contains(tab, "k-123") {
		t.Error("a stack with variables shows them unasked")
	}
}

// The form of a stack from Git is in two groups, the repository first, and
// takes its name from the repository that is chosen.
func TestTheGitStackFormIsGrouped(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	_, form := a.get("/projects/" + projectID + "/env/" + env.ID + "/service/new?template=git")
	wantAll(t, "the form", form, `class="card card-groups"`, `>Repository</h2>`, `>Stack</h2>`, `data-suggest-here`)
	if repo, name := strings.Index(form, `id="repo"`), strings.Index(form, `id="name"`); repo < 0 || name < repo {
		t.Error("the name comes before the repository")
	}
	// A pasted file has nothing to take a name from: its form is as it was.
	_, form = a.get("/projects/" + projectID + "/env/" + env.ID + "/service/new?template=custom")
	if strings.Contains(form, "card-groups") || !strings.Contains(form, `id="name"`) {
		t.Error("the form of a pasted file changed")
	}
}
