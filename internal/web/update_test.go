package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
)

func TestCheckForUpdates(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	answer, fail := "v0.1.3", false
	a.server.Ops.Version = "v0.1.2"
	a.server.Ops.Latest = func(context.Context) (string, error) {
		if fail {
			return "", errors.New("dial tcp: no route to host 10.0.0.1")
		}
		return answer, nil
	}

	// Before anything was asked: the card, the Owner's button, no note.
	_, body := a.get("/settings")
	for _, want := range []string{`id="updates"`, "Check for updates", "v0.1.2", "Not looked up yet", "Never", "Once a day"} {
		if !strings.Contains(body, want) {
			t.Errorf("no %q on Settings before a check", want)
		}
	}
	if strings.Contains(body, "nav-update") {
		t.Error("an update is announced before one was seen")
	}

	res, _ := a.post("/settings", "/settings/updates/check", nil)
	wantRedirect(t, res, "/settings#updates")
	_, body = a.get("/settings")
	for _, want := range []string{"musdash v0.1.3 is available.", "releases/latest/download/install.sh | sudo sh", "/releases/tag/v0.1.3", `class="nav-update" href="/settings#updates"`} {
		if !strings.Contains(body, want) {
			t.Errorf("no %q on Settings after a check that found a release", want)
		}
	}

	// The note is on every page, for everybody; the button and the switch
	// are the Owner's.
	member, admin := a.newPerson("Member", db.RoleMember), a.newPerson("Admin", db.RoleAdmin)
	_, body = member.get("/")
	if !strings.Contains(body, `<p class="nav-update">`) || !strings.Contains(body, "v0.1.3") || strings.Contains(body, "/settings#updates") {
		t.Errorf("a Member's sidebar does not say there is an update, or links to a page they cannot open")
	}
	_, body = admin.get("/settings")
	if !strings.Contains(body, "musdash v0.1.3 is available.") || strings.Contains(body, "Check for updates") || strings.Contains(body, "/settings/updates/auto") {
		t.Errorf("an Admin's Settings: the update is not named, or the Owner's controls are there")
	}
	for _, p := range []person{member, admin} {
		for _, path := range []string{"/settings/updates/check", "/settings/updates/auto"} {
			if res, _ := p.post(path, url.Values{"auto": {"off"}}); res.StatusCode != http.StatusForbidden {
				t.Errorf("%s as %s: status %d, want 403", path, p.user.Name, res.StatusCode)
			}
		}
	}

	// A check that fails says so without the server's own words, and keeps
	// what was known.
	fail = true
	res, _ = a.post("/settings", "/settings/updates/check", nil)
	wantRedirect(t, res, "/settings#updates")
	_, body = a.get("/settings")
	if !strings.Contains(body, "could not be looked up") || strings.Contains(body, "10.0.0.1") || !strings.Contains(body, "musdash v0.1.3 is available.") {
		t.Errorf("after a failed check:\n%s", body)
	}

	// Up to date after the upgrade: nothing is announced.
	fail = false
	a.server.Ops.Version = "v0.1.3"
	a.post("/settings", "/settings/updates/check", nil)
	_, body = a.get("/settings")
	if !strings.Contains(body, "musdash v0.1.3 is up to date.") || strings.Contains(body, "nav-update") || strings.Contains(body, "is available") {
		t.Errorf("an install that is up to date still announces an update")
	}

	res, _ = a.post("/settings", "/settings/updates/auto", url.Values{"auto": {"off"}})
	wantRedirect(t, res, "/settings#updates")
	if _, body = a.get("/settings"); !strings.Contains(body, "does not look for a new release by itself") || !strings.Contains(body, "Switch on") {
		t.Error("the daily check was not switched off")
	}
	a.post("/settings", "/settings/updates/auto", url.Values{"auto": {"on"}})
	if u, err := a.server.Ops.Update(context.Background()); err != nil || !u.Auto {
		t.Errorf("the daily check was not switched on again: %+v, %v", u, err)
	}
}
