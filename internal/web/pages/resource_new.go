package pages

import (
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/catalog"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

// servicesPerPage is how many service cards the Add resource page draws at
// a time: full rows at two, three and four columns.
const servicesPerPage = 48

// maxFindText is where the field's text is cut. A name in the catalogue is
// far shorter, and the text is in the address of every page of the list.
const maxFindText = 64

// NewResource is what the Add resource page offers: the ways to make an
// app, the database engines and the service templates, narrowed by what the
// address says and, for the services, cut to one page. The server finds and
// cuts; the browser holds only the cards it was given.
type NewResource struct {
	Project db.Project
	Env     db.Environment
	// HasApp, HasGitLab and HasKey say whether the team has a GitHub App,
	// a GitLab source and a deploy key: the ways into a private repository.
	HasApp    bool
	HasGitLab bool
	HasKey    bool

	// Query is the field's text and Chosen the categories that are ticked,
	// both as Find took them from the address. Page is the last of the
	// services' pages that is drawn, from 1.
	Query  string
	Chosen []string
	Page   int

	apps     []ui.OfferProps
	engines  []catalog.DBTemplate
	services []catalog.ServiceTemplate
	// found is how many services there are in all the pages.
	found int
}

func (v NewResource) Path() string { return EnvPath(v.Project.ID, v.Env.ID) }

// Find narrows the page to what text and categories find, and the services
// to one page of them or, with whole, to every page up to that one: a page
// drawn from its start has what the list had been scrolled to, so Back and
// Refresh bring a person to where they were. All three come from the
// address, so nothing of them is taken as it is: the text is cut, a
// category counts only if it is one, and no page is past the end. Set the
// Has fields first, which the app cards' words depend on.
func (v *NewResource) Find(text string, categories []string, page int, whole bool) {
	v.Query = strings.TrimSpace(text)
	if r := []rune(v.Query); len(r) > maxFindText {
		v.Query = strings.TrimSpace(string(r[:maxFindText]))
	}
	// In the catalogue's order and each once, so that one choice has one
	// address however it was sent.
	v.Chosen = nil
	for _, c := range catalog.Categories() {
		if slices.Contains(categories, c.Key) {
			v.Chosen = append(v.Chosen, c.Key)
		}
	}
	// The number is the address's too. Every page past the catalogue's
	// last is the one after it, which is empty.
	v.Page = min(max(page, 1), len(catalog.Services())/servicesPerPage+2)
	words := strings.Fields(strings.ToLower(v.Query))

	// A way to make an app has no category: any choice of them leaves it out.
	v.apps = nil
	if len(v.Chosen) == 0 {
		for _, a := range v.appOffers() {
			if finds(words, a.known, a.card.Title, a.card.Text) {
				v.apps = append(v.apps, a.card)
			}
		}
	}
	v.engines = nil
	if len(v.Chosen) == 0 || slices.Contains(v.Chosen, engineCategory) {
		for _, t := range catalog.Databases() {
			if finds(words, "database "+t.Engine, t.Label, t.About) {
				v.engines = append(v.engines, t)
			}
		}
	}
	// All of the catalogue is walked for the count, and only this page's
	// templates are kept.
	v.services, v.found = nil, 0
	first, end := (v.Page-1)*servicesPerPage, v.Page*servicesPerPage
	if whole {
		first = 0
	}
	for _, t := range catalog.Services() {
		if len(v.Chosen) > 0 && !slices.ContainsFunc(t.Categories, func(c string) bool { return slices.Contains(v.Chosen, c) }) {
			continue
		}
		// Asked only with something to find: most requests have no text,
		// and the catalogue is several hundred templates.
		if len(words) > 0 && !finds(words, "service "+t.Key, t.Name, t.About, strings.Join(categoryLabels(t.Categories), " ")) {
			continue
		}
		if v.found >= first && v.found < end {
			v.services = append(v.services, t)
		}
		v.found++
	}
}

// finds says whether every word is somewhere in what a card is known by.
// The words are lowercase; no word finds everything.
func finds(words []string, known ...string) bool {
	if len(words) == 0 {
		return true
	}
	all := strings.ToLower(strings.Join(known, " "))
	for _, w := range words {
		if !strings.Contains(all, w) {
			return false
		}
	}
	return true
}

// Narrowed says whether the address leaves anything out.
func (v NewResource) Narrowed() bool { return v.Query != "" || len(v.Chosen) > 0 }

// Nothing says whether what the address asks for finds no card at all.
func (v NewResource) Nothing() bool {
	return len(v.apps) == 0 && len(v.engines) == 0 && v.found == 0
}

// More says whether there are services after this page.
func (v NewResource) More() bool { return v.Page*servicesPerPage < v.found }

// Address is the page's address with what narrows it and how far the list
// goes, in its simplest form: what the address bar shows.
func (v NewResource) Address() string { return v.address(v.Page) }

// Drawn is how many service cards Find kept.
func (v NewResource) Drawn() int { return len(v.services) }

// address is where a page of the list is asked for, and the address of the
// page that has the list up to there.
func (v NewResource) address(page int) string {
	q := url.Values{}
	if v.Query != "" {
		q.Set("q", v.Query)
	}
	for _, c := range v.Chosen {
		q.Add("category", c)
	}
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	if len(q) == 0 {
		return v.Path() + "/new"
	}
	return v.Path() + "/new?" + q.Encode()
}

// privateRepo is where a private-repository card leads: to the Git form
// with that access chosen or, while the team has none of the kind, to where
// one is added.
func (v NewResource) privateRepo(access string, has bool) string {
	if has {
		return v.Path() + "/app/new?source=git&access=" + access
	}
	if access == "key" {
		return KeysPath
	}
	return "/sources"
}

func gitHubAppText(has bool) string {
	if has {
		return "Choose a repository through your GitHub App. A push deploys it, with nothing to set up in the repository."
	}
	return "Connect a GitHub App under Sources first. It reads private repositories and deploys on every push."
}

func gitLabText(has bool) string {
	if has {
		return "Choose a repository your GitLab token can read, on gitlab.com or your own instance."
	}
	return "Connect GitLab under Sources first, with an access token. It reads private repositories on gitlab.com or your own instance."
}

func deployKeyText(has bool) string {
	if has {
		return "Any Git host over SSH: GitLab, Bitbucket, Gitea or GitHub. The repository gets a read-only deploy key."
	}
	return "Add an SSH key under Keys & tokens first. It reads a private repository on any Git host over SSH."
}

// setUp is what the button of a private-repository card says: nothing of
// its own (so Deploy) when the team has that access, and otherwise what
// the page it leads to is for.
func setUp(has bool, otherwise string) string {
	if has {
		return ""
	}
	return otherwise
}

// appOffer is a way to make an app: its card, and the words the field finds
// it by besides the card's own.
type appOffer struct {
	card  ui.OfferProps
	known string
}

// appOffers are the ways to make an app, in the page's order. A person's
// own Compose file is among them, with what else is their own, though what
// it makes is a service.
func (v NewResource) appOffers() []appOffer {
	return []appOffer{
		{ui.OfferProps{Href: v.Path() + "/app/new?source=git", Logo: "git", Icon: "git-branch", Title: "Public repository", Text: "Build from a public Git repository on any host.", Shared: true},
			"public repository git github gitlab bitbucket gitea dockerfile nixpacks railpack static"},
		{ui.OfferProps{Href: v.privateRepo("app", v.HasApp), Logo: "github", Icon: "github", Title: "Private repository, with a GitHub App", Text: gitHubAppText(v.HasApp), Action: setUp(v.HasApp, "Connect"), Shared: true},
			"private repository github app git"},
		{ui.OfferProps{Href: v.privateRepo("gitlab", v.HasGitLab), Logo: "gitlab", Icon: "gitlab", Title: "Private repository, with GitLab", Text: gitLabText(v.HasGitLab), Action: setUp(v.HasGitLab, "Connect"), Shared: true},
			"private repository gitlab token git"},
		{ui.OfferProps{Href: v.privateRepo("key", v.HasKey), Icon: "key", Title: "Private repository, with a deploy key", Text: deployKeyText(v.HasKey), Action: setUp(v.HasKey, "Add key"), Shared: true},
			"private repository deploy key ssh git gitlab bitbucket gitea"},
		{ui.OfferProps{Href: v.Path() + "/app/new", Logo: "docker", Icon: "box", Title: "Docker image", Text: "Run an image from a registry, such as nginx:alpine or ghcr.io/you/app:1.4.", Shared: true},
			"docker image registry container"},
		{ui.OfferProps{Href: v.Path() + "/service/new?template=" + db.TemplateCustom, Logo: "docker", Icon: "code", Title: "Your own Compose file", Text: "Paste a docker-compose.yml. Templates written for Coolify work as they are.", Shared: true},
			"service compose docker-compose yaml custom own coolify"},
		{ui.OfferProps{Href: v.Path() + "/service/new?template=" + db.TemplateGit, Logo: "git", Icon: "git-branch", Title: "Compose file in a Git repository", Text: "Read from the repository at every deployment. It may build the repository's own Dockerfiles.", Shared: true},
			"service compose git repository"},
	}
}

// categoryLabels is how a card names a template's categories.
func categoryLabels(keys []string) []string {
	labels := make([]string, 0, len(keys))
	for _, k := range keys {
		if l := catalog.CategoryLabel(k); l != "" {
			labels = append(labels, l)
		}
	}
	return labels
}

// engineCategory is the category the database engines are found under, with
// the services that are databases' tools.
const engineCategory = "database"

// categoryOptions are the categories the page can be narrowed to: those
// that have something, in the catalogue's order, each with how many, and
// ticked when the address chose it.
func (v NewResource) categoryOptions() []ui.MultiOption {
	_, count := catalog.CategoriesInUse()
	var options []ui.MultiOption
	for _, c := range catalog.Categories() {
		n := count[c.Key]
		if c.Key == engineCategory {
			n += len(catalog.Databases())
		}
		if n > 0 {
			options = append(options, ui.MultiOption{Value: c.Key, Label: c.Label, Count: n, Checked: slices.Contains(v.Chosen, c.Key)})
		}
	}
	return options
}
