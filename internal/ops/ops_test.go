package ops

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/config"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/jobs"
	"github.com/MahmoudDahdouh/musdash-go/internal/notify"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner/runnertest"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
	"github.com/MahmoudDahdouh/musdash-go/migrations"
)

type fixedRunners struct{ r runner.Runner }

func (f fixedRunners) Runner(context.Context, db.Server) (runner.Runner, error) { return f.r, nil }

const (
	dbPassword   = "pg-pass-0123456789"
	dbContainer  = "musdash-db-test"
	appContainer = "musdash-app-test"
	accessKey    = "AKIAEXAMPLEKEY"
	secretKey    = "s3-secret-0123456789"
)

type env struct {
	t      *testing.T
	o      *Ops
	db     *db.DB
	cfg    *config.Config
	fake   *runnertest.Fake
	team   string
	server db.Server
	app    db.App
	pg     db.Database

	mu     sync.Mutex
	hooks  []map[string]any // what the webhook channel received
	stdin  []string         // what restore commands were fed
	hookAt *httptest.Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	cfg := &config.Config{DataDir: t.TempDir()}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.Migrate(ctx, migrations.FS); err != nil {
		t.Fatal(err)
	}
	_, team, err := d.CreateFirstUser(ctx, "o@example.com", "O", "hash")
	if err != nil {
		t.Fatal(err)
	}
	server, err := d.EnsureLocalServer(ctx, team, "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	project, _ := d.CreateProject(ctx, team, "Shop", "")
	envs, _ := d.ListEnvironments(ctx, project.ID)
	app, err := d.CreateApp(ctx, team, db.App{EnvironmentID: envs[0].ID, ServerID: server.ID, Name: "web", Image: "nginx:alpine", Port: 80, HealthTimeout: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetAppRuntime(ctx, app.ID, db.AppRunning, appContainer, 20001, "nginx:alpine"); err != nil {
		t.Fatal(err)
	}
	app, _ = d.AppByID(ctx, app.ID)

	box, _ := secret.New(secret.RandomBytes(secret.KeySize))
	sealed, _ := box.SealString(dbPassword)
	pg, err := d.CreateDatabase(ctx, team, db.Database{EnvironmentID: envs[0].ID, ServerID: server.ID, Name: "maindb", Engine: "postgres", Image: "postgres:17-alpine", Username: "app", DBName: "app", Password: sealed})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetDatabaseState(ctx, pg.ID, db.AppRunning, dbContainer, ""); err != nil {
		t.Fatal(err)
	}
	pg, _ = d.DatabaseByID(ctx, pg.ID)

	e := &env{t: t, db: d, cfg: cfg, team: team, server: server, app: app, pg: pg}
	e.fake = &runnertest.Fake{Handle: e.handle}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := jobs.New(d.DB, log, 2)
	e.o = New(d, box, q, fixedRunners{e.fake}, cfg, log)
	e.o.Sender = notify.Sender{Dialer: notify.Dialer{AllowLoopback: true}}
	e.o.S3Lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("198.51.100.7")}, nil
	}
	e.o.Register()
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		q.Stop(stop)
	})

	// A webhook channel that wants everything.
	e.hookAt = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		json.NewDecoder(r.Body).Decode(&m)
		e.mu.Lock()
		e.hooks = append(e.hooks, m)
		e.mu.Unlock()
	}))
	t.Cleanup(e.hookAt.Close)
	e.channel("hook", "deploy,backup,task,container,disk", true)
	return e
}

func (e *env) channel(name, events string, enabled bool) db.Channel {
	e.t.Helper()
	cfg, err := e.o.SealChannelConfig(map[string]string{"url": e.hookAt.URL})
	if err != nil {
		e.t.Fatal(err)
	}
	ch, err := e.db.CreateChannel(context.Background(), db.Channel{TeamID: e.team, Name: name, Kind: notify.KindWebhook, Config: cfg, Events: events, Enabled: enabled})
	if err != nil {
		e.t.Fatal(err)
	}
	return ch
}

// handle answers as a server whose database dumps "DUMP OF app".
func (e *env) handle(line string, c runner.Cmd) (string, error) {
	switch {
	case strings.HasPrefix(line, "docker exec --interactive "+dbContainer):
		raw, _ := io.ReadAll(c.Stdin)
		e.mu.Lock()
		e.stdin = append(e.stdin, string(raw))
		e.mu.Unlock()
	case strings.HasPrefix(line, "docker exec "+dbContainer):
		return "DUMP OF app", nil
	}
	return "", nil
}

