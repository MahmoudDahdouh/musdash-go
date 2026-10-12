package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		latest, current string
		want            bool
	}{
		{"v0.1.3", "v0.1.2", true},
		{"v0.2.0", "v0.1.9", true},
		{"v1.0.0", "v0.9.9", true},
		{"v0.1.10", "v0.1.9", true}, // numbers, not text
		{"v0.1.2", "v0.1.2", false},
		{"v0.1.1", "v0.1.2", false},
		{"v0.1.3", "dev", false},
		{"", "v0.1.2", false},
		{"v0.1.3-rc1", "v0.1.2", false},
		{"0.1.3", "v0.1.2", false},
		{"v0.1.3\n", "v0.1.2", false},
	} {
		if got := Newer(c.latest, c.current); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.latest, c.current, got)
		}
	}
}

func TestNotesURL(t *testing.T) {
	if got := NotesURL("v0.1.3"); got != Repo+"/releases/tag/v0.1.3" {
		t.Errorf("NotesURL = %q", got)
	}
	if got := NotesURL("v1/../../evil"); got != "" {
		t.Errorf("a link was made of what is not a tag: %q", got)
	}
}

func TestLatestReadsTheRedirect(t *testing.T) {
	for _, c := range []struct {
		name, location string
		status         int
		want           string
	}{
		{"a release", "https://github.com/o/r/releases/tag/v0.1.3", http.StatusFound, "v0.1.3"},
		{"no release yet", "https://github.com/o/r/releases", http.StatusFound, ""},
		{"not a tag", "https://github.com/o/r/releases/tag/<script>", http.StatusFound, ""},
		{"no redirect", "", http.StatusOK, ""},
		{"an error", "", http.StatusInternalServerError, ""},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodHead {
				t.Errorf("%s: method %s", c.name, r.Method)
			}
			if c.location != "" {
				w.Header().Set("Location", c.location)
			}
			w.WriteHeader(c.status)
		}))
		got, err := latestAt(context.Background(), srv.URL)
		srv.Close()
		if got != c.want || (err == nil) != (c.want != "") {
			t.Errorf("%s: %q, %v", c.name, got, err)
		}
	}
}
