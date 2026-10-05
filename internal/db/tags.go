package db

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"sort"
	"strings"
)

// MaxTags is how many tags one resource may have, and MaxTeamTags how many
// a team may have in all: the Tags page lists every one of them.
const (
	MaxTags     = 10
	MaxTeamTags = 200
)

// ErrTagExists is returned when a tag is made, or renamed to a name, that
// the team already has.
var ErrTagExists = errors.New("the team already has this tag")

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

// addTag makes the team's tag if it does not exist yet. It refuses one
// tag more than a team may have.
func addTag(ctx context.Context, tx *sql.Tx, teamID, tag string) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM tags WHERE team_id = ? AND tag = ?)`, teamID, tag).Scan(&exists); err != nil || exists {
		return err
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tags WHERE team_id = ?`, teamID).Scan(&n); err != nil {
		return err
	}
	if n >= MaxTeamTags {
		return ErrTooMany
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO tags (team_id, tag, created_at) VALUES (?, ?, ?)`, teamID, tag, now())
	return err
}

// CreateTag makes a tag that nothing has yet.
func (d *DB) CreateTag(ctx context.Context, teamID, tag string) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM tags WHERE team_id = ? AND tag = ?)`, teamID, tag).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return ErrTagExists
		}
		return addTag(ctx, tx, teamID, tag)
	})
}

// TagExists reports whether the team has the tag, with or without
// anything that has it.
func (d *DB) TagExists(ctx context.Context, teamID, tag string) (bool, error) {
	var exists bool
	err := d.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM tags WHERE team_id = ? AND tag = ?)`, teamID, tag).Scan(&exists)
	return exists, err
}

// RenameTag gives a tag another name, on the tag and on everything that
// has it. A name the team already uses is refused: two tags are not made
// one by renaming.
func (d *DB) RenameTag(ctx context.Context, teamID, tag, name string) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM tags WHERE team_id = ? AND tag = ?)`, teamID, tag).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		if name == tag {
			return nil
		}
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM tags WHERE team_id = ? AND tag = ?)`, teamID, name).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return ErrTagExists
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tags SET tag = ? WHERE team_id = ? AND tag = ?`, name, teamID, tag); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE resource_tags SET tag = ? WHERE team_id = ? AND tag = ?`, name, teamID, tag)
		return err
	})
}

// DeleteTag removes a tag, and takes it off everything that has it.
func (d *DB) DeleteTag(ctx context.Context, teamID, tag string) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM resource_tags WHERE team_id = ? AND tag = ?`, teamID, tag); err != nil {
			return err
		}
		return affected(tx.ExecContext(ctx, `DELETE FROM tags WHERE team_id = ? AND tag = ?`, teamID, tag))
	})
}

// SetTags replaces the tags of an app or a service; a tag the team did not
// have yet is made. Call it with a resource a loader returned for the team.
func (d *DB) SetTags(ctx context.Context, teamID, kind, id string, tags []string) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM resource_tags WHERE resource_kind = ? AND resource_id = ?`, kind, id); err != nil {
			return err
		}
		for _, tag := range tags {
			if err := addTag(ctx, tx, teamID, tag); err != nil {
				return err
			}
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

// ListTags returns the team's tags with how many resources carry each. A
// tag nothing has is listed with none: sum over no rows is NULL.
func (d *DB) ListTags(ctx context.Context, teamID string) ([]TagCount, error) {
	rows, err := d.QueryContext(ctx, `SELECT t.tag, coalesce(sum(r.resource_kind = ?), 0), coalesce(sum(r.resource_kind = ?), 0)
		FROM tags t LEFT JOIN resource_tags r ON r.team_id = t.team_id AND r.tag = t.tag
		WHERE t.team_id = ? GROUP BY t.tag ORDER BY t.tag`, KindApp, KindService, teamID)
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
