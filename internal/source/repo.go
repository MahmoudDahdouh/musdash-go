// Package source talks to Git hosts: it validates repository addresses,
// authenticates as a GitHub App, generates deploy keys and verifies push
// webhooks.
package source

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// Repo is a parsed repository address.
type Repo struct {
	URL   string // the address to hand to git, normalised
	Host  string // "github.com"
	Owner string // "acme"; may contain slashes on hosts with nested groups
	Name  string // "shop", without ".git"
	SSH   bool   // cloned over SSH, so it needs a deploy key
}

// FullName is "owner/name", the form GitHub uses in API paths and webhooks.
func (r Repo) FullName() string { return r.Owner + "/" + r.Name }

var (
	// scpRE matches git's scp-like form: git@github.com:acme/shop.git
	scpRE     = regexp.MustCompile(`^([A-Za-z0-9_][A-Za-z0-9_.-]*)@([A-Za-z0-9][A-Za-z0-9.-]*):([A-Za-z0-9_.~/-]+)$`)
	pathRE    = regexp.MustCompile(`^[A-Za-z0-9_.~/-]+$`)
	hostRE    = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?$`)
	branchRE  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*$`)
	errBadURL = errors.New("enter a repository address like https://github.com/acme/shop or git@github.com:acme/shop.git")
)

// ParseRepo validates a repository address and splits it into its parts.
//
// Only HTTPS and SSH are accepted. git itself understands more: "ext::"
// runs an arbitrary command, "file://" reads the server's own disk, and an
// address starting with "-" is read as an option. All of those are refused
// here, and callers still pass the address after "--".
func ParseRepo(raw string) (Repo, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 512 || strings.HasPrefix(raw, "-") || strings.ContainsAny(raw, " \t\r\n\\'\"`$;|&<>(){}*?!") {
		return Repo{}, errBadURL
	}

	if m := scpRE.FindStringSubmatch(raw); m != nil {
		return finish(Repo{URL: raw, Host: strings.ToLower(m[2]), SSH: true}, m[3])
	}

	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || !hostRE.MatchString(u.Host) || u.RawQuery != "" || u.Fragment != "" {
		return Repo{}, errBadURL
	}
	switch u.Scheme {
	case "https":
		// Credentials belong in a GitHub App or a deploy key, where they are
		// stored sealed; in the URL they would be shown and logged.
		if u.User != nil {
			return Repo{}, errors.New("leave the user name and password out of the address; connect a GitHub App or a deploy key instead")
		}
	case "ssh":
		if u.User != nil {
			if _, hasPassword := u.User.Password(); hasPassword {
				return Repo{}, errBadURL
			}
		}
	default:
		return Repo{}, errBadURL
	}
	return finish(Repo{URL: raw, Host: strings.ToLower(u.Hostname()), SSH: u.Scheme == "ssh"}, u.Path)
}

// finish splits "owner/name.git" and checks it.
func finish(r Repo, path string) (Repo, error) {
	if strings.Contains(path, "//") || strings.Contains(path, "..") {
		return Repo{}, errBadURL
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if !pathRE.MatchString(path) {
		return Repo{}, errBadURL
	}
	i := strings.LastIndex(path, "/")
	if i <= 0 || i == len(path)-1 {
		return Repo{}, errBadURL
	}
	r.Owner, r.Name = path[:i], path[i+1:]
	return r, nil
}

// ValidBranch reports whether name is safe to pass to git as a branch. It
// follows git's ref rules closely enough for real branch names and cannot
// match anything starting with "-".
func ValidBranch(name string) bool {
	if len(name) == 0 || len(name) > 200 || !branchRE.MatchString(name) {
		return false
	}
	return !strings.Contains(name, "..") && !strings.Contains(name, "//") &&
		!strings.HasSuffix(name, "/") && !strings.HasSuffix(name, ".") && !strings.HasSuffix(name, ".lock")
}

// ValidRelPath reports whether p is a relative path inside the repository:
// no leading slash, no "..", no option-like start. Empty means the root.
func ValidRelPath(p string) bool {
	if p == "" || p == "." {
		return true
	}
	if len(p) > 300 || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "-") || !pathRE.MatchString(p) {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || part == "" {
			return false
		}
	}
	return true
}
