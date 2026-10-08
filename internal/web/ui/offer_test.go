package ui

import (
	"context"
	"strings"
	"testing"
)

func renderOffer(t *testing.T, p OfferProps) string {
	t.Helper()
	var b strings.Builder
	if err := Offer(p).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestOffer pins the card of something that can be added: the owner's logo
// as an image, the links that lead outside before the one action, and that
// action the card's only link into the dashboard.
func TestOffer(t *testing.T) {
	full := renderOffer(t, OfferProps{Href: "/add", Logo: "postgres", Icon: "database", Title: "PostgreSQL", Text: "A database.", Docs: "https://docs.example/", Website: "https://example/"})
	if !strings.Contains(full, `<img src="/static/logo-postgres.svg?v=`) || !strings.Contains(full, `width="48" height="48"`) {
		t.Errorf("no logo of 48px: %s", full)
	}
	docs, site, action := strings.Index(full, "Docs</a>"), strings.Index(full, "Website</a>"), strings.Index(full, `href="/add"`)
	if docs < 0 || site < docs || action < site {
		t.Errorf("the foot is not Docs, Website, then the action: %s", full)
	}
	for _, link := range []string{`href="https://docs.example/" target="_blank" rel="noopener"`, `href="https://example/" target="_blank" rel="noopener"`} {
		if !strings.Contains(full, link) {
			t.Errorf("no %s in %s", link, full)
		}
	}
	if !strings.Contains(full, `class="btn btn-sm btn-secondary offer-go" href="/add" aria-label="Deploy: PostgreSQL"`) || strings.Count(full, `href="/add"`) != 1 {
		t.Errorf("the action is not the one link to the form: %s", full)
	}

	// Nothing to read about it, and an action of its own.
	bare := renderOffer(t, OfferProps{Href: "/sources", Logo: "github", Icon: "github", Title: "GitHub", Action: "Connect"})
	if strings.Contains(bare, "_blank") || !strings.Contains(bare, `aria-label="Connect: GitHub"`) || strings.Contains(bare, "Deploy") {
		t.Errorf("a card with no links and its own action: %s", bare)
	}

	// On a page that draws the icons once, a card refers to them.
	shared := renderOffer(t, OfferProps{Href: "/add", Icon: "layers", Title: "Umami", Docs: "https://docs.example/", Shared: true, Search: "service umami", Tags: []string{"analytics", "security"}, TagLabels: []string{"Analytics", "Security"}})
	for _, want := range []string{`<use href="#icon-layers">`, `<use href="#icon-book">`, `<use href="#icon-arrow-right">`, `data-search="service umami"`, `data-tags="analytics security"`, `<p class="offer-tags"><span>Analytics</span>`, `<span>Security</span></p>`, `data-search-text`} {
		if !strings.Contains(shared, want) {
			t.Errorf("no %s in %s", want, shared)
		}
	}
	// The categories are bare spans, which the stylesheet draws as badges:
	// a class on each would be on a thousand of them on the page that
	// lists everything.
	tags := shared[strings.Index(shared, `<p class="offer-tags">`):]
	if tags = tags[:strings.Index(tags, "</p>")]; strings.Count(tags, "class=") != 1 {
		t.Errorf("a category carries a class of its own: %s", tags)
	}
	if strings.Contains(full, "offer-tags") {
		t.Errorf("a card with no categories has a row for them: %s", full)
	}
	if strings.Contains(shared, "<path") {
		t.Errorf("a shared card draws an icon itself: %s", shared)
	}

	// A name this build has no file for is drawn with the icon, not with a
	// page that fails.
	for _, logo := range []string{"", "no-such-product"} {
		if plain := renderOffer(t, OfferProps{Href: "/add", Logo: logo, Icon: "key", Title: "Key"}); strings.Contains(plain, "<img") || !strings.Contains(plain, `<svg class="icon"`) {
			t.Errorf("logo %q: want the icon in its place: %s", logo, plain)
		}
	}
}
