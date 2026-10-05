package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/backup"
	"github.com/MahmoudDahdouh/musdash-go/internal/cron"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/deploy"
	"github.com/MahmoudDahdouh/musdash-go/internal/notify"
	"github.com/MahmoudDahdouh/musdash-go/internal/ops"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

const (
	backupsShown = 50
	// maxTaskOutput is how much of a run's output a page shows: its end.
	maxTaskOutput = 256 << 10
	maxLabel      = 60
)

// checkSchedule validates a cron expression from a form.
func checkSchedule(f *ui.Form, schedule string) {
	if _, err := cron.Parse(schedule); err != nil {
		f.Fail("schedule", "This is not a schedule musdash can read: "+err.Error()+". Try 0 3 * * * for every day at 03:00.")
	}
}

// next is a schedule's next run from now, or 0 when it is switched off.
func next(enabled bool, schedule string) int64 {
	if !enabled {
		return 0
	}
	return ops.NextRun(schedule, time.Now())
}

// ---- Database backups ----

func (s *Server) backupsView(ctx context.Context, teamID string, v pages.DatabaseView) (pages.BackupsView, error) {
	var b pages.BackupsView
	cfg, err := s.DB.BackupConfig(ctx, v.DB.ID)
	switch {
	case err == nil:
		b.Config, b.HasCfg = cfg, true
	case !errors.Is(err, db.ErrNotFound):
		return b, err
	}
	if b.Storages, err = s.DB.ListS3Storages(ctx, teamID); err != nil {
		return b, err
	}
	b.List, err = s.DB.ListBackups(ctx, v.DB.ID, backupsShown)
	return b, err
}

func (s *Server) renderBackups(w http.ResponseWriter, r *http.Request, status int, v pages.DatabaseView, f ui.Form) {
	b, err := s.backupsView(r.Context(), sessionFrom(r).TeamID, v)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, status, pages.DatabaseBackups(s.databaseShell(w, r, v), v, b, f))
}

func (s *Server) databaseBackups(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.loadDatabase(w, r); ok {
		s.renderBackups(w, r, http.StatusOK, v, ui.Form{})
	}
}

// databaseBackupList is the part of the page that refreshes while a backup
// or a restore is going.
func (s *Server) databaseBackupList(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadDatabase(w, r)
	if !ok {
		return
	}
	b, err := s.backupsView(r.Context(), sessionFrom(r).TeamID, v)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, pages.BackupList(sessionFrom(r).CSRFToken, v, b))
}

func (s *Server) databaseBackupSchedule(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadDatabase(w, r)
	if !ok {
		return
	}
	var f ui.Form
	f.Set("_submitted", "1")
	form := func(key string) string {
		val := strings.TrimSpace(r.PostFormValue(key))
		f.Set(key, val)
		return val
	}
	cfg := db.BackupConfig{DatabaseID: v.DB.ID, Schedule: form("schedule"), StorageID: form("storage_id"), Enabled: form("enabled") == "1"}
	checkSchedule(&f, cfg.Schedule)
	var err error
	if cfg.Keep, err = strconv.Atoi(form("keep")); err != nil || cfg.Keep < 1 || cfg.Keep > 365 {
		f.Fail("keep", "Enter a number from 1 to 365.")
	}
	if v.Engine.DumpCmd == "" {
		s.notFound(w, r)
		return
	}
	if f.OK() {
		cfg.NextRun = next(cfg.Enabled, cfg.Schedule)
		err := s.DB.SaveBackupConfig(r.Context(), sessionFrom(r).TeamID, cfg)
		switch {
		case errors.Is(err, db.ErrNotFound):
			f.Fail("storage_id", "Choose one of the buckets listed.")
		case err != nil:
			s.fail(w, r, err)
			return
		}
	}
	if !f.OK() {
		s.renderBackups(w, r, http.StatusUnprocessableEntity, v, f)
		return
	}
	if cfg.Enabled {
		setFlash(w, r, ui.ToneOK, "Schedule saved.")
	} else {
		setFlash(w, r, ui.ToneOK, "Saved. Scheduled backups are switched off.")
	}
	redirect(w, r, "/databases/"+v.DB.ID+"/backups")
}

