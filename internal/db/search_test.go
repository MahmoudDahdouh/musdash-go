package db

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// searchTeams is newHomeTeams with one of everything the search looks in,
// for both teams: a query that forgot the team finds the other's.
type searchTeams struct {
	homeTeams
	blog     Project
	staging  Environment
	api      App
	preview  App
	maindb   Database
	site     Service
	edge     Server
	theirEnv Environment
}

func newSearchTeams(t *testing.T, d *DB) searchTeams {
	t.Helper()
	ctx := context.Background()
	s := searchTeams{homeTeams: newHomeTeams(t, d)}
	step := 0
	must := func(err error) {
		t.Helper()
		step++
		if err != nil {
			t.Fatalf("seeding, step %d: %v", step, err)
		}
	}
	var err error
	s.blog, err = d.CreateProject(ctx, s.team, "Blog", "the company weblog")
	must(err)
	s.staging, err = d.CreateEnvironment(ctx, s.team, s.project.ID, "staging")
	must(err)
	s.api, err = d.CreateApp(ctx, s.team, App{EnvironmentID: s.env.ID, ServerID: s.server.ID, Name: "api", Source: SourceGit,
		RepoURL: "https://github.com/acme/storefront.git", RepoName: "acme/storefront", Branch: "release", Port: 80})
	must(err)
	_, err = d.CreateApp(ctx, s.team, App{EnvironmentID: s.env.ID, ServerID: s.server.ID, Name: "web-admin", Image: "ghcr.io/acme/admin:2", Port: 80})
	must(err)
	_, err = d.CreateApp(ctx, s.team, App{EnvironmentID: s.env.ID, ServerID: s.server.ID, Name: "old-web", Image: "nginx", Port: 80})
	must(err)
	s.preview, err = d.CreatePreview(ctx, s.api, 7, "api-pr-7", "feature")
	must(err)
	s.maindb, err = d.CreateDatabase(ctx, s.team, Database{EnvironmentID: s.env.ID, ServerID: s.server.ID, Name: "maindb", Engine: "postgres", Image: "postgres:17"})
	must(err)
	s.site, err = d.CreateService(ctx, s.team, Service{EnvironmentID: s.staging.ID, ServerID: s.server.ID, Name: "site", Template: "wordpress"})
	must(err)
	must(d.SyncEndpoints(ctx, s.site.ID, []string{"WORDPRESS"}, func(string) (string, bool) { return "press.example.com", true }))
	_, err = d.AddDomain(ctx, s.team, s.server.ID, Domain{ResourceKind: KindApp, ResourceID: s.web.ID, Host: "shop.example.com", TLS: true})
	must(err)
	must(d.SetTags(ctx, s.team, KindApp, s.web.ID, []string{"frontend"}))
	_, err = d.Exec(`INSERT INTO servers (id, team_id, name, kind, host, ip, created_at, status) VALUES ('edgeserverid', ?, 'edge', 'ssh', 'edge.internal', '203.0.113.9', 2, 'ok')`, s.team)
	must(err)
	s.edge, err = d.Server(ctx, s.team, "edgeserverid")
	must(err)

	// The other team has the same words in everything it owns.
	otherEnvs, _ := d.ListEnvironments(ctx, s.otherProject.ID)
	s.theirEnv = otherEnvs[0]
	_, err = d.AddDomain(ctx, s.other, s.otherServerID, Domain{ResourceKind: KindApp, ResourceID: s.otherApp.ID, Host: "theirs.example.com", TLS: true})
	must(err)
	must(d.SetTags(ctx, s.other, KindApp, s.otherApp.ID, []string{"frontend-theirs"}))
	_, err = d.CreateDatabase(ctx, s.other, Database{EnvironmentID: s.theirEnv.ID, ServerID: s.otherServerID, Name: "theirdb", Engine: "postgres", Image: "postgres:17"})
	must(err)
	theirService, err := d.CreateService(ctx, s.other, Service{EnvironmentID: s.theirEnv.ID, ServerID: s.otherServerID, Name: "theirsite", Template: "wordpress"})
	must(err)
	must(d.SyncEndpoints(ctx, theirService.ID, []string{"WORDPRESS"}, func(string) (string, bool) { return "theirpress.example.com", true }))
	return s
}

func search(t *testing.T, d *DB, team, text string) []Hit {
	t.Helper()
	hits, err := d.Search(context.Background(), team, strings.Fields(text), 50)
	if err != nil {
		t.Fatalf("search %q: %v", text, err)
	}
	return hits
}

// names is what a search answered with, as "kind:name" in its order.
func names(hits []Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Kind + ":" + h.Name
	}
	return out
}

func wantHits(t *testing.T, text string, got []Hit, want ...string) {
	t.Helper()
	if g := names(got); strings.Join(g, " ") != strings.Join(want, " ") {
		t.Errorf("search %q:\n got %v\nwant %v", text, g, want)
	}
}