// told waits for the next notification and returns its text.
func (e *env) told(n int) string {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		if len(e.hooks) >= n {
			raw, _ := json.Marshal(e.hooks[n-1])
			e.mu.Unlock()
			return string(raw)
		}
		e.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatalf("notification %d did not arrive", n)
	return ""
}

// toldAbout waits for a notification containing the text.
func (e *env) toldAbout(text string) {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		raw, _ := json.Marshal(e.hooks)
		e.mu.Unlock()
		if strings.Contains(string(raw), text) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatalf("no notification mentioned %q", text)
}

func (e *env) backup(trigger string) db.Backup {
	e.t.Helper()
	ctx := context.Background()
	b, err := e.o.EnqueueBackup(ctx, e.pg.ID, trigger)
	if err != nil {
		e.t.Fatal(err)
	}
	return e.waitBackup(b.ID, func(b db.Backup) bool { return b.Status != db.RunRunning })
}

func (e *env) waitBackup(id string, done func(db.Backup) bool) db.Backup {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, err := e.db.BackupByID(context.Background(), id)
		if err != nil {
			e.t.Fatal(err)
		}
		if done(b) {
			return b
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatal("the backup did not finish")
	return db.Backup{}
}

func gunzip(t *testing.T, s string) string {
	t.Helper()
	r, err := gzip.NewReader(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(r)
	return string(raw)
}

func (e *env) noSecretOnACommandLine() {
	e.t.Helper()
	for _, c := range e.fake.Calls() {
		for _, s := range []string{dbPassword, accessKey, secretKey} {
			if strings.Contains(c, s) {
				e.t.Fatalf("a secret is on a command line: %s", c)
			}
		}
	}
}

func TestBackupIsMadeKeptAndAnnounced(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	go e.o.deliverLoop(t.Context())

	b := e.backup(db.TriggerManual)
	if b.Status != db.RunSuccess || b.Error != "" || b.StorageID != "" || !strings.HasPrefix(b.File, "maindb-") || !strings.HasSuffix(b.File, "-"+b.ID+".dump.gz") {
		t.Fatalf("%+v", b)
	}
	content, mode, ok := e.fake.File(e.cfg.DatabaseBackupDir(e.pg.ID) + "/" + b.File)
	if !ok || mode != 0o600 || gunzip(t, content) != "DUMP OF app" || b.Size != int64(len(content)) {
		t.Fatalf("backup file: present %v, mode %o, size %d (recorded %d)", ok, mode, len(content), b.Size)
	}
	if msg := e.told(1); !strings.Contains(msg, "Backup of maindb finished") || !strings.Contains(msg, b.File) {
		t.Fatalf("notification: %s", msg)
	}

	// A dump that fails leaves no file and says why.
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "docker exec "+dbContainer) {
			io.WriteString(c.Stderr, "pg_dump: error: connection to server failed")
			return "half a du", runnertest.Exit("docker", 1, "")
		}
		return "", nil
	}
	failed := e.backup(db.TriggerManual)
	if failed.Status != db.RunFailed || failed.File != "" || !strings.Contains(failed.Error, "connection to server failed") {
		t.Fatalf("%+v", failed)
	}
	if msg := e.told(2); !strings.Contains(msg, "Backup of maindb failed") || !strings.Contains(msg, "connection to server failed") {
		t.Fatalf("notification: %s", msg)
	}
	list, _ := e.db.ListBackups(ctx, e.pg.ID, 10)
	if len(list) != 2 {
		t.Fatalf("%d backups listed", len(list))
	}

	// A database that is not running cannot be backed up.
	e.db.SetDatabaseState(ctx, e.pg.ID, db.AppStopped, dbContainer, "")
	if b := e.backup(db.TriggerManual); b.Status != db.RunFailed || !strings.Contains(b.Error, "not running") {
		t.Fatalf("%+v", b)
	}
	e.noSecretOnACommandLine()
}

