package ops

import (
	"context"
	"errors"
	"fmt"
	"path"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/backup"
	"github.com/MahmoudDahdouh/musdash-go/internal/catalog"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/deploy"
	"github.com/MahmoudDahdouh/musdash-go/internal/jobs"
	"github.com/MahmoudDahdouh/musdash-go/internal/notify"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
)

// How many backups are kept when nothing was chosen, and how many failed
// ones stay listed.
const (
	DefaultKeep = 7
	keepFailed  = 10
	// retainTimeout bounds the removal of old backups after a new one.
	retainTimeout = 10 * time.Minute
)

type backupPayload struct {
	BackupID string `json:"backup_id"`
}

// databaseLock is the lock key a database's start job also uses: a backup
// or a restore never overlaps a restart of the same database.
func databaseLock(id string) string { return "database:" + id }

// EnqueueBackup records a new backup of a database and queues the job that
// makes it.
func (o *Ops) EnqueueBackup(ctx context.Context, databaseID, trigger string) (db.Backup, error) {
	b, err := o.DB.CreateBackup(ctx, databaseID, trigger)
	if err != nil {
		return b, err
	}
	_, err = o.Queue.Enqueue(ctx, JobBackup, backupPayload{BackupID: b.ID}, jobs.WithLockKey(databaseLock(databaseID)), jobs.WithMaxAttempts(1))
	if err != nil {
		b.Status, b.Error = db.RunFailed, "could not be queued: "+err.Error()
		rec, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		o.DB.FinishBackup(rec, b)
	}
	return b, err
}

// target is a database with what is needed to run a command in it.
type target struct {
	db.Database
	tpl    catalog.DBTemplate
	r      runner.Runner
	dir    string // its backup directory on the server
	teamID string
}

func (o *Ops) target(ctx context.Context, databaseID string) (target, error) {
	var t target
	m, err := o.DB.DatabaseByID(ctx, databaseID)
	if err != nil {
		return t, err
	}
	t.Database = m
	tpl, ok := catalog.Database(m.Engine)
	if !ok {
		return t, fmt.Errorf("unknown engine %q", m.Engine)
	}
	t.tpl = tpl
	if t.teamID, err = o.DB.TeamOfEnvironment(ctx, m.EnvironmentID); err != nil {
		return t, err
	}
	server, err := o.DB.ServerByID(ctx, m.ServerID)
	if err != nil {
		return t, err
	}
	if t.r, err = o.Runners.Runner(ctx, server); err != nil {
		return t, err
	}
	t.dir = deploy.PathsOn(o.Cfg, t.r).DatabaseBackupDir(m.ID)
	return t, nil
}

func (t target) running() error {
	if t.Container == "" || t.Status != db.AppRunning {
		return errors.New("the database is not running")
	}
	return nil
}

func (o *Ops) runBackupJob(ctx context.Context, raw []byte) error {
	var p backupPayload
	if err := decode(raw, &p); err != nil {
		return jobs.Permanent(err)
	}
	b, err := o.DB.BackupByID(ctx, p.BackupID)
	if errors.Is(err, db.ErrNotFound) {
		return nil // the database was deleted while this waited
	}
	if err != nil {
		return err
	}
	if b.Status != db.RunRunning {
		// Already ended: marked failed by a restart, which also requeued
		// this job.
		return nil
	}

	t, err := o.target(ctx, b.DatabaseID)
	if err == nil {
		err = o.backup(ctx, t, &b)
	}

	// The outcome is recorded even when shutdown cancelled the job.
	rec, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	switch {
	case err == nil:
		b.Status = db.RunSuccess
	case ctx.Err() != nil:
		b.Status, b.Error = db.RunFailed, "interrupted: musdash was stopping"
	default:
		b.Status, b.Error = db.RunFailed, err.Error()
	}
	if ferr := o.DB.FinishBackup(rec, b); ferr != nil {
		o.Log.Error("record backup", "backup", b.ID, "err", ferr)
	}
	if err != nil && ctx.Err() != nil {
		return err
	}
	if t.teamID != "" {
		o.Notify(t.teamID, backupEvent(t, b))
	}
	if err != nil {
		return jobs.Permanent(err)
	}
	// Removing old copies from a bucket takes a moment each; it gets time
	// of its own, and is not cut short by a shutdown that has just begun.
	keep, cancelKeep := context.WithTimeout(context.WithoutCancel(ctx), retainTimeout)
	defer cancelKeep()
	o.retain(keep, t)
	return nil
}

