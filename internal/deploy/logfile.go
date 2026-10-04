package deploy

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// maxLogBytes caps a deployment log on disk. A noisy build cannot fill the
// server; what is cut is the middle-to-end of the noise, and the final
// status lines are still written.
const maxLogBytes = 2 << 20

// Log is a deployment's log file. It is safe for the concurrent writes of a
// command's stdout and stderr.
type Log struct {
	mu        sync.Mutex
	f         *os.File
	written   int64
	truncated bool
}

// OpenLog creates (or truncates) the log file at path.
func OpenLog(path string) (*Log, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	return &Log{f: f}, nil
}

// Write appends command output, dropping whatever exceeds the cap.
func (l *Log) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.truncated {
		return len(p), nil
	}
	if room := maxLogBytes - l.written; int64(len(p)) > room {
		l.f.Write(p[:room])
		l.f.WriteString("\n[log truncated: output exceeded 2 MB]\n")
		l.written = maxLogBytes
		l.truncated = true
		return len(p), nil
	}
	n, err := l.f.Write(p)
	l.written += int64(n)
	return len(p), err
}

// Step writes one of musdash's own progress lines. These are always
// written, even after command output was truncated, so the log still ends
// with what happened.
func (l *Log) Step(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.f, "%s  %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

func (l *Log) Close() error { return l.f.Close() }

// Follow copies a log file to w as it grows, like `tail -f`, and returns when
// finished reports true and everything written so far has been copied, or
// when ctx is cancelled. It reads through one small buffer, so following a
// log costs the same memory whatever the log's size.
func Follow(ctx context.Context, path string, w io.Writer, finished func() bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, 16<<10)
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		// Ask before reading: if the deployment had finished by now, the
		// read below sees its final lines and the loop can end.
		done := finished()
		for {
			n, err := f.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return werr
				}
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// FollowWhenReady is Follow for a log that may not exist yet: a queued
// deployment gets its file only when its job starts. It waits for the file,
// and returns nil without output if the deployment finishes without one.
func FollowWhenReady(ctx context.Context, path string, w io.Writer, finished func() bool) error {
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return Follow(ctx, path, w, finished)
		}
		if finished() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