func TestBackupIsCopiedToStorageAndOldOnesGo(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ak, _ := e.o.Box.SealString(accessKey)
	sk, _ := e.o.Box.SealString(secretKey)
	st, err := e.db.CreateS3Storage(ctx, db.S3Storage{TeamID: e.team, Name: "offsite", Endpoint: "https://s3.example.test", Bucket: "backups", Prefix: "musdash", AccessKey: ak, SecretKey: sk})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.db.SaveBackupConfig(ctx, e.team, db.BackupConfig{DatabaseID: e.pg.ID, Schedule: "@daily", Keep: 2, StorageID: st.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	dir := e.cfg.DatabaseBackupDir(e.pg.ID)
	envFileSeen, envFilePath := "", ""
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "docker run --rm --env-file") {
			envFilePath = strings.Fields(line)[4]
			envFileSeen, _, _ = e.fake.File(envFilePath)
		}
		return e.handle(line, c)
	}

	first := e.backup(db.TriggerSchedule)
	if first.Status != db.RunSuccess || first.StorageID != st.ID || first.Error != "" {
		t.Fatalf("%+v", first)
	}
	// The keys reach rclone through a file that is gone afterwards.
	if !strings.Contains(envFileSeen, secretKey) || !strings.Contains(envFileSeen, accessKey) {
		t.Fatal("rclone was not given the keys")
	}
	if _, _, still := e.fake.File(envFilePath); still || !strings.HasPrefix(envFilePath, dir+"/.rclone-") {
		t.Fatalf("the keys file %s was left behind, or is not in the backup directory", envFilePath)
	}
	upload := ""
	for _, c := range e.fake.Calls() {
		if strings.Contains(c, " copyto ") {
			upload = c
		}
	}
	if !strings.Contains(upload, "copyto /backup/"+first.File+" s3:backups/musdash/"+first.File) || !strings.Contains(upload, "source="+dir+",target=/backup,readonly") {
		t.Fatalf("upload command: %s", upload)
	}

	// Two more: the first is beyond "keep 2" and goes, here and there.
	time.Sleep(1100 * time.Millisecond) // started_at has one-second resolution
	second := e.backup(db.TriggerSchedule)
	time.Sleep(1100 * time.Millisecond)
	third := e.backup(db.TriggerSchedule)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := e.db.BackupByID(ctx, first.ID); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the oldest backup was kept beyond the limit")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, _, ok := e.fake.File(dir + "/" + first.File); ok {
		t.Fatal("the oldest backup's file is still on the server")
	}
	removedRemote := false
	for _, c := range e.fake.Calls() {
		removedRemote = removedRemote || strings.Contains(c, "deletefile s3:backups/musdash/"+first.File)
	}
	if !removedRemote {
		t.Fatal("the oldest backup's copy was not removed from the storage")
	}
	for _, b := range []db.Backup{second, third} {
		if _, _, ok := e.fake.File(dir + "/" + b.File); !ok {
			t.Fatalf("a backup within the limit was removed: %s", b.File)
		}
	}

	// An upload that fails leaves a good backup on the server and says so.
	go e.o.deliverLoop(t.Context())
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if strings.Contains(line, " copyto ") {
			return "", runnertest.Exit("docker", 1, "Failed to copyto: AccessDenied: Access Denied")
		}
		return e.handle(line, c)
	}
	time.Sleep(1100 * time.Millisecond)
	kept := e.backup(db.TriggerSchedule)
	if kept.Status != db.RunSuccess || kept.StorageID != "" || !strings.Contains(kept.Error, "Access Denied") {
		t.Fatalf("%+v", kept)
	}
	if _, _, ok := e.fake.File(dir + "/" + kept.File); !ok {
		t.Fatal("the backup whose upload failed is not on the server")
	}
	e.toldAbout("was not copied to the storage")
	e.noSecretOnACommandLine()

	// A bucket that cannot be reached must not be what fills the disk: the
	// file on the server goes, and the record stays so that the copy is
	// tried again.
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if strings.Contains(line, " deletefile ") {
			return "", runnertest.Exit("docker", 1, "Failed to deletefile: connection timed out")
		}
		return e.handle(line, c)
	}
	time.Sleep(1100 * time.Millisecond)
	newest := e.backup(db.TriggerSchedule)
	deadline = time.Now().Add(5 * time.Second)
	for {
		if _, _, ok := e.fake.File(dir + "/" + third.File); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("an old backup's file stayed on the server because its copy could not be removed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := e.db.BackupByID(ctx, third.ID); err != nil {
		t.Fatalf("the record of a backup whose copy is still in the bucket was dropped: %v", err)
	}
	_ = newest

	// A storage in use cannot be deleted from under its schedule.
	if err := e.db.DeleteS3Storage(ctx, e.team, st.ID); err != db.ErrInUse {
		t.Fatalf("deleting a storage in use: %v", err)
	}
}

