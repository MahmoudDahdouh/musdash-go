package web

import (
	"os"
	"path/filepath"
	"regexp"
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
			if !strings.Contains(block, "ui.Ask(") && !strings.Contains(block, "proxyAsk(") && !strings.Contains(block, "data-match") && !strings.Contains(block, "data-confirm") {
				t.Errorf("%s: the form %s acts on one click: give its button ui.Ask", file, a)
			}
		}
	}
	if seen < 20 {
		t.Fatalf("only %d critical forms found: were the addresses renamed?", seen)
	}
}
