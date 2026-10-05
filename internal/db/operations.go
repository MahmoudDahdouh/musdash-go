package db

import (
	"context"
	"database/sql"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
)

// S3Storage is an S3-compatible bucket backups are copied to. The keys are
// sealed.
type S3Storage struct {
	ID        string
	TeamID    string
	Name      string
	Endpoint  string
	Region    string
	Bucket    string
	Prefix    string
	AccessKey string // sealed
	SecretKey string // sealed
	CreatedAt int64
}

const s3Columns = `id, team_id, name, endpoint, region, bucket, prefix, access_key, secret_key, created_at`

func scanS3(row interface{ Scan(...any) error }) (S3Storage, error) {
	var m S3Storage
	err := row.Scan(&m.ID, &m.TeamID, &m.Name, &m.Endpoint, &m.Region, &m.Bucket, &m.Prefix, &m.AccessKey, &m.SecretKey, &m.CreatedAt)
	return m, notFound(err)
}

func (d *DB) CreateS3Storage(ctx context.Context, m S3Storage) (S3Storage, error) {
	m.ID = secret.RandomID()
	m.CreatedAt = now()
	_, err := d.ExecContext(ctx, `INSERT INTO s3_storages (`+s3Columns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.TeamID, m.Name, m.Endpoint, m.Region, m.Bucket, m.Prefix, m.AccessKey, m.SecretKey, m.CreatedAt)
	return m, err
}

// S3Storage loads one storage, scoped to the team.
func (d *DB) S3Storage(ctx context.Context, teamID, id string) (S3Storage, error) {
	return scanS3(d.QueryRowContext(ctx, `SELECT `+s3Columns+` FROM s3_storages WHERE id = ? AND team_id = ?`, id, teamID))
}

// S3StorageByID loads a storage without a team check, for jobs.
func (d *DB) S3StorageByID(ctx context.Context, id string) (S3Storage, error) {
	return scanS3(d.QueryRowContext(ctx, `SELECT `+s3Columns+` FROM s3_storages WHERE id = ?`, id))
}

