package web

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/catalog"
)

// hx asks as htmx does: for a fragment, and for the element with that id
// where it names one.
func (a *app) hx(path, target string) (*http.Response, string) {
	a.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, a.url+path, nil)
	req.Header.Set("HX-Request", "true")
	if target != "" {
		req.Header.Set("HX-Target", target)
	}
	return a.do(a.client, req)
}

var (
	templateCardRE = regexp.MustCompile(`/service/new\?template=([^"&]+)"`)
	lastRowRE      = regexp.MustCompile(`<li class="offers-more" hx-get="([^"]+)" hx-trigger="intersect once, click" hx-swap="outerHTML">`)
	foundRE        = regexp.MustCompile(`<h2 class="section-title">Services <span class="font-normal text-ink-mute">\((\d+)\)</span>`)
)

// serviceCards are the templates whose cards are in what the Add resource
// page answered, in their order: in a page, those under Services.
func serviceCards(body string) []string {
	if at := strings.Index(body, `aria-label="Kinds of service"`); at >= 0 {
		body = body[at:]
	} else if strings.Contains(body, "<html") {
		return nil // a page with no services: what it links to is not one
	}
	var keys []string
	for _, m := range templateCardRE.FindAllStringSubmatch(body, -1) {
		keys = append(keys, m[1])
	}
	return keys
}

// lastRow is the address the list's last row asks for, "" when the list
// has none: nothing comes after.
func lastRow(body string) string {
	if m := lastRowRE.FindStringSubmatch(body); m != nil {
		return html.UnescapeString(m[1])
	}
	return ""
}

// servicesFound is the number in the Services heading, -1 with no heading.
func servicesFound(body string) int {
	if m := foundRE.FindStringSubmatch(body); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return -1
}

