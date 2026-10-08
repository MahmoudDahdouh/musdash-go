package test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// The installer is a shell script that runs as root on somebody's server,
// so it is tested as what it is: its functions are called from a real shell
// that sourced it, and whole installs run in a container.
const installScript = "../install/install.sh"

// shells are the shells a server may have as sh: dash on Debian and Ubuntu,
// bash elsewhere (and on a Mac, where sh is an old bash).
func shells(t *testing.T) []string {
	t.Helper()
	out := []string{"sh"}
	for _, s := range []string{"dash", "bash"} {
		if _, err := exec.LookPath(s); err == nil {
			out = append(out, s)
		}
	}
	return out
}

// sourced runs a snippet in a shell that read the installer without
// running it. The environment is the given one and a PATH, so that the
// machine's locale decides nothing.
func sourced(t *testing.T, shell, snippet string, env ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(shell, "-c", `. "$0"; `+snippet, installScript)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "MUSDASH_INSTALL_LIB=1"}, env...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("%s: %v", shell, err)
		}
		code = exit.ExitCode()
	}
	return string(out), code
}

func TestInstallScriptParses(t *testing.T) {
	for _, sh := range shells(t) {
		if out, err := exec.Command(sh, "-n", installScript).CombinedOutput(); err != nil {
			t.Errorf("%s -n: %v\n%s", sh, err, out)
		}
	}
}

// Sourced as a library, the script must do nothing: that is what the test
// of every function below relies on, and what a half-downloaded copy does.
func TestInstallScriptDoesNothingUntilItsLastLine(t *testing.T) {
	for _, sh := range shells(t) {
		if out, code := sourced(t, sh, "echo read"); out != "read\n" || code != 0 {
			t.Errorf("%s: %q, exit %d", sh, out, code)
		}
	}
	body, err := os.ReadFile(installScript)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if last := lines[len(lines)-1]; !strings.HasSuffix(last, `main "$@"`) {
		t.Errorf("the last line must be the one that calls main, so that a cut download runs nothing: %q", last)
	}
}

