package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
)

// until waits for a condition that a job brings about.
func until(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestDatabaseBackupsPage(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	m := a.newDatabase(projectID, env, "postgres", "maindb", nil)
	base := "/databases/" + m.ID + "/backups"
	inner := a.fake.Handle
	var restored string
	a.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		switch {
		case strings.HasPrefix(line, "docker exec --interactive "+m.Container):
			raw, _ := io.ReadAll(c.Stdin)
			restored = string(raw)
			return "", nil
		case strings.HasPrefix(line, "docker exec "+m.Container+" sh -c pg_dump"):
			return "DUMP OF maindb", nil
		}
		return inner(line, c)
	}

	res, page := a.get(base)
	wantStatus(t, res, http.StatusOK)
	for _, want := range []string{"No backup has been made yet", "Back up now", `value="0 3 * * *"`, "Keep on this server only", "Not scheduled"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page is missing %q", want)
		}
	}

	// A schedule that cannot be read, and a count out of range, come back
	// with what was typed.
	res, page = a.post(base, base+"/schedule", url.Values{"enabled": {"1"}, "schedule": {"every night"}, "keep": {"0"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if !strings.Contains(page, "not a schedule musdash can read") || !strings.Contains(page, "from 1 to 365") || !strings.Contains(page, `value="every night"`) {
		t.Fatal("the schedule form did not explain what was wrong")
	}
	// A storage that is not the team's is refused.
	res, _ = a.post(base, base+"/schedule", url.Values{"enabled": {"1"}, "schedule": {"@daily"}, "keep": {"3"}, "storage_id": {"nosuchstorage"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)

	res, _ = a.post(base, base+"/schedule", url.Values{"enabled": {"1"}, "schedule": {"30 2 * * *"}, "keep": {"3"}})
	wantRedirect(t, res, base)
	cfg, err := a.db.BackupConfig(ctx, m.ID)
	if err != nil || !cfg.Enabled || cfg.Keep != 3 || cfg.NextRun <= time.Now().Unix() {
		t.Fatalf("%+v %v", cfg, err)
	}
	if _, page = a.get(base); strings.Contains(page, "Not scheduled") || !strings.Contains(page, "02:30 UTC") {
		t.Fatal("the page does not show the next run")
	}
	// Switched off: nothing is due any more.
	res, _ = a.post(base, base+"/schedule", url.Values{"schedule": {"30 2 * * *"}, "keep": {"3"}})
	wantRedirect(t, res, base)
	if cfg, _ = a.db.BackupConfig(ctx, m.ID); cfg.Enabled || cfg.NextRun != 0 {
		t.Fatalf("a switched-off schedule: %+v", cfg)
	}

	// Back up now.
	res, _ = a.post(base, base, url.Values{})
	wantRedirect(t, res, base)
	var b db.Backup
	until(t, "the backup", func() bool {
		list, _ := a.db.ListBackups(ctx, m.ID, 5)
		if len(list) == 1 && list[0].Status != db.RunRunning {
			b = list[0]
			return true
		}
		return false
	})
	if b.Status != db.RunSuccess {
		t.Fatalf("%+v", b)
	}
	if _, page = a.get(base); !strings.Contains(page, "Succeeded") || !strings.Contains(page, base+"/"+b.ID+"/download") || strings.Contains(page, `hx-trigger="every 2s"`) {
		t.Fatal("the finished backup is not listed as such")
	}

	// Download: the file as it is on the server.
	res, file := a.get(base + "/" + b.ID + "/download")
	wantStatus(t, res, http.StatusOK)
	if res.Header.Get("Content-Disposition") != `attachment; filename="`+b.File+`"` || int64(len(file)) != b.Size || len(file) == 0 {
		t.Fatalf("download: %q, %d bytes (recorded %d)", res.Header.Get("Content-Disposition"), len(file), b.Size)
	}

	// Restore needs the database's name typed.
	res, _ = a.post(base, base+"/"+b.ID+"/restore", url.Values{"confirm": {"wrong"}})
	wantRedirect(t, res, base)
	if got, _ := a.db.BackupByID(ctx, b.ID); got.RestoreStatus != "" {
		t.Fatal("a restore started without the right confirmation")
	}
	res, _ = a.post(base, base+"/"+b.ID+"/restore", url.Values{"confirm": {"maindb"}})
	wantRedirect(t, res, base)
	until(t, "the restore", func() bool {
		got, _ := a.db.BackupByID(ctx, b.ID)
		return got.RestoreStatus == db.RunSuccess
	})
	if restored != "DUMP OF maindb" {
		t.Fatalf("the restore was fed %q", restored)
	}
	if _, page = a.get(base); !strings.Contains(page, "Restored ") {
		t.Fatal("the page does not say the backup was restored")
	}

	// A stopped database cannot be backed up or restored.
	a.post("/databases/"+m.ID, "/databases/"+m.ID+"/stop", url.Values{})
	res, _ = a.post(base, base, url.Values{})
	wantRedirect(t, res, base)
	if list, _ := a.db.ListBackups(ctx, m.ID, 5); len(list) != 1 {
		t.Fatal("a backup was queued for a stopped database")
	}

	// Delete.
	res, _ = a.post(base, base+"/"+b.ID+"/delete", url.Values{})
	wantRedirect(t, res, base)
	if _, err := a.db.BackupByID(ctx, b.ID); err != db.ErrNotFound {
		t.Fatalf("after delete: %v", err)
	}
	res, _ = a.get(base + "/" + b.ID + "/download")
	wantStatus(t, res, http.StatusNotFound)

	// An engine musdash cannot dump says so instead of offering a button.
	k := a.newDatabase(projectID, env, "keydb", "cache", nil)
	if _, page = a.get("/databases/" + k.ID + "/backups"); !strings.Contains(page, "cannot back up KeyDB yet") || strings.Contains(page, "Back up now") {
		t.Fatal("the page of an engine without backups")
	}
	res, _ = a.post("/databases/"+k.ID+"/backups", "/databases/"+k.ID+"/backups/schedule", url.Values{"enabled": {"1"}, "schedule": {"@daily"}, "keep": {"3"}})
	wantStatus(t, res, http.StatusNotFound)
}

func TestAppTasksPages(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	appID := a.newApp(projectID, env, "web", true, nil)
	base := "/apps/" + appID + "/tasks"
	inner := a.fake.Handle
	a.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if strings.Contains(line, " sh -c echo tidy") {
			return "tidied <b>3</b> rows\n", nil
		}
		return inner(line, c)
	}

	res, page := a.get(base)
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(page, "No tasks yet") {
		t.Fatal("the empty state is missing")
	}
	res, page = a.post(base, base, url.Values{"name": {""}, "command": {""}, "schedule": {"61 * * * *"}, "enabled": {"1"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	for _, want := range []string{"Enter a name", "Enter the command", "not a schedule musdash can read"} {
		if !strings.Contains(page, want) {
			t.Errorf("the form is missing %q", want)
		}
	}
	res, _ = a.post(base, base, url.Values{"name": {"Tidy up"}, "command": {"echo tidy"}, "schedule": {"*/5 * * * *"}, "enabled": {"1"}})
	wantRedirect(t, res, base)
	tasks, _ := a.db.ListTasks(ctx, appID)
	if len(tasks) != 1 || tasks[0].NextRun <= time.Now().Unix() || !tasks[0].Enabled {
		t.Fatalf("%+v", tasks)
	}
	task := tasks[0]
	one := base + "/" + task.ID

	// Run now, then see the output, escaped.
	res, _ = a.post(one, one+"/run", url.Values{})
	wantRedirect(t, res, one)
	until(t, "the run", func() bool {
		runs, _ := a.db.ListTaskRuns(ctx, task.ID, 1)
		return len(runs) == 1 && runs[0].Status == db.RunSuccess
	})
	res, page = a.get(one)
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(page, "tidied &lt;b&gt;3&lt;/b&gt; rows") || strings.Contains(page, "<b>3</b>") || !strings.Contains(page, "Succeeded") {
		t.Fatal("the run's output is missing or not escaped")
	}
	if _, list := a.get(base); !strings.Contains(list, "Tidy up") || !strings.Contains(list, "*/5 * * * *") {
		t.Fatal("the task is not listed")
	}

	// Switching it off clears its next run.
	res, _ = a.post(one, one, url.Values{"name": {"Tidy up"}, "command": {"echo tidy"}, "schedule": {"@daily"}})
	wantRedirect(t, res, one)
	if got, _ := a.db.TaskByID(ctx, task.ID); got.Enabled || got.NextRun != 0 || got.Schedule != "@daily" {
		t.Fatalf("%+v", got)
	}

	// Without a running container, Run now explains instead of queueing.
	a.post("/apps/"+appID, "/apps/"+appID+"/stop", url.Values{})
	res, _ = a.post(one, one+"/run", url.Values{})
	wantRedirect(t, res, one)
	if runs, _ := a.db.ListTaskRuns(ctx, task.ID, 5); len(runs) != 1 {
		t.Fatalf("%d runs after Run now on a stopped app", len(runs))
	}

	// Deleting the task removes its runs and their output.
	runs, _ := a.db.ListTaskRuns(ctx, task.ID, 5)
	res, _ = a.post(one, one+"/delete", url.Values{})
	wantRedirect(t, res, base)
	if _, err := a.db.TaskByID(ctx, task.ID); err != db.ErrNotFound {
		t.Fatalf("after delete: %v", err)
	}
	if out := a.server.taskOutput(runs[0].ID); out != "" {
		t.Fatal("a deleted task's output is still on disk")
	}
}

func TestStoragesPage(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	team := firstTeam(t, a)
	page := "/settings/storages"

	res, body := a.get(page)
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(body, "No bucket yet") {
		t.Fatal("the empty state is missing")
	}
	good := url.Values{"name": {"Offsite"}, "endpoint": {"https://s3.example.test"}, "region": {"eu-central-1"}, "bucket": {"backups"}, "prefix": {"/musdash/"},
		"access_key": {"AKIAEXAMPLEKEY"}, "secret_key": {"s3-secret-0123456789"}}
	with := func(key, value string) url.Values {
		v := url.Values{}
		for k, vals := range good {
			v[k] = vals
		}
		v.Set(key, value)
		return v
	}
	for key, bad := range map[string]string{
		"name": "", "endpoint": "s3.example.test/path", "region": "EU West", "bucket": "My_Bucket", "prefix": "../up", "access_key": "", "secret_key": "",
	} {
		res, body := a.post(page, page, with(key, bad))
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `id="`+key+`-error"`) {
			t.Errorf("%s=%q: %d, error shown on the field: %v", key, bad, res.StatusCode, strings.Contains(body, `id="`+key+`-error"`))
		}
		if strings.Contains(body, "s3-secret-0123456789") || strings.Contains(body, "AKIAEXAMPLEKEY") {
			t.Errorf("%s: a rejected form sent the keys back", key)
		}
	}
	res, _ = a.post(page, page, good)
	wantRedirect(t, res, page)
	list, _ := a.db.ListS3Storages(ctx, team)
	if len(list) != 1 || list[0].Prefix != "musdash" || strings.Contains(list[0].SecretKey, "s3-secret") || strings.Contains(list[0].AccessKey, "AKIA") {
		t.Fatalf("stored: %+v", list)
	}
	st := list[0]
	if _, body = a.get(page); !strings.Contains(body, "Offsite") || !strings.Contains(body, "backups/musdash") || strings.Contains(body, "s3-secret-0123456789") || strings.Contains(body, "AKIAEXAMPLEKEY") {
		t.Fatal("the list shows the bucket wrongly, or shows its keys")
	}
	res, body = a.post(page, page, good)
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if !strings.Contains(body, "already listed") {
		t.Fatal("a second bucket of the same name was not refused")
	}

	// Test: rclone lists the folder, with the keys in a file, not in its
	// arguments, and the file gone afterwards.
	keysFile := ""
	inner := a.fake.Handle
	a.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "docker run --rm --env-file ") {
			path := strings.Fields(line)[4]
			keysFile, _, _ = a.fake.File(path)
			if strings.Contains(line, " lsjson ") {
				return "[]", nil
			}
		}
		return inner(line, c)
	}
	res, _ = a.post(page, page+"/"+st.ID+"/test", url.Values{})
	wantRedirect(t, res, page)
	if _, body = a.get(page); !strings.Contains(body, "Offsite works") {
		t.Fatal("the test did not report success")
	}
	if !strings.Contains(keysFile, "s3-secret-0123456789") {
		t.Fatal("rclone was not given the keys")
	}
	for _, c := range a.fake.Calls() {
		if strings.Contains(c, "s3-secret-0123456789") || strings.Contains(c, "AKIAEXAMPLEKEY") {
			t.Fatalf("a key is on a command line: %s", c)
		}
	}

	// In use by a schedule: not removable until the schedule lets go.
	projectID, env := a.project("Shop")
	m := a.newDatabase(projectID, env, "postgres", "maindb", nil)
	base := "/databases/" + m.ID + "/backups"
	res, _ = a.post(base, base+"/schedule", url.Values{"enabled": {"1"}, "schedule": {"@daily"}, "keep": {"3"}, "storage_id": {st.ID}})
	wantRedirect(t, res, base)
	res, _ = a.post(page, page+"/"+st.ID+"/delete", url.Values{})
	wantRedirect(t, res, page)
	if list, _ = a.db.ListS3Storages(ctx, team); len(list) != 1 {
		t.Fatal("a bucket in use was removed")
	}
	a.post(base, base+"/schedule", url.Values{"enabled": {"1"}, "schedule": {"@daily"}, "keep": {"3"}})
	res, _ = a.post(page, page+"/"+st.ID+"/delete", url.Values{})
	wantRedirect(t, res, page)
	if list, _ = a.db.ListS3Storages(ctx, team); len(list) != 0 {
		t.Fatal("the bucket was not removed")
	}
}

func TestNotificationsPage(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	team := firstTeam(t, a)
	page := "/settings/notifications"
	var got []map[string]any
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		json.NewDecoder(r.Body).Decode(&m)
		got = append(got, m)
	}))
	defer hook.Close()

	res, body := a.get(page)
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(body, "No channel yet") || !strings.Contains(body, "Add Discord channel") {
		t.Fatal("the empty page")
	}
	// Each kind has its own fields.
	if _, body = a.get(page + "?kind=email"); !strings.Contains(body, "SMTP server") || !strings.Contains(body, "Add Email channel") {
		t.Fatal("the email form")
	}
	if _, body = a.get(page + "?kind=nonsense"); !strings.Contains(body, "Add Discord channel") {
		t.Fatal("an unknown kind did not fall back to the first")
	}

	secretURL := hook.URL + "/hooks/T0PSECRET"
	res, body = a.post(page+"?kind=webhook", page, url.Values{"kind": {"webhook"}, "name": {"Ops"}, "cfg_url": {"not a url"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if !strings.Contains(body, "must be an http:// or https:// address") {
		t.Fatal("a bad address was not explained")
	}
	res, _ = a.post(page+"?kind=webhook", page, url.Values{"kind": {"carrier-pigeon"}, "name": {"Ops"}})
	wantStatus(t, res, http.StatusNotFound)
	res, _ = a.post(page+"?kind=webhook", page, url.Values{"kind": {"webhook"}, "name": {"Ops"}, "cfg_url": {secretURL}, "cfg_secret": {"signing-secret-xyz"}})
	wantRedirect(t, res, page)
	list, _ := a.db.ListChannels(ctx, team)
	if len(list) != 1 || strings.Contains(list[0].Config, "T0PSECRET") || !list[0].Enabled || !list[0].Wants("backup") {
		t.Fatalf("stored: %+v", list)
	}
	ch := list[0]
	if _, body = a.get(page); !strings.Contains(body, "Ops") || strings.Contains(body, "T0PSECRET") || strings.Contains(body, "signing-secret-xyz") {
		t.Fatal("the list is missing the channel, or shows its address or secret")
	}

	// Send a test.
	res, _ = a.post(page, page+"/"+ch.ID+"/test", url.Values{})
	wantRedirect(t, res, page)
	if len(got) != 1 {
		t.Fatalf("%d test notifications arrived", len(got))
	}
	if _, body = a.get(page); !strings.Contains(body, "Test sent to Ops") {
		t.Fatal("the test's success was not reported")
	}
	// A channel that cannot be reached says so, without its address.
	hook.Close()
	res, _ = a.post(page, page+"/"+ch.ID+"/test", url.Values{})
	wantRedirect(t, res, page)
	if _, body = a.get(page); !strings.Contains(body, "The test did not arrive") || strings.Contains(body, "T0PSECRET") {
		t.Fatal("a failed test: not reported, or reported with the channel's address")
	}

	// Choose events; switch off.
	res, _ = a.post(page, page+"/"+ch.ID, url.Values{"ev-" + ch.ID + "-backup": {"1"}, "ev-" + ch.ID + "-disk": {"1"}})
	wantRedirect(t, res, page)
	if c, _ := a.db.Channel(ctx, team, ch.ID); c.Enabled || c.Events != "backup,disk" {
		t.Fatalf("%+v", c)
	}
	if _, body = a.get(page); !strings.Contains(body, "Switched off") {
		t.Fatal("a switched-off channel is not marked")
	}
	res, _ = a.post(page, page+"/"+ch.ID+"/delete", url.Values{})
	wantRedirect(t, res, page)
	if list, _ = a.db.ListChannels(ctx, team); len(list) != 0 {
		t.Fatal("the channel was not removed")
	}
}

func TestOtherTeamsOperationsAreNotFound(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	a.db.Exec(`INSERT INTO teams (id, name, created_at) VALUES ('otherteam', 'Other', 1)`)
	a.db.Exec(`INSERT INTO servers (id, team_id, name, kind, created_at) VALUES ('othersrv', 'otherteam', 'theirs', 'ssh', 1)`)
	p, _ := a.db.CreateProject(ctx, "otherteam", "Secret", "")
	envs, _ := a.db.ListEnvironments(ctx, p.ID)
	theirDB, err := a.db.CreateDatabase(ctx, "otherteam", db.Database{EnvironmentID: envs[0].ID, ServerID: "othersrv", Name: "secret-db", Engine: "postgres", Image: "postgres:17-alpine", Username: "postgres", Password: a.seal("x"), DBName: "postgres"})
	if err != nil {
		t.Fatal(err)
	}
	theirBackup, _ := a.db.CreateBackup(ctx, theirDB.ID, db.TriggerManual)
	theirBackup.Status, theirBackup.File = db.RunSuccess, "secret-db.dump.gz"
	a.db.FinishBackup(ctx, theirBackup)
	theirApp, err := a.db.CreateApp(ctx, "otherteam", db.App{EnvironmentID: envs[0].ID, ServerID: "othersrv", Name: "secret-app", Image: "nginx:alpine", Port: 80})
	if err != nil {
		t.Fatal(err)
	}
	theirTask, _ := a.db.CreateTask(ctx, db.Task{AppID: theirApp.ID, Name: "secret-task", Schedule: "@daily", Command: "true", Enabled: true})
	theirStorage, _ := a.db.CreateS3Storage(ctx, db.S3Storage{TeamID: "otherteam", Name: "theirs", Bucket: "their-bucket", AccessKey: a.seal("k"), SecretKey: a.seal("s")})
	theirChannel, _ := a.db.CreateChannel(ctx, db.Channel{TeamID: "otherteam", Name: "their-channel", Kind: "webhook", Config: a.seal(`{"url":"https://example.test/x"}`), Events: "deploy", Enabled: true})

	// Our own database and app, to try their ids under.
	projectID, env := a.project("Mine")
	mine := a.newDatabase(projectID, env, "postgres", "maindb", nil)
	myApp := a.newApp(projectID, env, "web", true, nil)
	before := len(a.fake.Calls())

	for _, path := range []string{
		"/databases/" + theirDB.ID + "/backups",
		"/databases/" + theirDB.ID + "/backups/list",
		"/databases/" + theirDB.ID + "/backups/" + theirBackup.ID + "/download",
		// Their backup under our database.
		"/databases/" + mine.ID + "/backups/" + theirBackup.ID + "/download",
		"/apps/" + theirApp.ID + "/tasks",
		"/apps/" + theirApp.ID + "/tasks/" + theirTask.ID,
		"/apps/" + myApp + "/tasks/" + theirTask.ID,
		"/apps/" + myApp + "/tasks/" + theirTask.ID + "/runs",
	} {
		res, body := a.get(path)
		if res.StatusCode != http.StatusNotFound || strings.Contains(body, "secret-") {
			t.Errorf("GET %s: %d", path, res.StatusCode)
		}
	}
	for _, page := range []string{"/settings/storages", "/settings/notifications"} {
		if _, body := a.get(page); strings.Contains(body, "their") {
			t.Errorf("%s lists another team's entry", page)
		}
	}
	token := a.csrf("/projects/new")
	for _, path := range []string{
		"/databases/" + theirDB.ID + "/backups",
		"/databases/" + theirDB.ID + "/backups/schedule",
		"/databases/" + theirDB.ID + "/backups/" + theirBackup.ID + "/restore",
		"/databases/" + mine.ID + "/backups/" + theirBackup.ID + "/restore",
		"/databases/" + mine.ID + "/backups/" + theirBackup.ID + "/delete",
		"/apps/" + theirApp.ID + "/tasks",
		"/apps/" + myApp + "/tasks/" + theirTask.ID,
		"/apps/" + myApp + "/tasks/" + theirTask.ID + "/run",
		"/apps/" + myApp + "/tasks/" + theirTask.ID + "/delete",
		"/settings/storages/" + theirStorage.ID + "/test",
		"/settings/storages/" + theirStorage.ID + "/delete",
		"/settings/notifications/" + theirChannel.ID,
		"/settings/notifications/" + theirChannel.ID + "/test",
		"/settings/notifications/" + theirChannel.ID + "/delete",
	} {
		form := url.Values{"_csrf": {token}, "confirm": {"maindb"}, "schedule": {"@daily"}, "keep": {"3"}, "enabled": {"1"}, "name": {"x"}, "command": {"true"}}
		if res, _ := a.postRaw(a.client, path, form, nil); res.StatusCode != http.StatusNotFound {
			t.Errorf("POST %s: %d, want 404", path, res.StatusCode)
		}
	}
	// Our schedule cannot copy to their bucket.
	base := "/databases/" + mine.ID + "/backups"
	res, _ := a.post(base, base+"/schedule", url.Values{"enabled": {"1"}, "schedule": {"@daily"}, "keep": {"3"}, "storage_id": {theirStorage.ID}})
	wantStatus(t, res, http.StatusUnprocessableEntity)

	// Nothing of theirs changed, and nothing ran.
	if b, _ := a.db.BackupByID(ctx, theirBackup.ID); b.RestoreStatus != "" {
		t.Fatal("their backup was restored")
	}
	if _, err := a.db.TaskByID(ctx, theirTask.ID); err != nil {
		t.Fatal("their task was deleted")
	}
	if c, err := a.db.Channel(ctx, "otherteam", theirChannel.ID); err != nil || c.Events != "deploy" {
		t.Fatalf("their channel was changed: %+v %v", c, err)
	}
	if _, err := a.db.S3Storage(ctx, "otherteam", theirStorage.ID); err != nil {
		t.Fatal("their bucket was removed")
	}
	if list, _ := a.db.ListBackups(ctx, theirDB.ID, 5); len(list) != 1 {
		t.Fatal("a backup was queued for their database")
	}
	if n := len(a.fake.Calls()) - before; n != 0 {
		t.Fatalf("%d commands ran: %v", n, a.fake.Calls()[before:])
	}
}
