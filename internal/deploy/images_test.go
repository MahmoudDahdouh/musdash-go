package deploy

import (
	"regexp"
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/backup"
	"github.com/MahmoudDahdouh/musdash-go/internal/catalog"
	"github.com/MahmoudDahdouh/musdash-go/internal/compose"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
)

// docker.ValidImage is asked at every deployment, so a name it refuses is
// something that stops deploying. Every image musdash itself names has to
// pass it: the engines, the service templates, the builders, the tools,
// and the names it gives to what it builds and keeps.
func TestEveryImageMusdashNamesIsValid(t *testing.T) {
	names := map[string]string{
		"rclone":               backup.RcloneImage,
		"the Compose sandbox":  compose.SandboxImage("29.8.0"),
		"the sandbox, unknown": compose.SandboxImage(""),
		"Railpack's frontend":  railpackFrontend(),
		"a built image":        ImageRepository("abcdefghijkl") + ":0123456789ab",
		"a kept image":         ImageRepository("abcdefghijkl") + ":d-mnopqrstuvwx",
		"a stack's image":      ServiceProject("abcdefghijkl") + "-my_app",
	}
	for name, b := range builders {
		names["the builder "+name] = b.image()
	}
	for _, tpl := range catalog.Databases() {
		names["the engine "+tpl.Engine] = tpl.Image
	}
	imageLine := regexp.MustCompile(`(?m)^\s*image:\s*["']?([^"'\s#]+)`)
	imageDefault := regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*:?-([^${}]*)\}`)
	for _, listed := range catalog.Services() {
		// The list holds the headers only; the Compose text is read by key.
		tpl, ok := catalog.Service(listed.Key)
		if !ok {
			t.Fatalf("the template %s is listed and cannot be read", listed.Key)
		}
		found := imageLine.FindAllStringSubmatch(tpl.Compose, -1)
		if len(found) == 0 {
			t.Errorf("no image found in the template %s", tpl.Key)
		}
		for _, m := range found {
			// A name can hold variables, which Compose fills in. What can be
			// read here is the name its defaults make; one with a variable
			// that has no default is whatever the person types.
			image := imageDefault.ReplaceAllString(m[1], "$1")
			if strings.Contains(image, "$") {
				continue
			}
			names["the template "+tpl.Key+" ("+m[1]+")"] = image
		}
	}
	if len(names) < 20 {
		t.Fatalf("only %d images were found", len(names))
	}
	for what, image := range names {
		if !docker.ValidImage(image) {
			t.Errorf("%s: %q is refused", what, image)
		}
	}
}
