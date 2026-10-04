package config

import (
	"bytes"
	"os"
	"testing"
)

func TestMasterKeyCreatedOnceAndPrivate(t *testing.T) {
	t.Setenv("MUSDASH_MASTER_KEY", "")
	c := Config{DataDir: t.TempDir()}
	first, err := c.MasterKey()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(c.MasterKeyPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode %04o, want 0600", info.Mode().Perm())
	}
	second, err := c.MasterKey()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("second start regenerated the master key")
	}
}

func TestMasterKeyRefusesLoosePermissions(t *testing.T) {
	t.Setenv("MUSDASH_MASTER_KEY", "")
	c := Config{DataDir: t.TempDir()}
	if _, err := c.MasterKey(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(c.MasterKeyPath(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.MasterKey(); err == nil {
		t.Fatal("want error for a world-readable key file")
	}
}

func TestMasterKeyFromEnv(t *testing.T) {
	c := Config{DataDir: t.TempDir()}
	t.Setenv("MUSDASH_MASTER_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	key, err := c.MasterKey()
	if err != nil || len(key) != 32 {
		t.Fatalf("key %d bytes, err %v", len(key), err)
	}
	if _, err := os.Stat(c.MasterKeyPath()); err == nil {
		t.Fatal("key file must not be written when the env var is used")
	}
	t.Setenv("MUSDASH_MASTER_KEY", "too-short")
	if _, err := c.MasterKey(); err == nil {
		t.Fatal("want error for a malformed key")
	}
}

func TestEnsureDirs(t *testing.T) {
	c := Config{DataDir: t.TempDir() + "/nested/data"}
	if err := c.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{c.LogDir(), c.CertDir(), c.WorkDir(), c.BackupDir()} {
		if info, err := os.Stat(d); err != nil || !info.IsDir() {
			t.Fatalf("%s missing: %v", d, err)
		}
	}
}
