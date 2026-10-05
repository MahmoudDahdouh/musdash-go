// Package backup dumps databases to compressed files, restores them, and
// copies the files to S3-compatible storage.
//
// A dump is a stream from the database's own dump tool to a file. It is
// never held in memory, so a backup costs the same few hundred kilobytes
// whatever the size of the database.
package backup

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
)

// tail keeps the last limit bytes written to it: the end of a tool's
// complaints is what explains a failure.
type tail struct {
	buf   []byte
	limit int
}

func (t *tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-t.limit:]...)
	}
	return len(p), nil
}

func (t *tail) String() string { return strings.TrimSpace(string(t.buf)) }

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// Dump runs a dump command inside a database's container and writes its
// output, gzip-compressed, to dest on the server. It returns the size of
// the file.
//
// The file appears under its name only once the dump command has exited
// successfully and everything is written; a dump that fails halfway leaves
// nothing behind that could be mistaken for a backup.
func Dump(ctx context.Context, r runner.Runner, container, dumpCmd, dest string) (int64, error) {
	if !docker.ValidName(container) {
		return 0, fmt.Errorf("bad container name %q", container)
	}
	if dumpCmd == "" {
		return 0, errors.New("this engine has no dump command")
	}
	pr, pw := io.Pipe()
	stderr := &tail{limit: 1500}
	done := make(chan error, 1)
	go func() {
		gz := gzip.NewWriter(pw)
		err := r.Run(ctx, runner.Cmd{Name: "docker", Args: []string{"exec", container, "sh", "-c", dumpCmd}, Stdout: gz, Stderr: stderr})
		if err == nil {
			err = gz.Close()
		}
		// With an error the reader fails, and so does the write below.
		pw.CloseWithError(err)
		done <- err
	}()
	counted := &countingReader{r: pr}
	writeErr := r.WriteFile(ctx, dest, 0o600, counted)
	// Unblocks the dump if the write gave up first.
	pr.CloseWithError(writeErr)
	if dumpErr := <-done; dumpErr != nil {
		if msg := stderr.String(); msg != "" {
			return 0, fmt.Errorf("the dump failed: %w: %s", dumpErr, msg)
		}
		return 0, fmt.Errorf("the dump failed: %w", dumpErr)
	}
	if writeErr != nil {
		return 0, fmt.Errorf("write the backup file: %w", writeErr)
	}
	return counted.n, nil
}

// Restore feeds a backup file made by Dump to a restore command inside a
// database's container.
func Restore(ctx context.Context, r runner.Runner, container, restoreCmd, src string) error {
	if !docker.ValidName(container) {
		return fmt.Errorf("bad container name %q", container)
	}
	if restoreCmd == "" {
		return errors.New("this engine has no restore command")
	}
	f, err := r.ReadFile(ctx, src)
	if err != nil {
		return fmt.Errorf("open the backup file: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("the backup file is not a gzip file: %w", err)
	}
	defer gz.Close()
	out := &tail{limit: 1500}
	err = r.Run(ctx, runner.Cmd{Name: "docker", Args: []string{"exec", "--interactive", container, "sh", "-c", restoreCmd}, Stdin: gz, Stdout: out, Stderr: out})
	if err != nil {
		if msg := out.String(); msg != "" {
			return fmt.Errorf("the restore failed: %w: %s", err, msg)
		}
		return fmt.Errorf("the restore failed: %w", err)
	}
	return nil
}
