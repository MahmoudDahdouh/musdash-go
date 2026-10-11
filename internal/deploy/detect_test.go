package deploy

import "testing"

func TestGuessPack(t *testing.T) {
	for _, c := range []struct {
		files       []string
		pack, found string
	}{
		{[]string{"README.md", "Dockerfile", "package.json", "nixpacks.toml"}, PackDockerfile, "Dockerfile"},
		{[]string{"package.json", "index.html"}, PackRailpack, "package.json"},
		{[]string{"main.go", "go.mod", "go.sum"}, PackRailpack, "go.mod"},
		{[]string{"railpack.json", "nixpacks.toml", "package.json"}, PackRailpack, "railpack.json"},
		// Written for Nixpacks: it stays with Nixpacks.
		{[]string{"nixpacks.toml", "package.json"}, PackNixpacks, "nixpacks.toml"},
		{[]string{"index.html", "style.css"}, PackStatic, "index.html"},
		// A name is compared as it is: this is not a Dockerfile.
		{[]string{"dockerfile.txt", "README.md"}, "", ""},
		{nil, "", ""},
	} {
		pack, found := GuessPack(c.files)
		if pack != c.pack || found != c.found {
			t.Errorf("GuessPack(%v) = %q, %q; want %q, %q", c.files, pack, found, c.pack, c.found)
		}
		if pack != "" && !ValidPack(pack) {
			t.Errorf("GuessPack(%v) names %q, which is no pack", c.files, pack)
		}
	}
}

func TestGuessStack(t *testing.T) {
	for _, tc := range []struct {
		files []string
		want  string
	}{
		{nil, ""},
		{[]string{"README.md", "package.json"}, ""},
		{[]string{"docker-compose.yml"}, "docker-compose.yml"},
		{[]string{"docker-compose.yaml", "README.md"}, "docker-compose.yaml"},
		{[]string{"compose.yml"}, "compose.yml"},
		// Compose's own order, whatever the listing's.
		{[]string{"docker-compose.yml", "compose.yaml", "compose.yml"}, "compose.yaml"},
		// What builds the stack's images does not make it an app.
		{[]string{"package.json", "compose.yaml"}, "compose.yaml"},
		// A Dockerfile, or a builder's own file, is an app's: the Compose
		// file beside it is usually for the developer's machine.
		{[]string{"Dockerfile", "docker-compose.yml"}, ""},
		{[]string{"railpack.json", "compose.yaml"}, ""},
		{[]string{"nixpacks.toml", "compose.yaml"}, ""},
		// A name that only looks like one.
		{[]string{"docker-compose.override.yml", "compose.yaml.example"}, ""},
	} {
		if got := GuessStack(tc.files); got != tc.want {
			t.Errorf("GuessStack(%v) = %q, want %q", tc.files, got, tc.want)
		}
	}
}