func TestSearchFindsEveryKind(t *testing.T) {
	d := openTest(t)
	s := newSearchTeams(t, d)
	for text, want := range map[string]string{
		"blog":        "project:Blog",             // a project by its name
		"weblog":      "project:Blog",             // and by its description
		"staging":     "environment:staging",      // an environment
		"api":         "app:api",                  // an app by its name, and not its preview
		"storefront":  "app:api",                  // by its repository
		"release":     "app:api",                  // by its branch
		"ghcr.io":     "app:web-admin",            // by its image
		"maindb":      "database:maindb",          // a database by its name
		"postgres":    "database:maindb",          // by its engine
		"site":        "service:site",             // a service by its name
		"wordpress":   "service:site",             // by its template
		"shop.exam":   "domain:shop.example.com",  // an app's domain
		"press.exam":  "domain:press.example.com", // a service endpoint's domain
		"edge":        "server:edge",              // a server by its name
		"203.0.113":   "server:edge",              // by its address
		"internal":    "server:edge",              // by its host
		"frontend":    "tag:frontend",             // a tag
		"FRONTEND":    "tag:frontend",             // whatever the case
		"  maindb   ": "database:maindb",          // whatever the spaces
		"nosuchthing": "",
	} {
		wantHits(t, text, search(t, d, s.team, text), strings.Fields(want)...)
	}

	// A hit says where it is, and a domain says what it points at.
	api := search(t, d, s.team, "api")[0]
	if api.ID != s.api.ID || api.ProjectID != s.project.ID || api.Project != "Shop" || api.EnvID != s.env.ID || api.Env != s.env.Name || api.Detail != "acme/storefront" || api.Status != AppCreated {
		t.Errorf("app hit: %+v", api)
	}
	if m := search(t, d, s.team, "maindb")[0]; m.ID != s.maindb.ID || m.Detail != "postgres" || m.Project != "Shop" {
		t.Errorf("database hit: %+v", m)
	}
	if h := search(t, d, s.team, "shop.exam")[0]; h.OwnerKind != KindApp || h.OwnerID != s.web.ID || h.Owner != "web" || h.Project != "Shop" {
		t.Errorf("app domain hit: %+v", h)
	}
	if h := search(t, d, s.team, "press.exam")[0]; h.OwnerKind != KindService || h.OwnerID != s.site.ID || h.Owner != "site" || h.Env != "staging" {
		t.Errorf("service domain hit: %+v", h)
	}
	if h := search(t, d, s.team, "edge")[0]; h.ID != s.edge.ID || h.Status != ServerOK || h.Detail != "edge.internal" {
		t.Errorf("server hit: %+v", h)
	}
	if h := search(t, d, s.team, "staging")[0]; h.ID != s.staging.ID || h.ProjectID != s.project.ID || h.Project != "Shop" {
		t.Errorf("environment hit: %+v", h)
	}
}

func TestSearchIsTheTeamsOwn(t *testing.T) {
	d := openTest(t)
	s := newSearchTeams(t, d)
	// Everything of the other team, by every text it could be found by.
	for _, text := range []string{"their", "theirs", "theirapp", "theirdb", "theirsite", "theirpress", "theirs.example.com", "frontend-theirs", s.otherApp.ID, s.otherProject.ID, s.theirEnv.ID, "serverb"} {
		if hits := search(t, d, s.team, text); len(hits) != 0 {
			t.Errorf("search %q finds another team's %v", text, names(hits))
		}
	}
	// And the other way round: the same query, asked by them.
	for _, text := range []string{"web", "shop", "maindb", "edge", "storefront", "staging", s.web.ID, s.site.ID} {
		if hits := search(t, d, s.other, text); len(hits) != 0 {
			t.Errorf("the other team's search %q finds %v", text, names(hits))
		}
	}
	// Words both teams have find each team its own.
	wantHits(t, "postgres", search(t, d, s.other, "postgres"), "database:theirdb")
	wantHits(t, "frontend", search(t, d, s.other, "frontend"), "tag:frontend-theirs")
	wantHits(t, "example.com", search(t, d, s.other, "example.com"), "domain:theirpress.example.com", "domain:theirs.example.com")
}

func TestSearchOrder(t *testing.T) {
	d := openTest(t)
	s := newSearchTeams(t, d)
	// The name itself, then names that start with the word, then names
	// that hold it, then what holds it elsewhere.
	hits := search(t, d, s.team, "web")
	wantHits(t, "web", hits, "app:web", "app:web-admin", "app:old-web", "project:Blog")
	for i, rank := range []int{0, 1, 2, 3} {
		if hits[i].Rank != rank {
			t.Errorf("%s has rank %d, want %d", hits[i].Name, hits[i].Rank, rank)
		}
	}
	// Among equals: projects, environments, apps, databases, services,
	// domains, servers, tags, and then by name.
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	_, err := d.CreateProject(ctx, s.team, "zeta", "")
	must(err)
	_, err = d.CreateEnvironment(ctx, s.team, s.blog.ID, "zeta")
	must(err)
	blogEnvs, _ := d.ListEnvironments(ctx, s.blog.ID)
	for _, name := range []string{"Zeta-b", "zeta-a"} {
		_, err = d.CreateApp(ctx, s.team, App{EnvironmentID: blogEnvs[0].ID, ServerID: s.server.ID, Name: name, Image: "nginx", Port: 80})
		must(err)
	}
	must(d.SetTags(ctx, s.team, KindApp, s.api.ID, []string{"zeta"}))
	wantHits(t, "zeta", search(t, d, s.team, "zeta"), "project:zeta", "environment:zeta", "tag:zeta", "app:zeta-a", "app:Zeta-b")
}

