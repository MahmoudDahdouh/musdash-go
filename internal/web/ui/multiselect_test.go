package ui

import (
	"context"
	"strings"
	"testing"
)

func renderMultiSelect(t *testing.T, p MultiSelectProps) string {
	t.Helper()
	var b strings.Builder
	if err := MultiSelect(p).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestMultiSelect pins what static/app.js and a form read from a choice of
// several: every option a real checkbox under the one name, the list said
// to take several, and on the button how many are chosen.
func TestMultiSelect(t *testing.T) {
	few := renderMultiSelect(t, MultiSelectProps{ID: "kind", Name: "kind", Label: "Kind", Attrs: map[string]any{"data-filter-pick": "things"}, Options: []MultiOption{
		{Value: "app", Label: "Apps", Count: 3},
		{Value: "db", Label: "Databases", Checked: true},
	}})
	for _, want := range []string{
		`<div class="select" data-select data-select-multi data-filter-pick="things">`,
		`id="kind"`, `popovertarget="kind-menu"`, `aria-controls="kind-list"`,
		`role="listbox" aria-multiselectable="true" aria-labelledby="kind"`,
		`<input type="checkbox" name="kind" value="app" tabindex="-1">`,
		`<input type="checkbox" name="kind" value="db" checked tabindex="-1">`,
		`<span class="menu-count">3</span>`,
	} {
		if !strings.Contains(few, want) {
			t.Errorf("no %s in %s", want, few)
		}
	}
	// An option says whether it is chosen as its checkbox does, and the
	// button counts them.
	if strings.Count(few, `role="option" aria-selected="true"`) != 1 || strings.Count(few, `role="option" aria-selected="false"`) != 1 {
		t.Errorf("the options do not say which is chosen: %s", few)
	}
	if !strings.Contains(few, `data-select-count><span>1</span>`) {
		t.Errorf("the button does not count what is chosen: %s", few)
	}
	if strings.Contains(few, "data-select-filter") || strings.Count(few, `class="menu-count"`) != 1 {
		t.Errorf("a short list has a filter field, or an option with no count shows one: %s", few)
	}

	// Nothing chosen: no count on the button. A long list is searched.
	var many []MultiOption
	for _, v := range strings.Fields("a b c d e f g h i") {
		many = append(many, MultiOption{Value: v, Label: strings.ToUpper(v)})
	}
	long := renderMultiSelect(t, MultiSelectProps{ID: "letter", Name: "letter", Label: "Letter", Options: many})
	if !strings.Contains(long, `data-select-count hidden><span>0</span>`) || !strings.Contains(long, "data-select-filter") || !strings.Contains(long, "data-select-empty") {
		t.Errorf("a long list with nothing chosen: %s", long)
	}
}