func (s *Server) databaseBackupNow(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadDatabase(w, r)
	if !ok {
		return
	}
	back := "/databases/" + v.DB.ID + "/backups"
	switch {
	case v.Engine.DumpCmd == "":
		setFlash(w, r, ui.ToneDanger, "musdash cannot back up "+v.Engine.Label+" databases yet.")
	case v.DB.Status != db.AppRunning:
		setFlash(w, r, ui.ToneWarn, "Start the database first: it has to be running to be backed up.")
	default:
		if _, err := s.Ops.EnqueueBackup(r.Context(), v.DB.ID, db.TriggerManual); err != nil {
			s.Log.Error("queue backup", "database", v.DB.ID, "err", err)
			setFlash(w, r, ui.ToneDanger, "The backup could not be queued.")
			break
		}
		setFlash(w, r, ui.ToneOK, "Backup started.")
	}
	redirect(w, r, back)
}

// loadBackup fetches the backup in the path, of the database in the path.
func (s *Server) loadBackup(w http.ResponseWriter, r *http.Request) (pages.DatabaseView, db.Backup, bool) {
	v, ok := s.loadDatabase(w, r)
	if !ok {
		return v, db.Backup{}, false
	}
	b, err := s.DB.Backup(r.Context(), v.DB.ID, r.PathValue("bid"))
	if errors.Is(err, db.ErrNotFound) {
		s.notFound(w, r)
		return v, b, false
	}
	if err != nil {
		s.fail(w, r, err)
		return v, b, false
	}
	return v, b, true
}

func (s *Server) databaseBackupDownload(w http.ResponseWriter, r *http.Request) {
	_, b, ok := s.loadBackup(w, r)
	if !ok {
		return
	}
	if b.Status != db.RunSuccess || b.File == "" {
		s.notFound(w, r)
		return
	}
	rn, file, err := s.Ops.BackupPath(r.Context(), b)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	src, err := rn.ReadFile(r.Context(), file)
	if err != nil {
		setFlash(w, r, ui.ToneDanger, "The backup's file is no longer on the server.")
		redirect(w, r, "/databases/"+b.DatabaseID+"/backups")
		return
	}
	defer src.Close()
	// The name is one musdash made: letters, digits, dots and hyphens.
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+path.Base(b.File)+`"`)
	w.Header().Set("Content-Length", strconv.FormatInt(b.Size, 10))
	io.Copy(w, src)
}

func (s *Server) databaseBackupRestore(w http.ResponseWriter, r *http.Request) {
	v, b, ok := s.loadBackup(w, r)
	if !ok {
		return
	}
	back := "/databases/" + v.DB.ID + "/backups"
	switch {
	case strings.TrimSpace(r.PostFormValue("confirm")) != v.DB.Name:
		setFlash(w, r, ui.ToneDanger, "Nothing was restored: the name you typed did not match.")
	case v.Engine.RestoreCmd == "":
		setFlash(w, r, ui.ToneDanger, "musdash cannot restore "+v.Engine.Label+" databases. Download the file and load it with the engine's own tool.")
	case v.DB.Status != db.AppRunning:
		setFlash(w, r, ui.ToneWarn, "Start the database first: it has to be running to be restored.")
	default:
		if err := s.Ops.EnqueueRestore(r.Context(), b); err != nil {
			setFlash(w, r, ui.ToneDanger, sentence(err))
			break
		}
		setFlash(w, r, ui.ToneOK, "Restore started.")
	}
	redirect(w, r, back)
}

func (s *Server) databaseBackupDelete(w http.ResponseWriter, r *http.Request) {
	v, b, ok := s.loadBackup(w, r)
	if !ok {
		return
	}
	if err := s.Ops.DeleteBackup(r.Context(), b); err != nil {
		setFlash(w, r, ui.ToneDanger, "The backup could not be deleted: "+err.Error())
	} else if b.Status == db.RunSuccess {
		setFlash(w, r, ui.ToneOK, "Backup deleted.")
	}
	redirect(w, r, "/databases/"+v.DB.ID+"/backups")
}

// ---- Scheduled tasks ----