func TestSearchWords(t *testing.T) {
	d := openTest(t)
	s := newSearchTeams(t, d)
	// Every word must be found, in the row or in where it is.
	wantHits(t, "shop api", search(t, d, s.team, "shop api"), "app:api")
	wantHits(t, "api shop", search(t, d, s.team, "api shop"), "app:api")
	wantHits(t, "staging site", search(t, d, s.team, "staging site"), "service:site")
	wantHits(t, "blog api", search(t, d, s.team, "blog api"))
	// A domain is where its app is.
	wantHits(t, "shop.example web", search(t, d, s.team, "shop.example web"), "domain:shop.example.com")
	// Where a row is does not find it alone: "shop" is the project and the
	// domain of that name, not all that the project holds.
	wantHits(t, "shop", search(t, d, s.team, "shop"), "project:Shop", "domain:shop.example.com")
	wantHits(t, "staging", search(t, d, s.team, "staging"), "environment:staging")
	// An environment is found with its project.
	wantHits(t, "shop staging", search(t, d, s.team, "shop staging"), "environment:staging")
	// No words, no rows: the caller lists pages instead.
	if hits, err := d.Search(context.Background(), s.team, nil, 50); err != nil || len(hits) != 0 {
		t.Errorf("no words: %v, %v", names(hits), err)
	}
}

func TestSearchTakesTextAsItIs(t *testing.T) {
	d := openTest(t)
	s := newSearchTeams(t, d)
	ctx := context.Background()
	for _, name := range []string{"100% done", "under_score", `back\slash`} {
		if _, err := d.CreateProject(ctx, s.team, name, ""); err != nil {
			t.Fatal(err)
		}
	}
	// A wildcard typed is a character looked for.
	wantHits(t, "%", search(t, d, s.team, "%"), "project:100% done")
	wantHits(t, "_", search(t, d, s.team, "_"), "project:under_score")
	wantHits(t, `\`, search(t, d, s.team, `\`), `project:back\slash`)
	wantHits(t, "w_b", search(t, d, s.team, "w_b"))
	wantHits(t, "%%", search(t, d, s.team, "%%"))
	wantHits(t, "'", search(t, d, s.team, "'"))
	wantHits(t, `" OR 1=1 --`, search(t, d, s.team, `" OR 1=1 --`))
}

func TestSearchByID(t *testing.T) {
	d := openTest(t)
	s := newSearchTeams(t, d)
	// An id finds its row, alone or inside a name the server shows.
	wantHits(t, "app id", search(t, d, s.team, s.api.ID), "app:api")
	wantHits(t, "container name", search(t, d, s.team, "musdash-"+s.api.ID+"-abcdefabcdef"), "app:api")
	wantHits(t, "database container", search(t, d, s.team, "musdash-db-"+s.maindb.ID), "database:maindb")
	wantHits(t, "network", search(t, d, s.team, "musdash-"+s.staging.ID), "environment:staging")
	wantHits(t, "compose project", search(t, d, s.team, "musdash-"+s.site.ID), "service:site")
	wantHits(t, "project id", search(t, d, s.team, s.blog.ID), "project:Blog")
	// Half an id is letters that mean nothing.
	wantHits(t, "half an id", search(t, d, s.team, s.api.ID[:6]))
	// A preview is not found, by name or by id.
	wantHits(t, "preview name", search(t, d, s.team, "api-pr"))
	wantHits(t, "preview branch", search(t, d, s.team, "feature"))
	wantHits(t, "preview id", search(t, d, s.team, s.preview.ID))
	// Nor is the address a preview was given.
	if _, err := d.AddDomain(context.Background(), s.team, s.server.ID, Domain{ResourceKind: KindApp, ResourceID: s.preview.ID, Host: "pr-7.preview.example.com"}); err != nil {
		t.Fatal(err)
	}
	wantHits(t, "preview domain", search(t, d, s.team, "pr-7.preview"))
}

func TestSearchLimit(t *testing.T) {
	d := openTest(t)
	s := newSearchTeams(t, d)
	ctx := context.Background()
	for i := 0; i < 12; i++ {
		if _, err := d.CreateProject(ctx, s.team, "bulk-"+strconv.Itoa(i), ""); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := d.Search(ctx, s.team, []string{"bulk"}, 5)
	if err != nil || len(hits) != 5 {
		t.Fatalf("limit 5: %d rows, %v", len(hits), err)
	}
	// More words than the statement takes are not an error.
	many := strings.Fields("bulk b u l k - 1 2 3 4 5 6 7 8 9")
	if _, err := d.Search(ctx, s.team, many, 5); err != nil {
		t.Fatalf("many words: %v", err)
	}
}