func (d *DB) ListS3Storages(ctx context.Context, teamID string) ([]S3Storage, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+s3Columns+` FROM s3_storages WHERE team_id = ? ORDER BY name`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []S3Storage
	for rows.Next() {
		m, err := scanS3(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteS3Storage removes a storage unless a backup schedule still uses it.
func (d *DB) DeleteS3Storage(ctx context.Context, teamID, id string) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM backup_configs WHERE storage_id = ?`, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrInUse
		}
		// Copies already in the bucket stay there; musdash just stops
		// knowing about them.
		if _, err := tx.ExecContext(ctx, `UPDATE backups SET storage_id = '' WHERE storage_id = ?`, id); err != nil {
			return err
		}
		return affected(tx.ExecContext(ctx, `DELETE FROM s3_storages WHERE id = ? AND team_id = ?`, id, teamID))
	})
}

// BackupConfig is how one database is backed up.
type BackupConfig struct {
	DatabaseID string
	Schedule   string
	Keep       int
	StorageID  string
	Enabled    bool
	NextRun    int64
}

// BackupConfig returns a database's backup settings, or ErrNotFound when
// none were saved. The caller has checked the database is the team's.
func (d *DB) BackupConfig(ctx context.Context, databaseID string) (BackupConfig, error) {
	var m BackupConfig
	err := d.QueryRowContext(ctx, `SELECT database_id, schedule, keep, storage_id, enabled, next_run FROM backup_configs WHERE database_id = ?`, databaseID).
		Scan(&m.DatabaseID, &m.Schedule, &m.Keep, &m.StorageID, &m.Enabled, &m.NextRun)
	return m, notFound(err)
}

// SaveBackupConfig stores a database's backup settings. A storage must be
// the team's own.
func (d *DB) SaveBackupConfig(ctx context.Context, teamID string, m BackupConfig) error {
	if m.StorageID != "" {
		if _, err := d.S3Storage(ctx, teamID, m.StorageID); err != nil {
			return err
		}
	}
	_, err := d.ExecContext(ctx, `INSERT INTO backup_configs (database_id, schedule, keep, storage_id, enabled, next_run) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (database_id) DO UPDATE SET schedule = excluded.schedule, keep = excluded.keep, storage_id = excluded.storage_id,
			enabled = excluded.enabled, next_run = excluded.next_run`,
		m.DatabaseID, m.Schedule, m.Keep, m.StorageID, m.Enabled, m.NextRun)
	return err
}

// Due is a schedule that has come due, claimed for this run.
type Due struct {
	ID       string // the database's or task's id
	Schedule string
}

// claimDue marks every enabled row of a schedule table whose time has come
// as claimed, by moving next_run to "pending" (-1), and returns them. The
// caller works out and stores the real next run. Doing it in one statement
// means two ticks can never both see the same run as due.
func (d *DB) claimDue(ctx context.Context, table, idColumn string, nowUnix int64) ([]Due, error) {
	rows, err := d.QueryContext(ctx, `UPDATE `+table+` SET next_run = -1 WHERE enabled = 1 AND next_run > 0 AND next_run <= ? RETURNING `+idColumn+`, schedule`, nowUnix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Due
	for rows.Next() {
		var m Due
		if err := rows.Scan(&m.ID, &m.Schedule); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ClaimDueBackups returns the backup schedules that have come due.
func (d *DB) ClaimDueBackups(ctx context.Context, nowUnix int64) ([]Due, error) {
	return d.claimDue(ctx, "backup_configs", "database_id", nowUnix)
}

// ClaimDueTasks returns the scheduled tasks that have come due.
func (d *DB) ClaimDueTasks(ctx context.Context, nowUnix int64) ([]Due, error) {
	return d.claimDue(ctx, "scheduled_tasks", "id", nowUnix)
}

// SetBackupNextRun stores the next run of a schedule that was claimed. A
// schedule saved anew since the claim has a next run of its own, worked
// out from its new expression, and keeps it.
func (d *DB) SetBackupNextRun(ctx context.Context, databaseID string, next int64) error {
	_, err := d.ExecContext(ctx, `UPDATE backup_configs SET next_run = ? WHERE database_id = ? AND next_run = -1`, next, databaseID)
	return err
}

// SetTaskNextRun is SetBackupNextRun for a scheduled task.
func (d *DB) SetTaskNextRun(ctx context.Context, id string, next int64) error {
	_, err := d.ExecContext(ctx, `UPDATE scheduled_tasks SET next_run = ? WHERE id = ? AND next_run = -1`, next, id)
	return err
}

// PendingSchedules returns the schedules a process that died left claimed
// but without a next run, so that start-up can give them one.
func (d *DB) PendingSchedules(ctx context.Context) (backups, tasks []Due, err error) {
	for _, q := range []struct {
		query string
		into  *[]Due
	}{
		{`SELECT database_id, schedule FROM backup_configs WHERE next_run = -1`, &backups},
		{`SELECT id, schedule FROM scheduled_tasks WHERE next_run = -1`, &tasks},
	} {
		rows, err := d.QueryContext(ctx, q.query)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			var m Due
			if err := rows.Scan(&m.ID, &m.Schedule); err != nil {
				rows.Close()
				return nil, nil, err
			}
			*q.into = append(*q.into, m)
		}
		if err := rows.Close(); err != nil {
			return nil, nil, err
		}
	}
	return backups, tasks, nil
}

// Backup statuses and triggers.
const (
	RunRunning = "running"
	RunSuccess = "success"
	RunFailed  = "failed"

	TriggerManual   = "manual"
	TriggerSchedule = "schedule"
)

// Backup is one backup of a database.
type Backup struct {
	ID         string
	DatabaseID string
	Status     string
	Trigger    string
	File       string
	Size       int64
	StorageID  string // where a copy was uploaded; empty when nowhere
	Error      string
	StartedAt  int64
	FinishedAt int64
	// The last restore of this backup: its state, what went wrong, when.
	RestoreStatus string
	RestoreError  string
	RestoredAt    int64
}

const backupColumns = `id, database_id, status, trigger, file, size, storage_id, error, started_at, finished_at, restore_status, restore_error, restored_at`

func scanBackup(row interface{ Scan(...any) error }) (Backup, error) {
	var m Backup
	err := row.Scan(&m.ID, &m.DatabaseID, &m.Status, &m.Trigger, &m.File, &m.Size, &m.StorageID, &m.Error, &m.StartedAt, &m.FinishedAt,
		&m.RestoreStatus, &m.RestoreError, &m.RestoredAt)
	return m, notFound(err)
}

func (d *DB) CreateBackup(ctx context.Context, databaseID, trigger string) (Backup, error) {
	m := Backup{ID: secret.RandomID(), DatabaseID: databaseID, Status: RunRunning, Trigger: trigger, StartedAt: now()}
	_, err := d.ExecContext(ctx, `INSERT INTO backups (id, database_id, status, trigger, started_at) VALUES (?, ?, ?, ?, ?)`,
		m.ID, m.DatabaseID, m.Status, m.Trigger, m.StartedAt)
	return m, err
}

// Backup loads one backup of the given database.
func (d *DB) Backup(ctx context.Context, databaseID, id string) (Backup, error) {
	return scanBackup(d.QueryRowContext(ctx, `SELECT `+backupColumns+` FROM backups WHERE id = ? AND database_id = ?`, id, databaseID))
}

// BackupByID loads a backup for a job.
func (d *DB) BackupByID(ctx context.Context, id string) (Backup, error) {
	return scanBackup(d.QueryRowContext(ctx, `SELECT `+backupColumns+` FROM backups WHERE id = ?`, id))
}

// ListBackups returns a database's backups, newest first.
func (d *DB) ListBackups(ctx context.Context, databaseID string, limit int) ([]Backup, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+backupColumns+` FROM backups WHERE database_id = ? ORDER BY started_at DESC, rowid DESC LIMIT ?`, databaseID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Backup
	for rows.Next() {
		m, err := scanBackup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// FinishBackup records how a backup ended.
func (d *DB) FinishBackup(ctx context.Context, m Backup) error {
	return affected(d.ExecContext(ctx, `UPDATE backups SET status = ?, file = ?, size = ?, storage_id = ?, error = ?, finished_at = ? WHERE id = ?`,
		m.Status, m.File, m.Size, m.StorageID, m.Error, now(), m.ID))
}

func (d *DB) DeleteBackup(ctx context.Context, id string) error {
	return affected(d.ExecContext(ctx, `DELETE FROM backups WHERE id = ?`, id))
}

// SetBackupRestore records the state of a restore of this backup.
func (d *DB) SetBackupRestore(ctx context.Context, id, status, errText string) error {
	return affected(d.ExecContext(ctx, `UPDATE backups SET restore_status = ?, restore_error = ?, restored_at = ? WHERE id = ?`, status, errText, now(), id))
}

// BackupsBeyond returns a database's good backups older than its newest
// keep, oldest first: the ones retention removes. Rows are deleted by the
// caller once their files are gone, so a file is never left without a row
// pointing at it.
func (d *DB) BackupsBeyond(ctx context.Context, databaseID string, keep int) ([]Backup, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+backupColumns+` FROM backups WHERE database_id = ? AND status = ?
		ORDER BY started_at DESC, rowid DESC LIMIT -1 OFFSET ?`, databaseID, RunSuccess, keep)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Backup
	for rows.Next() {
		m, err := scanBackup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// PruneFailedBackups forgets a database's failed backups beyond the newest
// keep. They have no file.
func (d *DB) PruneFailedBackups(ctx context.Context, databaseID string, keep int) error {
	_, err := d.ExecContext(ctx, `DELETE FROM backups WHERE database_id = ? AND status = ? AND id NOT IN (
		SELECT id FROM backups WHERE database_id = ? AND status = ? ORDER BY started_at DESC, rowid DESC LIMIT ?)`,
		databaseID, RunFailed, databaseID, RunFailed, keep)
	return err
}

// FailRunningBackups marks backups, restores and task runs that a process
// that died left running.
func (d *DB) FailRunningBackups(ctx context.Context, reason string) error {
	_, err := d.ExecContext(ctx, `UPDATE backups SET status = ?, error = ?, finished_at = ? WHERE status = ?`, RunFailed, reason, now(), RunRunning)
	if err != nil {
		return err
	}
	_, err = d.ExecContext(ctx, `UPDATE backups SET restore_status = ?, restore_error = ?, restored_at = ? WHERE restore_status = ?`, RunFailed, reason, now(), RunRunning)
	if err != nil {
		return err
	}
	_, err = d.ExecContext(ctx, `UPDATE task_runs SET status = ?, error = ?, finished_at = ? WHERE status = ?`, RunFailed, reason, now(), RunRunning)
	return err
}

// Task is a command run inside an app's container on a schedule.
type Task struct {
	ID        string
	AppID     string
	Name      string
	Schedule  string
	Command   string
	Enabled   bool
	NextRun   int64
	CreatedAt int64
}

const taskColumns = `id, app_id, name, schedule, command, enabled, next_run, created_at`

func scanTask(row interface{ Scan(...any) error }) (Task, error) {
	var m Task
	err := row.Scan(&m.ID, &m.AppID, &m.Name, &m.Schedule, &m.Command, &m.Enabled, &m.NextRun, &m.CreatedAt)
	return m, notFound(err)
}

func (d *DB) CreateTask(ctx context.Context, m Task) (Task, error) {
	m.ID = secret.RandomID()
	m.CreatedAt = now()
	_, err := d.ExecContext(ctx, `INSERT INTO scheduled_tasks (`+taskColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.AppID, m.Name, m.Schedule, m.Command, m.Enabled, m.NextRun, m.CreatedAt)
	return m, err
}

// Task loads one task of the given app.
func (d *DB) Task(ctx context.Context, appID, id string) (Task, error) {
	return scanTask(d.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM scheduled_tasks WHERE id = ? AND app_id = ?`, id, appID))
}

// TaskByID loads a task for a job.
func (d *DB) TaskByID(ctx context.Context, id string) (Task, error) {
	return scanTask(d.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM scheduled_tasks WHERE id = ?`, id))
}

func (d *DB) ListTasks(ctx context.Context, appID string) ([]Task, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+taskColumns+` FROM scheduled_tasks WHERE app_id = ? ORDER BY name, rowid`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		m, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UpdateTask saves a task's editable fields.
func (d *DB) UpdateTask(ctx context.Context, m Task) error {
	return affected(d.ExecContext(ctx, `UPDATE scheduled_tasks SET name = ?, schedule = ?, command = ?, enabled = ?, next_run = ? WHERE id = ? AND app_id = ?`,
		m.Name, m.Schedule, m.Command, m.Enabled, m.NextRun, m.ID, m.AppID))
}

func (d *DB) DeleteTask(ctx context.Context, appID, id string) error {
	return affected(d.ExecContext(ctx, `DELETE FROM scheduled_tasks WHERE id = ? AND app_id = ?`, id, appID))
}

// TaskRun is one execution of a task.
type TaskRun struct {
	ID         string
	TaskID     string
	Status     string
	Trigger    string
	ExitCode   int
	Error      string
	StartedAt  int64
	FinishedAt int64
}

const taskRunColumns = `id, task_id, status, trigger, exit_code, error, started_at, finished_at`

func scanTaskRun(row interface{ Scan(...any) error }) (TaskRun, error) {
	var m TaskRun
	err := row.Scan(&m.ID, &m.TaskID, &m.Status, &m.Trigger, &m.ExitCode, &m.Error, &m.StartedAt, &m.FinishedAt)
	return m, notFound(err)
}

func (d *DB) CreateTaskRun(ctx context.Context, taskID, trigger string) (TaskRun, error) {
	m := TaskRun{ID: secret.RandomID(), TaskID: taskID, Status: RunRunning, Trigger: trigger, StartedAt: now()}
	_, err := d.ExecContext(ctx, `INSERT INTO task_runs (id, task_id, status, trigger, started_at) VALUES (?, ?, ?, ?, ?)`,
		m.ID, m.TaskID, m.Status, m.Trigger, m.StartedAt)
	return m, err
}

// TaskRun loads one run of the given task.
func (d *DB) TaskRun(ctx context.Context, taskID, id string) (TaskRun, error) {
	return scanTaskRun(d.QueryRowContext(ctx, `SELECT `+taskRunColumns+` FROM task_runs WHERE id = ? AND task_id = ?`, id, taskID))
}

// TaskRunByID loads a run for a job.
func (d *DB) TaskRunByID(ctx context.Context, id string) (TaskRun, error) {
	return scanTaskRun(d.QueryRowContext(ctx, `SELECT `+taskRunColumns+` FROM task_runs WHERE id = ?`, id))
}

func (d *DB) ListTaskRuns(ctx context.Context, taskID string, limit int) ([]TaskRun, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+taskRunColumns+` FROM task_runs WHERE task_id = ? ORDER BY started_at DESC, rowid DESC LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskRun
	for rows.Next() {
		m, err := scanTaskRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (d *DB) FinishTaskRun(ctx context.Context, id, status string, exitCode int, errText string) error {
	return affected(d.ExecContext(ctx, `UPDATE task_runs SET status = ?, exit_code = ?, error = ?, finished_at = ? WHERE id = ?`, status, exitCode, errText, now(), id))
}

// PruneTaskRuns deletes a task's runs beyond the newest keep and returns
// the ids of the deleted ones, so their logs can be removed.
func (d *DB) PruneTaskRuns(ctx context.Context, taskID string, keep int) ([]string, error) {
	rows, err := d.QueryContext(ctx, `DELETE FROM task_runs WHERE task_id = ? AND status <> ? AND id NOT IN (
			SELECT id FROM task_runs WHERE task_id = ? ORDER BY started_at DESC, rowid DESC LIMIT ?) RETURNING id`,
		taskID, RunRunning, taskID, keep)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// TaskRunIDs returns the ids of every run of an app's tasks, for removing
// their logs when the app is deleted.
func (d *DB) TaskRunIDs(ctx context.Context, appID string) ([]string, error) {
	rows, err := d.QueryContext(ctx, `SELECT r.id FROM task_runs r JOIN scheduled_tasks t ON t.id = r.task_id WHERE t.app_id = ?`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Channel is where a team is told about events. Config is sealed JSON.
type Channel struct {
	ID        string
	TeamID    string
	Name      string
	Kind      string
	Config    string // sealed
	Events    string // comma separated
	Enabled   bool
	CreatedAt int64
}

// Wants reports whether the channel subscribed to an event kind.
func (c Channel) Wants(event string) bool {
	for _, e := range strings.Split(c.Events, ",") {
		if e == event {
			return true
		}
	}
	return false
}

const channelColumns = `id, team_id, name, kind, config, events, enabled, created_at`

func scanChannel(row interface{ Scan(...any) error }) (Channel, error) {
	var m Channel
	err := row.Scan(&m.ID, &m.TeamID, &m.Name, &m.Kind, &m.Config, &m.Events, &m.Enabled, &m.CreatedAt)
	return m, notFound(err)
}

func (d *DB) CreateChannel(ctx context.Context, m Channel) (Channel, error) {
	m.ID = secret.RandomID()
	m.CreatedAt = now()
	_, err := d.ExecContext(ctx, `INSERT INTO notification_channels (`+channelColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.TeamID, m.Name, m.Kind, m.Config, m.Events, m.Enabled, m.CreatedAt)
	return m, err
}

// Channel loads one channel, scoped to the team.
func (d *DB) Channel(ctx context.Context, teamID, id string) (Channel, error) {
	return scanChannel(d.QueryRowContext(ctx, `SELECT `+channelColumns+` FROM notification_channels WHERE id = ? AND team_id = ?`, id, teamID))
}

func (d *DB) ListChannels(ctx context.Context, teamID string) ([]Channel, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+channelColumns+` FROM notification_channels WHERE team_id = ? ORDER BY name, rowid`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		m, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UpdateChannelEvents saves which events a channel receives and whether it
// is on.
func (d *DB) UpdateChannelEvents(ctx context.Context, teamID, id, events string, enabled bool) error {
	return affected(d.ExecContext(ctx, `UPDATE notification_channels SET events = ?, enabled = ? WHERE id = ? AND team_id = ?`, events, enabled, id, teamID))
}

func (d *DB) DeleteChannel(ctx context.Context, teamID, id string) error {
	return affected(d.ExecContext(ctx, `DELETE FROM notification_channels WHERE id = ? AND team_id = ?`, id, teamID))
}

// TeamOfEnvironment returns the team that owns an environment, for work
// that starts from a resource rather than a signed-in person.
func (d *DB) TeamOfEnvironment(ctx context.Context, environmentID string) (string, error) {
	var team string
	err := d.QueryRowContext(ctx, `SELECT p.team_id FROM environments e JOIN projects p ON p.id = e.project_id WHERE e.id = ?`, environmentID).Scan(&team)
	return team, notFound(err)
}

// BackupRunning reports whether a backup of the database is waiting or
// being made.
func (d *DB) BackupRunning(ctx context.Context, databaseID string) (bool, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT count(*) FROM backups WHERE database_id = ? AND status = ?`, databaseID, RunRunning).Scan(&n)
	return n > 0, err
}

// TaskRunning reports whether a run of the task is waiting or going.
func (d *DB) TaskRunning(ctx context.Context, taskID string) (bool, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT count(*) FROM task_runs WHERE task_id = ? AND status = ?`, taskID, RunRunning).Scan(&n)
	return n > 0, err
}