func (s *Server) renderTasks(w http.ResponseWriter, r *http.Request, status int, v pages.AppView, f ui.Form) {
	ctx := r.Context()
	tasks, err := s.DB.ListTasks(ctx, v.App.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rows := make([]pages.TaskRow, 0, len(tasks))
	for _, t := range tasks {
		row := pages.TaskRow{Task: t}
		runs, err := s.DB.ListTaskRuns(ctx, t.ID, 1)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if len(runs) > 0 {
			row.Last = &runs[0]
		}
		rows = append(rows, row)
	}
	s.render(w, r, status, pages.AppTasks(s.appShell(w, r, v), v, rows, f))
}

func (s *Server) appTasks(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.loadApp(w, r); ok {
		s.renderTasks(w, r, http.StatusOK, v, ui.Form{})
	}
}

// taskFromForm reads and checks a task's fields.
func taskFromForm(r *http.Request, t db.Task) (db.Task, ui.Form) {
	var f ui.Form
	f.Set("_submitted", "1")
	form := func(key string) string {
		val := strings.TrimSpace(r.PostFormValue(key))
		f.Set(key, val)
		return val
	}
	t.Name, t.Command, t.Schedule, t.Enabled = form("name"), form("command"), form("schedule"), form("enabled") == "1"
	if t.Name == "" || len(t.Name) > maxLabel || !plainText(t.Name) {
		f.Fail("name", labelProblem(t.Name, "Enter a name of up to 60 characters."))
	}
	if t.Command == "" || len(t.Command) > ops.MaxTaskCommand || strings.ContainsRune(t.Command, 0) {
		f.Fail("command", "Enter the command to run, up to 2000 characters.")
	}
	checkSchedule(&f, t.Schedule)
	t.NextRun = next(t.Enabled, t.Schedule)
	return t, f
}

func (s *Server) appTaskCreate(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	t, f := taskFromForm(r, db.Task{AppID: v.App.ID})
	if !f.OK() {
		s.renderTasks(w, r, http.StatusUnprocessableEntity, v, f)
		return
	}
	if _, err := s.DB.CreateTask(r.Context(), t); err != nil {
		s.fail(w, r, err)
		return
	}
	setFlash(w, r, ui.ToneOK, "Task added.")
	redirect(w, r, "/apps/"+v.App.ID+"/tasks")
}

// loadTask fetches the task in the path, of the app in the path.
func (s *Server) loadTask(w http.ResponseWriter, r *http.Request) (pages.AppView, db.Task, bool) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return v, db.Task{}, false
	}
	t, err := s.DB.Task(r.Context(), v.App.ID, r.PathValue("tid"))
	if errors.Is(err, db.ErrNotFound) {
		s.notFound(w, r)
		return v, t, false
	}
	if err != nil {
		s.fail(w, r, err)
		return v, t, false
	}
	return v, t, true
}

// taskOutput reads the end of a run's output.
func (s *Server) taskOutput(runID string) string {
	f, err := os.Open(s.Cfg.TaskLogPath(runID))
	if err != nil {
		return ""
	}
	defer f.Close()
	cut := ""
	if info, err := f.Stat(); err == nil && info.Size() > maxTaskOutput {
		f.Seek(-maxTaskOutput, io.SeekEnd)
		cut = "[the start of the output is not shown]\n"
	}
	raw, _ := io.ReadAll(io.LimitReader(f, maxTaskOutput))
	return cut + strings.ToValidUTF8(string(raw), "�")
}

func (s *Server) renderTask(w http.ResponseWriter, r *http.Request, status int, v pages.AppView, t db.Task, f ui.Form) {
	ctx := r.Context()
	runs, err := s.DB.ListTaskRuns(ctx, t.ID, ops.KeepTaskRuns)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// The run asked for, or the latest one that has ended.
	shown := ""
	if want := r.URL.Query().Get("run"); want != "" {
		if run, err := s.DB.TaskRun(ctx, t.ID, want); err == nil && run.Status != db.RunRunning {
			shown = run.ID
		}
	}
	for _, run := range runs {
		if shown == "" && run.Status != db.RunRunning {
			shown = run.ID
		}
	}
	output := ""
	if shown != "" {
		output = s.taskOutput(shown)
	}
	s.render(w, r, status, pages.AppTask(s.appShell(w, r, v), v, t, runs, shown, output, f))
}

