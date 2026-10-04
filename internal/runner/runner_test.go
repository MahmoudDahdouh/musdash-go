package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQuoteSurvivesTheShell(t *testing.T) {
	// Each value is passed through a real shell and must come back unchanged
	// and as exactly one argument.
	values := []string{
		"",
		"plain",
		"with space",
		"it's",
		"''",
		`"double"`,
		"$(touch /tmp/musdash-pwned)",
		"`id`",
		"a;b",
		"a && b || c",
		"a | b > c < d",
		"line1\nline2",
		"tab\there",
		"$HOME",
		"\\",
		"*?[x]",
		"~root",
		"#comment",
		"-rf",
		"héllo wörld ✓",
		"!history",
		"a=b",
		"{a,b}",
	}
	for _, v := range values {
		script := "printf '%s' " + Quote(v)
		out, err := exec.Command("sh", "-c", script).Output()
		if err != nil {
			t.Errorf("%q: shell failed: %v", v, err)
			continue
		}
		if string(out) != v {
			t.Errorf("Quote(%q) = %s → shell produced %q", v, Quote(v), out)
		}
	}
	if _, err := os.Stat("/tmp/musdash-pwned"); err == nil {
		os.Remove("/tmp/musdash-pwned")
		t.Fatal("command substitution was executed")
	}
}

func TestQuoteJoinArgumentCount(t *testing.T) {
	line := QuoteJoin("printf", "%s|", "a b", "", "c'd", "$x")
	out, err := exec.Command("sh", "-c", line).Output()
	if err != nil {
		t.Fatal(err)
	}
	if want := "a b||c'd|$x|"; string(out) != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

func TestLocalRunStreamsAndReportsExit(t *testing.T) {
	r := NewLocal()
	ctx := context.Background()

	var stdout, stderr bytes.Buffer
	err := r.Run(ctx, Cmd{Name: "sh", Args: []string{"-c", "echo out; echo err >&2; exit 3"}, Stdout: &stdout, Stderr: &stderr})
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 3 {
		t.Fatalf("want ExitError code 3, got %v", err)
	}
	if stdout.String() != "out\n" || stderr.String() != "err\n" {
		t.Fatalf("stdout %q stderr %q", stdout.String(), stderr.String())
	}
}

func TestLocalRunEnvDirStdin(t *testing.T) {
	r := NewLocal()
	dir := t.TempDir()
	var out bytes.Buffer
	err := r.Run(context.Background(), Cmd{
		Name:   "sh",
		Args:   []string{"-c", `printf '%s|%s|' "$MUSDASH_T" "$(pwd -P)"; cat`},
		Env:    []string{"MUSDASH_T=value with space"},
		Dir:    dir,
		Stdin:  strings.NewReader("stdin"),
		Stdout: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(dir)
	if want := "value with space|" + real + "|stdin"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestLocalRunCancelKillsProcessGroup(t *testing.T) {
	r := NewLocal()
	ctx, cancel := context.WithCancel(context.Background())
	marker := filepath.Join(t.TempDir(), "child-finished")

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		// The child of the shell must die too, or it would write the marker.
		done <- r.Run(ctx, Cmd{Name: "sh", Args: []string{"-c", "(sleep 2; touch " + Quote(marker) + ") & wait"}})
	}()
	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if time.Since(start) > 1500*time.Millisecond {
		t.Fatalf("cancel took %v", time.Since(start))
	}
	time.Sleep(2200 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("grandchild process survived cancellation")
	}
}

func TestLocalOutput(t *testing.T) {
	r := NewLocal()
	ctx := context.Background()

	out, err := r.Output(ctx, Cmd{Name: "printf", Args: []string{"hello"}})
	if err != nil || string(out) != "hello" {
		t.Fatalf("%q %v", out, err)
	}

	_, err = r.Output(ctx, Cmd{Name: "sh", Args: []string{"-c", "echo boom >&2; exit 1"}})
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Stderr != "boom" {
		t.Fatalf("want ExitError carrying stderr, got %v", err)
	}

	// 2 MiB of output must be refused, not truncated and not buffered whole.
	_, err = r.Output(ctx, Cmd{Name: "sh", Args: []string{"-c", "head -c 2097152 /dev/zero"}})
	if !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("want ErrOutputTooLarge, got %v", err)
	}

	if _, err := r.Output(ctx, Cmd{Name: "musdash-no-such-binary"}); err == nil {
		t.Fatal("want error for a missing binary")
	}
}

func TestLocalFiles(t *testing.T) {
	r := NewLocal()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "a", "b")
	if err := r.MkdirAll(ctx, dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "env")
	for _, content := range []string{"first", "second"} {
		if err := r.WriteFile(ctx, path, 0o600, strings.NewReader(content)); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v err %v", info.Mode().Perm(), err)
	}
	rc, err := r.ReadFile(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "second" {
		t.Fatalf("got %q", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
	if err := r.RemoveAll(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("directory still exists")
	}
}
