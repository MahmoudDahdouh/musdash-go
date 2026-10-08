package deploy

// builderFiles are the files that say a folder holds code a builder knows
// how to build: one per language or tool Nixpacks and Railpack both read.
var builderFiles = map[string]bool{
	"package.json": true, "deno.json": true, "bun.lockb": true,
	"go.mod":           true,
	"requirements.txt": true, "pyproject.toml": true, "Pipfile": true, "setup.py": true,
	"Gemfile":       true,
	"composer.json": true,
	"Cargo.toml":    true,
	"pom.xml":       true, "build.gradle": true, "build.gradle.kts": true,
	"mix.exs":       true,
	"nixpacks.toml": true,
}

// GuessPack works out how a folder of a repository is built from the names
// of the files in it, and names the file that decided. Both are "" when
// nothing there says.
//
// A Dockerfile is the repository's own word on how it is built, so it wins
// over everything. A folder with only pages in it is served as it is.
func GuessPack(files []string) (pack, found string) {
	has := make(map[string]bool, len(files))
	for _, name := range files {
		has[name] = true
	}
	if has["Dockerfile"] {
		return PackDockerfile, "Dockerfile"
	}
	if has["railpack.json"] {
		return PackRailpack, "railpack.json"
	}
	// In the order of the list, so the answer is the same every time.
	for _, name := range files {
		if builderFiles[name] {
			return PackNixpacks, name
		}
	}
	if has["index.html"] {
		return PackStatic, "index.html"
	}
	return "", ""
}