func (s *Server) appTask(w http.ResponseWriter, r *http.Request) {
	if v, t, ok := s.loadTask(w, r); ok {
		s.renderTask(w, r, http.StatusOK, v, t, ui.Form{})
	}
}

// appTaskRuns is the history, refreshed while a run is going. When the run
// ends the whole page is loaded again, to show its output.
func (s *Server) appTaskRuns(w http.ResponseWriter, r *http.Request) {
	v, t, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	runs, err := s.DB.ListTaskRuns(r.Context(), t.ID, ops.KeepTaskRuns)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	busy := false
	for _, run := range runs {
		busy = busy || run.Status == db.RunRunning
	}
	if !busy {
		w.Header().Set("HX-Refresh", "true")
	}
	s.render(w, r, http.StatusOK, pages.TaskRuns(v, t, runs, ""))
}

func (s *Server) appTaskSave(w http.ResponseWriter, r *http.Request) {
	v, t, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	updated, f := taskFromForm(r, t)
	if !f.OK() {
		s.renderTask(w, r, http.StatusUnprocessableEntity, v, t, f)
		return
	}
	if err := s.DB.UpdateTask(r.Context(), updated); err != nil {
		s.fail(w, r, err)
		return
	}
	setFlash(w, r, ui.ToneOK, "Task saved.")
	redirect(w, r, "/apps/"+v.App.ID+"/tasks/"+t.ID)
}

func (s *Server) appTaskRun(w http.ResponseWriter, r *http.Request) {
	v, t, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	back := "/apps/" + v.App.ID + "/tasks/" + t.ID
	busy, err := s.DB.TaskRunning(r.Context(), t.ID)
	switch {
	case err != nil:
		s.fail(w, r, err)
		return
	case busy:
		setFlash(w, r, ui.ToneWarn, "This task is already running.")
	case v.App.Container == "" || v.App.Status != db.AppRunning:
		setFlash(w, r, ui.ToneWarn, "The app has no running container to run the command in. Deploy or start it first.")
	default:
		if _, err := s.Ops.EnqueueTask(r.Context(), t, db.TriggerManual); err != nil {
			s.Log.Error("queue task", "task", t.ID, "err", err)
			setFlash(w, r, ui.ToneDanger, "The task could not be queued.")
			break
		}
		setFlash(w, r, ui.ToneOK, "Task started.")
	}
	redirect(w, r, back)
}

