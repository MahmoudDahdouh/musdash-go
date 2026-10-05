package db

import (
	"context"
	"database/sql"
)

// Sample is one stored reading of a server or of a resource on it.
type Sample struct {
	At int64
	// CPU is in hundredths of a percent: of the whole machine for a
	// server, of one core for a resource.
	CPU           int
	Mem, MemTotal int64
	// Of a server only.
	Load                int
	DiskUsed, DiskTotal int64
}

// ServerSampled reports whether a server is sampled once a minute.
func (d *DB) ServerSampled(ctx context.Context, id string) (bool, error) {
	var on bool
	err := d.QueryRowContext(ctx, `SELECT metrics FROM servers WHERE id = ?`, id).Scan(&on)
	return on, notFound(err)
}

// SetServerSampled switches a server's sampling on or off. Switching it
// off also removes what was stored.
func (d *DB) SetServerSampled(ctx context.Context, teamID, id string, on bool) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if err := affected(tx.ExecContext(ctx, `UPDATE servers SET metrics = ? WHERE id = ? AND team_id = ?`, on, id, teamID)); err != nil {
			return err
		}
		if on {
			return nil
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM metric_samples WHERE server_id = ?`, id)
		return err
	})
}

// SampledServers returns the servers that sampling is switched on for.
func (d *DB) SampledServers(ctx context.Context) ([]Server, error) {
	return d.queryServers(ctx, `SELECT `+serverColumns+` FROM servers WHERE metrics = 1 ORDER BY created_at, rowid`)
}

// AddSamples stores one reading of a server: rows maps a resource's id to
// its sample, and "" to the server's own. Nothing is stored for a server
// whose sampling was switched off in the meantime.
func (d *DB) AddSamples(ctx context.Context, serverID string, at int64, rows map[string]Sample) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		var on bool
		if err := tx.QueryRowContext(ctx, `SELECT metrics FROM servers WHERE id = ?`, serverID).Scan(&on); err != nil || !on {
			return notFound(err)
		}
		for resource, s := range rows {
			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO metric_samples (server_id, resource_id, at, cpu, mem, mem_total, load, disk_used, disk_total)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, serverID, resource, at, s.CPU, s.Mem, s.MemTotal, s.Load, s.DiskUsed, s.DiskTotal); err != nil {
				return err
			}
		}
		return nil
	})
}

// PruneSamples deletes samples older than the cutoff.
func (d *DB) PruneSamples(ctx context.Context, olderThan int64) error {
	_, err := d.ExecContext(ctx, `DELETE FROM metric_samples WHERE at < ?`, olderThan)
	return err
}

// SampleHistory returns the samples of a server ("" as the resource) or of
// a resource on it since a time, averaged over stretches of bucket
// seconds, oldest first. A page draws what this returns, so its length is
// bounded by the caller's choice of bucket. Call it only with a server or
// a resource a loader returned: it does not check the team.
func (d *DB) SampleHistory(ctx context.Context, serverID, resourceID string, since, bucket int64) ([]Sample, error) {
	if bucket < 1 {
		bucket = 1
	}
	rows, err := d.QueryContext(ctx, `SELECT (at / ?1) * ?1 AS t, CAST(avg(cpu) AS INTEGER), CAST(avg(mem) AS INTEGER), max(mem_total),
			CAST(avg(load) AS INTEGER), CAST(avg(disk_used) AS INTEGER), max(disk_total)
		FROM metric_samples WHERE server_id = ?2 AND resource_id = ?3 AND at >= ?4 GROUP BY t ORDER BY t`, bucket, serverID, resourceID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sample
	for rows.Next() {
		var s Sample
		if err := rows.Scan(&s.At, &s.CPU, &s.Mem, &s.MemTotal, &s.Load, &s.DiskUsed, &s.DiskTotal); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