// backup makes the file and copies it to the storage. A copy that fails
// leaves a good backup on the server; b.Error then says what happened.
func (o *Ops) backup(ctx context.Context, t target, b *db.Backup) error {
	if t.tpl.DumpCmd == "" {
		return fmt.Errorf("%s databases cannot be backed up by musdash yet", t.tpl.Label)
	}
	if err := t.running(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, o.backupTimeout)
	defer cancel()
	if err := t.r.MkdirAll(ctx, t.dir, 0o700); err != nil {
		return err
	}
	file := fmt.Sprintf("%s-%s-%s.dump.gz", t.Name, time.Unix(b.StartedAt, 0).UTC().Format("20060102-150405"), b.ID)
	size, err := backup.Dump(ctx, t.r, t.Container, t.tpl.DumpCmd, path.Join(t.dir, file))
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("stopped after %s", o.backupTimeout)
		}
		return err
	}
	b.File, b.Size = file, size

	cfg, err := o.DB.BackupConfig(ctx, t.ID)
	if err != nil || cfg.StorageID == "" {
		return nil
	}
	s3, err := o.storage(ctx, cfg.StorageID)
	if err == nil {
		err = s3.Upload(ctx, t.r, t.dir, file)
	}
	if err != nil {
		b.Error = "kept on the server, but not copied to the storage: " + err.Error()
		return nil
	}
	b.StorageID = cfg.StorageID
	return nil
}

// storage opens a storage's keys.
func (o *Ops) storage(ctx context.Context, id string) (backup.S3, error) {
	m, err := o.DB.S3StorageByID(ctx, id)
	if err != nil {
		return backup.S3{}, fmt.Errorf("the storage no longer exists")
	}
	return o.OpenStorage(m)
}

// OpenStorage turns a stored storage into one that can be used.
func (o *Ops) OpenStorage(m db.S3Storage) (backup.S3, error) {
	s := backup.S3{Endpoint: m.Endpoint, Region: m.Region, Bucket: m.Bucket, Prefix: m.Prefix, Lookup: o.S3Lookup, AllowLocal: o.S3AllowLocal}
	var err error
	if s.AccessKey, err = o.Box.OpenString(m.AccessKey); err != nil {
		return s, err
	}
	s.SecretKey, err = o.Box.OpenString(m.SecretKey)
	return s, err
}

// retain removes the backups beyond the number to keep. It is best effort:
// a backup that could not be removed is tried again after the next one.
func (o *Ops) retain(ctx context.Context, t target) {
	keep := DefaultKeep
	if cfg, err := o.DB.BackupConfig(ctx, t.ID); err == nil && cfg.Keep > 0 {
		keep = cfg.Keep
	}
	old, err := o.DB.BackupsBeyond(ctx, t.ID, keep)
	if err != nil {
		o.Log.Error("list old backups", "database", t.ID, "err", err)
		return
	}
	for _, b := range old {
		if err := o.remove(ctx, t.r, b); err != nil {
			o.Log.Warn("remove old backup", "backup", b.ID, "err", err)
		}
	}
	if err := o.DB.PruneFailedBackups(ctx, t.ID, keepFailed); err != nil {
		o.Log.Error("forget failed backups", "database", t.ID, "err", err)
	}
}

// remove deletes a backup's file, its copy in the storage, and then its
// record. The file on the server goes first: a bucket that cannot be
// reached must not be what fills the server's disk. The record goes last
// and stays while the copy could not be removed, so that it is tried again.
func (o *Ops) remove(ctx context.Context, r runner.Runner, b db.Backup) error {
	dir := deploy.PathsOn(o.Cfg, r).DatabaseBackupDir(b.DatabaseID)
	if b.File != "" {
		if err := r.RemoveAll(ctx, path.Join(dir, b.File)); err != nil {
			return err
		}
	}
	if b.File != "" && b.StorageID != "" {
		if s3, err := o.storage(ctx, b.StorageID); err == nil {
			if err := s3.Delete(ctx, r, dir, b.File); err != nil {
				return err
			}
		}
	}
	return o.DB.DeleteBackup(ctx, b.ID)
}

