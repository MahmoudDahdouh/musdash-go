// Package db opens the SQLite database, applies migrations and holds the
// queries for the core tables.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a row does not exist or is not visible to the
// caller's team. Handlers turn it into a 404 either way, so a team cannot
// learn which ids exist elsewhere.
var ErrNotFound = errors.New("not found")

// DB wraps the connection pool.
type DB struct {
	*sql.DB
}

// Open opens (and creates) the database at path with the pragmas the RAM
// budget depends on: a 2 MB page cache and a pool of two connections.
func Open(path string) (*DB, error) {
	q := url.Values{}
	for _, p := range []string{
		"busy_timeout(5000)",
		"journal_mode(WAL)",
		"synchronous(NORMAL)",
		"cache_size(-2000)",
		"foreign_keys(ON)",
	} {
		q.Add("_pragma", p)
	}
	// Take the write lock when a transaction begins. With two connections a
	// deferred transaction that later upgrades to a write can deadlock.
	q.Set("_txlock", "immediate")

	conn, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(2)
	conn.SetMaxIdleConns(2)
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	// The database holds password hashes and sealed secrets.
	if err := os.Chmod(path, 0o600); err != nil {
		conn.Close()
		return nil, err
	}
	return &DB{conn}, nil
}

// Migrate applies every embedded migration that has not run yet, in numeric
// order, each inside its own transaction.
func (d *DB) Migrate(ctx context.Context, files fs.FS) error {
	if _, err := d.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at INTEGER NOT NULL
	) STRICT`); err != nil {
		return err
	}

	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)

	for _, name := range names {
		prefix, _, _ := strings.Cut(name, "_")
		version, err := strconv.Atoi(prefix)
		if err != nil {
			return fmt.Errorf("migration %q: name must start with a number", name)
		}
		var applied int
		if err := d.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version = ?`, version).Scan(&applied); err != nil {
			return err
		}
		if applied > 0 {
			continue
		}
		body, err := fs.ReadFile(files, name)
		if err != nil {
			return err
		}
		if err := d.Tx(ctx, func(tx *sql.Tx) error {
			// Check again under the write lock: `musdash migrate` and
			// `musdash server` may start at the same moment.
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version = ?`, version).Scan(&applied); err != nil {
				return err
			}
			if applied > 0 {
				return nil
			}
			if _, err := tx.ExecContext(ctx, string(body)); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
				version, name, time.Now().Unix())
			return err
		}); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
	}
	return nil
}

// Tx runs fn in a transaction, committing when it returns nil.
func (d *DB) Tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// notFound maps sql.ErrNoRows to ErrNotFound.
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// affected returns ErrNotFound when a write matched no row.
func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// IsUnique reports whether err is a UNIQUE or PRIMARY KEY violation.
func IsUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "constraint failed: UNIQUE")
}

func now() int64 { return time.Now().Unix() }
