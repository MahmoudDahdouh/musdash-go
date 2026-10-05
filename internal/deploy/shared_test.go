package deploy

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
)

func TestExpandShared(t *testing.T) {
	known := map[string]string{
		"team.TOKEN":        "t0k",
		"project.DB_HOST":   "db.internal",
		"environment.STAGE": "production",
		"server.REGION":     "fra",
		// A shared value that itself looks like a reference is not read again.
		"team.LOOP":  "{{team.LOOP}}",
		"team.OTHER": "{{project.DB_HOST}}",
		"team.EMPTY": "",
	}
	lookup := func(scope, name string) (string, bool) {
		v, ok := known[scope+"."+name]
		return v, ok
	}
	for _, tc := range []struct {
		in, want string
		missing  []SharedRef
	}{
		{"plain", "plain", nil},
		{"", "", nil},
		{"{{team.TOKEN}}", "t0k", nil},
		{"{{ team.TOKEN }}", "t0k", nil},
		{"postgres://{{project.DB_HOST}}:5432/{{environment.STAGE}}?r={{server.REGION}}", "postgres://db.internal:5432/production?r=fra", nil},
		{"{{team.TOKEN}}{{team.TOKEN}}", "t0kt0k", nil},
		{"a{{team.EMPTY}}b", "ab", nil},
		// No loop, no second pass.
		{"{{team.LOOP}}", "{{team.LOOP}}", nil},
		{"{{team.OTHER}}", "{{project.DB_HOST}}", nil},
		// Not this form: left alone, and not reported.
		{"{{ .Name }}", "{{ .Name }}", nil},
		{"{{TOKEN}}", "{{TOKEN}}", nil},
		{"{{org.TOKEN}}", "{{org.TOKEN}}", nil},
		{"{{team.}}", "{{team.}}", nil},
		{"{{team.9X}}", "{{team.9X}}", nil},
		{"{{team.TOKEN.more}}", "{{team.TOKEN.more}}", nil},
		{"{team.TOKEN}", "{team.TOKEN}", nil},
		{"{{Team.TOKEN}}", "{{Team.TOKEN}}", nil},
		{"{{team.TOKEN}", "{{team.TOKEN}", nil},
		// A name is exact.
		{"{{team.token}}", "{{team.token}}", []SharedRef{{"team", "token"}}},
		// Missing names stay in place and are reported once each.
		{"{{project.NOPE}}-{{team.TOKEN}}-{{project.NOPE}}-{{server.X}}", "{{project.NOPE}}-t0k-{{project.NOPE}}-{{server.X}}",
			[]SharedRef{{"project", "NOPE"}, {"server", "X"}}},
		// Braces around a reference.
		{"{{{team.TOKEN}}}", "{t0k}", nil},
		// A backslash keeps the text for the app itself, known name or not.
		{`\{{team.TOKEN}}`, "{{team.TOKEN}}", nil},
		{`\{{ environment.name }}`, "{{ environment.name }}", nil},
		{`a \{{team.NOPE}} b {{team.TOKEN}}`, "a {{team.NOPE}} b t0k", nil},
		// Only directly before the braces, and only one is taken.
		{`\\{{team.TOKEN}}`, `\{{team.TOKEN}}`, nil},
		{`\ {{team.TOKEN}}`, `\ t0k`, nil},
		{`\{{ .Name }}`, `\{{ .Name }}`, nil},
	} {
		got, missing := ExpandShared(tc.in, lookup)
		if got != tc.want || !reflect.DeepEqual(missing, tc.missing) {
			t.Errorf("ExpandShared(%q) = %q, %v; want %q, %v", tc.in, got, missing, tc.want, tc.missing)
		}
	}
}

