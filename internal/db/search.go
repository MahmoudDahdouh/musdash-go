package db

import (
	"context"
	"strconv"
	"strings"
)

// What the search in the bar looks in: everything of a team that has a
// page to open. It is one statement, asked again for every pause in the
// typing, and nothing of it is kept: a list of the team's rows held in
// memory is what the RAM budget forbids, and the tables are small.
//
// Not looked in, on purpose: variables and their values, Compose text,
// keys, tokens and people. A search that matched inside a stored secret
// would say what it holds.

// The kinds of Hit besides KindApp, KindDatabase and KindService.
const (
	HitProject     = "project"
	HitEnvironment = "environment"
	HitDomain      = "domain"
	HitServer      = "server"
	HitTag         = "tag"
)

// MaxSearchWords is how many words of a search are looked for. More make
// the statement longer and the answer no better.
const MaxSearchWords = 5

// The ranks of a Hit, best first.
const (
	RankExact  = iota // the name is what was typed
	RankPrefix        // the name starts with a word
	RankInside        // the name holds a word
	RankOther         // a word is in something else the row has
)

// Hit is one thing a search found.
type Hit struct {
	Kind string
	ID   string
	Name string
	// Detail is the one more fact a row is told apart by: a project's
	// description, an app's repository or image, a database's engine, a
	// service's template or repository, a server's host.
	Detail string
	// Status is the state of an app, database, service or server.
	Status string
	// Where it is. A project is its own; a server and a tag are nowhere.
	ProjectID, Project string
	EnvID, Env         string
	// What a domain points at: KindApp or KindService, with its id and name.
	OwnerKind, OwnerID, Owner string
	Rank                      int
}

// searchable is every row a search can find, each arm filtered on the team
// (?1) itself. own is the text a row is found by, place the names of where
// it is, and ref the id that names on a server are made of. Previews are
// left out: a preview is listed with its parent and nowhere else. A domain
// has no team of its own and is reached through what it points at.
const searchable = `
	SELECT 'project' AS kind, 1 AS ord, p.id AS id, p.name AS name, p.description AS detail, '' AS status,
	       p.id AS project_id, p.name AS project, '' AS env_id, '' AS env, '' AS owner_kind, '' AS owner_id, '' AS owner,
	       p.name || ' ' || p.description AS own, '' AS place, p.id AS ref
	FROM projects p WHERE p.team_id = ?1
	UNION ALL
	SELECT 'environment', 2, e.id, e.name, '', '', p.id, p.name, e.id, e.name, '', '', '',
	       e.name, p.name, e.id
	FROM environments e JOIN projects p ON p.id = e.project_id WHERE p.team_id = ?1
	UNION ALL
	SELECT 'app', 3, a.id, a.name, CASE WHEN a.source = 'git' THEN a.repo_name ELSE a.image END, a.status,
	       p.id, p.name, e.id, e.name, '', '', '',
	       a.name || ' ' || a.image || ' ' || a.repo_name || ' ' || a.branch, p.name || ' ' || e.name, a.id
	FROM apps a JOIN environments e ON e.id = a.environment_id JOIN projects p ON p.id = e.project_id
	WHERE p.team_id = ?1 AND a.preview_of = ''
	UNION ALL
	SELECT 'database', 4, m.id, m.name, m.engine, m.status, p.id, p.name, e.id, e.name, '', '', '',
	       m.name || ' ' || m.engine || ' ' || m.image, p.name || ' ' || e.name, m.id
	FROM databases m JOIN environments e ON e.id = m.environment_id JOIN projects p ON p.id = e.project_id
	WHERE p.team_id = ?1
	UNION ALL
	SELECT 'service', 5, s.id, s.name, CASE s.template WHEN 'git' THEN s.repo_name WHEN 'custom' THEN '' ELSE s.template END, s.status,
	       p.id, p.name, e.id, e.name, '', '', '',
	       s.name || ' ' || s.template || ' ' || s.repo_name, p.name || ' ' || e.name, s.id
	FROM services s JOIN environments e ON e.id = s.environment_id JOIN projects p ON p.id = e.project_id
	WHERE p.team_id = ?1
	UNION ALL
	SELECT 'domain', 6, d.id, d.host || d.path, '', '', p.id, p.name, e.id, e.name, 'app', a.id, a.name,
	       d.host || d.path, a.name || ' ' || p.name || ' ' || e.name, ''
	FROM domains d JOIN apps a ON d.resource_kind = 'app' AND a.id = d.resource_id
	JOIN environments e ON e.id = a.environment_id JOIN projects p ON p.id = e.project_id
	WHERE p.team_id = ?1 AND a.preview_of = ''
	UNION ALL
	SELECT 'domain', 6, d.id, d.host || d.path, '', '', p.id, p.name, e.id, e.name, 'service', s.id, s.name,
	       d.host || d.path, s.name || ' ' || p.name || ' ' || e.name, ''
	FROM domains d JOIN service_endpoints ep ON d.resource_kind = 'service' AND ep.id = d.resource_id
	JOIN services s ON s.id = ep.service_id
	JOIN environments e ON e.id = s.environment_id JOIN projects p ON p.id = e.project_id
	WHERE p.team_id = ?1
	UNION ALL
	SELECT 'server', 7, v.id, v.name, v.host, v.status, '', '', '', '', '', '', '',
	       v.name || ' ' || v.host || ' ' || v.ip, '', ''
	FROM servers v WHERE v.team_id = ?1
	UNION ALL
	SELECT 'tag', 8, t.tag, t.tag, '', '', '', '', '', '', '', '', '',
	       t.tag, '', ''
	FROM tags t WHERE t.team_id = ?1`

