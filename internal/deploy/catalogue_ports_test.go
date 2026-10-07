package deploy

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/catalog"
)

// A template may publish the ports the proxy cannot carry (SSH, a game's).
// Each has to be one a deployment lets a stack publish, or the template
// could not be deployed as it is listed: not under 1024, and not in the
// range musdash gives out on loopback. A port that is a variable is held
// to its default, which is what a deployment gets.
func TestCatalogueTemplatesPublishPortsAStackMay(t *testing.T) {
	entry := regexp.MustCompile(`(?m)^\s+- "(?:(\d+)(?:-(\d+))?|\$\{[A-Z][A-Z0-9_]*:-(\d+)\}):[0-9-]+(/udp)?"$`)
	found := 0
	for _, listed := range catalog.Services() {
		tpl, ok := catalog.Service(listed.Key)
		if !ok {
			t.Fatalf("the template %s is listed and cannot be read", listed.Key)
		}
		seen := map[string]bool{}
		for _, block := range strings.Split(tpl.Compose, "\n    ports:\n")[1:] {
			for _, line := range strings.Split(block, "\n") {
				m := entry.FindStringSubmatch(line)
				if m == nil {
					break
				}
				found++
				first, last := m[1], m[2]
				if first == "" {
					first = m[3]
				}
				if last == "" {
					last = first
				}
				lo, _ := strconv.Atoi(first)
				hi, _ := strconv.Atoi(last)
				// Its ends, and that the range does not span musdash's own.
				if !ValidPublicPort(lo) || !ValidPublicPort(hi) || hi < lo || (lo < portMin && hi > portMax) {
					t.Errorf("%s: publishes %s, which a stack may not", tpl.Key, strings.TrimSpace(line))
				}
				// One port of the server goes to one container.
				at := first + "-" + last + m[4]
				if seen[at] {
					t.Errorf("%s: publishes the port %s of the server twice", tpl.Key, first)
				}
				seen[at] = true
			}
		}
	}
	if found < 40 {
		t.Fatalf("only %d published ports were read: the test no longer sees them", found)
	}
}
