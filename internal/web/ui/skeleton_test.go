package ui

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

func rendered(t *testing.T, c templ.Component) string {
	t.Helper()
	var b strings.Builder
	if err := c.Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// A skeleton is for the eye: a reader who does not see it is not read a
// list of empty boxes. What they are told is the line of words beside it.
// It carries no inline style, which the policy would drop, and as many
// rows as it was asked for.
func TestSkeletonIsHiddenFromAReaderAndSaysItInWords(t *testing.T) {
	for kind, row := range map[string]string{SkeletonMenu: `class="skeleton-option"`, SkeletonRows: `class="skeleton-row"`, SkeletonCards: `class="tile offer"`} {
		got := rendered(t, Skeleton(kind, 3))
		if !strings.HasPrefix(got, `<div class="skeleton skeleton-`+kind+`" aria-hidden="true">`) {
			t.Errorf("%s: not hidden from a reader:\n%.120s", kind, got)
		}
		if n := strings.Count(got, row); n != 3 {
			t.Errorf("%s: %d rows, want 3", kind, n)
		}
		if strings.Contains(got, "style=") || regexp.MustCompile(`>[^<\s]`).MatchString(got) {
			t.Errorf("%s: an inline style, or text in what is hidden:\n%s", kind, got)
		}
	}
	got := rendered(t, Loading(SkeletonRows, 2, "Asking the server…"))
	if !strings.HasPrefix(got, `<p class="sr-only" role="status">Asking the server…</p>`) || !strings.Contains(got, `aria-hidden="true"`) {
		t.Errorf("Loading does not say what it stands for before the bars:\n%s", got)
	}
	// A menu's list holds options and nothing else: its words are plain
	// text there, with no role of their own.
	if got = rendered(t, MenuLoading()); !strings.HasPrefix(got, `<p class="sr-only">Loading…</p>`) || strings.Contains(got, "role=") {
		t.Errorf("a menu's loading:\n%s", got)
	}
}

// A cell of a strip before its first reading has a bar where its figure
// will be, hidden from a reader, and no dash that reads as a value.
func TestStatBeforeItsReadingIsASkeleton(t *testing.T) {
	got := rendered(t, Stat(MeterProps{Label: "Memory", Share: -1}))
	if !strings.Contains(got, `<dd class="stat-value" aria-hidden="true"><span class="bone bone-figure"></span></dd>`) || strings.Contains(got, "–") {
		t.Errorf("the cell before a reading:\n%s", got)
	}
	if got = rendered(t, Stat(MeterProps{Label: "Memory", Value: "1 GiB", Share: 0.5})); strings.Contains(got, "bone") {
		t.Errorf("a cell with a reading has a skeleton:\n%s", got)
	}
}
