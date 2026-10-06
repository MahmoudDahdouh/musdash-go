package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

func renderEmpty(t *testing.T, p EmptyProps, action string) string {
	t.Helper()
	ctx := context.Background()
	if action != "" {
		ctx = templ.WithChildren(ctx, templ.Raw(action))
	}
	var b strings.Builder
	if err := EmptyState(p).Render(ctx, &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// The title of an empty state is a heading of the level its place has, so
// a page's outline stays true whichever size is used.
func TestEmptyStateHeadingFollowsItsSize(t *testing.T) {
	for size, want := range map[string]string{
		"":         `<h2 class="empty-title">No projects yet</h2>`,
		EmptySmall: `<h3 class="empty-title">No projects yet</h3>`,
		EmptyPage:  `<h1 class="empty-title">No projects yet</h1>`,
	} {
		got := renderEmpty(t, EmptyProps{Icon: "folder", Title: "No projects yet", Size: size}, "")
		if !strings.Contains(got, want) {
			t.Errorf("size %q: no %s in\n%s", size, want, got)
		}
		class := `class="empty"`
		if size != "" {
			class = `class="empty empty-` + size + `"`
		}
		if !strings.Contains(got, class) {
			t.Errorf("size %q: no %s in\n%s", size, class, got)
		}
		if !strings.Contains(got, `aria-hidden="true"`) {
			t.Errorf("size %q: the icon is not hidden from a screen reader", size)
		}
	}
}

// Under a heading that names it already, the title is a line of text.
func TestEmptyStatePlainTitleIsNoHeading(t *testing.T) {
	got := renderEmpty(t, EmptyProps{Icon: "chart", Title: "No samples", Size: EmptySmall, Plain: true}, "")
	if !strings.Contains(got, `<p class="empty-title">No samples</p>`) || strings.Contains(got, "<h") {
		t.Errorf("a plain title is a heading:\n%s", got)
	}
}

// What was not given is not drawn: no empty paragraph, no link to nowhere,
// and nothing in the row of actions, which is what lets the CSS drop it.
func TestEmptyStateDrawsOnlyWhatItHas(t *testing.T) {
	bare := renderEmpty(t, EmptyProps{Icon: "clock", Title: "No activity yet"}, "")
	for _, not := range []string{"empty-text", "empty-link", "<a "} {
		if strings.Contains(bare, not) {
			t.Errorf("a state with a title alone has %q:\n%s", not, bare)
		}
	}
	if !strings.Contains(bare, `<div class="empty-actions"></div>`) {
		t.Errorf("the row of actions is not empty, so it would take room:\n%s", bare)
	}

	full := renderEmpty(t, EmptyProps{
		Icon: "search", Title: "Nothing matches", Text: "Try another word.",
		Link: "Use your own Compose file", LinkHref: "/services/new",
		Attrs: templ.Attributes{"data-filter-empty": "kinds", "hidden": true},
	}, `<button class="btn">Add</button>`)
	for _, want := range []string{
		`<p class="empty-text">Try another word.</p>`,
		`<a class="empty-link" href="/services/new">Use your own Compose file</a>`,
		`<div class="empty-actions"><button class="btn">Add</button></div>`,
		`data-filter-empty="kinds"`, ` hidden`,
	} {
		if !strings.Contains(full, want) {
			t.Errorf("no %s in\n%s", want, full)
		}
	}
	// The link comes after the action: it is the second thing to do.
	if strings.Index(full, "empty-link") < strings.Index(full, "empty-actions") {
		t.Errorf("the link is above the action:\n%s", full)
	}
}
