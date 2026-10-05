package catalog

import (
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestEveryEngineIsComplete(t *testing.T) {
	want := []string{"postgres", "mysql", "mariadb", "mongodb", "redis", "keydb", "dragonfly", "clickhouse"}
	if len(Databases()) != len(want) {
		t.Fatalf("%d engines, want %d", len(Databases()), len(want))
	}
	creds := Creds{User: "appuser", Pass: "S3cretPassw0rd", DB: "appdb"}
	leftover := regexp.MustCompile(`\{\{[^}]*\}\}`)
	imageRE := regexp.MustCompile(`^[a-z0-9./-]+:[A-Za-z0-9._-]+$`)

	for i, tpl := range Databases() {
		if tpl.Engine != want[i] {
			t.Errorf("engine %d is %q, want %q", i, tpl.Engine, want[i])
		}
		got, ok := Database(tpl.Engine)
		if !ok || got.Label != tpl.Label {
			t.Errorf("%s: lookup failed", tpl.Engine)
		}
		if tpl.Label == "" || tpl.About == "" || tpl.Port == 0 || tpl.DefaultUser == "" {
			t.Errorf("%s: a required field is empty", tpl.Engine)
		}
		if !imageRE.MatchString(tpl.Image) {
			t.Errorf("%s: image %q must carry an explicit tag", tpl.Engine, tpl.Image)
		}
		if !strings.HasPrefix(tpl.VolumePath, "/") {
			t.Errorf("%s: no volume path, so data would be lost on restart", tpl.Engine)
		}

		// The password must reach the container through its environment.
		env := tpl.RenderEnv(creds)
		hasPassword := false
		for k, v := range env {
			if leftover.MatchString(v) {
				t.Errorf("%s: %s has an unfilled placeholder: %q", tpl.Engine, k, v)
			}
			hasPassword = hasPassword || v == creds.Pass
		}
		if !hasPassword {
			t.Errorf("%s: no environment variable carries the password, so the server would start without one", tpl.Engine)
		}
		// Nothing passed as an argument may contain the password itself.
		for _, arg := range append(append([]string{}, tpl.Command...), tpl.RenderHealth(creds)...) {
			if strings.Contains(arg, creds.Pass) || leftover.MatchString(arg) {
				t.Errorf("%s: argument %q carries the password or a placeholder", tpl.Engine, arg)
			}
		}

		raw := tpl.URL(creds, "db-host", 15432)
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() != "db-host" || u.Port() != "15432" || leftover.MatchString(raw) {
			t.Errorf("%s: URL %q does not parse: %v", tpl.Engine, raw, err)
			continue
		}
		if pw, _ := u.User.Password(); pw != creds.Pass {
			t.Errorf("%s: URL password %q", tpl.Engine, pw)
		}
		if tpl.DefaultDB != "" && !strings.Contains(u.Path, creds.DB) {
			t.Errorf("%s: URL %q does not name the database", tpl.Engine, raw)
		}
	}
	if _, ok := Database("oracle"); ok {
		t.Error("an unknown engine was found")
	}
}

// Docker starts the same container again after a crash, a restart of the
// daemon or a reboot, and what the first start wrote is then still there. A
// command that writes a file must remove it first: the file belongs to the
// engine's user by then, and a kernel with fs.protected_regular set refuses
// root the redirection (Redis and KeyDB could not come back after a reboot).
func TestCommandsCanRunAgainInTheSameContainer(t *testing.T) {
	written := regexp.MustCompile(`>\s*(/[^\s;&|]+)`)
	for _, tpl := range Databases() {
		for _, arg := range tpl.Command {
			for _, m := range written.FindAllStringSubmatchIndex(arg, -1) {
				file := arg[m[2]:m[3]]
				if file == "/dev/null" {
					continue
				}
				if !strings.Contains(arg[:m[0]], "rm -f "+file+" ") {
					t.Errorf("%s: the command writes %s without removing it first, so the container cannot be started a second time", tpl.Engine, file)
				}
			}
		}
	}
	// The rule must see the file of the two engines known to write one.
	for _, engine := range []string{"redis", "keydb"} {
		tpl, _ := Database(engine)
		if len(tpl.Command) != 3 || !written.MatchString(tpl.Command[2]) {
			t.Errorf("%s: this test no longer sees the file its command writes", engine)
		}
	}
}

func TestURLEscapesCredentials(t *testing.T) {
	tpl, _ := Database("postgres")
	raw := tpl.URL(Creds{User: "user name", Pass: "p@ss/w:rd?# +x", DB: "my db"}, "host", 5432)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("%q: %v", raw, err)
	}
	pw, _ := u.User.Password()
	if u.Hostname() != "host" || u.User.Username() != "user name" || pw != "p@ss/w:rd?# +x" {
		t.Fatalf("round trip failed: host %q user %q pass %q", u.Hostname(), u.User.Username(), pw)
	}
}
