package servers

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
)

var dataDirRE = regexp.MustCompile(`^/[A-Za-z0-9._/-]{1,200}$`)

// neverOurs are trees of a server that musdash's files do not go into:
// the system's own, what is emptied without asking, and Docker's.
var neverOurs = []string{
	"/etc", "/proc", "/sys", "/dev", "/boot", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32", "/usr", "/snap", "/nix",
	"/run", "/var/run", "/var/lock", "/var/empty", "/tmp", "/var/tmp", "/var/lib/docker", "/var/lib/containerd",
}

// shared are directories that hold other things than musdash's. A
// directory inside one is fine; the directory itself is not.
var shared = map[string]bool{
	"/var": true, "/var/lib": true, "/var/log": true, "/var/mail": true, "/var/spool": true, "/var/cache": true,
	"/home": true, "/root": true, "/opt": true, "/srv": true, "/mnt": true, "/media": true,
}

// Problems with a data directory, for DataDirProblem.
const (
	DataDirShape  = "shape"  // not a full, plain path
	DataDirShared = "shared" // the system's, or shared with other things
)

// DataDirProblem says what is wrong with a path as a remote server's data
// directory, or "" when nothing is.
//
// musdash makes directories there, keeps secrets in them, and on the first
// connection of a process empties the one named work. So the directory has
// to be musdash's own: not /etc, and not a home directory either. The list
// is for what a person might type; what really keeps musdash out of a
// directory that is not its own is the look at it (dataDirTaken).
func DataDirProblem(dir string) string {
	if !dataDirRE.MatchString(dir) || path.Clean(dir) != dir {
		return DataDirShape
	}
	for _, tree := range neverOurs {
		if dir == tree || strings.HasPrefix(dir, tree+"/") {
			return DataDirShared
		}
	}
	if shared[dir] {
		return DataDirShared
	}
	// A home directory itself: /home/<name>.
	if rest, ok := strings.CutPrefix(dir, "/home/"); ok && !strings.Contains(rest, "/") {
		return DataDirShared
	}
	for _, part := range strings.Split(dir, "/") {
		if part == ".ssh" {
			return DataDirShared
		}
	}
	return ""
}

// ownedMark is a file a check leaves in a data directory: what says that
// the directory is musdash's. Names such as apps or work say nothing;
// other people have directories called that.
const ownedMark = ".owned-by-musdash"

const ownedMarkText = "musdash keeps its files in this directory, and clears out parts of it by itself.\nDo not keep anything else here.\n"

// oursFunc is shell text that defines ours DIR: whether a directory is
// musdash's. It is, with the mark in it; and without, when it has every
// directory a check makes, which is a server checked before there was a
// mark.
const oursFunc = `ours() { [ -f "$1/` + ownedMark + `" ] || { [ -d "$1/apps" ] && [ -d "$1/work" ] && [ -d "$1/backups" ] && [ -d "$1/proxy" ] && [ -d "$1/bin" ]; }; }
`

// dataDirTaken reports whether a server's data directory is somebody
// else's: it is there, and it is neither musdash's nor empty. Nothing may
// be made in such a directory, and nothing swept. A directory that cannot
// be looked into counts as taken.
//
// The answer is the server's, so it is one of four words or it is an
// error: an answer that is not understood must not be read as "go on".
func dataDirTaken(ctx context.Context, r runner.Runner, dir string) (bool, error) {
	const look = oursFunc + `if [ ! -e "$1" ]; then echo new
elif [ ! -d "$1" ]; then echo taken
elif ours "$1"; then echo ours
elif ! list=$(ls -A -- "$1" 2>/dev/null); then echo taken
elif [ -z "$list" ]; then echo empty
else echo taken; fi`
	out, err := r.Output(ctx, runner.Cmd{Name: "sh", Args: []string{"-c", look, "sh", dir}})
	if err != nil {
		return false, fmt.Errorf("look at the data directory %s: %w", dir, err)
	}
	switch strings.TrimSpace(string(out)) {
	case "taken":
		return true, nil
	case "new", "ours", "empty":
		return false, nil
	}
	return false, fmt.Errorf("look at the data directory %s: the server's answer was not understood", dir)
}
