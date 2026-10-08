package deploy

// railpackFiles are the files that say a folder holds code in a language
// Railpack builds. It is the builder chosen for them: the newer of the two,
// and the faster. Only what it is known to build is here, since a folder
// it does not know fails the build.
var railpackFiles = map[string]bool{
	"package.json": true, "deno.json": true,
	"go.mod":           true,
	"requirements.txt": true, "pyproject.toml": true, "Pipfile": true,
	"Gemfile":       true,
	"composer.json": true,
	"Cargo.toml":    true,
	"pom.xml":       true, "build.gradle": true, "build.gradle.kts": true,
	"mix.exs": true,
}

// GuessPack works out how a folder of a repository is built from the names
// of the files in it, and names the file that decided. Both are "" when
// nothing there says.
//
// A Dockerfile is the repository's own word on how it is built, so it wins
// over everything. A builder's own settings file says which builder the
// repository was written for. A folder with only pages in it is served as
// it is.
func GuessPack(files []string) (pack, found string) {
	has := make(map[string]bool, len(files))
	for _, name := range files {
		has[name] = true
	}
	switch {
	case has["Dockerfile"]:
		return PackDockerfile, "Dockerfile"
	case has["railpack.json"]:
		return PackRailpack, "railpack.json"
	case has["nixpacks.toml"]:
		return PackNixpacks, "nixpacks.toml"
	}
	// In the order of the list, so the answer is the same every time.
	for _, name := range files {
		if railpackFiles[name] {
			return PackRailpack, name
		}
	}
	if has["index.html"] {
		return PackStatic, "index.html"
	}
	return "", ""
}
