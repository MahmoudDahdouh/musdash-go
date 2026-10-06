package web

import (
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
	critical := regexp.MustCompile(`/(delete|stop|rollback|restore|forget-host-key|two-step-off|role|reset|proxy|logout|deploy-token|webhook-secret)"`)
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
