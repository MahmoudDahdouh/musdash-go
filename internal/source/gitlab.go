package source

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/netguard"
)

// GitLab is a client for the two things musdash asks a GitLab instance:
// whose access token this is, and which projects it can read. The instance
// is whichever one a source names, so every call takes its address.
type GitLab struct {
	HTTP *http.Client
}

// NewGitLab returns a client for instances on the network. The address of
// an instance is typed by a person, so the client connects only where
// netguard allows: it must not become a way to reach the server's own
// services or a container's private port.
func NewGitLab() *GitLab {
	dial := net.Dialer{Timeout: 10 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return netguard.ErrForbidden
		}
		return netguard.Policy{}.Check(ap.Addr(), int(ap.Port()))
	}}
	return &GitLab{HTTP: &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			// No proxy from the environment: the check above is on the
			// address that is really connected to.
			Proxy:                 nil,
			DialContext:           dial.DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
			MaxIdleConns:          2,
			IdleConnTimeout:       30 * time.Second,
		},
	}}
}

var (
	// A GitLab user name: letters, numbers, "_", "." and "-".
	gitlabUserRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,254}$`)
	// A GitLab access token as it is typed: nothing that could end a
	// header or start an option.
	gitlabTokenRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._=-]{7,254}$`)
)

// ValidGitLabToken reports whether token is written as an access token.
func ValidGitLabToken(token string) bool { return gitlabTokenRE.MatchString(token) }

// GitLabBase checks the address of a GitLab instance as a person types it
// and returns it as "https://host[:port]". Only HTTPS: the token travels
// with every request. Nothing may follow the host.
func GitLabBase(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || u.Scheme != "https" || !hostRE.MatchString(u.Host) || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", false
	}
	return "https://" + strings.ToLower(u.Host), true
}

// GitLabHost is the host name of an instance's address, in the form a
// Repo's Host has: lower case, without a port.
func GitLabHost(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// GitLabAuthHeader is the HTTP header git sends to clone with an access
// token: GitLab takes the token as the password of any user name.
func GitLabAuthHeader(token string) string {
	return "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("oauth2:"+token))
}

// call asks the instance one question and decodes its JSON answer into out.
func (g *GitLab) call(ctx context.Context, base, token, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return errors.New("the GitLab address cannot be used")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "musdash")
	req.Header.Set("Authorization", "Bearer "+token)
	// A redirect is not followed, whatever the client is set to: it would
	// carry the token to wherever the instance pointed.
	client := *g.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		// The client's error quotes the address; the cause is enough.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("GitLab did not answer: %w", err)
	}
	defer res.Body.Close()
	body := io.LimitReader(res.Body, maxAPIBody)
	switch {
	case res.StatusCode == http.StatusUnauthorized:
		return errors.New("GitLab did not accept the token: it is wrong, expired or revoked")
	case res.StatusCode == http.StatusForbidden:
		return errors.New("GitLab refused the token: it needs the read_api scope")
	case res.StatusCode < 200 || res.StatusCode > 299:
		// The answer's own words are not passed on: at an address that is
		// not a GitLab they could be anything.
		return &gitlabStatus{res.StatusCode}
	}
	if err := json.NewDecoder(body).Decode(out); err != nil {
		return errors.New("GitLab's answer could not be read: is this the address of a GitLab instance?")
	}
	return nil
}

// User returns the name of the user a token acts as, which also proves the
// token works. The name is the instance's to choose: one that is not
// written as a user name comes back empty.
func (g *GitLab) User(ctx context.Context, base, token string) (string, error) {
	var out struct {
		Username string `json:"username"`
	}
	if err := g.call(ctx, base, token, "/api/v4/user", &out); err != nil {
		return "", err
	}
	if !gitlabUserRE.MatchString(out.Username) {
		return "", nil
	}
	return out.Username, nil
}