func TestSharedRefs(t *testing.T) {
	got := SharedRefs("{{team.A}} {{ project.B }} {{team.A}} {{ .C }} {{server.D}}{{environment.E}}")
	want := []SharedRef{{"team", "A"}, {"project", "B"}, {"server", "D"}, {"environment", "E"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := SharedRefs(`\{{team.A}} {{team.B}}`); !reflect.DeepEqual(got, []SharedRef{{"team", "B"}}) {
		t.Fatalf("an escaped name was listed: %v", got)
	}
	if SharedRefs("no braces here") != nil || SharedRefs("{{nothing}}") != nil {
		t.Fatal("found references where there are none")
	}
	if s := (SharedRef{"team", "A"}).String(); s != "{{team.A}}" {
		t.Fatalf("String: %s", s)
	}
}

// share stores shared variables for one scope of the test team.
func (e *env) share(scope, scopeID string, vars map[string]string) {
	e.t.Helper()
	var list []db.EnvVar
	for k, v := range vars {
		sealed, err := e.d.Box.SealString(v)
		if err != nil {
			e.t.Fatal(err)
		}
		list = append(list, db.EnvVar{Key: k, Value: sealed})
	}
	if err := e.db.ReplaceSharedVars(context.Background(), e.team, scope, scopeID, list); err != nil {
		e.t.Fatal(err)
	}
}

// setEnv stores an app's variables; a name starting with BUILD_ is a
// build-time one.
func (e *env) setEnv(appID string, vars map[string]string) {
	e.t.Helper()
	var list []db.EnvVar
	for k, v := range vars {
		sealed, _ := e.d.Box.SealString(v)
		list = append(list, db.EnvVar{Key: k, Value: sealed, BuildTime: strings.HasPrefix(k, "BUILD_")})
	}
	if err := e.db.ReplaceEnvVars(context.Background(), db.KindApp, appID, list); err != nil {
		e.t.Fatal(err)
	}
}

// envFile reads what a deployment wrote for a container's environment.
func (e *env) envFile(id string) map[string]string {
	e.t.Helper()
	raw, mode, ok := e.fake.File(filepath.Join(e.cfg.AppDir(id), "env"))
	if !ok {
		e.t.Fatal("no env file was written")
	}
	if mode != 0o600 {
		e.t.Fatalf("env file mode %o", mode)
	}
	out := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		k, v, _ := strings.Cut(line, "=")
		out[k] = v
	}
	return out
}

// shareEverywhere gives each of the app's four scopes a variable, and
// another team variables that must never be reachable.
func (e *env) shareEverywhere() {
	e.t.Helper()
	ctx := context.Background()
	env, err := e.db.Environment(ctx, e.team, e.app.EnvironmentID)
	if err != nil {
		e.t.Fatal(err)
	}
	e.share(db.ScopeTeam, e.team, map[string]string{"TOKEN": "team-token"})
	e.share(db.ScopeProject, env.ProjectID, map[string]string{"DB_HOST": "db.internal"})
	e.share(db.ScopeEnvironment, env.ID, map[string]string{"STAGE": "production"})
	e.share(db.ScopeServer, e.server.ID, map[string]string{"REGION": "fra"})

	// Another team: its own team variable, and rows that claim this team's
	// project, environment and server as their scope.
	if _, err := e.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`); err != nil {
		e.t.Fatal(err)
	}
	theirs, _ := e.d.Box.SealString("theirs")
	for scope, id := range map[string]string{db.ScopeTeam: "otherteam", db.ScopeProject: env.ProjectID, db.ScopeEnvironment: env.ID, db.ScopeServer: e.server.ID} {
		if err := e.db.ReplaceSharedVars(ctx, "otherteam", scope, id, []db.EnvVar{{Key: "THEIRS", Value: theirs}}); err != nil {
			e.t.Fatal(err)
		}
	}
}

func TestSharedVariablesReachAnApp(t *testing.T) {
	e := newEnv(t)
	e.shareEverywhere()
	e.setEnv(e.app.ID, map[string]string{
		"A":     "{{team.TOKEN}}",
		"B":     "{{project.DB_HOST}}",
		"C":     "{{ environment.STAGE }}",
		"D":     "{{server.REGION}}",
		"URL":   "postgres://{{project.DB_HOST}}/{{environment.STAGE}}",
		"PLAIN": "as it is",
		"TMPL":  "{{ .Values.name }}",
		"OWN":   `\{{ environment.name }}`,
	})
	dep := e.deploy()
	if dep.Status != db.DeploySuccess {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	want := map[string]string{
		"A": "team-token", "B": "db.internal", "C": "production", "D": "fra",
		"URL": "postgres://db.internal/production", "PLAIN": "as it is", "TMPL": "{{ .Values.name }}",
		"OWN": "{{ environment.name }}",
	}
	if got := e.envFile(e.app.ID); !reflect.DeepEqual(got, want) {
		t.Fatalf("env file:\n got %v\nwant %v", got, want)
	}
	// What is stored still names the shared variable: the next deployment
	// reads it again.
	e.share(db.ScopeTeam, e.team, map[string]string{"TOKEN": "rotated"})
	if dep := e.deploy(); dep.Status != db.DeploySuccess {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	if got := e.envFile(e.app.ID)["A"]; got != "rotated" {
		t.Fatalf("after the shared value changed, A = %q", got)
	}
}

func TestAMissingSharedVariableFailsTheDeploy(t *testing.T) {
	for name, value := range map[string]string{
		"a name nobody set":           "{{project.NOPE}}",
		"another team's team":         "{{team.THEIRS}}",
		"another team's project row":  "{{project.THEIRS}}",
		"another team's environment":  "{{environment.THEIRS}}",
		"another team's server row":   "{{server.THEIRS}}",
		"a name of a different scope": "{{server.TOKEN}}",
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.shareEverywhere()
			e.setEnv(e.app.ID, map[string]string{"OK": "{{team.TOKEN}}", "BAD": value})
			dep := e.deploy()
			if dep.Status != db.DeployFailed || !strings.Contains(dep.Error, "BAD") || !strings.Contains(dep.Error, value) {
				t.Fatalf("%s %q", dep.Status, dep.Error)
			}
			if strings.Contains(dep.Error, "theirs") || strings.Contains(dep.Error, "team-token") {
				t.Fatalf("the error carries a value: %q", dep.Error)
			}
			for _, call := range e.fake.Calls() {
				if strings.HasPrefix(call, "docker run") {
					t.Fatalf("a container was started: %s", call)
				}
			}
		})
	}
}

func TestSharedVariablesInBuildArguments(t *testing.T) {
	e := newEnv(t)
	rec := &gitEnvRecorder{}
	e.fake.Handle = rec.handle
	e.shareEverywhere()
	e.gitApp(nil)
	e.setEnv(e.app.ID, map[string]string{"BUILD_TOKEN": "x-{{team.TOKEN}}", "RUN": "{{server.REGION}}"})
	dep := e.deploy()
	if dep.Status != db.DeploySuccess {
		t.Fatalf("%s %q\n%s", dep.Status, dep.Error, e.log(dep))
	}
	found := false
	for _, kv := range rec.envs["docker build"] {
		if kv == "BUILD_TOKEN=x-team-token" {
			found = true
		}
		if strings.Contains(kv, "{{team.") {
			t.Fatalf("the build got the name, not the value: %s", kv)
		}
	}
	if !found {
		t.Fatalf("the build's environment: %v", rec.envs["docker build"])
	}
	if got := e.envFile(e.app.ID); got["RUN"] != "fra" || got["BUILD_TOKEN"] != "" {
		t.Fatalf("env file: %v", got)
	}
	if strings.Contains(e.log(dep), "team-token") {
		t.Fatal("a shared value reached the deployment log")
	}

	// A missing name in a build-time variable fails before anything is built.
	e.setEnv(e.app.ID, map[string]string{"BUILD_TOKEN": "{{project.NOPE}}"})
	before := len(e.fake.Calls())
	dep = e.deploy()
	if dep.Status != db.DeployFailed || !strings.Contains(dep.Error, "{{project.NOPE}}") {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	for _, call := range e.fake.Calls()[before:] {
		if strings.HasPrefix(call, "docker build") {
			t.Fatal("the image was built without its variable")
		}
	}
}

func TestAPreviewResolvesSharedVariablesAsItsParent(t *testing.T) {
	e, _ := previewEnv(t)
	e.shareEverywhere()
	e.setEnv(e.app.ID, map[string]string{"STAGE": "{{environment.STAGE}}", "HOST": "{{project.DB_HOST}}"})
	child, err := e.d.SyncPreview(context.Background(), e.reload(), pull(7, "feature/login"))
	if err != nil {
		t.Fatal(err)
	}
	if dep := e.last(child.ID); dep.Status != db.DeploySuccess {
		t.Fatalf("%s %q", dep.Status, dep.Error)
	}
	got := e.envFile(child.ID)
	if got["STAGE"] != "production" || got["HOST"] != "db.internal" || got["MUSDASH_PREVIEW"] != "1" {
		t.Fatalf("the preview's env file: %v", got)
	}
}

func TestSharedVariablesReachAService(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.shareEverywhere()
	s, _ := e.scriptedService(false)
	vars, err := e.d.ServiceVariables(s)
	if err != nil {
		t.Fatal(err)
	}
	vars["SMTP_HOST"] = "mail.{{project.DB_HOST}}"
	sealed, _ := e.d.SealServiceVariables(vars)
	if err := e.db.SetServiceVariables(ctx, s.ID, sealed); err != nil {
		t.Fatal(err)
	}
	s, _ = e.db.ServiceByID(ctx, s.ID)

	got := e.deployService(s, 10*time.Second)
	if got.Status != db.AppRunning {
		t.Fatalf("%+v\n%s", got, e.serviceLog(s))
	}
	raw, _, ok := e.fake.File(filepath.Join(e.cfg.AppDir(s.ID), sandboxEnvFile))
	if !ok || !strings.Contains(raw, "SMTP_HOST=mail.db.internal\n") {
		t.Fatalf("the stack's variables file:\n%s", raw)
	}
	// What is stored keeps the name.
	stored, _ := e.d.ServiceVariables(got)
	if stored["SMTP_HOST"] != "mail.{{project.DB_HOST}}" {
		t.Fatalf("stored: %q", stored["SMTP_HOST"])
	}

	// A missing name fails the deployment and says which.
	vars["SMTP_HOST"] = "{{team.THEIRS}}"
	sealed, _ = e.d.SealServiceVariables(vars)
	e.db.SetServiceVariables(ctx, s.ID, sealed)
	s, _ = e.db.ServiceByID(ctx, s.ID)
	got = e.deployService(s, 10*time.Second)
	if got.Status != db.AppFailed || !strings.Contains(e.serviceLog(s), "{{team.THEIRS}}") {
		t.Fatalf("%+v\n%s", got, e.serviceLog(s))
	}
}

func TestMissingShared(t *testing.T) {
	e := newEnv(t)
	e.shareEverywhere()
	missing, err := e.d.MissingShared(context.Background(), e.app.EnvironmentID, e.app.ServerID,
		[]string{"{{team.TOKEN}} {{team.NOPE}}", "{{project.THEIRS}}", "plain", "{{team.NOPE}}"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []SharedRef{{"team", "NOPE"}, {"project", "THEIRS"}}; !reflect.DeepEqual(missing, want) {
		t.Fatalf("got %v, want %v", missing, want)
	}
}
