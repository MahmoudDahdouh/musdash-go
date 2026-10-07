package web

import (
	"context"
	"errors"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
)

const refused = "Your role does not allow this"

var linkRE = regexp.MustCompile(`data-copy="(https?://[^"]+/(invite|reset)/([A-Za-z0-9_-]+))"`)

// person is a signed-in browser of somebody other than the owner.
type person struct {
	a      *app
	client *http.Client
	user   db.User
}

func (p person) get(path string) (*http.Response, string) {
	p.a.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, p.a.url+path, nil)
	return p.a.do(p.client, req)
}

// csrf is the person's token, read from a page every role can open.
func (p person) csrf() string {
	p.a.t.Helper()
	_, body := p.get("/account")
	m := csrfRE.FindStringSubmatch(body)
	if m == nil {
		p.a.t.Fatal("no CSRF field on /account")
	}
	return m[1]
}

func (p person) post(path string, form url.Values) (*http.Response, string) {
	p.a.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	form.Set("_csrf", p.csrf())
	return p.a.postRaw(p.client, path, form, nil)
}

// invite makes an invitation as the owner and returns the token in its link.
func (a *app) invite(email, role string) string {
	a.t.Helper()
	res, body := a.post("/team", "/team/invitations", url.Values{"email": {email}, "role": {role}})
	wantStatus(a.t, res, http.StatusOK)
	m := linkRE.FindStringSubmatch(body)
	if m == nil || m[2] != "invite" {
		a.t.Fatalf("no invitation link on the page:\n%s", body)
	}
	return m[3]
}

// join opens an invitation link in a browser of its own and creates the
// account.
func (a *app) join(token, name string) person {
	a.t.Helper()
	c := a.newClient()
	req, _ := http.NewRequest(http.MethodGet, a.url+"/invite/"+token, nil)
	res, page := a.do(c, req)
	wantStatus(a.t, res, http.StatusOK)
	csrf := csrfRE.FindStringSubmatch(page)[1]
	res, body := a.postRaw(c, "/invite/"+token, url.Values{"_csrf": {csrf}, "name": {name}, "password": {testPassword}}, nil)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/" {
		a.t.Fatalf("join: %d\n%s", res.StatusCode, body)
	}
	inv := strings.ToLower(name) + "@example.com"
	u, err := a.db.UserByEmail(context.Background(), inv)
	if err != nil {
		a.t.Fatalf("the account of %s was not made: %v", inv, err)
	}
	return person{a: a, client: c, user: u}
}

// newPerson invites and joins somebody called name, as name@example.com.
func (a *app) newPerson(name, role string) person {
	a.t.Helper()
	return a.join(a.invite(strings.ToLower(name)+"@example.com", role), name)
}

var pathValueRE = regexp.MustCompile(`\{[^}]+\}`)

// TestRouteTableRefusesByRole walks the route table: whoever ranks below
// what a route asks for is refused before its handler runs, and nobody is
// refused at a route their role may call.
func TestRouteTableRefusesByRole(t *testing.T) {
	a := newApp(t, true)
	a.setup()
	people := map[access]person{
		member: a.newPerson("Member", db.RoleMember),
		admin:  a.newPerson("Admin", db.RoleAdmin),
	}
	counted := map[access]int{}
	for _, route := range a.server.routes {
		method, path, _ := strings.Cut(route.pattern, " ")
		if route.who < member || path == "" || route.pattern == "POST /logout" {
			continue
		}
		counted[route.who]++
		path = strings.ReplaceAll(pathValueRE.ReplaceAllString(path, "nosuchid"), "{$}", "")
		for rank, p := range people {
			var res *http.Response
			var body string
			if method == http.MethodGet {
				res, body = p.get(path)
			} else {
				res, body = p.post(path, nil)
			}
			wasRefused := res.StatusCode == http.StatusForbidden && strings.Contains(body, refused)
			if want := rank < route.who; wasRefused != want {
				t.Errorf("%s as %v: refused=%v (status %d), want refused=%v", route.pattern, rank, wasRefused, res.StatusCode, want)
			}
		}
	}
	// The table must hold the routes this test is about, or it proves
	// nothing.
	if counted[admin] < 20 || counted[owner] < 1 || counted[member] < 80 {
		t.Fatalf("the route table looks wrong: %v", counted)
	}
}

