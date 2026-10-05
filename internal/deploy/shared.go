package deploy

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
)

// A shared variable is used by naming it inside a value:
//
//	DATABASE_URL={{environment.DATABASE_URL}}
//	API=https://{{team.API_HOST}}/v1
//
// Only this exact form is read. Anything else in double braces, such as
// "{{ .Name }}" for a template engine, is left as it is. A value that needs
// this very text for itself puts a backslash before it: "\{{team.NAME}}"
// reaches the container as "{{team.NAME}}".
var sharedRE = regexp.MustCompile(`(\\?)\{\{\s*(team|project|environment|server)\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// SharedRef names one shared variable.
type SharedRef struct {
	Scope string
	Name  string
}

func (r SharedRef) String() string { return "{{" + r.Scope + "." + r.Name + "}}" }

// SharedRefs lists the shared variables a value names, each once.
func SharedRefs(value string) []SharedRef {
	if !strings.Contains(value, "{{") {
		return nil
	}
	var out []SharedRef
	seen := map[SharedRef]bool{}
	for _, m := range sharedRE.FindAllStringSubmatch(value, -1) {
		if m[1] != "" {
			continue // escaped: the text itself is wanted
		}
		ref := SharedRef{Scope: m[2], Name: m[3]}
		if !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	return out
}

// ExpandShared puts shared variables' values where a value names them and
// reports the names lookup did not know, which are left in place. What is
// put in is not read again: a shared variable's own value is taken as it
// is, so one cannot name another and nothing can loop.
func ExpandShared(value string, lookup func(scope, name string) (string, bool)) (string, []SharedRef) {
	if !strings.Contains(value, "{{") {
		return value, nil
	}
	var missing []SharedRef
	seen := map[SharedRef]bool{}
	out := sharedRE.ReplaceAllStringFunc(value, func(match string) string {
		m := sharedRE.FindStringSubmatch(match)
		if m[1] != "" {
			return match[1:]
		}
		if v, ok := lookup(m[2], m[3]); ok {
			return v
		}
		if ref := (SharedRef{Scope: m[2], Name: m[3]}); !seen[ref] {
			seen[ref] = true
			missing = append(missing, ref)
		}
		return match
	})
	return out, missing
}

// sharedLookup returns the shared variables a resource may name: those of
// its own team, project, environment and server, and no others. Values are
// opened only when they are asked for.
func (d *Deployer) sharedLookup(ctx context.Context, environmentID, serverID string) (func(scope, name string) (string, bool), error) {
	scope, err := d.DB.ScopeOf(ctx, environmentID, serverID)
	if err != nil {
		return nil, err
	}
	sealed, err := d.DB.SharedFor(ctx, scope)
	if err != nil {
		return nil, err
	}
	return func(scope, name string) (string, bool) {
		v, ok := sealed[scope][name]
		if !ok {
			return "", false
		}
		plain, err := d.Box.OpenString(v)
		if err != nil {
			// Reported as missing by name; the cause goes to the log.
			d.Log.Error("open shared variable", "scope", scope, "name", name, "err", err)
			return "", false
		}
		return plain, true
	}, nil
}

// MissingShared lists the shared variables that values name and that do
// not exist for a resource in this environment and on this server.
func (d *Deployer) MissingShared(ctx context.Context, environmentID, serverID string, values []string) ([]SharedRef, error) {
	var refs []SharedRef
	seen := map[SharedRef]bool{}
	for _, v := range values {
		for _, ref := range SharedRefs(v) {
			if !seen[ref] {
				seen[ref] = true
				refs = append(refs, ref)
			}
		}
	}
	if len(refs) == 0 {
		return nil, nil
	}
	scope, err := d.DB.ScopeOf(ctx, environmentID, serverID)
	if err != nil {
		return nil, err
	}
	sealed, err := d.DB.SharedFor(ctx, scope)
	if err != nil {
		return nil, err
	}
	var missing []SharedRef
	for _, ref := range refs {
		if _, ok := sealed[ref.Scope][ref.Name]; !ok {
			missing = append(missing, ref)
		}
	}
	return missing, nil
}

// expandShared fills in the shared variables that the values of vars name,
// in place. Nothing is read from the database when no value names one. A
// name that does not exist is an error that says which: an empty value in
// its place would be found only when the app misbehaves.
func (d *Deployer) expandShared(ctx context.Context, environmentID, serverID string, vars []db.EnvVar) error {
	uses := false
	for _, v := range vars {
		// An escaped name counts too: its backslash has to come off.
		if strings.Contains(v.Value, "{{") && sharedRE.MatchString(v.Value) {
			uses = true
			break
		}
	}
	if !uses {
		return nil
	}
	lookup, err := d.sharedLookup(ctx, environmentID, serverID)
	if err != nil {
		return err
	}
	for i := range vars {
		value, missing := ExpandShared(vars[i].Value, lookup)
		if len(missing) > 0 {
			return fmt.Errorf("%s uses %s, and there is no %s variable of that name: add it under the %s's shared variables, or write \\%s if %s is meant to hold that text itself",
				vars[i].Key, missing[0], missing[0].Scope, missing[0].Scope, missing[0], vars[i].Key)
		}
		vars[i].Value = value
	}
	return nil
}
