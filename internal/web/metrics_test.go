package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
)

const hostReading = `cpu  1000 0 500 8000 500 0 0 0 0 0
0.52
2
MemTotal:        2030000 kB
MemAvailable:    203000 kB
/dev/vda1         25000000 10000000  14000000      42% /
cpu  1040 0 520 8030 510 0 0 0 0 0
`

// measured makes the scripted server answer questions about usage.
func (a *app) measured(stats string, ps func() string) {
	prev := a.fake.Handle
	a.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		switch {
		case c.Name == "sh" && strings.Contains(line, "/proc/stat"):
			return hostReading, nil
		case strings.HasPrefix(line, "docker stats"):
			return stats, nil
		case strings.HasPrefix(line, "docker ps --all") && ps != nil:
			return ps(), nil
		}
		return prev(line, c)
	}
}

func TestAppMetrics(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")

	// Nothing runs yet: nothing to ask the server about.
	idle := a.newApp(projectID, env, "idle", false, nil)
	if _, page := a.get("/apps/" + idle + "/metrics"); !strings.Contains(page, "Nothing is running") || strings.Contains(page, "/metrics/now") {
		t.Fatal("the metrics page of an app that is not running asks for a reading")
	}

	appID := a.newApp(projectID, env, "web", true, nil)
	app, _ := a.db.AppByID(ctx, appID)
	a.measured(app.Container+"\t12.50%\t100MiB / 512MiB\t1.2kB / 3MB\t4.1MB / 0B\t7\n", nil)

	_, page := a.get("/apps/" + appID + "/metrics")
	for _, want := range []string{`hx-get="/apps/` + appID + `/metrics/now"`, "sampling is switched off", `href="/apps/` + appID + `/metrics"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the metrics page lacks %q", want)
		}
	}
	res, now := a.get("/apps/" + appID + "/metrics/now")
	wantStatus(t, res, http.StatusOK)
	for _, want := range []string{"12.5%", "100 MiB of 512 MiB", "1.2 KiB in", "7 processes", `hx-get="/apps/` + appID + `/metrics/now?n=1"`, `hx-trigger="every 5s"`} {
		if !strings.Contains(now, want) {
			t.Errorf("the reading lacks %q:\n%s", want, now)
		}
	}
	// The server was asked about this app's container and no other.
	asked := ""
	for _, call := range a.fake.Calls() {
		if strings.HasPrefix(call, "docker stats") {
			asked = call
		}
	}
	if !strings.HasSuffix(asked, " -- "+app.Container) {
		t.Fatalf("asked %q", asked)
	}
	// A page that has been open long enough stops asking.
	if _, last := a.get("/apps/" + appID + "/metrics/now?n=119"); !strings.Contains(last, "n=120") {
		t.Fatal("the reading before the last does not ask for one more")
	}
	if _, paused := a.get("/apps/" + appID + "/metrics/now?n=120"); strings.Contains(paused, "hx-get") || !strings.Contains(paused, "Paused") {
		t.Fatalf("a page open for a long time is still asking:\n%s", paused)
	}
	for _, n := range []string{"-5", "x", "99999999999999999999"} {
		if res, _ := a.get("/apps/" + appID + "/metrics/now?n=" + n); res.StatusCode != http.StatusOK {
			t.Errorf("n=%s: %d", n, res.StatusCode)
		}
	}

	// A server that answers nonsense gets none of it shown.
	a.measured("<script>alert(1)</script>\t1%\t1MiB / 1MiB\t0B / 0B\t0B / 0B\t1\n"+app.Container+"\t<b>\t-- / --\t--\t--\t--\n", nil)
	if _, now := a.get("/apps/" + appID + "/metrics/now"); strings.Contains(now, "script") || strings.Contains(now, "<b>") || !strings.Contains(now, "Nothing of this is running") {
		t.Fatalf("an unreadable answer was shown:\n%s", now)
	}

	// Samples become charts once the server is sampled.
	if err := a.db.SetServerSampled(ctx, sessionTeam(a), app.ServerID, true); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Truncate(time.Minute).Unix()
	for i := int64(0); i < 5; i++ {
		a.db.AddSamples(ctx, app.ServerID, at-i*60, map[string]db.Sample{appID: {CPU: 1250 + int(i)*100, Mem: 100 << 20, MemTotal: 512 << 20}})
	}
	_, page = a.get("/apps/" + appID + "/metrics?range=6h")
	for _, want := range []string{"<svg", "chart-line", "Processor", "limit 512 MiB", `aria-current="true"`, "?range=24h#history", "100 MiB"} {
		if !strings.Contains(page, want) {
			t.Errorf("the charts lack %q", want)
		}
	}
	if strings.Contains(page, "style=") {
		t.Error("the charts carry inline styles, which the content policy forbids")
	}
	// An unknown stretch of time is the shortest one.
	if res, _ := a.get("/apps/" + appID + "/metrics?range=10y"); res.StatusCode != http.StatusOK {
		t.Fatalf("an unknown range: %d", res.StatusCode)
	}
}

// sessionTeam is the team the test's account belongs to.
func sessionTeam(a *app) string {
	var id string
	a.db.QueryRow(`SELECT id FROM teams ORDER BY created_at LIMIT 1`).Scan(&id)
	return id
}

func TestServerMetrics(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	appID := a.newApp(projectID, env, "web", true, nil)
	app, _ := a.db.AppByID(ctx, appID)
	a.measured(app.Container+"\t12.50%\t100MiB / 512MiB\t0B / 0B\t0B / 0B\t7\n"+
		"somebody-elses\t90.00%\t1GiB / 2GiB\t0B / 0B\t0B / 0B\t50\n"+
		"forged\t1.00%\t1MiB / 2MiB\t0B / 0B\t0B / 0B\t1\n"+
		"stranger\t2.00%\t1MiB / 2MiB\t0B / 0B\t0B / 0B\t1\n",
		func() string {
			return app.Container + "\trunning\tapp\t" + appID + "\td1\tUp\n" +
				"forged\trunning\tapp\t../../settings\t\tUp\n" +
				"stranger\trunning\tapp\tabcdefghijkl\t\tUp\n"
		})
	base := "/servers/" + app.ServerID + "/metrics"

	if _, page := a.get("/servers"); !strings.Contains(page, `href="`+base+`"`) {
		t.Fatal("the servers page does not link to the server's usage")
	}
	_, page := a.get(base)
	if !strings.Contains(page, "Switch sampling on") || !strings.Contains(page, `hx-get="`+base+`/now"`) {
		t.Fatalf("the usage page:\n%s", page)
	}
	res, now := a.get(base + "/now")
	wantStatus(t, res, http.StatusOK)
	// 1827000 of 2030000 kB in use: nine tenths, said in words as well.
	for _, want := range []string{"60%", "Of 2 cores", "1.7 GiB of 1.9 GiB", "Nearly full", "0.52", "9.5 GiB of 23.8 GiB",
		app.Container, `href="/apps/` + appID + `/metrics"`, "12.5%", `hx-trigger="every 10s"`} {
		if !strings.Contains(now, want) {
			t.Errorf("the reading lacks %q:\n%s", want, now)
		}
	}
	// Containers that are not musdash's are not listed, and a label that
	// is not an id is not made into a link.
	// A container of an app this dashboard does not know is listed by
	// name, without a page to go to.
	if !strings.Contains(now, "stranger") || strings.Contains(now, "/apps/abcdefghijkl") {
		t.Fatalf("a container of an unknown app:\n%s", now)
	}
	if strings.Contains(now, "somebody-elses") || strings.Contains(now, "../../settings") {
		t.Fatalf("the reading shows what it should not:\n%s", now)
	}

	// Switching sampling on and off.
	res, _ = a.post(base, base, url.Values{"sample": {"1"}})
	wantRedirect(t, res, base)
	if on, _ := a.db.ServerSampled(ctx, app.ServerID); !on {
		t.Fatal("sampling was not switched on")
	}
	a.db.AddSamples(ctx, app.ServerID, time.Now().Truncate(time.Minute).Unix(), map[string]db.Sample{
		"": {CPU: 6000, Mem: 812000 << 10, MemTotal: 2030000 << 10, Load: 52, DiskUsed: 10000000 << 10, DiskTotal: 25000000 << 10}})
	_, page = a.get(base)
	for _, want := range []string{"Switch sampling off", "<svg", "Processor", "Memory", "Load", "Disk", "of all cores"} {
		if !strings.Contains(page, want) {
			t.Errorf("the usage page lacks %q", want)
		}
	}
	res, _ = a.post(base, base, url.Values{"sample": {"0"}})
	wantRedirect(t, res, base)
	var rows int
	a.db.QueryRow(`SELECT count(*) FROM metric_samples`).Scan(&rows)
	if on, _ := a.db.ServerSampled(ctx, app.ServerID); on || rows != 0 {
		t.Fatalf("after switching off: on %v, %d samples", on, rows)
	}
	// A request without the form's token changes nothing.
	if res, _ := a.postRaw(a.client, base, url.Values{"sample": {"1"}}, nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("without a token: %d", res.StatusCode)
	}
}

func TestOtherTeamsMetricsAreNotFound(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`)
	a.db.Exec(`INSERT INTO servers (id, team_id, name, kind, created_at) VALUES ('othersrv', 'otherteam', 'theirs', 'ssh', 1)`)
	p, _ := a.db.CreateProject(ctx, "otherteam", "Secret", "")
	envs, _ := a.db.ListEnvironments(ctx, p.ID)
	other, err := a.db.CreateApp(ctx, "otherteam", db.App{EnvironmentID: envs[0].ID, ServerID: "othersrv", Name: "secret-app", Image: "nginx", Port: 80})
	if err != nil {
		t.Fatal(err)
	}
	before := len(a.fake.Calls())
	for _, path := range []string{"/apps/" + other.ID + "/metrics", "/apps/" + other.ID + "/metrics/now",
		"/servers/othersrv/metrics", "/servers/othersrv/metrics/now", "/servers/nosuchserver/metrics",
		"/databases/" + other.ID + "/metrics", "/services/" + other.ID + "/metrics/now"} {
		if res, _ := a.get(path); res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", path, res.StatusCode)
		}
	}
	projectID, _ := a.project("Mine")
	token := a.csrf("/projects/" + projectID)
	if res, _ := a.postRaw(a.client, "/servers/othersrv/metrics", url.Values{"sample": {"1"}, "_csrf": {token}}, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("switching another team's sampling on: %d", res.StatusCode)
	}
	if on, _ := a.db.ServerSampled(ctx, "othersrv"); on {
		t.Fatal("another team's server is sampled")
	}
	if n := len(a.fake.Calls()) - before; n != 0 {
		t.Fatalf("%d commands ran for another team's resources", n)
	}
	// Signed out, every one of them is the sign-in page.
	if res, _ := a.do(a.newClient(), mustGet(a.url+"/apps/"+other.ID+"/metrics/now")); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("signed out: %d", res.StatusCode)
	}
}

func mustGet(url string) *http.Request {
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	return req
}
