// Package config resolves the data directory layout and the master key.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
)

// Config is the runtime configuration shared by every subcommand.
type Config struct {
	DataDir string // everything musdash writes lives under here
	Dev     bool   // enables the component gallery and verbose logging
}

// DefaultDataDir is used when neither --data nor MUSDASH_DATA is set.
const DefaultDataDir = "/var/lib/musdash"

// On returns the same layout under another data directory: that of a
// remote server, where the files a deployment writes through its Runner
// live. An empty directory means the server is this machine.
func (c Config) On(dataDir string) Config {
	if dataDir != "" {
		c.DataDir = dataDir
	}
	return c
}

// EnvOr returns the environment variable's value, or def when it is unset.
func EnvOr(name, def string) string {
	if v, ok := os.LookupEnv(name); ok && v != "" {
		return v
	}
	return def
}

func (c Config) DBPath() string          { return filepath.Join(c.DataDir, "musdash.db") }
func (c Config) MasterKeyPath() string   { return filepath.Join(c.DataDir, "master.key") }
func (c Config) LogDir() string          { return filepath.Join(c.DataDir, "logs") }
func (c Config) ProxyDir() string        { return filepath.Join(c.DataDir, "proxy") }
func (c Config) RoutesPath() string      { return filepath.Join(c.ProxyDir(), "routes.json") }
func (c Config) ProxyPIDPath() string    { return filepath.Join(c.ProxyDir(), "proxy.pid") }
func (c Config) ProxyFormatPath() string { return filepath.Join(c.ProxyDir(), "proxy.format") }
func (c Config) CertDir() string         { return filepath.Join(c.ProxyDir(), "certs") }
func (c Config) WorkDir() string         { return filepath.Join(c.DataDir, "work") }
func (c Config) BackupDir() string       { return filepath.Join(c.DataDir, "backups") }
func (c Config) AppsDir() string         { return filepath.Join(c.DataDir, "apps") }

// AppDir holds one resource's env file and file mounts on its server.
func (c Config) AppDir(id string) string { return filepath.Join(c.AppsDir(), id) }

// DeployLogPath is where a deployment's build and deploy output is kept.
func (c Config) DeployLogPath(id string) string {
	return filepath.Join(c.LogDir(), "deployments", id+".log")
}

// ServiceLogPath is where the output of a service's latest deployment is
// kept.
func (c Config) ServiceLogPath(id string) string {
	return filepath.Join(c.LogDir(), "services", id+".log")
}

// DatabaseBackupDir is where one database's backup files are kept.
func (c Config) DatabaseBackupDir(id string) string { return filepath.Join(c.BackupDir(), id) }

// TaskLogPath is the output of one run of a scheduled task.
func (c Config) TaskLogPath(runID string) string {
	return filepath.Join(c.LogDir(), "tasks", runID+".log")
}

// EnsureDirs creates the data directory tree. Directories are private to the
// musdash user because they hold the database, keys and env files.
func (c Config) EnsureDirs() error {
	for _, d := range []string{c.DataDir, c.LogDir(), filepath.Join(c.LogDir(), "deployments"), filepath.Join(c.LogDir(), "services"), filepath.Join(c.LogDir(), "tasks"), c.ProxyDir(), c.CertDir(), c.WorkDir(), c.BackupDir(), c.AppsDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	return nil
}

// MasterKey returns the 32-byte key that seals every secret. It is read from
// MUSDASH_MASTER_KEY (base64) when set, otherwise from the key file, which is
// created on first run. A key file readable by group or others is refused:
// whoever can read it can decrypt every stored secret.
func (c Config) MasterKey() ([]byte, error) {
	if v := os.Getenv("MUSDASH_MASTER_KEY"); v != "" {
		return decodeKey(v, "MUSDASH_MASTER_KEY")
	}
	path := c.MasterKeyPath()
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return createKey(path)
	case err != nil:
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s has permissions %04o; run: chmod 600 %s", path, info.Mode().Perm(), path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decodeKey(string(raw), path)
}

func createKey(path string) ([]byte, error) {
	key := secret.RandomBytes(secret.KeySize)
	// O_EXCL: two processes starting together must not each write a key.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil, rerr
		}
		return decodeKey(string(raw), path)
	}
	if err != nil {
		return nil, fmt.Errorf("create master key: %w", err)
	}
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(key) + "\n"); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return nil, err
	}
	return key, f.Close()
}

func decodeKey(v, source string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
	if err != nil || len(key) != secret.KeySize {
		return nil, fmt.Errorf("%s must be %d bytes encoded as base64", source, secret.KeySize)
	}
	return key, nil
}
