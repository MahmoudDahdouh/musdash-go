// Package update finds out whether a newer release of musdash exists. It
// installs nothing: the server runs as a user that cannot replace its own
// binary, so an upgrade is the install command, run by a person as root.
package update

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"time"
)

// Repo is where releases are published.
const Repo = "https://github.com/MahmoudDahdouh/musdash-go"

// InstallCommand is what upgrades an install, as the README has it.
const InstallCommand = "curl -fsSL " + Repo + "/releases/latest/download/install.sh | sudo sh"

// A release's tag. What GitHub answers is data, and only a tag of this
// form is kept, shown or made part of a link.
var tag = regexp.MustCompile(`^v(\d{1,6})\.(\d{1,6})\.(\d{1,6})$`)

func parse(v string) (n [3]int, ok bool) {
	m := tag.FindStringSubmatch(v)
	if m == nil {
		return n, false
	}
	for i := range n {
		n[i], _ = strconv.Atoi(m[i+1])
	}
	return n, true
}

// Release reports whether v is a release's version. A build from a
// checkout ("dev") is not one.
func Release(v string) bool {
	_, ok := parse(v)
	return ok
}

// Newer reports whether latest is a later release than current. It is
// false when either is not a release's version: a development build is
// never told to upgrade, and a stored value that is not a tag says nothing.
func Newer(latest, current string) bool {
	l, ok := parse(latest)
	c, ok2 := parse(current)
	if !ok || !ok2 {
		return false
	}
	for i := range l {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

// NotesURL is the page of a release, or "" for what is not a release's tag.
func NotesURL(v string) string {
	if !Release(v) {
		return ""
	}
	return Repo + "/releases/tag/" + v
}

// Latest asks GitHub for the tag of the latest release.
func Latest(ctx context.Context) (string, error) {
	return latestAt(ctx, Repo+"/releases/latest")
}

// latestAt reads the tag from where the address of the latest release
// redirects to. That is the address the installer downloads from: it needs
// no API, has no hourly limit for an address, and no body is read.
func latestAt(ctx context.Context, address string) (string, error) {
	client := &http.Client{
		Timeout: 10 * time.Second,
		// Asked once a day: no connection is kept for the next time.
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DisableKeepAlives: true, TLSHandshakeTimeout: 10 * time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, address, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "musdash")
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	res.Body.Close()
	if res.StatusCode < 300 || res.StatusCode > 399 {
		return "", fmt.Errorf("GitHub answered %d", res.StatusCode)
	}
	to, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		return "", errors.New("GitHub's answer names no release")
	}
	v := path.Base(to.Path)
	if !Release(v) {
		return "", errors.New("GitHub's answer names no release")
	}
	return v, nil
}