func TestRestore(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	b := e.backup(db.TriggerManual)

	if err := e.o.EnqueueRestore(ctx, b); err != nil {
		t.Fatal(err)
	}
	b = e.waitBackup(b.ID, func(b db.Backup) bool { return b.RestoreStatus != db.RunRunning })
	if b.RestoreStatus != db.RunSuccess || b.RestoreError != "" || b.RestoredAt == 0 {
		t.Fatalf("%+v", b)
	}
	e.mu.Lock()
	fed := append([]string(nil), e.stdin...)
	e.mu.Unlock()
	if len(fed) != 1 || fed[0] != "DUMP OF app" {
		t.Fatalf("the restore command was fed %q", fed)
	}
	restoreCmd := ""
	for _, c := range e.fake.Calls() {
		if strings.HasPrefix(c, "docker exec --interactive") {
			restoreCmd = c
		}
	}
	if !strings.Contains(restoreCmd, "pg_restore") || !strings.Contains(restoreCmd, "--clean") {
		t.Fatalf("restore command: %s", restoreCmd)
	}

	// A restore that fails says what the engine said.
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "docker exec --interactive") {
			io.WriteString(c.Stderr, "pg_restore: error: could not execute query")
			return "", runnertest.Exit("docker", 1, "")
		}
		return "", nil
	}
	if err := e.o.EnqueueRestore(ctx, b); err != nil {
		t.Fatal(err)
	}
	b = e.waitBackup(b.ID, func(b db.Backup) bool { return b.RestoreStatus != db.RunRunning })
	if b.RestoreStatus != db.RunFailed || !strings.Contains(b.RestoreError, "could not execute query") {
		t.Fatalf("%+v", b)
	}

	// Only a finished backup can be restored, and not twice at once.
	if err := e.o.EnqueueRestore(ctx, db.Backup{ID: "x", Status: db.RunFailed}); err == nil {
		t.Fatal("a failed backup was accepted for restore")
	}
	b.RestoreStatus = db.RunRunning
	if err := e.o.EnqueueRestore(ctx, b); err == nil {
		t.Fatal("a second restore of the same backup was accepted")
	}

	// Deleting a backup removes its file and its record.
	b.RestoreStatus = db.RunFailed
	if err := e.o.DeleteBackup(ctx, b); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := e.fake.File(e.cfg.DatabaseBackupDir(e.pg.ID) + "/" + b.File); ok {
		t.Fatal("the deleted backup's file is still there")
	}
	if _, err := e.db.BackupByID(ctx, b.ID); err != db.ErrNotFound {
		t.Fatalf("the deleted backup's record: %v", err)
	}
}