// TestTeamOwnedRoutesNeedAnAdmin pins what the table says for the parts of
// the dashboard that are the team's own. The walk above proves the table
// is enforced; this proves a route added under one of these addresses is
// not left open to a Member by default.
func TestTeamOwnedRoutesNeedAnAdmin(t *testing.T) {
	a := newApp(t, true)
	seen := 0
	for _, route := range a.server.routes {
		method, path, _ := strings.Cut(route.pattern, " ")
		under := func(prefix string) bool { return path == prefix || strings.HasPrefix(path, prefix+"/") }
		changes := method != http.MethodGet
		var want access
		switch {
		case under("/settings"), under("/notifications"):
			want = admin
		case under("/servers") && changes, under("/team") && changes:
			want = admin
		case strings.HasSuffix(path, "/variables/values"), strings.HasSuffix(path, "/variables/edit"):
			// Reading a value is as much as changing it. A project's and
			// an environment's are a Member's to change.
			if !under("/servers") && !under("/team") {
				continue
			}
			want = admin
		case under("/sources") && (changes || path == "/sources/github/callback"):
			want = admin
		default:
			continue
		}
		seen++
		if route.who < want {
			t.Errorf("%s is open to a role below Admin", route.pattern)
		}
	}
	if seen < 25 {
		t.Fatalf("only %d team-owned routes found: were they renamed?", seen)
	}
}

