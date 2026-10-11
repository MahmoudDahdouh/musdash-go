package web

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// A form that takes something away, stops something or replaces a secret
// must not act on one click. The pages are read as they are written, so
// that a branch no test happens to render is checked too: every such form
// written out in a template carries a question for the confirm dialog
// (ui.Ask) or asks for a name to be typed (data-match). Forms drawn by
// ui.Confirm and ui.FormDialog are dialogs already and are not written out
// as a <form> in a page.
func TestCriticalFormsAsk(t *testing.T) {
	critical := regexp.MustCompile(`/(delete|stop|restart|rollback|restore|forget-host-key|two-step-off|role|reset|proxy|logout|deploy-token|webhook-secret)"`)
	form := regexp.MustCompile(`(?s)<form\b.*?</form>`)
	action := regexp.MustCompile(`action=(\{[^\n]*\}|"[^"]*")`)
	// Not critical, though the address says delete: the row of a backup
	// that failed has no file, and dismissing it loses nothing.
	exempt := []string{"Dismiss"}

	files, _ := filepath.Glob("pages/*.templ")
	more, _ := filepath.Glob("ui/*.templ")
	files = append(files, more...)
	if len(files) < 10 {
		t.Fatalf("only %d templates found", len(files))
	}
	seen := 0
	for _, file := range files {
		if strings.HasSuffix(file, "gallery.templ") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, block := range form.FindAllString(string(src), -1) {
			a := action.FindString(block)
			if !critical.MatchString(a) {
				continue
			}
			skip := false
			for _, e := range exempt {
				skip = skip || strings.Contains(block, e)
			}
			if skip {
				continue
			}
			seen++
			if !strings.Contains(block, "Ask(") && !strings.Contains(block, "data-match") && !strings.Contains(block, "data-confirm") {
				t.Errorf("%s: the form %s acts on one click: give its button ui.Ask", file, a)
			}
		}
	}
	if seen < 20 {
		t.Fatalf("only %d critical forms found: were the addresses renamed?", seen)
	}
}

// A question is asked in red when what it confirms takes something away or
// interrupts something that runs, which is every question but the few that
// only add something and are named here. And the button that asks has an
// icon, which the button that confirms shows too (static/app.js copies
// it): Delete has the bin, Stop the square. The templates are read as they
// are written, so a branch no test renders is held to it as well.
func TestQuestionsAreRedAndHaveAnIcon(t *testing.T) {
	// Asked, because they change a server or make a credential, but they
	// take nothing away: the primary button confirms them.
	blue := []string{`Ask("Install the proxy on "`, `Ask("Make a reset link for "`}
	asked := regexp.MustCompile(`Ask\(.*,\s*(true|false)\)`)
	asks := regexp.MustCompile(`(Button|MenuItem)\(.*Ask\(`)

	files, _ := filepath.Glob("pages/*.templ")
	more, _ := filepath.Glob("ui/*.templ")
	files = append(files, more...)
	questions, blues, buttons := 0, 0, 0
	for _, file := range files {
		if strings.HasSuffix(file, "gallery.templ") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for n, line := range strings.Split(string(src), "\n") {
			if m := asked.FindStringSubmatch(line); m != nil && !strings.Contains(line, "func Ask(") {
				questions++
				if m[1] == "false" {
					named := false
					for _, b := range blue {
						named = named || strings.Contains(line, b)
					}
					if !named {
						t.Errorf("%s:%d: a question that is not asked in red. Pass true to ui.Ask, or name it in this test if it takes nothing away:\n%s", file, n+1, strings.TrimSpace(line))
					}
					blues++
				}
			}
			if asks.MatchString(line) {
				buttons++
				if !strings.Contains(line, "Icon:") {
					t.Errorf("%s:%d: a button that asks has no icon for the button that confirms to show:\n%s", file, n+1, strings.TrimSpace(line))
				}
			}
		}
	}
	if questions < 30 || buttons < 30 {
		t.Fatalf("only %d questions and %d buttons that ask were found: was ui.Ask renamed?", questions, buttons)
	}
	if blues != len(blue) {
		t.Fatalf("%d questions are asked in blue, and %d are named: a name here is out of date", blues, len(blue))
	}
}

// TestEmptyListsUseTheComponent reads the templates: a list that has
// nothing in it says so with ui.EmptyState, not with a grey sentence where
// its rows would be, which has no title, no icon and no action. A card
// whose body is one grey sentence is such a list unless it is named here,
// and so is any grey paragraph that opens by saying there is none.
func TestEmptyListsUseTheComponent(t *testing.T) {
	body := regexp.MustCompile(`<p class="card-body muted[^"]*"[^>]*>\s*([^<]*)`)
	none := regexp.MustCompile(`<p class="muted[^"]*"[^>]*>\s*((No|Nothing|None|Nobody|There is no|There are no)\b[^<]*)`)
	// Notes and missing values, not empty lists.
	allowed := []string{
		"The database has to be running to be backed up.",
		"The database keeps answering while the dump is made.",
		"Nothing is deployed yet. These steps get the first thing running.",
		"No address: the one it would have is taken.",
		"No recent reading of what it uses.",
	}

	files, _ := filepath.Glob("pages/*.templ")
	more, _ := filepath.Glob("ui/*.templ")
	files = append(files, more...)
	if len(files) < 10 {
		t.Fatalf("only %d templates found", len(files))
	}
	states, notes := 0, 0
	for _, file := range files {
		if strings.HasSuffix(file, "gallery.templ") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		states += strings.Count(string(src), "EmptyState(")
		found := body.FindAllStringSubmatch(string(src), -1)
		found = append(found, none.FindAllStringSubmatch(string(src), -1)...)
		for _, m := range found {
			text := strings.TrimSpace(m[1])
			if slices.Contains(allowed, text) {
				notes++
				continue
			}
			t.Errorf("%s: %q stands where a list would be: use ui.EmptyState, or name it in this test if it is a note", file, text)
		}
	}
	if states < 40 {
		t.Fatalf("only %d empty states found: was the component renamed?", states)
	}
	if notes < len(allowed) {
		t.Fatalf("only %d of the %d allowed notes found: were the classes renamed?", notes, len(allowed))
	}
}

// What was asked for by name: stopping a Compose stack asks in red, as
// stopping an app and a database does, and each of those buttons has the
// icon the confirm button will show.
func TestStopAsksInRed(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	web := a.newApp(projectID, env, "web", true, nil)
	a.stackServer("front", "3000", nil)
	site := a.newService(projectID, env, "site", url.Values{"deploy": {"1"}})
	maindb := a.newDatabase(projectID, env, "postgres", "maindb", nil)

	stop := regexp.MustCompile(`(?s)<form method="post" action="[^"]*/stop">.*?<button type="submit" class="btn btn-secondary"([^>]*)>\s*<svg class="icon[^"]*"`)
	for what, page := range map[string]string{"app": a.appPath(web), "service": a.servicePath(site.ID), "database": a.databasePath(maindb.ID)} {
		_, body := a.get(page)
		m := stop.FindStringSubmatch(body)
		if m == nil {
			t.Errorf("the %s's page has no Stop button with an icon", what)
			continue
		}
		if !strings.Contains(m[1], `data-confirm-tone="danger"`) || !strings.Contains(m[1], `data-confirm-submit="Stop `+what+`"`) {
			t.Errorf("stopping the %s does not ask in red: %s", what, m[1])
		}
	}
}