// likeEscaper makes a word a text LIKE looks for as it is: the two
// wildcards and the escape character itself stand for themselves.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// Search returns what the team has that holds every one of the words, best
// first: by rank, then projects, environments, apps, databases, services,
// domains, servers and tags, then by name. Each word must be in the row's
// own text or in the names of where it is, and at least one in its own
// text: "shop web" finds the app web of the project Shop, and "shop" alone
// does not list all that Shop holds. A word that has a row's id in it finds
// that row too, so a container's name can be pasted whole. Case is ignored
// as LIKE ignores it, for ASCII letters. Without words there is nothing
// to find.
func (d *DB) Search(ctx context.Context, teamID string, words []string, limit int) ([]Hit, error) {
	// ?1 is the team and ?2 the whole text, to compare a name with. The
	// words follow, written into the statement by number only: what was
	// typed is never part of its text.
	args := []any{teamID, ""}
	var every, some, prefix, inside []string
	for _, w := range words {
		if w == "" {
			continue
		}
		if len(every) == MaxSearchWords {
			break
		}
		args = append(args, likeEscaper.Replace(w), w)
		like, raw := "?"+strconv.Itoa(len(args)-1), "?"+strconv.Itoa(len(args))
		holds := ` LIKE '%' || ` + like + ` || '%' ESCAPE '\'`
		byID := `(ref <> '' AND instr(` + raw + `, ref) > 0)`
		every = append(every, `(own`+holds+` OR place`+holds+` OR `+byID+`)`)
		some = append(some, `own`+holds, byID)
		prefix = append(prefix, `name LIKE `+like+` || '%' ESCAPE '\'`)
		inside = append(inside, `name`+holds)
	}
	if len(every) == 0 {
		return nil, nil
	}
	typed := make([]string, 0, len(every))
	for i := 2; i < len(args); i += 2 {
		typed = append(typed, args[i].(string))
	}
	args[1] = strings.Join(typed, " ")
	args = append(args, limit)

	rows, err := d.QueryContext(ctx, `
		SELECT kind, id, name, detail, status, project_id, project, env_id, env, owner_kind, owner_id, owner,
		       CASE WHEN name LIKE ?2 ESCAPE '\' THEN 0
		            WHEN `+strings.Join(prefix, " OR ")+` THEN 1
		            WHEN `+strings.Join(inside, " OR ")+` THEN 2
		            ELSE 3 END AS rank
		FROM (`+searchable+`)
		WHERE `+strings.Join(every, " AND ")+` AND (`+strings.Join(some, " OR ")+`)
		ORDER BY rank, ord, name COLLATE NOCASE, id LIMIT ?`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.Kind, &h.ID, &h.Name, &h.Detail, &h.Status, &h.ProjectID, &h.Project, &h.EnvID, &h.Env,
			&h.OwnerKind, &h.OwnerID, &h.Owner, &h.Rank); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