func TestTickFiresEachScheduleOnce(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 5, 3, 0, 1, 0, time.UTC)
	// Due a minute ago.
	if err := e.db.SaveBackupConfig(ctx, e.team, db.BackupConfig{DatabaseID: e.pg.ID, Schedule: "0 3 * * *", Keep: 7, Enabled: true, NextRun: at.Add(-time.Second).Unix()}); err != nil {
		t.Fatal(err)
	}
	task, err := e.db.CreateTask(ctx, db.Task{AppID: e.app.ID, Name: "tidy", Schedule: "*/5 * * * *", Command: "echo tidy", Enabled: true, NextRun: at.Add(-time.Second).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	off, err := e.db.CreateTask(ctx, db.Task{AppID: e.app.ID, Name: "off", Schedule: "* * * * *", Command: "echo off", Enabled: false, NextRun: at.Add(-time.Second).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	// The clean-up already ran today.
	e.db.SetSetting(ctx, settingCleanupDay, at.Format("2006-01-02"))

	e.o.Tick(ctx, at)
	e.o.Tick(ctx, at) // a second tick in the same minute finds nothing due

	backups, _ := e.db.ListBackups(ctx, e.pg.ID, 10)
	if len(backups) != 1 || backups[0].Trigger != db.TriggerSchedule {
		t.Fatalf("backups after two ticks: %+v", backups)
	}
	runs, _ := e.db.ListTaskRuns(ctx, task.ID, 10)
	if len(runs) != 1 {
		t.Fatalf("task runs after two ticks: %+v", runs)
	}
	if runs, _ := e.db.ListTaskRuns(ctx, off.ID, 10); len(runs) != 0 {
		t.Fatal("a disabled task ran")
	}
	cfg, _ := e.db.BackupConfig(ctx, e.pg.ID)
	if want := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC).Unix(); cfg.NextRun != want {
		t.Fatalf("next backup at %s, want %s", time.Unix(cfg.NextRun, 0).UTC(), time.Unix(want, 0).UTC())
	}
	task, _ = e.db.TaskByID(ctx, task.ID)
	if want := time.Date(2026, 10, 5, 3, 5, 0, 0, time.UTC).Unix(); task.NextRun != want {
		t.Fatalf("next task run at %s", time.Unix(task.NextRun, 0).UTC())
	}

	// A schedule saved anew between the claim and the bookkeeping keeps
	// the next run of its new expression.
	e.db.SetTaskNextRun(ctx, task.ID, 0) // not claimed: no effect
	if got, _ := e.db.TaskByID(ctx, task.ID); got.NextRun == 0 {
		t.Fatal("a next run was stored over a schedule that was not claimed")
	}
	// One left claimed by a tick that could not finish is settled by the
	// next tick instead of staying silent until a restart.
	e.db.Exec(`UPDATE scheduled_tasks SET next_run = -1 WHERE id = ?`, off.ID)
	e.o.Tick(ctx, at.Add(30*time.Second))
	if err := e.db.QueryRowContext(ctx, `SELECT next_run FROM scheduled_tasks WHERE id = ?`, off.ID).Scan(new(int64)); err != nil {
		t.Fatal(err)
	}
	var settled int64
	e.db.QueryRowContext(ctx, `SELECT next_run FROM scheduled_tasks WHERE id = ?`, off.ID).Scan(&settled)
	if settled <= 0 {
		t.Fatalf("a schedule left claimed was not settled: %d", settled)
	}

	// A schedule whose previous run is still going is skipped, not queued
	// behind it.
	e.waitBackup(backups[0].ID, func(b db.Backup) bool { return b.Status != db.RunRunning })
	busy, _ := e.db.CreateBackup(ctx, e.pg.ID, db.TriggerManual) // never finishes
	e.db.SetBackupNextRun(ctx, e.pg.ID, at.Unix())
	e.o.Tick(ctx, at.Add(time.Minute))
	if backups, _ := e.db.ListBackups(ctx, e.pg.ID, 10); len(backups) != 2 {
		t.Fatalf("a backup was queued behind one still running: %d", len(backups))
	}
	if cfg, _ := e.db.BackupConfig(ctx, e.pg.ID); cfg.NextRun <= at.Unix() {
		t.Fatal("the skipped schedule was not moved on")
	}
	_ = busy
}

func TestRecoverRepairsWhatACrashLeft(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// A schedule claimed by a tick that died, a backup and a task run left
	// running.
	e.db.SaveBackupConfig(ctx, e.team, db.BackupConfig{DatabaseID: e.pg.ID, Schedule: "@hourly", Keep: 7, Enabled: true, NextRun: -1})
	task, _ := e.db.CreateTask(ctx, db.Task{AppID: e.app.ID, Name: "tidy", Schedule: "@daily", Command: "true", Enabled: true, NextRun: -1})
	b, _ := e.db.CreateBackup(ctx, e.pg.ID, db.TriggerSchedule)
	run, _ := e.db.CreateTaskRun(ctx, task.ID, db.TriggerSchedule)
	// What that backup had written so far: a file without a name yet, which
	// no page lists and retention does not count. Next to it a finished
	// dump and the keys of an upload that was cut short.
	dir := e.o.Cfg.DatabaseBackupDir(e.pg.ID)
	os.MkdirAll(dir, 0o700)
	half, keys, finished := filepath.Join(dir, ".musdash-1234567"), filepath.Join(dir, ".rclone-abc.env"), filepath.Join(dir, "pg-20260101-000000-abc.dump.gz")
	for _, p := range []string{half, keys, finished} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := e.o.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{half, keys} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s was left behind", filepath.Base(p))
		}
	}
	if _, err := os.Stat(finished); err != nil {
		t.Errorf("a finished backup was removed: %v", err)
	}
	cfg, _ := e.db.BackupConfig(ctx, e.pg.ID)
	task, _ = e.db.TaskByID(ctx, task.ID)
	if cfg.NextRun <= time.Now().Unix() || task.NextRun <= time.Now().Unix() {
		t.Fatalf("next runs after recovery: %d, %d", cfg.NextRun, task.NextRun)
	}
	b, _ = e.db.BackupByID(ctx, b.ID)
	run, _ = e.db.TaskRunByID(ctx, run.ID)
	if b.Status != db.RunFailed || run.Status != db.RunFailed || !strings.Contains(b.Error, "restarted") {
		t.Fatalf("backup %q, run %q", b.Status, run.Status)
	}

	// Their jobs, requeued by the restart, find nothing left to do.
	before := len(e.fake.Calls())
	if err := e.o.runBackupJob(ctx, []byte(`{"backup_id":"`+b.ID+`"}`)); err != nil {
		t.Fatal(err)
	}
	if err := e.o.runTaskJob(ctx, []byte(`{"run_id":"`+run.ID+`"}`)); err != nil {
		t.Fatal(err)
	}
	if calls := e.fake.Calls()[before:]; len(calls) != 0 {
		t.Fatalf("a job that had already ended ran again: %v", calls)
	}
}

