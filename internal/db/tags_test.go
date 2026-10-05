package db

import (
	"context"
	"reflect"
	"testing"
)

func TestParseTags(t *testing.T) {
	for typed, want := range map[string][]string{
		"":                         nil,
		"  ":                       nil,
		"api":                      {"api"},
		"Web, api  web,\nv1.2_x-y": {"api", "v1.2_x-y", "web"},
		"a,b,,c":                   {"a", "b", "c"},
		"0day":                     {"0day"},
	} {
		got, bad := ParseTags(typed)
		if bad != "" || !reflect.DeepEqual(got, want) {
			t.Errorf("ParseTags(%q) = %v, %q; want %v", typed, got, bad, want)
		}
	}
	for typed, wantBad := range map[string]string{
		"ok, not/ok":                        "not/ok",
		"-leading":                          "-leading",
		"über":                              "über",
		"a?b":                               "a?b",
		"../x":                              "../x",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // 33
	} {
		if _, bad := ParseTags(typed); bad != wantBad {
			t.Errorf("ParseTags(%q): bad = %q, want %q", typed, bad, wantBad)
		}
	}
}

func TestTags(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	_, team, _ := d.CreateFirstUser(ctx, "a@example.com", "A", "hash")
	server, _ := d.EnsureLocalServer(ctx, team, "")
	p, _ := d.CreateProject(ctx, team, "Shop", "")
	envs, _ := d.ListEnvironments(ctx, p.ID)
	newApp := func(name string) App {
		a, err := d.CreateApp(ctx, team, App{EnvironmentID: envs[0].ID, ServerID: server.ID, Name: name, Image: "nginx", Port: 80})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	web, api := newApp("web"), newApp("api")
	svc, err := d.CreateService(ctx, team, Service{EnvironmentID: envs[0].ID, ServerID: server.ID, Name: "blog", Template: TemplateCustom, Compose: "services: {}"})
	if err != nil {
		t.Fatal(err)
	}

	for id, tags := range map[string][]string{web.ID: {"frontend", "nightly"}, api.ID: {"nightly"}} {
		if err := d.SetTags(ctx, team, KindApp, id, tags); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.SetTags(ctx, team, KindService, svc.ID, []string{"nightly"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.TagsOf(ctx, team, KindApp, web.ID); !reflect.DeepEqual(got, []string{"frontend", "nightly"}) {
		t.Fatalf("tags of web: %v", got)
	}
	list, _ := d.ListTags(ctx, team)
	if want := []TagCount{{"frontend", 1, 0}, {"nightly", 2, 1}}; !reflect.DeepEqual(list, want) {
		t.Fatalf("tags: %+v", list)
	}
	apps, services, err := d.Tagged(ctx, team, "nightly")
	if err != nil || len(apps) != 2 || apps[0].Name != "api" || apps[1].Name != "web" || len(services) != 1 || services[0].ID != svc.ID {
		t.Fatalf("tagged: %v %v %v", apps, services, err)
	}

	// Another team sees none of it, and a row of its own that names this
	// team's app finds nothing.
	d.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`)
	if list, _ := d.ListTags(ctx, "otherteam"); len(list) != 0 {
		t.Fatalf("another team's tags: %+v", list)
	}
	if err := d.SetTags(ctx, "otherteam", KindApp, "stolen", []string{"nightly"}); err != nil {
		t.Fatal(err)
	}
	d.Exec(`UPDATE resource_tags SET resource_id = ? WHERE team_id = 'otherteam'`, web.ID)
	if apps, services, _ := d.Tagged(ctx, "otherteam", "nightly"); len(apps)+len(services) != 0 {
		t.Fatalf("another team reached an app through a tag: %v %v", apps, services)
	}
	d.Exec(`DELETE FROM resource_tags WHERE team_id = 'otherteam'`)

	// A preview is never among the tagged, even when a row says so.
	d.Exec(`UPDATE apps SET source = 'git' WHERE id = ?`, web.ID)
	preview, err := d.CreatePreview(ctx, web, 7, "web-pr-7", "feature")
	if err != nil {
		t.Fatal(err)
	}
	d.SetTags(ctx, team, KindApp, preview.ID, []string{"nightly"})
	if apps, _, _ := d.Tagged(ctx, team, "nightly"); len(apps) != 2 {
		t.Fatalf("a preview was returned for a tag: %d apps", len(apps))
	}
	d.SetTags(ctx, team, KindApp, preview.ID, nil)
	if err := d.DeleteApp(ctx, preview.ID); err != nil {
		t.Fatal(err)
	}

	// Replacing, and going with the resource.
	d.SetTags(ctx, team, KindApp, web.ID, []string{"frontend"})
	if err := d.DeleteApp(ctx, api.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteService(ctx, svc.ID); err != nil {
		t.Fatal(err)
	}
	list, _ = d.ListTags(ctx, team)
	if want := []TagCount{{"frontend", 1, 0}}; !reflect.DeepEqual(list, want) {
		t.Fatalf("after deleting what had it, nightly should be gone: %+v", list)
	}
}