// Projects lists the projects a token is a member of, the most recently
// active first, up to maxRepos. A project's path becomes part of an
// address, so one that is not written as a path is left out.
func (g *GitLab) Projects(ctx context.Context, base, token string) ([]RepoInfo, error) {
	const perPage = 100
	var all []RepoInfo
	for page := 1; len(all) < maxRepos; page++ {
		var out []struct {
			Path          string `json:"path_with_namespace"`
			DefaultBranch string `json:"default_branch"`
			Visibility    string `json:"visibility"`
		}
		if err := g.call(ctx, base, token, "/api/v4/projects?membership=true&simple=true&archived=false&order_by=last_activity_at&per_page="+
			strconv.Itoa(perPage)+"&page="+strconv.Itoa(page), &out); err != nil {
			return nil, err
		}
		for _, p := range out {
			if len(p.Path) > 255 || !pathRE.MatchString(p.Path) || !strings.Contains(p.Path, "/") || strings.Contains(p.Path, "..") || strings.Contains(p.Path, "//") {
				continue
			}
			branch := p.DefaultBranch
			if !ValidBranch(branch) {
				branch = ""
			}
			all = append(all, RepoInfo{FullName: p.Path, DefaultBranch: branch, Private: p.Visibility != "public"})
		}
		if len(out) < perPage {
			break
		}
	}
	if len(all) > maxRepos {
		all = all[:maxRepos]
	}
	return all, nil
}

// gitlabStatus is an answer that is neither a success nor about the token.
type gitlabStatus struct{ status int }

func (e *gitlabStatus) Error() string {
	return fmt.Sprintf("GitLab answered %d %s: is this the address of a GitLab instance?", e.status, http.StatusText(e.status))
}

// gitlabNotThere turns a 404 about a project into ErrNotThere: asked for a
// branch or a folder of a project, that is what it means.
func gitlabNotThere(err error) error {
	var st *gitlabStatus
	if errors.As(err, &st) && st.status == http.StatusNotFound {
		return ErrNotThere
	}
	return err
}

// Branches lists a project's branches, up to maxBranches. project is its
// path with the namespace, such as group/shop.
func (g *GitLab) Branches(ctx context.Context, base, token, project string) ([]string, error) {
	const perPage = 100
	var all []string
	for page := 1; len(all) < maxBranches; page++ {
		var out []struct {
			Name string `json:"name"`
		}
		if err := g.call(ctx, base, token, "/api/v4/projects/"+url.PathEscape(project)+"/repository/branches?per_page="+
			strconv.Itoa(perPage)+"&page="+strconv.Itoa(page), &out); err != nil {
			return nil, gitlabNotThere(err)
		}
		for _, b := range out {
			if ValidBranch(b.Name) {
				all = append(all, b.Name)
			}
		}
		if len(out) < perPage {
			break
		}
	}
	if len(all) > maxBranches {
		all = all[:maxBranches]
	}
	return all, nil
}

// maxTreePages bounds how much of one folder's listing is asked for.
const maxTreePages = 3

// Files lists the names of the files in one folder of a project at a
// branch; dir "" is the root. ErrNotThere when the branch has no such
// folder.
func (g *GitLab) Files(ctx context.Context, base, token, project, branch, dir string) ([]string, error) {
	if !ValidBranch(branch) || !ValidRelPath(dir) {
		return nil, ErrNotThere
	}
	const perPage = 100
	query := "?ref=" + url.QueryEscape(branch) + "&per_page=" + strconv.Itoa(perPage)
	if dir = strings.Trim(dir, "/"); dir != "" && dir != "." {
		query += "&path=" + url.QueryEscape(dir)
	}
	var names []string
	for page := 1; page <= maxTreePages; page++ {
		var out []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		}
		if err := g.call(ctx, base, token, "/api/v4/projects/"+url.PathEscape(project)+"/repository/tree"+query+"&page="+strconv.Itoa(page), &out); err != nil {
			return nil, gitlabNotThere(err)
		}
		for _, f := range out {
			if f.Type == "blob" {
				names = append(names, f.Name)
			}
		}
		if len(out) < perPage {
			break
		}
	}
	return names, nil
}