func (e *env) runTask(task db.Task) db.TaskRun {
	e.t.Helper()
	ctx := context.Background()
	run, err := e.o.EnqueueTask(ctx, task, db.TriggerManual)
	if err != nil {
		e.t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := e.db.TaskRunByID(ctx, run.ID)
		if err != nil {
			e.t.Fatal(err)
		}
		if got.Status != db.RunRunning {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatal("the task did not finish")
	return run
}

func TestScheduledTask(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	go e.o.deliverLoop(t.Context())
	task, err := e.db.CreateTask(ctx, db.Task{AppID: e.app.ID, Name: "tidy", Schedule: "@daily", Command: `php artisan "schedule:run" --quiet; echo done`, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	exit := 0
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "docker exec "+appContainer) {
			if exit != 0 {
				return "about to fail\n", runnertest.Exit("docker", exit, "")
			}
			return "tidied 3 rows\n", nil
		}
		return "", nil
	}

	run := e.runTask(task)
	if run.Status != db.RunSuccess || run.ExitCode != 0 || run.Error != "" {
		t.Fatalf("%+v", run)
	}
	if out, _ := os.ReadFile(e.cfg.TaskLogPath(run.ID)); string(out) != "tidied 3 rows\n" {
		t.Fatalf("log: %q", out)
	}
	// The command is one argument to the container's shell, as typed.
	calls := e.fake.Calls()
	if got, want := calls[len(calls)-1], "docker exec "+appContainer+" sh -c "+task.Command; got != want {
		t.Fatalf("command\n got  %s\n want %s", got, want)
	}

	// A command that fails is recorded with its status and output, and
	// people are told.
	exit = 3
	run = e.runTask(task)
	if run.Status != db.RunFailed || run.ExitCode != 3 || !strings.Contains(run.Error, "status 3") {
		t.Fatalf("%+v", run)
	}
	if out, _ := os.ReadFile(e.cfg.TaskLogPath(run.ID)); !bytes.Contains(out, []byte("about to fail")) {
		t.Fatalf("log: %q", out)
	}
	if msg := e.told(1); !strings.Contains(msg, "Scheduled task tidy of web failed") {
		t.Fatalf("notification: %s", msg)
	}

	// Without a running container nothing is run, and the run says why.
	// Nobody is told: the app's people know it is stopped.
	e.db.SetAppRuntime(ctx, e.app.ID, db.AppStopped, "", 0, "")
	before := len(e.fake.Calls())
	run = e.runTask(task)
	if run.Status != db.RunFailed || !strings.Contains(run.Error, "no running container") || len(e.fake.Calls()) != before {
		t.Fatalf("%+v", run)
	}
	time.Sleep(100 * time.Millisecond)
	e.mu.Lock()
	told := len(e.hooks)
	e.mu.Unlock()
	if told != 1 {
		t.Fatalf("%d notifications after a task on a stopped app, want the 1 from before", told)
	}

	// A run whose task has gone meanwhile is ended, not left running.
	orphan, _ := e.db.CreateTaskRun(ctx, task.ID, db.TriggerSchedule)
	e.db.Exec(`PRAGMA foreign_keys = OFF`)
	e.db.Exec(`UPDATE task_runs SET task_id = 'gone' WHERE id = ?`, orphan.ID)
	e.o.runTaskJob(ctx, []byte(`{"run_id":"`+orphan.ID+`"}`))
	if got, _ := e.db.TaskRunByID(ctx, orphan.ID); got.Status != db.RunFailed {
		t.Fatalf("a run whose task is gone: %q", got.Status)
	}
	e.db.Exec(`DELETE FROM task_runs WHERE id = ?`, orphan.ID)
	e.db.Exec(`PRAGMA foreign_keys = ON`)

	// History is bounded, and the logs of forgotten runs go with them.
	var oldest string
	for i := 0; i < KeepTaskRuns+3; i++ {
		r, _ := e.db.CreateTaskRun(ctx, task.ID, db.TriggerSchedule)
		e.db.FinishTaskRun(ctx, r.ID, db.RunSuccess, 0, "")
		if i == 0 {
			oldest = r.ID
			os.WriteFile(e.cfg.TaskLogPath(r.ID), []byte("old"), 0o600)
		}
	}
	e.db.SetAppRuntime(ctx, e.app.ID, db.AppRunning, appContainer, 20001, "nginx:alpine")
	exit = 0
	e.runTask(task)
	runs, _ := e.db.ListTaskRuns(ctx, task.ID, 500)
	if len(runs) != KeepTaskRuns {
		t.Fatalf("%d runs kept, want %d", len(runs), KeepTaskRuns)
	}
	_ = oldest
}

func TestCleanup(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	go e.o.deliverLoop(t.Context())
	e.fake.Handle = func(line string, c runner.Cmd) (string, error) {
		switch {
		case strings.HasPrefix(line, "docker image prune"):
			return "Deleted Images:\nsha256:abc\n\nTotal reclaimed space: 1.2GB\n", nil
		case strings.HasPrefix(line, "docker ps --all --filter label="):
			return appContainer + "\trunning\tapp\t" + e.app.ID + "\td1\tUp 2 hours\n" +
				// Its app exists: kept although stopped.
				"musdash-old-of-web\texited\tapp\t" + e.app.ID + "\td0\tExited (0) 3 days ago\n" +
				// Their resources are gone: removed, unless still running.
				"musdash-gone-app\texited\tapp\tgoneapp12345\td9\tExited (137) 3 days ago\n" +
				"musdash-db-gone\tcreated\tdatabase\tgonedb123456\t\tCreated\n" +
				"musdash-gone-running\trunning\tapp\tgoneapp99999\td9\tUp 3 days\n", nil
		case strings.HasPrefix(line, "docker network ls"):
			return "musdash-" + e.app.EnvironmentID + "\n" + // its environment exists
				"musdash-goneenvaaaaa\n" + // gone, and nothing is attached
				"musdash-goneenvbbbbb\n" + // gone, but a stopped container of somebody's is attached
				"musdash-db-abc\nmusdash-GONEENVCCCCC\nmusdash-goneenvaaaaa-extra\nbridge\n", nil // not the name of an environment's network
		case strings.HasPrefix(line, "docker ps --all --quiet --filter network="):
			if strings.HasSuffix(line, "=musdash-goneenvbbbbb") {
				return "3f2a9c1d\n", nil
			}
			return "", nil
		case strings.HasPrefix(line, "docker info"):
			return "/var/lib/docker\n", nil
		case strings.HasPrefix(line, "df "):
			return "Filesystem     1024-blocks     Used Available Capacity Mounted on\n/dev/sda1         80000000 72800000   7200000      91% /\n", nil
		}
		return "", nil
	}
	if err := e.o.runCleanupJob(ctx, []byte(`{"server_id":"`+e.server.ID+`"}`)); err != nil {
		t.Fatal(err)
	}
	all := "\n" + strings.Join(e.fake.Calls(), "\n") + "\n"
	for _, want := range []string{
		"\ndocker image prune --force\n",
		"\ndocker builder prune --force --filter until=168h\n",
		"\ndocker rm --force musdash-gone-app\n",
		"\ndocker rm --force musdash-db-gone\n",
		"\ndocker network rm musdash-goneenvaaaaa\n",
		"\ndf -P -k -- /var/lib/docker\n",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:%s", strings.TrimSpace(want), all)
		}
	}
	for _, never := range []string{"prune --all", "prune -a", "volume prune", "system prune", "rm --force " + appContainer, "rm --force musdash-old-of-web", "rm --force musdash-gone-running",
		"network rm musdash-" + e.app.EnvironmentID, "network rm musdash-goneenvbbbbb", "network rm musdash-db-abc", "network rm musdash-GONEENVCCCCC", "network rm musdash-goneenvaaaaa-extra", "network rm bridge",
		"network=musdash-db-abc", "network=bridge", "network=musdash-" + e.app.EnvironmentID} {
		if strings.Contains(all, never) {
			t.Errorf("the clean-up ran something with %q:%s", never, all)
		}
	}
	if msg := e.told(1); !strings.Contains(msg, "is 91% full") {
		t.Fatalf("notification: %s", msg)
	}
}

func TestCleanupRunsOnceADay(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	queued := func() int {
		var n int
		e.db.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE kind = ?`, JobCleanup).Scan(&n)
		return n
	}
	day := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	e.o.cleanupIfDue(ctx, day.Add(2*time.Hour+59*time.Minute))
	if queued() != 0 {
		t.Fatal("the clean-up ran before its hour")
	}
	e.o.cleanupIfDue(ctx, day.Add(3*time.Hour))
	e.o.cleanupIfDue(ctx, day.Add(3*time.Hour+time.Minute))
	e.o.cleanupIfDue(ctx, day.Add(23*time.Hour))
	if queued() != 1 {
		t.Fatalf("%d clean-ups queued in one day, want 1", queued())
	}
	e.o.cleanupIfDue(ctx, day.Add(27*time.Hour))
	if queued() != 2 {
		t.Fatalf("%d clean-ups after the next day's hour, want 2", queued())
	}
}

func TestParseDF(t *testing.T) {
	for out, want := range map[string]int{
		"Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 100 42 58 42% /\n":                  42,
		"Filesystem 1024-blocks Used Available Capacity Mounted on\noverlay 100 100 0 100% /var/lib/docker\n":     100,
		"Filesystem 1024-blocks Used Available Capacity Mounted on\n":                                             -1,
		"df: /var/lib/docker: No such file or directory\n":                                                        -1,
		"Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 100 42 58 lots /\n":                 -1,
		"Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/mapper/a-very-long-name 100 7 93 7% /\n": 7,
	} {
		got, ok := parseDF(out)
		if (want < 0) == ok || (ok && got != want) {
			t.Errorf("%q: %d %v, want %d", out, got, ok, want)
		}
	}
}

func TestNotificationsGoWhereTheyWereAskedFor(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.channel("only deploys", "deploy", true)
	e.channel("switched off", "deploy,backup", false)
	e.db.SetSetting(ctx, db.SettingInstanceDomain, "dash.example.com")
	count := func() int {
		e.mu.Lock()
		defer e.mu.Unlock()
		return len(e.hooks)
	}

	// Two channels want deployments; one of them is all that wants backups.
	e.o.deliver(ctx, e.team, notify.Event{Kind: notify.EventDeploy, OK: true, Title: "web was deployed", URL: "/apps/x/deployments/y"})
	if count() != 2 {
		t.Fatalf("%d deliveries of a deployment, want 2", count())
	}
	if msg := e.told(1); !strings.Contains(msg, "https://dash.example.com/apps/x/deployments/y") {
		t.Fatalf("the link is not an address: %s", msg)
	}
	e.o.deliver(ctx, e.team, notify.Event{Kind: notify.EventBackup, Title: "Backup of maindb failed"})
	if count() != 3 {
		t.Fatalf("%d deliveries after a backup, want 3", count())
	}
	// Another team's event reaches none of them.
	e.o.deliver(ctx, "someotherteam", notify.Event{Kind: notify.EventDeploy, Title: "x"})
	if count() != 3 {
		t.Fatal("an event of another team was delivered")
	}

	// A container that keeps crashing is reported once in a while.
	at := time.Now()
	for i := 0; i < 5; i++ {
		e.o.deliver(ctx, e.team, notify.Event{Kind: notify.EventContainer, Title: "The app web stopped unexpectedly", At: at.Add(time.Duration(i) * time.Minute)})
	}
	e.o.deliver(ctx, e.team, notify.Event{Kind: notify.EventContainer, Title: "The app api stopped unexpectedly", At: at})
	if count() != 5 {
		t.Fatalf("%d deliveries after a crash loop, want 5 (one per app)", count())
	}
	e.o.deliver(ctx, e.team, notify.Event{Kind: notify.EventContainer, Title: "The app web stopped unexpectedly", At: at.Add(containerQuiet + time.Minute)})
	if count() != 6 {
		t.Fatal("a crash after the quiet time was not reported")
	}

	// A channel that takes its time does not hold up the others.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(800 * time.Millisecond)
	}))
	defer slow.Close()
	slowCfg, _ := e.o.SealChannelConfig(map[string]string{"url": slow.URL})
	for i := range 3 {
		e.db.CreateChannel(ctx, db.Channel{TeamID: e.team, Name: "slow" + strconv.Itoa(i), Kind: notify.KindWebhook, Config: slowCfg, Events: "disk", Enabled: true})
	}
	began := time.Now()
	e.o.deliver(ctx, e.team, notify.Event{Kind: notify.EventDisk, Title: "The disk is 91% full"})
	if took := time.Since(began); took > 2*time.Second {
		t.Fatalf("three slow channels took %s: they were sent one after the other", took)
	}

	// A full queue drops the event instead of blocking the caller.
	done := make(chan struct{})
	go func() {
		for i := 0; i < cap(e.o.events)+10; i++ {
			e.o.Notify(e.team, notify.Event{Kind: notify.EventDeploy, Title: "flood"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Notify blocked with nothing reading the queue")
	}

	// The test event goes to a channel whatever it subscribes to.
	chs, _ := e.db.ListChannels(ctx, e.team)
	before := count()
	for _, ch := range chs {
		if ch.Name == "switched off" {
			if err := e.o.TestChannel(ctx, ch); err != nil {
				t.Fatal(err)
			}
		}
	}
	if count() != before+1 {
		t.Fatal("the test notification did not arrive")
	}
}
