package source

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// GitHub is a client for the parts of the GitHub API a GitHub App needs.
type GitHub struct {
	// APIBase is "https://api.github.com"; tests point it at a stand-in.
	APIBase string
	HTTP    *http.Client
}

// NewGitHub returns a client for github.com.
func NewGitHub() *GitHub {
	return &GitHub{APIBase: "https://api.github.com", HTTP: &http.Client{Timeout: 20 * time.Second}}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// AppJWT signs the short-lived token a GitHub App authenticates with:
// RS256 over the app id, valid for nine minutes. The issue time is set a
// minute in the past so a slightly slow clock on this server is tolerated.
func AppJWT(appID int64, privateKeyPEM []byte, now time.Time) (string, error) {
	block, _ := pem.Decode(privateKeyPEM)
	if block == nil {
		return "", errors.New("the GitHub App's private key is not PEM")
	}
	var key *rsa.PrivateKey
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		key = k
	} else if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return "", errors.New("the GitHub App's private key is not an RSA key")
		}
		key = rk
	} else {
		return "", errors.New("the GitHub App's private key cannot be parsed")
	}

	header := b64([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"iat": now.Add(-time.Minute).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": strconv.FormatInt(appID, 10),
	})
	signing := header + "." + b64(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + b64(sig), nil
}

// maxAPIBody bounds how much of a GitHub response is read.
const maxAPIBody = 4 << 20

// call performs one API request and decodes a JSON answer into out. A
// non-2xx answer becomes an error carrying GitHub's own message.
func (g *GitHub) call(ctx context.Context, method, path, bearer string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.APIBase+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "musdash")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := g.HTTP.Do(req)
	if err != nil {
		// The client's error quotes the request's address, and the address
		// of a manifest conversion contains its one-time code. Keep the
		// cause, drop the address.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("GitHub did not answer: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxAPIBody))
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		var apiErr struct {
			Message string `json:"message"`
		}
		json.Unmarshal(raw, &apiErr)
		if apiErr.Message == "" {
			apiErr.Message = http.StatusText(res.StatusCode)
		}
		return &APIError{Status: res.StatusCode, Message: apiErr.Message}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// APIError is a non-2xx answer from GitHub.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("GitHub answered %d: %s", e.Status, e.Message)
}

