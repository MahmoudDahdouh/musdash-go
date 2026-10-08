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
