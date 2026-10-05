package db

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/migrations"
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
	// The tag stays when the last thing that had it is deleted.
	list, _ = d.ListTags(ctx, team)
	if want := []TagCount{{"frontend", 1, 0}, {"nightly", 0, 0}}; !reflect.DeepEqual(list, want) {
		t.Fatalf("after deleting what had nightly: %+v", list)
	}
}

// A tag exists on its own: made empty, renamed with what has it, deleted
// from what has it.
func TestTagsOfTheirOwn(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	_, team, _ := d.CreateFirstUser(ctx, "a@example.com", "A", "hash")
	server, _ := d.EnsureLocalServer(ctx, team, "")
	p, _ := d.CreateProject(ctx, team, "Shop", "")
	envs, _ := d.ListEnvironments(ctx, p.ID)
	web, err := d.CreateApp(ctx, team, App{EnvironmentID: envs[0].ID, ServerID: server.ID, Name: "web", Image: "nginx", Port: 80})
	if err != nil {
		t.Fatal(err)
	}
	d.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`)

	if err := d.CreateTag(ctx, team, "nightly"); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateTag(ctx, team, "nightly"); !errors.Is(err, ErrTagExists) {
		t.Fatalf("making a tag twice: %v", err)
	}
	// The same name in another team is another tag.
	if err := d.CreateTag(ctx, "otherteam", "nightly"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := d.TagExists(ctx, team, "nightly"); !ok {
		t.Fatal("the tag does not exist")
	}
	if ok, _ := d.TagExists(ctx, team, "nope"); ok {
		t.Fatal("a tag that was never made exists")
	}
	if list, _ := d.ListTags(ctx, team); !reflect.DeepEqual(list, []TagCount{{"nightly", 0, 0}}) {
		t.Fatalf("an empty tag is not listed with no resources: %+v", list)
	}

	// Renaming takes what has the tag along.
	d.SetTags(ctx, team, KindApp, web.ID, []string{"frontend", "nightly"})
	if err := d.RenameTag(ctx, team, "nightly", "daily"); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.TagsOf(ctx, team, KindApp, web.ID); !reflect.DeepEqual(got, []string{"daily", "frontend"}) {
		t.Fatalf("after the rename: %v", got)
	}
	if err := d.RenameTag(ctx, team, "daily", "frontend"); !errors.Is(err, ErrTagExists) {
		t.Fatalf("renaming onto another tag: %v", err)
	}
	if err := d.RenameTag(ctx, team, "daily", "daily"); err != nil {
		t.Fatalf("renaming a tag to itself: %v", err)
	}
	if err := d.RenameTag(ctx, team, "nope", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("renaming a tag that does not exist: %v", err)
	}
	// The other team's tag of the old name was not touched, and cannot be
	// renamed or deleted from here.
	if ok, _ := d.TagExists(ctx, "otherteam", "nightly"); !ok {
		t.Fatal("another team's tag was renamed")
	}
	if err := d.RenameTag(ctx, team, "nightly", "mine"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("renaming another team's tag: %v", err)
	}
	if err := d.DeleteTag(ctx, team, "nightly"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting another team's tag: %v", err)
	}

	if err := d.DeleteTag(ctx, team, "daily"); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.TagsOf(ctx, team, KindApp, web.ID); !reflect.DeepEqual(got, []string{"frontend"}) {
		t.Fatalf("after deleting the tag: %v", got)
	}
	if list, _ := d.ListTags(ctx, team); !reflect.DeepEqual(list, []TagCount{{"frontend", 1, 0}}) {
		t.Fatalf("tags after the delete: %+v", list)
	}

	// A team has a limit, which giving a resource a new tag keeps too.
	for i := 1; i < MaxTeamTags; i++ {
		if err := d.CreateTag(ctx, team, "t"+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.CreateTag(ctx, team, "one-more"); !errors.Is(err, ErrTooMany) {
		t.Fatalf("a tag past the limit: %v", err)
	}
	if err := d.SetTags(ctx, team, KindApp, web.ID, []string{"frontend", "one-more"}); !errors.Is(err, ErrTooMany) {
		t.Fatalf("a new tag on a resource past the limit: %v", err)
	}
	if got, _ := d.TagsOf(ctx, team, KindApp, web.ID); !reflect.DeepEqual(got, []string{"frontend"}) {
		t.Fatalf("a refused change was half made: %v", got)
	}
}

// Migration 0015 makes a tag of every name that something already had.
func TestTagsMigrationKeepsExistingTags(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	_, team, _ := d.CreateFirstUser(ctx, "a@example.com", "A", "hash")
	// As the tables were before the migration: names on resources, no tags.
	for _, row := range [][2]string{{"nightly", "a1"}, {"nightly", "a2"}, {"frontend", "a1"}} {
		if _, err := d.Exec(`INSERT INTO resource_tags (team_id, tag, resource_kind, resource_id) VALUES (?, ?, 'app', ?)`, team, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	file, err := migrations.FS.ReadFile("0015_tags.sql")
	if err != nil {
		t.Fatal(err)
	}
	fill := string(file)[strings.LastIndex(string(file), "INSERT INTO tags"):]
	if _, err := d.Exec(fill); err != nil {
		t.Fatal(err)
	}
	list, _ := d.ListTags(ctx, team)
	if want := []TagCount{{"frontend", 1, 0}, {"nightly", 2, 0}}; !reflect.DeepEqual(list, want) {
		t.Fatalf("tags after the migration's fill: %+v", list)
	}
}