// ManifestResult is what GitHub returns when an App is created from a
// manifest. Everything secret in it is sealed before it is stored.
type ManifestResult struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	HTMLURL       string `json:"html_url"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	WebhookSecret string `json:"webhook_secret"`
	PEM           string `json:"pem"`
}

// ConvertManifest exchanges the one-time code GitHub redirects back with for
// the new App's credentials.
func (g *GitHub) ConvertManifest(ctx context.Context, code string) (ManifestResult, error) {
	var out ManifestResult
	err := g.call(ctx, http.MethodPost, "/app-manifests/"+url.PathEscape(code)+"/conversions", "", nil, &out)
	if err == nil && (out.ID == 0 || out.PEM == "") {
		err = errors.New("GitHub's answer did not include the App's credentials")
	}
	return out, err
}

// ErrNotInstalled means the App has no access to the repository: it is not
// installed on that account, or the repository was not selected.
var ErrNotInstalled = errors.New("the GitHub App is not installed on this repository")

// InstallationToken returns a one-hour token that can read the repository.
// The installation is looked up from the repository each time, so it is
// right even when the App is installed on several accounts. The token is
// never stored.
func (g *GitHub) InstallationToken(ctx context.Context, appID int64, key []byte, owner, repo string) (string, error) {
	// Scoped to reading the one repository's contents.
	return g.token(ctx, appID, key, owner, repo, map[string]string{"contents": "read"})
}

// token returns a one-hour token for one repository with the given
// permissions and no others.
func (g *GitHub) token(ctx context.Context, appID int64, key []byte, owner, repo string, permissions map[string]string) (string, error) {
	jwt, err := AppJWT(appID, key, time.Now())
	if err != nil {
		return "", err
	}
	var inst struct {
		ID int64 `json:"id"`
	}
	err = g.call(ctx, http.MethodGet, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/installation", jwt, nil, &inst)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return "", ErrNotInstalled
	}
	if err != nil {
		return "", err
	}
	var tok struct {
		Token string `json:"token"`
	}
	body := map[string]any{"repositories": []string{repo}, "permissions": permissions}
	if err := g.call(ctx, http.MethodPost, "/app/installations/"+strconv.FormatInt(inst.ID, 10)+"/access_tokens", jwt, body, &tok); err != nil {
		return "", err
	}
	if tok.Token == "" {
		return "", errors.New("GitHub returned an empty installation token")
	}
	return tok.Token, nil
}

// maxComment bounds a comment's text.
const maxComment = 4000

// CommentOnPullRequest writes a comment on a pull request as the App, or
// rewrites the comment with the given id when there is one. It returns the
// comment's id.
//
// It needs the App to have the "Pull requests: write" permission. An App
// without it gets an error here and nothing else changes: the comment is a
// courtesy, not part of a deployment.
func (g *GitHub) CommentOnPullRequest(ctx context.Context, appID int64, key []byte, owner, repo string, number int, commentID int64, text string) (int64, error) {
	if number <= 0 {
		return 0, errors.New("not a pull request number")
	}
	if len(text) > maxComment {
		text = text[:maxComment]
	}
	// Scoped to the one repository and to writing on its pull requests.
	token, err := g.token(ctx, appID, key, owner, repo, map[string]string{"pull_requests": "write"})
	if err != nil {
		return 0, err
	}
	base := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/issues/"
	body := map[string]string{"body": text}
	var out struct {
		ID int64 `json:"id"`
	}
	if commentID > 0 {
		err := g.call(ctx, http.MethodPatch, base+"comments/"+strconv.FormatInt(commentID, 10), token, body, &out)
		var apiErr *APIError
		if err == nil {
			return commentID, nil
		}
		// Somebody deleted the comment: write a new one.
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
			return 0, err
		}
	}
	if err := g.call(ctx, http.MethodPost, base+strconv.Itoa(number)+"/comments", token, body, &out); err != nil {
		return 0, err
	}
	return out.ID, nil
}

// RepoInfo is one repository the App can read.
type RepoInfo struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}

// maxRepos bounds the repository picker; beyond it a person types the name.
const maxRepos = 300

// Repositories lists the repositories the App is installed on, across all
// its installations, up to maxRepos.
func (g *GitHub) Repositories(ctx context.Context, appID int64, key []byte) ([]RepoInfo, error) {
	jwt, err := AppJWT(appID, key, time.Now())
	if err != nil {
		return nil, err
	}
	var installs []struct {
		ID int64 `json:"id"`
	}
	if err := g.call(ctx, http.MethodGet, "/app/installations?per_page=100", jwt, nil, &installs); err != nil {
		return nil, err
	}
	var all []RepoInfo
	for _, inst := range installs {
		var tok struct {
			Token string `json:"token"`
		}
		if err := g.call(ctx, http.MethodPost, "/app/installations/"+strconv.FormatInt(inst.ID, 10)+"/access_tokens", jwt,
			map[string]any{"permissions": map[string]string{"metadata": "read"}}, &tok); err != nil {
			return nil, err
		}
		for page := 1; len(all) < maxRepos; page++ {
			var out struct {
				Repositories []RepoInfo `json:"repositories"`
			}
			if err := g.call(ctx, http.MethodGet, "/installation/repositories?per_page=100&page="+strconv.Itoa(page), tok.Token, nil, &out); err != nil {
				return nil, err
			}
			all = append(all, out.Repositories...)
			if len(out.Repositories) < 100 {
				break
			}
		}
	}
	if len(all) > maxRepos {
		all = all[:maxRepos]
	}
	return all, nil
}

// BasicAuthHeader is the HTTP header git sends to clone with an installation
// token: "Authorization: Basic base64(x-access-token:<token>)".
func BasicAuthHeader(token string) string {
	return "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))
}
