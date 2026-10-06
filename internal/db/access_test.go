package db

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/MahmoudDahdouh/musdash-go/migrations"
)

func TestNormalAbilities(t *testing.T) {
	for _, c := range []struct {
		chosen []string
		want   string
	}{
		{[]string{AbilityRead}, "read"},
		{[]string{AbilityDeploy, AbilityRead, AbilityRead}, "read,deploy"},
		{[]string{AbilityDeploy}, "deploy"},
		// Reading sensitive data is reading.
		{[]string{AbilitySensitive}, "read,read:sensitive"},
		{[]string{AbilityWrite, AbilitySensitive, AbilityDeploy, AbilityRead}, "read,read:sensitive,write,deploy"},
		// Root stands alone, whatever came with it.
		{[]string{AbilityRead, AbilityRoot, AbilityDeploy}, "root"},
	} {
		got, err := NormalAbilities(c.chosen)
		if err != nil || got != c.want {
			t.Errorf("NormalAbilities(%v) = %q, %v; want %q", c.chosen, got, err, c.want)
		}
	}
	for _, bad := range [][]string{nil, {}, {""}, {"admin"}, {AbilityRead, "read,deploy"}, {"Root"}} {
		if got, err := NormalAbilities(bad); !errors.Is(err, ErrAbilities) {
			t.Errorf("NormalAbilities(%q) = %q, %v; want ErrAbilities", bad, got, err)
		}
	}
}

func TestTokenMay(t *testing.T) {
	all := []string{AbilityRead, AbilitySensitive, AbilityWrite, AbilityDeploy, AbilityRoot}
	for _, c := range []struct {
		abilities string
		may       []string
	}{
		{"", nil},
		{"read", []string{AbilityRead}},
		{"deploy", []string{AbilityDeploy}},
		{"read,read:sensitive", []string{AbilityRead, AbilitySensitive}},
		{"read,write,deploy", []string{AbilityRead, AbilityWrite, AbilityDeploy}},
		// Root is every permission, the one called root included.
		{"root", all},
	} {
		token := APIToken{Abilities: c.abilities}
		for _, a := range all {
			want := false
			for _, m := range c.may {
				want = want || m == a
			}
			if token.May(a) != want {
				t.Errorf("a token with %q: May(%q) = %v", c.abilities, a, !want)
			}
		}
	}
}

// A token is stored with its permissions in their normal form, and one
// without any is not stored.
func TestCreateAPITokenNormalisesAbilities(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	owner, team, _ := d.CreateFirstUser(ctx, "owner@example.com", "Owner", "hash")
	made, err := d.CreateAPIToken(ctx, APIToken{UserID: owner.ID, TeamID: team, Name: "ci", TokenHash: "h1", Abilities: "deploy,root,read"})
	if err != nil || made.Abilities != "root" {
		t.Fatalf("made %q, %v", made.Abilities, err)
	}
	found, err := d.TokenByHash(ctx, "h1")
	if err != nil || found.Abilities != "root" || !found.May(AbilityWrite) {
		t.Fatalf("found %q, %v", found.Abilities, err)
	}
	for _, bad := range []string{"", "admin", "read,,deploy"} {
		if _, err := d.CreateAPIToken(ctx, APIToken{UserID: owner.ID, TeamID: team, Name: "ci", TokenHash: "h-" + bad, Abilities: bad}); !errors.Is(err, ErrAbilities) {
			t.Errorf("a token with the permissions %q: %v", bad, err)
		}
	}
}

// Migration 0016 turns the one ability a token had into the permissions
// that let it do exactly what it did.
func TestTokenAbilitiesMigration(t *testing.T) {
	ctx := context.Background()
	d, err := Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	before := fstest.MapFS{}
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name >= "0016" {
			continue
		}
		data, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		before[name] = &fstest.MapFile{Data: data}
	}
	if len(before) != 15 {
		t.Fatalf("%d migrations before 0016", len(before))
	}
	if err := d.Migrate(ctx, before); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO users (id, email, name, password_hash, created_at) VALUES ('u1', 'a@example.com', 'A', 'h', 1)`,
		`INSERT INTO teams (id, name, created_at) VALUES ('t1', 'Team', 1)`,
		`INSERT INTO team_members (team_id, user_id, role, created_at) VALUES ('t1', 'u1', 'owner', 1)`,
		`INSERT INTO api_tokens (id, user_id, team_id, name, token_hash, ability, created_at, expires_at) VALUES ('k1', 'u1', 't1', 'reads', 'hash-read', 'read', 1, 0)`,
		`INSERT INTO api_tokens (id, user_id, team_id, name, token_hash, ability, created_at, last_used_at, expires_at) VALUES ('k2', 'u1', 't1', 'deploys', 'hash-deploy', 'deploy', 2, 7, 0)`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := d.Migrate(ctx, migrations.FS); err != nil {
		t.Fatal(err)
	}
	read, err := d.TokenByHash(ctx, "hash-read")
	if err != nil || read.Abilities != "read" || read.May(AbilityDeploy) || read.May(AbilityWrite) {
		t.Fatalf("the read token: %q, %v", read.Abilities, err)
	}
	deploy, err := d.TokenByHash(ctx, "hash-deploy")
	if err != nil || deploy.Abilities != "read,write,deploy" || deploy.Name != "deploys" || deploy.LastUsedAt != 7 || deploy.May(AbilitySensitive) || deploy.May(AbilityRoot) {
		t.Fatalf("the deploy token: %+v, %v", deploy, err)
	}
	// What was stored is in the normal form already.
	if normal, err := NormalAbilities(deploy.AbilityList()); err != nil || normal != deploy.Abilities {
		t.Fatalf("not normal: %q, %v", normal, err)
	}
	list, err := d.ListAPITokens(ctx, "u1", "t1")
	if err != nil || len(list) != 2 {
		t.Fatalf("%d tokens, %v", len(list), err)
	}
}
