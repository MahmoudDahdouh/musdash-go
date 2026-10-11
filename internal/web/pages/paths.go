package pages

import (
	"net/url"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
)

// EnvPath is the address of an environment's page, and what the address of
// everything inside the environment starts with: the project, then the
// environment.
func EnvPath(projectID, envID string) string {
	return "/projects/" + projectID + "/env/" + envID
}

// ResourcePath is the address of a resource's page: where it is, then its
// kind (db.KindApp, db.KindDatabase, db.KindService) and itself. The
// server answers it only when the three belong together (Server.placed).
func ResourcePath(projectID, envID, kind, id string) string {
	return EnvPath(projectID, envID) + "/" + kind + "/" + id
}

// Path is the address of the app's page.
func (v AppView) Path() string {
	return ResourcePath(v.Project.ID, v.Env.ID, db.KindApp, v.App.ID)
}

// Beside is the address of another app of the same environment: the app a
// preview belongs to, or a preview of this one.
func (v AppView) Beside(id string) string {
	return ResourcePath(v.Project.ID, v.Env.ID, db.KindApp, id)
}

// Path is the address of the database's page.
func (v DatabaseView) Path() string {
	return ResourcePath(v.Project.ID, v.Env.ID, db.KindDatabase, v.DB.ID)
}

// Path is the address of the service's page.
func (v ServiceView) Path() string {
	return ResourcePath(v.Project.ID, v.Env.ID, db.KindService, v.Service.ID)
}

// path is the address of something in the list. A list can hold things
// from several projects (a tag's), so each is looked up by its environment.
func (r Resources) path(envID, kind, id string) string {
	return ResourcePath(r.Places[envID].ProjectID, envID, kind, id)
}

// DeployPath is the address of the page that asks where something of the
// catalogue goes (Deploy): the kind of thing (db.KindService for a service
// template, db.KindDatabase for an engine) and its key. projectID and envID
// are the choice the page starts with, when the place it is opened from is
// known; the page checks them like any id it is sent.
func DeployPath(kind, key, projectID, envID string) string {
	path := "/deploy/" + kind + "/" + url.PathEscape(key)
	if projectID == "" {
		return path
	}
	q := url.Values{"project": {projectID}}
	if envID != "" {
		q.Set("env", envID)
	}
	return path + "?" + q.Encode()
}