// DeleteBackup removes one backup at a person's request.
func (o *Ops) DeleteBackup(ctx context.Context, b db.Backup) error {
	if b.Status == db.RunRunning || b.RestoreStatus == db.RunRunning {
		return errors.New("this backup is in use right now")
	}
	t, err := o.target(ctx, b.DatabaseID)
	if err != nil {
		return err
	}
	return o.remove(ctx, t.r, b)
}

// BackupPath is where a backup's file is on its server, with the Runner to
// read it.
func (o *Ops) BackupPath(ctx context.Context, b db.Backup) (runner.Runner, string, error) {
	t, err := o.target(ctx, b.DatabaseID)
	if err != nil {
		return nil, "", err
	}
	return t.r, path.Join(t.dir, b.File), nil
}

// EnqueueRestore queues putting a backup's content back into its database.
func (o *Ops) EnqueueRestore(ctx context.Context, b db.Backup) error {
	if b.Status != db.RunSuccess || b.File == "" {
		return errors.New("only a finished backup can be restored")
	}
	if b.RestoreStatus == db.RunRunning {
		return errors.New("this backup is already being restored")
	}
	if err := o.DB.SetBackupRestore(ctx, b.ID, db.RunRunning, ""); err != nil {
		return err
	}
	_, err := o.Queue.Enqueue(ctx, JobRestore, backupPayload{BackupID: b.ID}, jobs.WithLockKey(databaseLock(b.DatabaseID)), jobs.WithMaxAttempts(1))
	if err != nil {
		rec, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		o.DB.SetBackupRestore(rec, b.ID, db.RunFailed, "could not be queued: "+err.Error())
	}
	return err
}

func (o *Ops) runRestoreJob(ctx context.Context, raw []byte) error {
	var p backupPayload
	if err := decode(raw, &p); err != nil {
		return jobs.Permanent(err)
	}
	b, err := o.DB.BackupByID(ctx, p.BackupID)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if b.RestoreStatus != db.RunRunning {
		return nil
	}
	t, err := o.target(ctx, b.DatabaseID)
	if err == nil {
		err = o.restore(ctx, t, b)
	}
	rec, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	status, text := db.RunSuccess, ""
	switch {
	case err == nil:
	case ctx.Err() != nil:
		// A restore cut short leaves the database part old, part new.
		status, text = db.RunFailed, "interrupted: musdash was stopping. The database may hold only part of the backup; restore it again."
	default:
		status, text = db.RunFailed, err.Error()
	}
	if ferr := o.DB.SetBackupRestore(rec, b.ID, status, text); ferr != nil {
		o.Log.Error("record restore", "backup", b.ID, "err", ferr)
	}
	if err != nil && ctx.Err() == nil {
		return jobs.Permanent(err)
	}
	return err
}

func (o *Ops) restore(ctx context.Context, t target, b db.Backup) error {
	if t.tpl.RestoreCmd == "" {
		return fmt.Errorf("%s databases cannot be restored by musdash; download the file and load it by hand", t.tpl.Label)
	}
	if err := t.running(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, o.backupTimeout)
	defer cancel()
	err := backup.Restore(ctx, t.r, t.Container, t.tpl.RestoreCmd, path.Join(t.dir, b.File))
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("stopped after %s", o.backupTimeout)
	}
	return err
}

func backupEvent(t target, b db.Backup) notify.Event {
	e := notify.Event{Kind: notify.EventBackup, URL: "/databases/" + t.ID + "/backups", At: time.Now()}
	switch {
	case b.Status != db.RunSuccess:
		e.Title = "Backup of " + t.Name + " failed"
		e.Body = b.Error
	case b.Error != "":
		e.Title = "Backup of " + t.Name + " was not copied to the storage"
		e.Body = b.Error
	default:
		e.OK = true
		e.Title = "Backup of " + t.Name + " finished"
		e.Body = b.File + ", " + HumanSize(b.Size)
		if b.StorageID != "" {
			e.Body += ", copied to the storage"
		}
	}
	return e
}

// HumanSize writes a number of bytes the way a person reads it.
func HumanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