func (s *Server) appTaskDelete(w http.ResponseWriter, r *http.Request) {
	v, t, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	runs, err := s.DB.ListTaskRuns(ctx, t.ID, 10*ops.KeepTaskRuns)
	if err == nil {
		err = s.DB.DeleteTask(ctx, v.App.ID, t.ID)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, run := range runs {
		os.Remove(s.Cfg.TaskLogPath(run.ID))
	}
	setFlash(w, r, ui.ToneOK, "Task deleted.")
	redirect(w, r, "/apps/"+v.App.ID+"/tasks")
}

// ---- Backup storage ----

func (s *Server) renderStorages(w http.ResponseWriter, r *http.Request, status int, f ui.Form) {
	list, err := s.DB.ListS3Storages(r.Context(), sessionFrom(r).TeamID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, status, pages.Storages(s.shell(w, r, "Backup storage", "settings"), list, f))
}

func (s *Server) storagesPage(w http.ResponseWriter, r *http.Request) {
	s.renderStorages(w, r, http.StatusOK, ui.Form{})
}

func (s *Server) storageCreate(w http.ResponseWriter, r *http.Request) {
	var f ui.Form
	f.Set("_submitted", "1")
	form := func(key string) string {
		val := strings.TrimSpace(r.PostFormValue(key))
		f.Set(key, val)
		return val
	}
	m := db.S3Storage{TeamID: sessionFrom(r).TeamID, Name: form("name"), Endpoint: form("endpoint"), Region: form("region"), Bucket: form("bucket"), Prefix: strings.Trim(form("prefix"), "/")}
	keys := backup.S3{Endpoint: m.Endpoint, Region: m.Region, Bucket: m.Bucket, Prefix: m.Prefix, AccessKey: form("access_key"), SecretKey: strings.TrimSpace(r.PostFormValue("secret_key"))}
	if m.Name == "" || len(m.Name) > maxLabel || !plainText(m.Name) {
		f.Fail("name", labelProblem(m.Name, "Enter a name of up to 60 characters."))
	}
	if err := keys.Validate(); err != nil {
		// Validate names the field in its message; put it where it belongs.
		msg := sentence(err)
		switch {
		case strings.Contains(msg, "endpoint"):
			f.Fail("endpoint", msg)
		case strings.Contains(msg, "region"):
			f.Fail("region", msg)
		case strings.Contains(msg, "bucket"):
			f.Fail("bucket", msg)
		case strings.Contains(msg, "folder"):
			f.Fail("prefix", msg)
		case strings.Contains(msg, "access key"):
			f.Fail("access_key", msg)
		default:
			f.Fail("secret_key", msg)
		}
	}
	if f.OK() {
		var err error
		if m.AccessKey, err = s.Box.SealString(keys.AccessKey); err == nil {
			m.SecretKey, err = s.Box.SealString(keys.SecretKey)
		}
		if err == nil {
			_, err = s.DB.CreateS3Storage(r.Context(), m)
		}
		switch {
		case db.IsUnique(err):
			f.Fail("name", "A bucket with this name is already listed.")
		case err != nil:
			s.fail(w, r, err)
			return
		}
	}
	if !f.OK() {
		// The access key is not a secret on its own, but there is no need
		// to send it back either.
		delete(f.Values, "access_key")
		s.renderStorages(w, r, http.StatusUnprocessableEntity, f)
		return
	}
	setFlash(w, r, ui.ToneOK, "Bucket added. Use Test to check that musdash can reach it.")
	redirect(w, r, "/settings/storages")
}

func (s *Server) storageTest(w http.ResponseWriter, r *http.Request) {
	teamID := sessionFrom(r).TeamID
	m, err := s.DB.S3Storage(r.Context(), teamID, r.PathValue("sid"))
	if errors.Is(err, db.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.checkStorage(r.Context(), teamID, m); err != nil {
		setFlash(w, r, ui.ToneDanger, m.Name+" could not be reached: "+err.Error())
	} else {
		setFlash(w, r, ui.ToneOK, m.Name+" works: musdash could list the folder.")
	}
	redirect(w, r, "/settings/storages")
}

// checkStorage lists a storage's folder from the team's first server.
func (s *Server) checkStorage(ctx context.Context, teamID string, m db.S3Storage) error {
	keys, err := s.Ops.OpenStorage(m)
	if err != nil {
		return errors.New("its keys cannot be decrypted: was the master key changed?")
	}
	servers, err := s.DB.ListServers(ctx, teamID)
	if err != nil || len(servers) == 0 {
		return errors.New("there is no server to test from")
	}
	rn, err := s.Pool.Runner(ctx, servers[0])
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// A directory of its own for the keys file, gone afterwards.
	dir := path.Join(deploy.PathsOn(s.Cfg, rn).WorkDir(), "storage-"+secret.RandomID())
	if err := rn.MkdirAll(ctx, dir, 0o700); err != nil {
		return err
	}
	defer rn.RemoveAll(context.WithoutCancel(ctx), dir)
	return keys.Check(ctx, rn, dir)
}

func (s *Server) storageDelete(w http.ResponseWriter, r *http.Request) {
	err := s.DB.DeleteS3Storage(r.Context(), sessionFrom(r).TeamID, r.PathValue("sid"))
	switch {
	case errors.Is(err, db.ErrNotFound):
		s.notFound(w, r)
		return
	case errors.Is(err, db.ErrInUse):
		setFlash(w, r, ui.ToneWarn, "A database's backup schedule still copies to this bucket. Change that schedule first.")
	case err != nil:
		s.fail(w, r, err)
		return
	default:
		setFlash(w, r, ui.ToneOK, "Bucket removed from musdash. What is stored in it was not touched.")
	}
	redirect(w, r, "/settings/storages")
}

// ---- Notification channels ----

func (s *Server) renderNotifications(w http.ResponseWriter, r *http.Request, status int, kind notify.KindInfo, f ui.Form) {
	list, err := s.DB.ListChannels(r.Context(), sessionFrom(r).TeamID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, status, pages.Notifications(s.shell(w, r, "Notifications", "settings"), list, kind, f))
}

func (s *Server) notificationsPage(w http.ResponseWriter, r *http.Request) {
	kind, ok := notify.Kind(r.URL.Query().Get("kind"))
	if !ok {
		kind = notify.Kinds[0]
	}
	s.renderNotifications(w, r, http.StatusOK, kind, ui.Form{})
}

func (s *Server) notificationCreate(w http.ResponseWriter, r *http.Request) {
	kind, ok := notify.Kind(r.PostFormValue("kind"))
	if !ok {
		s.notFound(w, r)
		return
	}
	var f ui.Form
	f.Set("_submitted", "1")
	name := strings.TrimSpace(r.PostFormValue("name"))
	f.Set("name", name)
	if name == "" || len(name) > maxLabel || !plainText(name) {
		f.Fail("name", labelProblem(name, "Enter a name of up to 60 characters."))
	}
	cfg := map[string]string{}
	for _, field := range kind.Fields {
		v := strings.TrimSpace(r.PostFormValue("cfg_" + field.Key))
		if v != "" {
			cfg[field.Key] = v
		}
		if !field.Secret {
			f.Set("cfg_"+field.Key, v)
		}
	}
	if err := notify.Check(kind.Kind, cfg); err != nil {
		f.Fail("_form", sentence(err))
	}
	if !f.OK() {
		s.renderNotifications(w, r, http.StatusUnprocessableEntity, kind, f)
		return
	}
	sealed, err := s.Ops.SealChannelConfig(cfg)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// A new channel starts with every kind of event; what is not wanted is
	// switched off on the page.
	events := []string{notify.EventDeploy, notify.EventBackup, notify.EventTask, notify.EventContainer, notify.EventDisk}
	if _, err := s.DB.CreateChannel(r.Context(), db.Channel{TeamID: sessionFrom(r).TeamID, Name: name, Kind: kind.Kind, Config: sealed, Events: strings.Join(events, ","), Enabled: true}); err != nil {
		s.fail(w, r, err)
		return
	}
	setFlash(w, r, ui.ToneOK, "Channel added. Send a test to check that it arrives.")
	redirect(w, r, "/settings/notifications")
}

func (s *Server) loadChannel(w http.ResponseWriter, r *http.Request) (db.Channel, bool) {
	ch, err := s.DB.Channel(r.Context(), sessionFrom(r).TeamID, r.PathValue("cid"))
	if errors.Is(err, db.ErrNotFound) {
		s.notFound(w, r)
		return ch, false
	}
	if err != nil {
		s.fail(w, r, err)
		return ch, false
	}
	return ch, true
}

func (s *Server) notificationSave(w http.ResponseWriter, r *http.Request) {
	ch, ok := s.loadChannel(w, r)
	if !ok {
		return
	}
	var events []string
	for _, e := range notify.Events {
		if r.PostFormValue("ev-"+ch.ID+"-"+e.Kind) == "1" {
			events = append(events, e.Kind)
		}
	}
	enabled := r.PostFormValue("on-"+ch.ID) == "1"
	if err := s.DB.UpdateChannelEvents(r.Context(), ch.TeamID, ch.ID, strings.Join(events, ","), enabled); err != nil {
		s.fail(w, r, err)
		return
	}
	setFlash(w, r, ui.ToneOK, "Channel saved.")
	redirect(w, r, "/settings/notifications")
}

func (s *Server) notificationTest(w http.ResponseWriter, r *http.Request) {
	ch, ok := s.loadChannel(w, r)
	if !ok {
		return
	}
	if err := s.Ops.TestChannel(r.Context(), ch); err != nil {
		setFlash(w, r, ui.ToneDanger, "The test did not arrive: "+err.Error())
	} else {
		setFlash(w, r, ui.ToneOK, "Test sent to "+ch.Name+".")
	}
	redirect(w, r, "/settings/notifications")
}

func (s *Server) notificationDelete(w http.ResponseWriter, r *http.Request) {
	ch, ok := s.loadChannel(w, r)
	if !ok {
		return
	}
	if err := s.DB.DeleteChannel(r.Context(), ch.TeamID, ch.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	setFlash(w, r, ui.ToneOK, "Channel removed.")
	redirect(w, r, "/settings/notifications")
}
