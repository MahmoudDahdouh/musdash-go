package db

import (
	"context"
	"database/sql"
	"regexp"
	"sort"
	"strings"
)

// MaxTags is how many tags one resource may have.
const MaxTags = 10

var tagRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

// ValidTag reports whether a tag is written as tags are: lowercase
// letters, digits, dots, underscores and hyphens, up to 32 characters. A
// tag is part of an address (/tags/<tag>) and of the API's query string.
func ValidTag(tag string) bool { return tagRE.MatchString(tag) }

// ParseTags reads tags as a person types them, separated by commas or
// spaces, and returns them lowercased, once each and sorted. The second
// result is the first word that cannot be a tag.
func ParseTags(typed string) (tags []string, bad string) {
	seen := map[string]bool{}
	for _, word := range strings.FieldsFunc(typed, func(c rune) bool { return c == ',' || c == ' ' || c == '\t' || c == '\n' || c == '\r' }) {
		tag := strings.ToLower(word)
		if !ValidTag(tag) {
			return nil, word
		}
		if !seen[tag] {
			seen[tag] = true
			tags = append(tags, tag)
		}
	}
	sort.Strings(tags)
	return tags, ""
}

// TagCount is a tag and how many of the team's resources have it.
type TagCount struct {
	Tag      string
	Apps     int
	Services int
}

// SetTags replaces the tags of an app or a service. Call it with a
// resource a loader returned for the team.
func (d *DB) SetTags(ctx context.Context, teamID, kind, id string, tags []string) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM resource_tags WHERE resource_kind = ? AND resource_id = ?`, kind, id); err != nil {
			return err
		}
		for _, tag := range tags {
			if _, err := tx.ExecContext(ctx, `INSERT INTO resource_tags (team_id, tag, resource_kind, resource_id) VALUES (?, ?, ?, ?)`, teamID, tag, kind, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// TagsOf returns the tags of an app or a service.
func (d *DB) TagsOf(ctx context.Context, teamID, kind, id string) ([]string, error) {
	return d.queryIDs(ctx, `SELECT tag FROM resource_tags WHERE team_id = ? AND resource_kind = ? AND resource_id = ? ORDER BY tag`, teamID, kind, id)
}

// ListTags returns the team's tags with how many resources carry each.
func (d *DB) ListTags(ctx context.Context, teamID string) ([]TagCount, error) {
	rows, err := d.QueryContext(ctx, `SELECT tag, sum(resource_kind = ?), sum(resource_kind = ?) FROM resource_tags
		WHERE team_id = ? GROUP BY tag ORDER BY tag`, KindApp, KindService, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TagCount
	for rows.Next() {
		var t TagCount
		if err := rows.Scan(&t.Tag, &t.Apps, &t.Services); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Tagged returns the team's apps and services that have a tag. The team
// is checked twice: on the tag's row, and on the resource through its
// project, so a row that named another team's resource would find nothing.
func (d *DB) Tagged(ctx context.Context, teamID, tag string) ([]App, []Service, error) {
	apps, err := d.queryApps(ctx, `SELECT `+appColumns+` FROM apps a
		JOIN environments e ON e.id = a.environment_id JOIN projects p ON p.id = e.project_id
		JOIN resource_tags t ON t.resource_kind = ? AND t.resource_id = a.id
		WHERE t.team_id = ? AND t.tag = ? AND p.team_id = ? AND a.preview_of = '' ORDER BY a.name, a.id`, KindApp, teamID, tag, teamID)
	if err != nil {
		return nil, nil, err
	}
	services, err := d.queryServices(ctx, `SELECT `+serviceColumns+` FROM services s
		JOIN environments e ON e.id = s.environment_id JOIN projects p ON p.id = e.project_id
		JOIN resource_tags t ON t.resource_kind = ? AND t.resource_id = s.id
		WHERE t.team_id = ? AND t.tag = ? AND p.team_id = ? ORDER BY s.name, s.id`, KindService, teamID, tag, teamID)
	return apps, services, err
}