// The phase's done-when: a Member cannot delete a server.
func TestMemberCannotDeleteAServer(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	server, _ := a.remoteServer("frankfurt")
	m := a.newPerson("Member", db.RoleMember)

	res, body := m.post("/servers/"+server.ID+"/delete", nil)
	wantStatus(t, res, http.StatusForbidden)
	if !strings.Contains(body, refused) {
		t.Fatalf("not refused for the role:\n%s", body)
	}
	if _, err := a.db.ServerByID(ctx, server.ID); err != nil {
		t.Fatalf("the server is gone: %v", err)
	}

	// What a Member sees has no controls they cannot use.
	_, page := m.get("/servers")
	for _, absent := range []string{"Add a server", "Remove server", "Forget host key", `href="/settings"`} {
		if strings.Contains(page, absent) {
			t.Errorf("a Member's Servers page shows %q", absent)
		}
	}
	if !strings.Contains(page, "frankfurt") || !strings.Contains(page, `href="/team"`) {
		t.Error("a Member should still see the servers and the Team link")
	}
	_, page = m.get("/sources")
	for _, absent := range []string{"Create GitHub App", "Create deploy key"} {
		if strings.Contains(page, absent) {
			t.Errorf("a Member's Sources page shows %q", absent)
		}
	}
	res, _ = m.get("/settings")
	wantStatus(t, res, http.StatusForbidden)

	// A Member still does a Member's work.
	res, _ = m.post("/projects", url.Values{"name": {"Shop"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("a Member could not create a project: %d", res.StatusCode)
	}

	// An Admin can remove the server.
	ad := a.newPerson("Admin", db.RoleAdmin)
	res, _ = ad.post("/servers/"+server.ID+"/delete", nil)
	wantRedirect(t, res, "/servers")
	if _, err := a.db.ServerByID(ctx, server.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("an Admin could not remove the server: %v", err)
	}
}

func TestInvitations(t *testing.T) {
	var logs strings.Builder
	a := newAppWithLog(t, true, &logs)
	a.setup()
	ctx := context.Background()
	team := firstTeam(t, a)

	// Validation, and who is already here.
	res, _ := a.post("/team", "/team/invitations", url.Values{"email": {"not-an-address"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	res, body := a.post("/team", "/team/invitations", url.Values{"email": {testEmail}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if !strings.Contains(body, "already a member") {
		t.Fatalf("inviting a member:\n%s", body)
	}
	res, _ = a.post("/team", "/team/invitations", url.Values{"email": {"boss@example.com"}, "role": {db.RoleOwner}})
	wantStatus(t, res, http.StatusUnprocessableEntity)

	token := a.invite("new@example.com", db.RoleAdmin)
	if strings.Contains(logs.String(), token) {
		t.Fatal("the invitation token was written to the log")
	}
	inv, err := a.db.InvitationByHash(ctx, secret.HashToken(token))
	owner, _ := a.db.UserByEmail(ctx, testEmail)
	if err != nil || inv.Role != db.RoleAdmin || inv.InvitedBy != owner.ID {
		t.Fatalf("stored invitation: %+v %v", inv, err)
	}
	var stored string
	a.db.QueryRow(`SELECT token_hash FROM invitations`).Scan(&stored)
	if stored == token || stored != secret.HashToken(token) {
		t.Fatal("the token must be stored as its hash")
	}
	res, body = a.post("/team", "/team/invitations", url.Values{"email": {"NEW@example.com"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if !strings.Contains(body, "already invited") {
		t.Fatalf("inviting twice:\n%s", body)
	}
	// The link is shown once: the page does not carry it again.
	_, page := a.get("/team/invitations")
	if strings.Contains(page, token) || !strings.Contains(page, "new@example.com") {
		t.Fatal("the Invitations tab should list the invitation without its link")
	}

	// Whoever is signed in is a member already.
	res, _ = a.get("/invite/" + token)
	wantStatus(t, res, http.StatusConflict)

	// The form, its validation, and the account.
	c := a.newClient()
	req, _ := http.NewRequest(http.MethodGet, a.url+"/invite/"+token, nil)
	res, page = a.do(c, req)
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(page, "new@example.com") || !strings.Contains(page, "Default team") {
		t.Fatalf("the invitation page should name the address and the team:\n%s", page)
	}
	csrf := csrfRE.FindStringSubmatch(page)[1]
	res, _ = a.postRaw(c, "/invite/"+token, url.Values{"_csrf": {csrf}, "name": {"New"}, "password": {"short"}}, nil)
	wantStatus(t, res, http.StatusUnprocessableEntity)
	res, _ = a.postRaw(c, "/invite/"+token, url.Values{"name": {"New"}, "password": {testPassword}}, nil)
	wantStatus(t, res, http.StatusForbidden) // no CSRF token
	// A role or an address in the form changes nothing.
	res, _ = a.postRaw(c, "/invite/"+token, url.Values{"_csrf": {csrf}, "name": {"New"}, "password": {testPassword}, "role": {db.RoleOwner}, "email": {"other@example.com"}}, nil)
	wantRedirect(t, res, "/")
	u, err := a.db.UserByEmail(ctx, "new@example.com")
	if err != nil {
		t.Fatal(err)
	}
	m, _ := a.db.Member(ctx, team, u.ID)
	if m.Role != db.RoleAdmin {
		t.Fatalf("role %q, want the invitation's", m.Role)
	}
	req, _ = http.NewRequest(http.MethodGet, a.url+"/", nil)
	res, _ = a.do(c, req)
	wantStatus(t, res, http.StatusOK)

	// The link works once.
	other := a.newClient()
	req, _ = http.NewRequest(http.MethodGet, a.url+"/invite/"+token, nil)
	res, _ = a.do(other, req)
	wantStatus(t, res, http.StatusNotFound)

	// A cancelled invitation and one that ran out do not work either.
	cancelled := a.invite("cancelled@example.com", db.RoleMember)
	inv, _ = a.db.InvitationByHash(ctx, secret.HashToken(cancelled))
	res, _ = a.post("/team", "/team/invitations/"+inv.ID+"/delete", nil)
	wantRedirect(t, res, "/team/invitations")
	late := a.invite("late@example.com", db.RoleMember)
	a.db.Exec(`UPDATE invitations SET expires_at = ? WHERE email = 'late@example.com'`, time.Now().Unix()-1)
	for _, tok := range []string{cancelled, late} {
		req, _ = http.NewRequest(http.MethodGet, a.url+"/invite/"+tok, nil)
		res, _ = a.do(a.newClient(), req)
		wantStatus(t, res, http.StatusNotFound)
	}

	// An Admin invites Members, not Admins.
	ad := person{a: a, client: c, user: u}
	res, body = ad.post("/team/invitations", url.Values{"email": {"second@example.com"}, "role": {db.RoleAdmin}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if !strings.Contains(body, "Only an Owner can invite an Admin") {
		t.Fatalf("an Admin inviting an Admin:\n%s", body)
	}
	res, _ = ad.post("/team/invitations", url.Values{"email": {"second@example.com"}})
	wantStatus(t, res, http.StatusOK)
	// And a Member invites nobody.
	mem := a.newPerson("Member", db.RoleMember)
	res, _ = mem.post("/team/invitations", url.Values{"email": {"third@example.com"}})
	wantStatus(t, res, http.StatusForbidden)
	for _, path := range []string{"/team", "/team/invitations"} {
		_, page = mem.get(path)
		if strings.Contains(page, "Create invitation link") || strings.Contains(page, "Remove member") {
			t.Fatalf("a Member's %s shows controls they cannot use", path)
		}
	}
}

// TestTeamTabs: the Team page is three tabs, and the invitations that are
// waiting are on one of their own, with the button that makes one.
func TestTeamTabs(t *testing.T) {
	a := newApp(t, true)
	a.setup()
	ctx := context.Background()
	tabs := []string{"/team", "/team/invitations", "/team/variables"}
	for _, at := range tabs {
		res, page := a.get(at)
		wantStatus(t, res, http.StatusOK)
		for _, href := range tabs {
			tab := regexp.MustCompile(`<a class="tab" href="` + href + `"[^>]*>`).FindString(page)
			if tab == "" {
				t.Fatalf("%s has no tab to %s", at, href)
			}
			if strings.Contains(tab, `aria-current="page"`) != (href == at) {
				t.Errorf("%s: the tab to %s is marked wrongly: %s", at, href, tab)
			}
		}
	}
	const opens = `data-open="invite"`

	// With nobody invited the button is in the empty state and not also in
	// the header. Members, which is never empty, has it in its header.
	_, page := a.get("/team/invitations")
	if !strings.Contains(page, "Nobody is invited") || strings.Count(page, opens) != 1 {
		t.Fatalf("the empty Invitations tab should say so, with one Invite button:\n%s", page)
	}
	if _, members := a.get("/team"); strings.Count(members, opens) != 1 || strings.Contains(members, "Nobody is invited") {
		t.Fatal("Members should have the Invite button and no list of invitations")
	}

	mem := a.newPerson("Member", db.RoleMember)
	token := a.invite("waiting@example.com", db.RoleAdmin)
	inv, err := a.db.InvitationByHash(ctx, secret.HashToken(token))
	if err != nil {
		t.Fatal(err)
	}
	cancel := `action="/team/invitations/` + inv.ID + `/delete"`
	_, page = a.get("/team/invitations")
	// One row says who, as what, by whom and until when.
	row := regexp.MustCompile(`(?s)<tr>(?:[^<]|<[^/]|</[^t]|</t[^r])*waiting@example\.com.*?</tr>`).FindString(page)
	for _, want := range []string{"Admin", "Owner", "Just now", "In 7 days", cancel} {
		if !strings.Contains(row, want) {
			t.Errorf("the invitation's row lacks %q:\n%s", want, row)
		}
	}
	if strings.Contains(page, "Nobody is invited") || strings.Count(page, opens) != 1 {
		t.Error("with an invitation waiting, the tab has one Invite button and no empty state")
	}
	// A row is what an invitation is, never what opens it.
	if strings.Contains(page, token) || strings.Contains(page, inv.TokenHash) {
		t.Fatal("the Invitations tab holds an invitation's token or its hash")
	}
	if _, members := a.get("/team"); strings.Contains(members, "waiting@example.com") {
		t.Fatal("Members still lists the invitations")
	}

	// A Member reads who is invited and is offered nothing to do about it.
	res, page := mem.get("/team/invitations")
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(page, "waiting@example.com") || strings.Contains(page, opens) || strings.Contains(page, cancel) || strings.Contains(page, inv.TokenHash) {
		t.Fatalf("a Member's Invitations tab:\n%s", page)
	}
}

func TestInvitationLinksAreRateLimited(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	c := a.newClient()
	for i := 0; i < 5; i++ {
		req, _ := http.NewRequest(http.MethodGet, a.url+"/invite/guess", nil)
		res, _ := a.do(c, req)
		wantStatus(t, res, http.StatusNotFound)
	}
	req, _ := http.NewRequest(http.MethodGet, a.url+"/invite/guess", nil)
	res, _ := a.do(c, req)
	wantStatus(t, res, http.StatusTooManyRequests)
}

func TestRolesAndRemovalOnTheTeamPage(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	team := firstTeam(t, a)
	owner, _ := a.db.UserByEmail(ctx, testEmail)
	ad := a.newPerson("Admin", db.RoleAdmin)
	mem := a.newPerson("Member", db.RoleMember)
	role := func(id string) string {
		m, err := a.db.Member(ctx, team, id)
		if err != nil {
			return "gone"
		}
		return m.Role
	}

	// Only an Owner changes a role.
	for _, p := range []person{ad, mem} {
		res, _ := p.post("/team/members/"+mem.user.ID+"/role", url.Values{"role": {db.RoleOwner}})
		wantStatus(t, res, http.StatusForbidden)
		res, _ = p.post("/team/members/"+p.user.ID+"/role", url.Values{"role": {db.RoleOwner}})
		wantStatus(t, res, http.StatusForbidden)
	}
	if role(mem.user.ID) != db.RoleMember || role(ad.user.ID) != db.RoleAdmin {
		t.Fatal("a role changed without an Owner")
	}
	res, _ := a.post("/team", "/team/members/"+mem.user.ID+"/role", url.Values{"role": {"root"}})
	wantRedirect(t, res, "/team")
	if role(mem.user.ID) != db.RoleMember {
		t.Fatal("an unknown role was stored")
	}
	// The only Owner keeps the role.
	res, _ = a.post("/team", "/team/members/"+owner.ID+"/role", url.Values{"role": {db.RoleMember}})
	wantRedirect(t, res, "/team")
	if role(owner.ID) != db.RoleOwner {
		t.Fatal("the last Owner was demoted")
	}
	res, _ = a.post("/team", "/team/members/nosuchid/role", url.Values{"role": {db.RoleAdmin}})
	wantStatus(t, res, http.StatusNotFound)

	// An Admin removes a Member, not an Admin, an Owner or themselves.
	second := a.newPerson("Second", db.RoleAdmin)
	for _, target := range []string{second.user.ID, owner.ID, ad.user.ID} {
		res, body := ad.post("/team/members/"+target+"/delete", url.Values{"confirm": {"x"}})
		wantStatus(t, res, http.StatusForbidden)
		if !strings.Contains(body, refused) {
			t.Fatalf("not refused for the role:\n%s", body)
		}
		res, _ = ad.post("/team/members/"+target+"/reset", nil)
		wantStatus(t, res, http.StatusForbidden)
	}
	// The typed confirmation is checked on the server too.
	res, _ = ad.post("/team/members/"+mem.user.ID+"/delete", url.Values{"confirm": {"someone@example.com"}})
	wantRedirect(t, res, "/team")
	if role(mem.user.ID) != db.RoleMember {
		t.Fatal("removed without the confirmation")
	}

	// A reset link for a Member, made by an Admin: shown once, works once.
	res, body := ad.post("/team/members/"+mem.user.ID+"/reset", nil)
	wantStatus(t, res, http.StatusOK)
	m := linkRE.FindStringSubmatch(html.UnescapeString(body))
	if m == nil || m[2] != "reset" {
		t.Fatalf("no reset link on the page:\n%s", body)
	}
	fresh := a.newClient()
	req, _ := http.NewRequest(http.MethodGet, a.url+"/reset/"+m[3], nil)
	res, page := a.do(fresh, req)
	wantStatus(t, res, http.StatusOK)
	res, _ = a.postRaw(fresh, "/reset/"+m[3], url.Values{"_csrf": {csrfRE.FindStringSubmatch(page)[1]}, "password": {"another long password"}}, nil)
	wantRedirect(t, res, "/login")
	// The Member's old session ended with the old password.
	res, _ = mem.get("/")
	wantRedirect(t, res, "/login")

	// Removal deletes the account and ends its sessions.
	res, _ = second.get("/")
	wantStatus(t, res, http.StatusOK)
	res, _ = a.post("/team", "/team/members/"+second.user.ID+"/delete", url.Values{"confirm": {"second@example.com"}})
	wantRedirect(t, res, "/team")
	if role(second.user.ID) != "gone" {
		t.Fatal("the member is still there")
	}
	if _, err := a.db.UserByID(ctx, second.user.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("the account survived: %v", err)
	}
	res, _ = second.get("/")
	wantRedirect(t, res, "/login")

	// An Owner can hand the install over and step down.
	res, _ = a.post("/team", "/team/members/"+ad.user.ID+"/role", url.Values{"role": {db.RoleOwner}})
	wantRedirect(t, res, "/team")
	res, _ = a.post("/team", "/team/members/"+owner.ID+"/role", url.Values{"role": {db.RoleMember}})
	wantRedirect(t, res, "/team")
	if role(ad.user.ID) != db.RoleOwner || role(owner.ID) != db.RoleMember {
		t.Fatalf("handover: %s / %s", role(ad.user.ID), role(owner.ID))
	}
	// Being given a higher role signs a person out: they sign in again.
	if res, _ := ad.get("/"); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("the promoted Admin's old session still works: %d", res.StatusCode)
	}
	var signedInAs *http.Response
	ad.client, signedInAs = a.signIn("admin@example.com", testPassword)
	wantRedirect(t, signedInAs, "/")
	// The change holds at once: the former Owner's next request is a Member's.
	res, _ = a.get("/settings")
	wantStatus(t, res, http.StatusForbidden)

	// Renaming the team.
	res, _ = ad.post("/team", url.Values{"name": {"Acme"}})
	wantRedirect(t, res, "/team")
	if got, _ := a.db.Team(ctx, team); got.Name != "Acme" {
		t.Fatalf("team name %q", got.Name)
	}
}

// What follows was found by the phase's independent review: authority
// that was checked once and held on to.

// A reset link made for a Member is not a reset link for the Owner that
// Member becomes.
func TestAResetLinkEndsWhenItsAccountIsPromoted(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	ctx := context.Background()
	team := firstTeam(t, a)
	ad := a.newPerson("Admin", db.RoleAdmin)
	mem := a.newPerson("Member", db.RoleMember)
	memToken := mem.newToken(operator...)

	res, body := ad.post("/team/members/"+mem.user.ID+"/reset", nil)
	wantStatus(t, res, http.StatusOK)
	link := linkRE.FindStringSubmatch(html.UnescapeString(body))
	if link == nil {
		t.Fatal("no reset link")
	}

	res, _ = a.post("/team", "/team/members/"+mem.user.ID+"/role", url.Values{"role": {db.RoleOwner}})
	wantRedirect(t, res, "/team")

	// The link in the Admin's hand, the Member's session and their API
	// token all ended with the promotion.
	req, _ := http.NewRequest(http.MethodGet, a.url+"/reset/"+link[3], nil)
	res, _ = a.do(a.newClient(), req)
	wantStatus(t, res, http.StatusNotFound)
	res, _ = mem.get("/")
	wantRedirect(t, res, "/login")
	res, _ = a.call(http.MethodGet, "/api/v1/me", memToken)
	wantStatus(t, res, http.StatusUnauthorized)
	// And the Admin may not ask for another.
	res, _ = ad.post("/team/members/"+mem.user.ID+"/reset", nil)
	wantStatus(t, res, http.StatusForbidden)
	if got, _ := a.db.Member(ctx, team, ad.user.ID); got.Role != db.RoleAdmin {
		t.Fatalf("the Admin is now %s", got.Role)
	}
	// The promoted person signs in again with their own password.
	_, res = a.signIn("member@example.com", testPassword)
	wantRedirect(t, res, "/")
}

// An invitation is withdrawn with the person who made it.
func TestAnInvitationEndsWithItsMaker(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	second := a.newPerson("Second", db.RoleAdmin)
	res, _ := a.post("/team", "/team/members/"+second.user.ID+"/role", url.Values{"role": {db.RoleOwner}})
	wantRedirect(t, res, "/team")
	second.client, res = a.signIn("second@example.com", testPassword)
	wantRedirect(t, res, "/")

	invite := func(email string) string {
		t.Helper()
		res, body := second.post("/team/invitations", url.Values{"email": {email}, "role": {db.RoleAdmin}})
		wantStatus(t, res, http.StatusOK)
		m := linkRE.FindStringSubmatch(html.UnescapeString(body))
		if m == nil {
			t.Fatal("no invitation link")
		}
		return m[3]
	}
	opens := func(token string) int {
		req, _ := http.NewRequest(http.MethodGet, a.url+"/invite/"+token, nil)
		res, _ := a.do(a.newClient(), req)
		return res.StatusCode
	}

	// Made as an Owner, then the maker is given a lower role.
	first := invite("one@example.com")
	if opens(first) != http.StatusOK {
		t.Fatal("the invitation should work while its maker is an Owner")
	}
	res, _ = a.post("/team", "/team/members/"+second.user.ID+"/role", url.Values{"role": {db.RoleAdmin}})
	wantRedirect(t, res, "/team")
	if opens(first) != http.StatusNotFound {
		t.Fatal("an Admin invitation outlived its maker's demotion")
	}

	// Made as an Admin (for a Member), then the maker is removed.
	res, body := second.post("/team/invitations", url.Values{"email": {"two@example.com"}})
	wantStatus(t, res, http.StatusOK)
	way := linkRE.FindStringSubmatch(html.UnescapeString(body))[3]
	res, _ = a.post("/team", "/team/members/"+second.user.ID+"/delete", url.Values{"confirm": {"second@example.com"}})
	wantRedirect(t, res, "/team")
	if opens(way) != http.StatusNotFound {
		t.Fatal("an invitation outlived its maker's removal")
	}
	if list, _ := a.db.ListInvitations(context.Background(), firstTeam(t, a)); len(list) != 0 {
		t.Fatalf("invitations left: %+v", list)
	}
}
