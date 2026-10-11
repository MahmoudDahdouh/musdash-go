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

// ComposeFiles are the names Compose itself looks for in a folder, in the
// order it takes them.
var ComposeFiles = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// StandardComposeFile reports whether a path is one of those names, in the
// folder itself: what a form's field holds before a person names a file of
// their own.
func StandardComposeFile(name string) bool {
	for _, known := range ComposeFiles {
		if name == known {
			return true
		}
	}
	return false
}

// GuessStack names the Compose file of a folder that holds a stack: it has
// one, and nothing that says how an app is built. A Dockerfile, or a
// builder's own settings file, beside a Compose file is the usual shape of
// an app whose Compose file is for the developer's machine, so there the
// answer is "" and GuessPack's stands.
func GuessStack(files []string) string {
	has := make(map[string]bool, len(files))
	for _, name := range files {
		has[name] = true
	}
	if has["Dockerfile"] || has["railpack.json"] || has["nixpacks.toml"] {
		return ""
	}
	for _, name := range ComposeFiles {
		if has[name] {
			return name
		}
	}
	return ""
}