func TestInstallArch(t *testing.T) {
	want := map[string]string{"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}
	for _, sh := range shells(t) {
		for in, arch := range want {
			out, code := sourced(t, sh, `arch_of "$M"`, "M="+in)
			if code != 0 || strings.TrimSpace(out) != arch {
				t.Errorf("%s: arch_of %s = %q, exit %d; want %s", sh, in, out, code, arch)
			}
		}
		for _, in := range []string{"riscv64", "armv7l", "i686", ""} {
			if out, code := sourced(t, sh, `arch_of "$M"`, "M="+in); code == 0 {
				t.Errorf("%s: arch_of %q was accepted: %q", sh, in, out)
			}
		}
	}
}

func TestInstallVersion(t *testing.T) {
	good := []string{"v0.1.0", "v10.20.30"}
	bad := []string{"0.1.0", "v1.2", "v1.2.3-rc1", "v1.2.3/../x", "latest", "v1.2.3 ", " v1.2.3", "v1.2.3\nv", "-n", ""}
	for _, sh := range shells(t) {
		for _, v := range good {
			if _, code := sourced(t, sh, `valid_version "$V"`, "V="+v); code != 0 {
				t.Errorf("%s: %q was refused", sh, v)
			}
		}
		for _, v := range bad {
			if _, code := sourced(t, sh, `valid_version "$V"`, "V="+v); code == 0 {
				t.Errorf("%s: %q was accepted, and a version becomes part of a URL", sh, v)
			}
		}
	}
}

func TestInstallReleaseURL(t *testing.T) {
	for _, sh := range shells(t) {
		for _, c := range []struct{ set, want string }{
			{"WANTED=; BASE=", "https://github.com/MahmoudDahdouh/musdash-go/releases/latest/download/checksums.txt"},
			{"WANTED=v1.2.3; BASE=", "https://github.com/MahmoudDahdouh/musdash-go/releases/download/v1.2.3/checksums.txt"},
			{"WANTED=v1.2.3; BASE=file:///rel", "file:///rel/checksums.txt"},
		} {
			if out, _ := sourced(t, sh, c.set+"; release_url checksums.txt"); strings.TrimSpace(out) != c.want {
				t.Errorf("%s: %s gives %q, want %q", sh, c.set, out, c.want)
			}
		}
	}
}

func TestInstallChecksumFor(t *testing.T) {
	file := filepath.Join(t.TempDir(), "checksums.txt")
	sums := "aaa  xmusdash-linux-amd64\nbbb  musdash-linux-amd64\nccc *musdash-linux-arm64\n"
	if err := os.WriteFile(file, []byte(sums), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, sh := range shells(t) {
		for name, want := range map[string]string{
			"musdash-linux-amd64": "bbb", // not the longer name that ends the same way
			"musdash-linux-arm64": "ccc", // sha256sum's mark of a binary file
			"musdash-linux-riscv": "",
		} {
			out, _ := sourced(t, sh, `checksum_for "$N" "$F"`, "N="+name, "F="+file)
			if strings.TrimSpace(out) != want {
				t.Errorf("%s: checksum_for %s = %q, want %q", sh, name, out, want)
			}
		}
	}
}

func TestInstallSHA256(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, []byte("musdash\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("musdash\n"))
	for _, sh := range shells(t) {
		if out, _ := sourced(t, sh, `sha256_of "$F"`, "F="+file); strings.TrimSpace(out) != hex.EncodeToString(sum[:]) {
			t.Errorf("%s: sha256_of = %q", sh, out)
		}
	}
}

func TestInstallAddresses(t *testing.T) {
	private := []string{"10.0.0.1", "172.16.0.1", "172.31.255.255", "192.168.1.1", "100.64.0.1", "100.127.255.255", "127.0.0.1", "169.254.1.1"}
	public := []string{"172.32.0.1", "172.15.0.1", "100.128.0.1", "100.63.0.1", "192.167.1.1", "8.8.8.8", "203.0.113.10"}
	notAddresses := []string{"1.2.3", "1.2.3.4.5", "256.1.1.1", "a.b.c.d", "1.2.3.4; id", "<html>", "1..2.3", "1.2.3.", ".1.2.3", "1.2.3.4\n5.6.7.8", "", "*"}
	for _, sh := range shells(t) {
		for _, a := range append(append([]string{}, private...), public...) {
			if _, code := sourced(t, sh, `valid_ipv4 "$A"`, "A="+a); code != 0 {
				t.Errorf("%s: %s is an address and was refused", sh, a)
			}
		}
		for _, a := range notAddresses {
			if _, code := sourced(t, sh, `valid_ipv4 "$A"`, "A="+a); code == 0 {
				t.Errorf("%s: %q was taken for an address, and it is what a service on the internet answered", sh, a)
			}
		}
		for _, a := range private {
			if _, code := sourced(t, sh, `is_private "$A"`, "A="+a); code != 0 {
				t.Errorf("%s: %s is private", sh, a)
			}
		}
		for _, a := range public {
			if _, code := sourced(t, sh, `is_private "$A"`, "A="+a); code == 0 {
				t.Errorf("%s: %s is not private", sh, a)
			}
		}
	}
}

// What a server says of itself is drawn as text: an escape sequence in a
// system's name must not reach the terminal.
func TestInstallClean(t *testing.T) {
	for _, sh := range shells(t) {
		if out, _ := sourced(t, sh, `clean "$T" 40`, "T=Ubuntu\x1b[31m 24.04\x07\n"); strings.TrimSpace(out) != "Ubuntu[31m 24.04" {
			t.Errorf("%s: %q", sh, out)
		}
		if out, _ := sourced(t, sh, `clean "$T" 34`, "T="+strings.Repeat("x", 100)); len(strings.TrimSpace(out)) != 34 {
			t.Errorf("%s: a long text was cut to %d", sh, len(strings.TrimSpace(out)))
		}
	}
}

func TestInstallUTF8(t *testing.T) {
	for _, sh := range shells(t) {
		for _, c := range []struct {
			env  []string
			want bool
		}{
			{[]string{"LANG=C"}, true}, // a server's locale says little about the window that is looked at
			{nil, true},
			{[]string{"LANG=en_US.UTF-8"}, true},
			{[]string{"LC_ALL=de_DE.utf8"}, true},
			{[]string{"LANG=en_US.ISO-8859-1"}, false},
			{[]string{"LANG=en_US.UTF-8", "LC_ALL=ru_RU.KOI8-R"}, false},
			{[]string{"LANG=en_US.UTF-8", "TERM=linux"}, false}, // the console's font has no braille
		} {
			_, code := sourced(t, sh, "utf8_ok", c.env...)
			if (code == 0) != c.want {
				t.Errorf("%s: %v gives %v, want %v", sh, c.env, code == 0, c.want)
			}
		}
	}
}

// The units are what the two files held that this script replaced: a
// change to one is a change to what runs as a service, and must be meant.
func TestInstallUnits(t *testing.T) {
	for _, sh := range shells(t) {
		for fn, file := range map[string]string{"unit_server": "testdata/musdash-server.service", "unit_proxy": "testdata/musdash-proxy.service"} {
			want, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if out, _ := sourced(t, sh, fn); out != string(want) {
				t.Errorf("%s: %s is not %s:\n%s", sh, fn, file, out)
			}
		}
	}
}

// states fills a directory with the state of the seven rows, through the
// script's own set_state.
const threeRows = `TMP=$D; set_state 1 ok "Ubuntu 24.04, amd64"; set_state 2 ok "Docker 29.8.0, git 2.43.0"; set_state 3 run; `

func TestInstallFrame(t *testing.T) {
	for _, sh := range shells(t) {
		out, code := sourced(t, sh, threeRows+"UTF8=1; glyphs; colors; frame 2 1", "D="+t.TempDir())
		if code != 0 {
			t.Fatalf("%s: exit %d\n%s", sh, code, out)
		}
		if n := strings.Count(out, "\n"); n != 12 {
			t.Errorf("%s: the checklist is %d lines, and every redraw goes back up 12:\n%s", sh, n, out)
		}
		for _, want := range []string{
			"musdash installer   step 3 of 7",
			"✓  Checking this server        Ubuntu 24.04, amd64",
			"✓  Installing Docker and git   Docker 29.8.0, git 2.43.0",
			"⠹  Downloading musdash",
			"○  Creating the musdash user",
			"○  Waiting for the dashboard",
			"  45%", // the first two steps' shares, and nothing for the one that runs
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: no %q in:\n%s", sh, want, out)
			}
		}
		if strings.Contains(out, "\x1b") {
			t.Errorf("%s: an escape sequence without colour and without a redraw:\n%q", sh, out)
		}

		ascii, _ := sourced(t, sh, threeRows+"UTF8=; glyphs; colors; frame 2 1", "D="+t.TempDir())
		for _, want := range []string{"[ok]  Checking this server", "[oo]  Downloading musdash", "[  ]  Starting musdash", "###################-------------------------  45%"} {
			if !strings.Contains(ascii, want) {
				t.Errorf("%s: no %q in the ASCII look:\n%s", sh, want, ascii)
			}
		}
		for _, r := range ascii {
			if r > 127 {
				t.Errorf("%s: %q in the look for a terminal that has no UTF-8", sh, r)
				break
			}
		}
	}
}

func TestInstallFrameSaysHowItEnded(t *testing.T) {
	for _, sh := range shells(t) {
		all := `TMP=$D; for i in 1 2 4 5 6 7; do set_state $i ok; done; set_state 3 skip "v0.1.0, from a file here"; `
		if out, _ := sourced(t, sh, all+"UTF8=1; glyphs; colors; frame 0 1", "D="+t.TempDir()); !strings.Contains(out, "done") || !strings.Contains(out, "100%") || !strings.Contains(out, "–  Downloading musdash") {
			t.Errorf("%s: a finished list:\n%s", sh, out)
		}
		failed := `TMP=$D; set_state 1 ok; set_state 2 fail "failed after 12s"; `
		out, _ := sourced(t, sh, failed+"UTF8=1; glyphs; colors; frame 0 1", "D="+t.TempDir())
		if !strings.Contains(out, "✗  Installing Docker and git   failed after 12s") || !strings.Contains(out, "stopped at step 2 of 7") || !strings.Contains(out, "○  Downloading musdash") {
			t.Errorf("%s: a failed list:\n%s", sh, out)
		}
	}
}

func TestInstallFrameRedraws(t *testing.T) {
	for _, sh := range shells(t) {
		if out, _ := sourced(t, sh, threeRows+"FANCY=1; CLR=; glyphs; colors; frame 1 0", "D="+t.TempDir()); !strings.HasPrefix(out, "\x1b[12A") {
			t.Errorf("%s: a redraw does not start by going up twelve lines: %q", sh, out[:min(20, len(out))])
		}
		if out, _ := sourced(t, sh, threeRows+"FANCY=1; CLR=; glyphs; colors; frame 0 1", "D="+t.TempDir()); strings.Contains(out, "\x1b[12A") {
			t.Errorf("%s: the first frame went up over what was on the screen", sh)
		}
	}
}

// A detail comes from the server. Whatever it holds, a row stays in its
// columns and moves no cursor.
func TestInstallFrameKeepsItsColumns(t *testing.T) {
	for _, sh := range shells(t) {
		state := `TMP=$D; set_state 1 ok "$LONG"; set_state 2 ok "$ESC"; `
		out, _ := sourced(t, sh, state+"UTF8=1; glyphs; colors; frame 0 1",
			"D="+t.TempDir(), "LONG="+strings.Repeat("Ubuntu ", 30), "ESC=Docker \x1b[2J\x1b[H29")
		if strings.Contains(out, "\x1b") {
			t.Errorf("%s: an escape sequence from a detail reached the screen: %q", sh, out)
		}
		for _, line := range strings.Split(out, "\n") {
			if n := utf8.RuneCountInString(line); n > 80 {
				t.Errorf("%s: a row of %d columns: %q", sh, n, line)
			}
		}
	}
}

func TestInstallBanner(t *testing.T) {
	for _, sh := range shells(t) {
		blocks, _ := sourced(t, sh, "UTF8=1; banner_lines")
		lines := strings.Split(strings.TrimRight(blocks, "\n"), "\n")
		if len(lines) != 5 {
			t.Fatalf("%s: the name is %d lines, and the fade goes back up 5", sh, len(lines))
		}
		for _, l := range lines {
			if n := utf8.RuneCountInString(l); n != utf8.RuneCountInString(lines[0]) || n > 62 {
				t.Errorf("%s: a line of %d columns in the name: %q", sh, n, l)
			}
		}
		slant, _ := sourced(t, sh, "UTF8=; banner_lines")
		lines = strings.Split(strings.TrimRight(slant, "\n"), "\n")
		if len(lines) != 5 {
			t.Fatalf("%s: the ASCII name is %d lines", sh, len(lines))
		}
		for _, r := range slant {
			if r > 127 {
				t.Errorf("%s: %q in the ASCII name", sh, r)
				break
			}
		}
	}
}

func TestInstallBox(t *testing.T) {
	const url = "http://203.0.113.10:8000"
	for _, sh := range shells(t) {
		for _, look := range []string{"UTF8=1", "UTF8="} {
			for _, lit := range []string{"0", "3"} {
				out, _ := sourced(t, sh, look+"; glyphs; colors; box "+url+" "+lit)
				lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
				if len(lines) != 5 {
					t.Fatalf("%s %s: the box is %d lines, and its pointers are redrawn by going up 5", sh, look, len(lines))
				}
				for _, l := range lines {
					if utf8.RuneCountInString(l) != utf8.RuneCountInString(lines[0]) {
						t.Errorf("%s %s: the box's sides do not line up:\n%s", sh, look, out)
						break
					}
				}
				if !strings.Contains(lines[2], "Dashboard    "+url) {
					t.Errorf("%s %s: no address in the box:\n%s", sh, look, out)
				}
				if look == "UTF8=1" && (!strings.Contains(out, "┏") || !strings.Contains(lines[2], "❯❯❯")) {
					t.Errorf("%s: no heavy box with its three pointers:\n%s", sh, out)
				}
				if look == "UTF8=" && (strings.IndexFunc(out, func(r rune) bool { return r > 127 }) >= 0 || !strings.Contains(lines[2], ">>>")) {
					t.Errorf("%s: the ASCII box:\n%s", sh, out)
				}
			}
		}
	}
}

// --- Whole installs, in a container ---------------------------------------

const installImage = "buildpack-deps:bookworm-curl"

// What stands in for systemd in a container, which has none. It writes down
// what it was asked, answers is-active from what was restarted, and a
// restart of the server starts the real binary, so that the installer's
// last step waits on a real dashboard.
const standInSystemctl = `#!/bin/sh
echo "$*" >>/tmp/systemctl.log
case "$1" in
  is-active)
    shift
    for a in "$@"; do
      case "$a" in
        -*) ;;
        musdash-proxy.service) [ -f "/tmp/active.$a" ] && [ ! -f /tmp/proxy-cannot-listen ] || exit 3 ;;
        *) [ -f "/tmp/active.$a" ] || exit 3 ;;
      esac
    done
    ;;
  restart | start)
    # A test can make one restart fail, as a connection that dropped would.
    if [ "$1" = restart ] && [ -f /tmp/restart-fails ]; then exit 1; fi
    if [ "$1" = start ] && [ -f "/tmp/active.$2" ]; then exit 0; fi
    touch "/tmp/active.$2"
    if [ "$2" = musdash-server.service ]; then
      if [ -f /tmp/server.pid ]; then kill "$(cat /tmp/server.pid)" 2>/dev/null; sleep 1; fi
      su -s /bin/sh musdash -c 'MUSDASH_DATA=/var/lib/musdash /usr/local/bin/musdash server >/tmp/server.log 2>&1 & echo $! >/tmp/server.pid'
    fi
    ;;
esac
exit 0
`

const standInDocker = `#!/bin/sh
case "$1" in
  --version) echo "Docker version 0.0.0, build stand-in" ;;
  info) exit 0 ;;
  *) exit 1 ;;
esac
`

const standInGit = `#!/bin/sh
echo "git version 0.0.0"
`

// installEnv is what a container test is given: a release directory for
// each version asked for, and the stand-ins.
type installEnv struct {
	t      *testing.T
	arch   string
	dir    string
	script string
}

func newInstallEnv(t *testing.T, versions ...string) *installEnv {
	t.Helper()
	if os.Getenv("MUSDASH_DOCKER_TEST") != "1" {
		t.Skip("set MUSDASH_DOCKER_TEST=1 to run the installer in a container")
	}
	arch, err := exec.Command("docker", "version", "--format", "{{.Server.Arch}}").Output()
	if err != nil {
		t.Fatalf("docker: %v", err)
	}
	script, err := filepath.Abs(installScript)
	if err != nil {
		t.Fatal(err)
	}
	e := &installEnv{t: t, arch: strings.TrimSpace(string(arch)), dir: t.TempDir(), script: script}
	if err := os.Chmod(e.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, v := range versions {
		e.release(v)
	}
	e.write("stubs/systemctl", standInSystemctl)
	e.write("stubs/docker", standInDocker)
	e.write("stubs/git", standInGit)
	return e
}

func (e *installEnv) write(name, body string) {
	e.t.Helper()
	path := filepath.Join(e.dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		e.t.Fatal(err)
	}
}

// release builds the binary under a version and writes it with its
// checksums where a release has them.
func (e *installEnv) release(version string) {
	e.t.Helper()
	name := "musdash-linux-" + e.arch
	bin := filepath.Join(e.dir, version, name)
	build := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w -X main.version="+version, "-o", bin, "../cmd/musdash")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+e.arch)
	if out, err := build.CombinedOutput(); err != nil {
		e.t.Fatalf("build: %v\n%s", err, out)
	}
	body, err := os.ReadFile(bin)
	if err != nil {
		e.t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	e.write(version+"/checksums.txt", hex.EncodeToString(sum[:])+"  "+name+"\n")
}

// run starts a container with the installer, the releases and the
// stand-ins in it, and runs the script given. It returns what was printed.
func (e *installEnv) run(script string, args ...string) string {
	e.t.Helper()
	cmd := []string{"run", "--rm",
		"-v", e.script + ":/install.sh:ro",
		"-v", e.dir + ":/t:ro",
		"-e", "MUSDASH_ADDRESS=203.0.113.10",
	}
	cmd = append(cmd, args...)
	cmd = append(cmd, installImage, "sh", "-c", "export PATH=/t/stubs:$PATH\nmkdir -p /run/systemd/system 2>/dev/null\n"+script)
	out, err := exec.Command("docker", cmd...).CombinedOutput()
	if err != nil {
		e.t.Fatalf("docker run: %v\n%s", err, out)
	}
	return string(out)
}

func has(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("no %q in:\n%s", w, out)
		}
	}
}

// facts prints what an install left behind, for a test to read.
const facts = `
echo "user: $(id musdash 2>&1)"
echo "binary: $(/usr/local/bin/musdash version 2>&1)"
echo "new: $(ls /usr/local/bin/musdash.new 2>/dev/null)"
echo "data: $(stat -c '%a %U' /var/lib/musdash 2>&1)"
echo "log: $(stat -c '%a' /var/log/musdash-install.log 2>&1)"
echo "health: $(curl -fsS http://127.0.0.1:8000/healthz 2>&1)"
echo "restarts: $(grep -c '^restart' /tmp/systemctl.log 2>/dev/null)"
echo "pending: $(ls /run/musdash-install.restart 2>/dev/null)"
echo "--systemctl"; cat /tmp/systemctl.log 2>/dev/null
echo "--server unit"; cat /etc/systemd/system/musdash-server.service 2>/dev/null
echo "--proxy unit"; cat /etc/systemd/system/musdash-proxy.service 2>/dev/null
echo "--end"
`

func TestInstallFresh(t *testing.T) {
	e := newInstallEnv(t, "v0.0.1")
	out := e.run(`groupadd docker
MUSDASH_RELEASE_URL=file:///t/v0.0.1 sh /install.sh; echo "rc=$?"` + facts)
	has(t, out, "rc=0",
		"[1/7] Checking this server", "[7/7] done: answering on port 8000",
		"[3/7] done: v0.0.1",
		"musdash v0.0.1 is running.", "Dashboard: http://203.0.113.10:8000",
		"Open it and create the owner account.",
		"binary: musdash v0.0.1", "data: 700 musdash", "log: 600", "health: ok", "new: \n",
		"enable musdash-proxy.service musdash-server.service", "daemon-reload")
	if !strings.Contains(out, "user: uid=") || !strings.Contains(out, "(docker)") {
		t.Errorf("the musdash user is not in the docker group:\n%s", out)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("an escape sequence in output that is not a terminal:\n%q", out)
	}
	// The proxy first: on an upgrade apps are unreachable only while it restarts.
	proxy, server := strings.Index(out, "restart musdash-proxy.service"), strings.Index(out, "restart musdash-server.service")
	if proxy < 0 || server < proxy {
		t.Errorf("the proxy is not restarted before the server:\n%s", out)
	}
	for marker, file := range map[string]string{"--server unit\n": "testdata/musdash-server.service", "--proxy unit\n": "testdata/musdash-proxy.service"} {
		want, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, marker+string(want)) {
			t.Errorf("the installed unit is not %s:\n%s", file, out)
		}
	}
}

// Pasting the command a second time must not cost the apps the second the
// proxy's restart takes.
func TestInstallSameAgain(t *testing.T) {
	e := newInstallEnv(t, "v0.0.1")
	out := e.run(`groupadd docker
export MUSDASH_RELEASE_URL=file:///t/v0.0.1
sh /install.sh >/dev/null; echo "first=$? restarts=$(grep -c '^restart' /tmp/systemctl.log)"
sh /install.sh; echo "rc=$?"` + facts)
	has(t, out, "first=0 restarts=2", "rc=0", "restarts: 2\n",
		"[5/7] done: nothing changed", "[6/7] not needed: already running",
		"musdash v0.0.1 was installed already. Nothing that was running was restarted.", "health: ok")
}

func TestInstallUpgrade(t *testing.T) {
	e := newInstallEnv(t, "v0.0.1", "v0.0.2")
	out := e.run(`groupadd docker
MUSDASH_RELEASE_URL=file:///t/v0.0.1 sh /install.sh >/dev/null; echo "first=$?"
MUSDASH_RELEASE_URL=file:///t/v0.0.2 sh /install.sh; echo "rc=$?"` + facts)
	has(t, out, "first=0", "rc=0", "restarts: 4\n", "[4/7] done: already there",
		"Upgraded from v0.0.1 to v0.0.2.", "binary: musdash v0.0.2", "health: ok")
}

// A download that was cut short must not become the binary.
func TestInstallWrongChecksum(t *testing.T) {
	e := newInstallEnv(t, "v0.0.1")
	e.write("bad/musdash-linux-"+e.arch, "half a download")
	sums, err := os.ReadFile(filepath.Join(e.dir, "v0.0.1", "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	e.write("bad/checksums.txt", string(sums))
	out := e.run(`groupadd docker
MUSDASH_RELEASE_URL=file:///t/bad sh /install.sh; echo "rc=$?"` + facts)
	has(t, out, "rc=1", "[3/7] failed", "The download does not match its checksum. Nothing was installed.",
		"The whole output is in /var/log/musdash-install.log", "new: \n")
	if strings.Contains(out, "binary: musdash v") || strings.Contains(out, "[4/7]") {
		t.Errorf("the install went on after a download that did not match:\n%s", out)
	}
}

func TestInstallNoChecksums(t *testing.T) {
	e := newInstallEnv(t, "v0.0.1")
	bin, err := os.ReadFile(filepath.Join(e.dir, "v0.0.1", "musdash-linux-"+e.arch))
	if err != nil {
		t.Fatal(err)
	}
	e.write("bare/musdash-linux-"+e.arch, string(bin))
	e.write("other/musdash-linux-"+e.arch, string(bin))
	e.write("other/checksums.txt", "0000  musdash-linux-other\n")
	out := e.run(`groupadd docker
MUSDASH_RELEASE_URL=file:///t/bare sh /install.sh; echo "rc=$?"
MUSDASH_RELEASE_URL=file:///t/other sh /install.sh; echo "rc2=$?"` + facts)
	has(t, out, "rc=1", "The release's checksums could not be downloaded",
		"rc2=1", "The release's checksums have no line for musdash-linux-"+e.arch, "new: \n")
	if strings.Contains(out, "binary: musdash v") {
		t.Errorf("a binary that could not be checked was installed:\n%s", out)
	}
}

func TestInstallArchNotSupported(t *testing.T) {
	e := newInstallEnv(t, "v0.0.1")
	e.write("riscv/uname", "#!/bin/sh\ncase \"$1\" in -m) echo riscv64 ;; *) exec /usr/bin/uname \"$@\" ;; esac\n")
	out := e.run(`export PATH=/t/riscv:$PATH
MUSDASH_RELEASE_URL=file:///t/v0.0.1 sh /install.sh; echo "rc=$?"` + facts)
	has(t, out, "rc=1", "[1/7] failed", "There is no musdash for this processor (riscv64).")
	if strings.Contains(out, "[2/7]") {
		t.Errorf("the install went on:\n%s", out)
	}
}

// Without Docker, Docker's own script is fetched and run. Here a stand-in
// answers for https://get.docker.com with a script that says it ran.
func TestInstallWithoutDocker(t *testing.T) {
	e := newInstallEnv(t, "v0.0.1")
	if err := os.Remove(filepath.Join(e.dir, "stubs", "docker")); err != nil {
		t.Fatal(err)
	}
	e.write("get-docker.sh", "#!/bin/sh\necho \"Docker's script ran\" >/tmp/get-docker.ran\ncp /t/standin-docker /usr/local/bin/docker\ngroupadd docker\n")
	e.write("standin-docker", standInDocker)
	e.write("nodocker/curl", `#!/bin/sh
for a in "$@"; do
  if [ "$a" = https://get.docker.com ]; then
    while [ $# -gt 0 ]; do
      if [ "$1" = -o ]; then cp /t/get-docker.sh "$2"; exit 0; fi
      shift
    done
  fi
done
exec /usr/bin/curl "$@"
`)
	out := e.run(`export PATH=/t/nodocker:$PATH
MUSDASH_RELEASE_URL=file:///t/v0.0.1 sh /install.sh; echo "rc=$?"
echo "docker script: $(cat /tmp/get-docker.ran 2>&1)"` + facts)
	has(t, out, "rc=0", "docker script: Docker's script ran", "[2/7] done: Docker 0.0.0, git 0.0.0", "health: ok", "(docker)")
}

func TestInstallLocalBinary(t *testing.T) {
	e := newInstallEnv(t, "v0.0.1")
	out := e.run(`groupadd docker
sh /install.sh /t/v0.0.1/musdash-linux-`+e.arch+`; echo "rc=$?"`+facts,
		// Nothing may be downloaded: an address that cannot be reached fails the test if it is asked.
		"-e", "MUSDASH_RELEASE_URL=http://127.0.0.1:9/none")
	has(t, out, "rc=0", "[3/7] not needed: v0.0.1, from a file here", "binary: musdash v0.0.1", "health: ok")

	out = e.run(`sh /install.sh /t/nothing-here; echo "rc=$?"`)
	has(t, out, "rc=1", "/t/nothing-here is not a file")
}

func TestInstallNotRoot(t *testing.T) {
	e := newInstallEnv(t)
	out := e.run(`sh /install.sh; echo "rc=$?"; ls /var/log/musdash-install.log 2>&1; true`, "--user", "65534:65534")
	has(t, out, "rc=1", "this needs root", "| sudo sh", "No such file")
}

func TestInstallRefusesAVersionThatIsNotOne(t *testing.T) {
	e := newInstallEnv(t)
	out := e.run(`MUSDASH_VERSION='v1.2.3/../../x' sh /install.sh; echo "rc=$?"`)
	has(t, out, "rc=1", "a version looks like v1.2.3")
}

// underTerminal runs the installer on a terminal of 40 rows and 100
// columns, which is what makes it draw.
const underTerminal = `script -qec 'stty rows 40 cols 100; sh /install.sh' /dev/null; echo "rc=$?"`

func TestInstallUnderATerminal(t *testing.T) {
	e := newInstallEnv(t, "v0.0.1")
	out := e.run("groupadd docker\n"+underTerminal+facts,
		"-e", "MUSDASH_RELEASE_URL=file:///t/v0.0.1", "-e", "TERM=xterm-256color", "-e", "LANG=C.UTF-8")
	has(t, out, "rc=0", "health: ok",
		"\x1b[12A",          // the list is redrawn in place
		"musdash installer", //
		"✓", "⠋", "100%",    //
		"██      ██  ██████  ██", // the name
		"┏━━━", "❯", "http://203.0.113.10:8000", "┗━━━",
		"Open it and create the owner account.")
	hide, show := strings.Index(out, "\x1b[?25l"), strings.LastIndex(out, "\x1b[?25h")
	if hide < 0 || show < hide {
		t.Errorf("the cursor is hidden and not shown again (hidden at %d, shown at %d)", hide, show)
	}
	if strings.Contains(out, "[1/7]") {
		t.Errorf("the plain lines were printed over the list:\n%q", out)
	}

	plain := e.run("groupadd docker\n"+underTerminal,
		"-e", "MUSDASH_RELEASE_URL=file:///t/v0.0.1", "-e", "TERM=xterm-256color", "-e", "NO_COLOR=1", "-e", "LANG=en_US.ISO-8859-1")
	has(t, plain, "rc=0", "[ok]", "#####", "/_/  /_/", "+---", ">>>")
	if strings.Contains(plain, "\x1b[3") || strings.Contains(plain, "\x1b[1m") || strings.Contains(plain, "✓") {
		t.Errorf("colour with NO_COLOR, or UTF-8 in a locale that has none:\n%q", plain)
	}
}

// A step that fails under a terminal: the list ends in its last state, the
// reason is on the screen, and the cursor is back.
func TestInstallFailsUnderATerminal(t *testing.T) {
	e := newInstallEnv(t, "v0.0.1")
	e.write("riscv/uname", "#!/bin/sh\ncase \"$1\" in -m) echo riscv64 ;; *) exec /usr/bin/uname \"$@\" ;; esac\n")
	out := e.run("export PATH=/t/riscv:$PATH\n"+underTerminal,
		"-e", "MUSDASH_RELEASE_URL=file:///t/v0.0.1", "-e", "TERM=xterm-256color", "-e", "LANG=C.UTF-8")
	has(t, out, "rc=1", "✗", "failed after", "stopped at step 1 of 7",
		"There is no musdash for this processor (riscv64).", "The whole output is in /var/log/musdash-install.log")
	if strings.Contains(out, "██") {
		t.Errorf("the name was drawn after a failure:\n%q", out)
	}
	if !strings.Contains(out, "\x1b[?25h") || strings.LastIndex(out, "\x1b[?25h") < strings.LastIndex(out, "\x1b[?25l") {
		t.Errorf("the cursor stays hidden after a failure")
	}
}

// An upgrade that was cut off after the binary was replaced and before the
// services were restarted: the next run finds the new binary in place, and
// must still restart, or the old version would run on and be called new.
func TestInstallFinishesAnInterruptedUpgrade(t *testing.T) {
	e := newInstallEnv(t, "v0.0.1", "v0.0.2")
	out := e.run(`groupadd docker
MUSDASH_RELEASE_URL=file:///t/v0.0.1 sh /install.sh >/dev/null; echo "first=$?"
touch /tmp/restart-fails
MUSDASH_RELEASE_URL=file:///t/v0.0.2 sh /install.sh; echo "cut=$? restarts=$(grep -c '^restart' /tmp/systemctl.log) pending=$(ls /run/musdash-install.restart)"
rm /tmp/restart-fails
MUSDASH_RELEASE_URL=file:///t/v0.0.2 sh /install.sh; echo "rc=$?"` + facts)
	has(t, out, "first=0", "[6/7] failed", "The proxy did not start.",
		"cut=1 restarts=3 pending=/run/musdash-install.restart",
		"rc=0", "restarts: 5\n", "pending: \n", "binary: musdash v0.0.2", "health: ok")
	if strings.Contains(out, "Nothing that was running was restarted") {
		t.Errorf("the run after a cut-off upgrade restarted nothing:\n%s", out)
	}
}

// Nothing new and one service down: it is started, and the other, which
// serves the apps, is left running.
func TestInstallStartsWhatIsDown(t *testing.T) {
	e := newInstallEnv(t, "v0.0.1")
	out := e.run(`groupadd docker
export MUSDASH_RELEASE_URL=file:///t/v0.0.1
sh /install.sh >/dev/null; echo "first=$?"
rm /tmp/active.musdash-server.service
sh /install.sh; echo "rc=$?"` + facts)
	has(t, out, "first=0", "rc=0", "restarts: 2\n", "[6/7] done: started what was not running",
		"start musdash-proxy.service", "start musdash-server.service", "health: ok")
}

// A proxy that cannot have its ports exits and is started again every
// second, while the dashboard answers. That is not an install that worked.
func TestInstallSaysWhenTheProxyDoesNotStayUp(t *testing.T) {
	e := newInstallEnv(t, "v0.0.1")
	out := e.run(`groupadd docker
touch /tmp/proxy-cannot-listen
MUSDASH_RELEASE_URL=file:///t/v0.0.1 sh /install.sh; echo "rc=$?"` + facts)
	has(t, out, "rc=1", "[7/7] failed", "the proxy that serves your apps does not", "port 80 or 443", "health: ok")
	if strings.Contains(out, "is running.") || strings.Contains(out, "Dashboard:") {
		t.Errorf("the install was called done with no proxy:\n%s", out)
	}
}

// systemd installed is not systemd running (a container, WSL without it).
// That is said in the first step, before Docker is installed for nothing.
func TestInstallNeedsSystemdToBeRunning(t *testing.T) {
	e := newInstallEnv(t, "v0.0.1")
	out := e.run(`rmdir /run/systemd/system
MUSDASH_RELEASE_URL=file:///t/v0.0.1 sh /install.sh; echo "rc=$?"` + facts)
	has(t, out, "rc=1", "[1/7] failed", "systemd is installed here but is not what runs this machine")
	if strings.Contains(out, "[2/7]") {
		t.Errorf("the install went on without systemd:\n%s", out)
	}
}

// A window too short for the list gets the plain lines.
func TestInstallInASmallWindow(t *testing.T) {
	e := newInstallEnv(t, "v0.0.1")
	out := e.run("groupadd docker\n"+strings.Replace(underTerminal, "rows 40", "rows 12", 1),
		"-e", "MUSDASH_RELEASE_URL=file:///t/v0.0.1", "-e", "TERM=xterm-256color")
	has(t, out, "rc=0", "[1/7] Checking this server", "Dashboard: http://203.0.113.10:8000")
	if strings.Contains(out, "\x1b") {
		t.Errorf("an escape sequence in a window too small to draw in:\n%q", out)
	}
	// The widest row is 70 columns: one column less, and it would wrap and
	// every redraw scroll the list by a line.
	narrow := e.run("groupadd docker\n"+strings.Replace(underTerminal, "cols 100", "cols 69", 1),
		"-e", "MUSDASH_RELEASE_URL=file:///t/v0.0.1", "-e", "TERM=xterm-256color")
	has(t, narrow, "rc=0", "[1/7] Checking this server")
	if strings.Contains(narrow, "\x1b") {
		t.Errorf("the list was drawn in a window of 69 columns:\n%q", narrow)
	}
}

// What is printed plainly is cleaned as what is drawn is: a system's name
// or an address given in the environment must not move a cursor, and
// dash's echo would make a real escape of a written-out one.
func TestInstallPlainOutputIsCleaned(t *testing.T) {
	for _, sh := range shells(t) {
		if out, _ := sourced(t, sh, `FANCY=; plain "$T"`, "T=[1/7] done: Evil\x1b[2JOS \\033[2J"); strings.Contains(out, "\x1b") || !strings.Contains(out, "Evil[2JOS") {
			t.Errorf("%s: plain printed %q", sh, out)
		}
		out, _ := sourced(t, sh, "public_address", `MUSDASH_ADDRESS=1.2.3.4\033[2J; id`)
		if out != "1.2.3.4033[2Jid" && out != "1.2.3.40332Jid" {
			t.Errorf("%s: an address from the environment came out as %q", sh, out)
		}
	}
}

// The frames of the ASCII spinner look like file name patterns ("[oo]").
func TestInstallSpinnerIsNotAPattern(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "o"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, sh := range shells(t) {
		out, _ := sourced(t, sh, `cd "$D"; `+threeRows+"UTF8=; glyphs; colors; frame 2 1", "D="+dir)
		if !strings.Contains(out, "[oo]  Downloading musdash") {
			t.Errorf("%s: with a file called o beside it, the spinner became a file name:\n%s", sh, out)
		}
	}
}

// --- A release -------------------------------------------------------------

// A release's installer installs that release: "make release" writes the
// tag into its copy, and the checksums are of the binaries beside it.
func TestReleaseStampsTheInstaller(t *testing.T) {
	if testing.Short() {
		t.Skip("builds both release binaries")
	}
	dist := t.TempDir()
	if out, err := exec.Command("make", "-C", "..", "release", "VERSION=dev", "DIST="+dist).CombinedOutput(); err == nil {
		t.Fatalf("a version that is not v1.2.3 made a release:\n%s", out)
	}
	if out, err := exec.Command("make", "-C", "..", "release", "VERSION=v9.8.7", "DIST="+dist).CombinedOutput(); err != nil {
		t.Fatalf("make release: %v\n%s", err, out)
	}
	source, err := os.ReadFile(installScript)
	if err != nil {
		t.Fatal(err)
	}
	stamped, err := os.ReadFile(filepath.Join(dist, "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(string(source), "\nRELEASE=\"\"\n", "\nRELEASE=\"v9.8.7\"\n", 1); string(stamped) != want || string(stamped) == string(source) {
		t.Errorf("the release's installer is not the script with its version in it and nothing else changed")
	}
	sums, err := os.ReadFile(filepath.Join(dist, "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"musdash-linux-amd64", "musdash-linux-arm64"} {
		body, err := os.ReadFile(filepath.Join(dist, name))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		if line := hex.EncodeToString(sum[:]) + "  " + name + "\n"; !strings.Contains(string(sums), line) {
			t.Errorf("checksums.txt has no line %q:\n%s", line, sums)
		}
		// The installer finds the line the way a server will.
		if out, _ := sourced(t, "sh", `checksum_for "$N" "$F"`, "N="+name, "F="+filepath.Join(dist, "checksums.txt")); strings.TrimSpace(out) != hex.EncodeToString(sum[:]) {
			t.Errorf("the installer reads %q for %s", out, name)
		}
	}
}