// The Add resource page is found and cut by the server: what narrows it is
// in the address, and the services come 48 at a time.
func TestAddResourceIsFoundAndPaged(t *testing.T) {
	a := newApp(t, false)
	a.setup()
	projectID, env := a.project("Shop")
	base := "/projects/" + projectID + "/env/" + env.ID + "/new"
	all := catalog.Services()
	_, inCategory := catalog.CategoriesInUse()
	byKey := map[string]catalog.ServiceTemplate{}
	for _, tpl := range all {
		byKey[tpl.Key] = tpl
	}
	if len(all) <= 96 {
		t.Fatalf("the catalogue has %d templates: too few to page", len(all))
	}

	// The page holds the first 48 and says how many there are; each answer
	// to the last row brings the next 48 and the next last row, until the
	// catalogue has been given once, in its order.
	res, page := a.get(base)
	wantStatus(t, res, http.StatusOK)
	got := serviceCards(page)
	if len(got) != 48 || servicesFound(page) != len(all) {
		t.Fatalf("the page has %d service cards and says %d: want 48 of %d", len(got), servicesFound(page), len(all))
	}
	next := lastRow(page)
	if next != base+"?page=2" {
		t.Fatalf("the last row asks for %q", next)
	}
	for n := 2; next != ""; n++ {
		if n > len(all) {
			t.Fatal("the list never ends")
		}
		res, more := a.hx(next, "")
		wantStatus(t, res, http.StatusOK)
		if strings.Contains(more, "<html") || strings.Contains(more, "<h2") || strings.Contains(more, "Kinds of") {
			t.Fatalf("page %d is more than cards: %.300s", n, more)
		}
		cards := serviceCards(more)
		if len(cards) == 0 || len(cards) > 48 {
			t.Fatalf("page %d has %d cards", n, len(cards))
		}
		got = append(got, cards...)
		// The address bar follows the list.
		if at := res.Header.Get("HX-Replace-Url"); at != base+"?page="+strconv.Itoa(n) {
			t.Fatalf("with page %d the address becomes %q", n, at)
		}
		next = lastRow(more)
		if want := base + "?page=" + strconv.Itoa(n+1); next != "" && next != want {
			t.Fatalf("page %d's last row asks for %q, want %q", n, next, want)
		}
	}
	want := make([]string, len(all))
	for i, tpl := range all {
		want[i] = tpl.Key
	}
	if !slices.Equal(got, want) {
		t.Fatalf("the pages together are %d cards, not the catalogue's %d in its order", len(got), len(want))
	}
	// A page past the end is empty, however far past, and leaves the
	// address alone.
	for _, past := range []string{"999", strconv.Itoa(len(all)/48 + 2)} {
		res, more := a.hx(base+"?page="+past, "")
		if wantStatus(t, res, http.StatusOK); strings.TrimSpace(more) != "" || res.Header.Get("HX-Replace-Url") != "" {
			t.Errorf("page %s is not empty: %.200s", past, more)
		}
	}
	// A browser's own request with a page in it (Back, Refresh, a link) is
	// answered with the page and the list from its start up to there.
	res, third := a.get(base + "?page=3")
	if !slices.Equal(serviceCards(third), want[:144]) || !strings.Contains(third, "<html") || lastRow(third) != base+"?page=4" || res.Header.Get("HX-Replace-Url") != "" {
		t.Errorf("a browser that asks for page 3 gets %d cards and a last row for %q", len(serviceCards(third)), lastRow(third))
	}
	for _, far := range []string{"999", "99999999999999999999", "-4", "x"} {
		_, whole := a.get(base + "?page=" + far)
		// A number past the end, however large, is the whole list; what
		// is no page number is the first page.
		past := far[0] == '9'
		if n := len(serviceCards(whole)); (past && (n != len(all) || lastRow(whole) != "")) || (!past && (n != 48 || lastRow(whole) != base+"?page=2")) {
			t.Errorf("page=%s draws %d cards, last row %q", far, n, lastRow(whole))
		}
	}

	// The field's text is in the address. Every word has to be in the card;
	// capitals and the spaces around do not matter.
	wordpress := 0
	for _, tpl := range all {
		if strings.Contains(strings.ToLower(tpl.Key+" "+tpl.Name+" "+tpl.About), "wordpress") {
			wordpress++
		}
	}
	if wordpress == 0 || wordpress > 48 {
		t.Fatalf("%d templates name WordPress: the test wants some, on one page", wordpress)
	}
	_, found := a.get(base + "?q=" + url.QueryEscape("  WordPress "))
	cards := serviceCards(found)
	if len(cards) != wordpress || servicesFound(found) != wordpress || !slices.Contains(cards, "wordpress") || lastRow(found) != "" {
		t.Errorf("WordPress finds %d cards and says %d, want %d on one page: %v", len(cards), servicesFound(found), wordpress, cards)
	}
	if !strings.Contains(found, `name="q" value="WordPress"`) && !regexp.MustCompile(`<input[^>]*id="kind-filter"[^>]*value="WordPress"`).MatchString(found) {
		t.Error("the field does not hold what the address says")
	}
	if strings.Contains(found, `aria-label="Kinds of app"`) || strings.Contains(found, `aria-label="Database engines"`) {
		t.Error("WordPress finds a way to make an app, or an engine")
	}
	if strings.Count(found, "Clear filters") != 1 || !strings.Contains(found, `href="`+base+`"`) {
		t.Error("a narrowed page has no Clear filters that leads to the bare address")
	}
	label := catalog.CategoryLabel(byKey["wordpress"].Categories[0])
	_, both := a.get(base + "?q=" + url.QueryEscape("wordpress "+strings.ToUpper(label)))
	if cards := serviceCards(both); !slices.Contains(cards, "wordpress") || len(cards) > wordpress {
		t.Errorf("WordPress and its category's name find %v", cards)
	}
	if _, none := a.get(base + "?q=wordpress+zzzzqq"); len(serviceCards(none)) != 0 {
		t.Error("a word that is in no card does not narrow")
	}
	// The other kinds are found by the same field.
	_, postgres := a.get(base + "?q=postgres")
	if !strings.Contains(postgres, `/database/new?engine=postgres"`) || strings.Contains(postgres, `engine=redis"`) {
		t.Error("postgres does not find its engine alone")
	}
	_, compose := a.get(base + "?q=compose")
	if !strings.Contains(compose, `/service/new?template=custom"`) || strings.Contains(compose, `aria-label="Database engines"`) {
		t.Error("compose does not find a person's own Compose file under Apps")
	}
	// A text that finds more than a page keeps itself in the last row's
	// address, so the next page is of the same list.
	_, wide := a.get(base + "?q=e")
	if n := servicesFound(wide); n <= 48 || lastRow(wide) != base+"?page=2&q=e" {
		t.Errorf("e finds %d and the last row asks for %q", n, lastRow(wide))
	}
	_, second := a.hx(base+"?page=2&q=e", "")
	for _, key := range serviceCards(second) {
		tpl := byKey[key]
		if !strings.Contains(strings.ToLower(tpl.Key+" "+tpl.Name+" "+tpl.About+" service "+strings.Join(tpl.Categories, " ")), "e") {
			t.Errorf("the second page of e holds %s", key)
		}
	}
	if len(serviceCards(second)) == 0 || slices.Contains(serviceCards(second), serviceCards(wide)[0]) {
		t.Error("the second page of e is empty, or begins again")
	}

	// A category is in the address as well, ticked in the menu and counted
	// on its button. A way to make an app has none.
	_, cms := a.get(base + "?category=cms")
	if !regexp.MustCompile(`name="category" value="cms" checked`).MatchString(cms) || strings.Count(cms, " checked") != 1 || !strings.Contains(cms, `data-select-count><span>1</span>`) {
		t.Error("the chosen category is not ticked alone, or the button does not count it")
	}
	if servicesFound(cms) != inCategory["cms"] || strings.Contains(cms, `aria-label="Kinds of app"`) || strings.Contains(cms, `aria-label="Database engines"`) {
		t.Errorf("CMS says %d of %d, or keeps what has no such category", servicesFound(cms), inCategory["cms"])
	}
	for _, key := range serviceCards(cms) {
		if !slices.Contains(byKey[key].Categories, "cms") {
			t.Errorf("%s is under CMS", key)
		}
	}
	either := 0
	for _, tpl := range all {
		if slices.Contains(tpl.Categories, "cms") || slices.Contains(tpl.Categories, "analytics") {
			either++
		}
	}
	// Either of two, and one that is no category is as if it was not sent.
	_, two := a.get(base + "?category=analytics&category=nope&category=cms")
	if servicesFound(two) != either || either <= inCategory["cms"] || strings.Count(two, " checked") != 2 {
		t.Errorf("CMS and Analytics say %d, want %d", servicesFound(two), either)
	}
	// In the catalogue's order, whatever order they were sent in.
	if want := base + "?category=analytics&category=cms&page=2"; either > 48 && lastRow(two) != want {
		t.Errorf("the last row of two categories asks for %q, want %q", lastRow(two), want)
	}
	// The engines are databases.
	_, databases := a.get(base + "?category=database")
	if !strings.Contains(databases, `/database/new?engine=postgres"`) || strings.Contains(databases, `aria-label="Kinds of app"`) {
		t.Error("the engines are not under the Databases category, or a way to make an app is")
	}

	// Nothing found: no group, and the way back to everything.
	_, nothing := a.get(base + "?q=zzzzqq")
	if strings.Contains(nothing, "Kinds of") || !strings.Contains(nothing, "Nothing matches") || strings.Count(nothing, "Clear filters") != 2 || servicesFound(nothing) != -1 {
		t.Errorf("a text that finds nothing does not say so")
	}

	// The form's question is answered with the list alone, the Clear button
	// for its place by the field, and the address in its simplest form.
	res, list := a.hx(base+"?q=+WordPress+&page=7&category=nope&category=cms&category=", "kinds")
	wantStatus(t, res, http.StatusOK)
	if got, want := res.Header.Get("HX-Replace-Url"), base+"?category=cms&q=WordPress"; got != want {
		t.Errorf("the address becomes %q, want %q", got, want)
	}
	if strings.Contains(list, "<html") || strings.Contains(list, "<form") || !strings.Contains(list, `<span id="kinds-clear" hx-swap-oob="true"><a href="`+base+`"`) {
		t.Errorf("the answer to the form is not the list and the Clear button: %.300s", list)
	}
	if cards := serviceCards(list); !slices.Contains(cards, "wordpress") || servicesFound(list) != len(cards) {
		t.Errorf("the answer to the form holds %v and says %d", cards, servicesFound(list))
	}
	res, list = a.hx(base+"?q=", "kinds")
	if got := res.Header.Get("HX-Replace-Url"); got != base || !strings.Contains(list, `<span id="kinds-clear" hx-swap-oob="true"></span>`) || len(serviceCards(list)) != 48 {
		t.Errorf("an emptied field leaves the address at %q, or Clear filters, or not the first page", got)
	}
	// Another element's question is not the form's: it gets the page.
	if res, other := a.hx(base, "content"); res.Header.Get("HX-Replace-Url") != "" || !strings.Contains(other, "<html") {
		t.Error("a request that is not the form's was answered as if it were")
	}

	// What was typed is text wherever it comes back, and is cut.
	_, typed := a.get(base + "?q=" + url.QueryEscape(`"><script>alert(1)</script>`))
	if strings.Contains(typed, "<script>alert") || !strings.Contains(typed, "Nothing matches") {
		t.Error("the field's text reached the page as markup")
	}
	res, _ = a.hx(base+"?q="+strings.Repeat("é", 500), "kinds")
	if got := res.Header.Get("HX-Replace-Url"); got != base+"?q="+url.QueryEscape(strings.Repeat("é", 64)) {
		t.Errorf("a long text is not cut to 64 characters: %q", got)
	}
}
