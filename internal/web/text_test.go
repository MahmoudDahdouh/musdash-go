package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// u is a character by its number. The characters this is about are written
// that way: one that reorders text would reorder the line of this file it
// is in, and the others cannot be seen in it.
func u(code rune) string { return string(code) }

func TestPlainText(t *testing.T) {
	for _, ok := range []string{
		"", "Shop", "Shop <script>", "Zoë's café – staging", "日本語", "πρόγραμμα", "emoji \U0001F680 allowed",
		// Joiners and direction marks are how some names are written:
		// Persian with a zero-width non-joiner, Hebrew after a
		// right-to-left mark, a family joined from three people.
		"می" + u(0x200C) + "خواهم", u(0x200F) + "שלום", "family \U0001F468" + u(0x200D) + "\U0001F469" + u(0x200D) + "\U0001F467",
		// A letter with a combining accent.
		"e" + u(0x0301),
	} {
		if !plainText(ok) {
			t.Errorf("refused %q", ok)
		}
	}
	for _, bad := range []string{
		"a\x00b", "line\nbreak", "carriage\rreturn", "tab\there", "bell\a", "escape\x1b[31m", "delete\x7f", "c1\u0085control",
		// Separators that end a line for whoever reads a log or JSON.
		"line" + u(0x2028) + "separator", "paragraph" + u(0x2029) + "separator",
		// What follows is drawn in another order: "exe.txt" can be made
		// to read as "txt.exe".
		"evil" + u(0x202E) + "txt.exe", u(0x202A) + "embedded", "isolate" + u(0x2066) + "d", "pop" + u(0x2069), "old" + u(0x206A) + "format",
		// Invisible copies of letters.
		"tag\U000E0041",
		// Not text at all.
		"broken\xff\xfeutf8", string([]byte{0xc3, 0x28}),
		// A name that shows as nothing.
		u(0x200B), u(0x200E) + u(0x200F), u(0xFEFF), u(0x0301), " " + u(0x00A0) + " ",
	} {
		if plainText(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}

// An address is shown, logged and returned by the API like a name.
func TestEmailAddressesFollowTheRuleToo(t *testing.T) {
	for _, ok := range []string{"owner@example.com", "First.Last+tag@Example.co.uk", "jürgen@example.de"} {
		if _, valid := normalEmail(ok); !valid {
			t.Errorf("refused %q", ok)
		}
	}
	for _, bad := range []string{
		"a" + u(0x202E) + "b@example.com", "a" + u(0x2028) + "b@example.com", "a\u0085b@example.com", "a@exa" + u(0x202E) + "mple.com",
		// Reads as ab@example.com, and would be another account.
		"a" + u(0x200B) + "b@example.com", "ab@exam" + u(0x200D) + "ple.com", u(0xFEFF) + "ab@example.com",
		"a\nb@example.com", "nobody", "a@localhost",
	} {
		if _, valid := normalEmail(bad); valid {
			t.Errorf("accepted %q", bad)
		}
	}
}

// A name was checked for its length only, so a NUL byte or a line break in
// the middle of one was stored, and went from there into notifications,
// logs and the API. Every text that is only shown follows the one rule
// now, and says what is wrong in its own words, not as a matter of length.
func TestNamesWithWhatCannotBeShownAreRefused(t *testing.T) {
	// The first account's name, before anything else exists.
	fresh := newApp(t, false)
	res, body := fresh.post("/setup", "/setup", url.Values{"name": {"two\nlines"}, "email": {testEmail}, "password": {testPassword}})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "cannot be shown") {
		t.Errorf("the first account's name: status %d, message about the character: %v", res.StatusCode, strings.Contains(body, "cannot be shown"))
	}
	var accounts int
	if err := fresh.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&accounts); err != nil || accounts != 0 {
		t.Fatalf("%d accounts after a refused setup: %v", accounts, err)
	}

	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	projectID, env := a.project("Shop")
	appID := a.newApp(projectID, env, "web", true, nil)

	forms := []struct {
		what, page, path, field string
		rest                    url.Values
	}{
		{"a project", "/projects", "/projects", "name", nil},
		{"a project's description", "/projects", "/projects", "description", url.Values{"name": {"Fine"}}},
		{"a project being renamed", "/projects/" + projectID + "/settings", "/projects/" + projectID, "name", nil},
		{"a person", "/account", "/account/profile", "name", url.Values{"email": {"owner@example.com"}}},
		{"the team", "/team", "/team", "name", nil},
		{"a server", "/servers", "/servers", "name", url.Values{"host": {"203.0.113.9"}, "ssh_user": {"root"}}},
		{"a deploy key", "/keys", "/sources/keys", "key_name", nil},
		{"a GitLab source", "/sources", "/sources/gitlab", "gitlab_name", url.Values{"gitlab_base": {"https://gitlab.com"}, "gitlab_token": {"glpat-example"}}},
		{"an API token", "/keys", "/account/tokens", "token_name", url.Values{"token_ability": {"read"}, "token_expires": {"30"}}},
		{"a scheduled task", a.appPath(appID) + "/tasks", a.appPath(appID) + "/tasks", "name", url.Values{"command": {"true"}, "schedule": {"@daily"}}},
		{"a backup storage", "/settings/storages", "/settings/storages", "name", url.Values{"bucket": {"backups"}, "access_key": {"k"}, "secret_key": {"s"}}},
		{"a notification channel", "/notifications", "/notifications", "name", url.Values{"kind": {"webhook"}}},
	}
	for _, f := range forms {
		for _, bad := range []string{"two\nlines", "nul\x00byte", "evil" + u(0x202E) + "txt.exe", u(0x200B)} {
			form := url.Values{}
			for k, v := range f.rest {
				form[k] = v
			}
			form.Set(f.field, bad)
			res, body := a.post(f.page, f.path, form)
			if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "cannot be shown") {
				t.Errorf("%s named %q: status %d, message about the character: %v", f.what, bad, res.StatusCode, strings.Contains(body, "cannot be shown"))
			}
		}
	}

	// Nothing of it was stored.
	var teamID string
	if err := a.db.QueryRow(`SELECT id FROM teams`).Scan(&teamID); err != nil {
		t.Fatal(err)
	}
	projects, err := a.db.ListProjects(ctx, teamID)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range projects {
		if !plainText(p.Name) || !plainText(p.Description) {
			t.Errorf("stored: %q %q", p.Name, p.Description)
		}
	}
	// A name with a break only at its ends is the name without it, as
	// before: browsers send what was pasted.
	res, _ = a.post("/projects", "/projects", url.Values{"name": {"  Trimmed\n"}})
	wantStatus(t, res, http.StatusSeeOther)
}
