package deploy

import (
	"bytes"
	"io"
	"sync"
)

// unrepeated passes on what is written to it a line at a time, and leaves
// out a line that says what the one before it said. Without a terminal,
// Compose reports the progress of a pull as lines, the same one many times
// while a layer comes down ("821d9dafb26d Downloading 1.049MB"), and names
// each thing it creates twice. A deployment's log is read by a person.
//
// It is given to a command as both its outputs, so it is safe to write to
// from two goroutines. Flush hands on a last line that had no end.
type unrepeated struct {
	w io.Writer

	mu   sync.Mutex
	held []byte // a line that has not ended yet
	last []byte // the line passed on before
	any  bool   // whether a line was passed on at all
}

// unrepeatedLimit is how long a line may grow before it is passed on as it
// is: output with no line ends in it is not kept in memory.
const unrepeatedLimit = 4096

func (u *unrepeated) Write(p []byte) (int, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	n := len(p)
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		if end < 0 {
			u.held = append(u.held, p...)
			if len(u.held) > unrepeatedLimit {
				if err := u.pass(u.held); err != nil {
					return n, err
				}
				u.held = u.held[:0]
			}
			break
		}
		line := p[:end+1]
		if len(u.held) > 0 {
			u.held = append(u.held, line...)
			line = u.held
		}
		if err := u.pass(line); err != nil {
			return n, err
		}
		u.held = u.held[:0]
		p = p[end+1:]
	}
	return n, nil
}

// pass hands a line on unless it is the one before it again.
func (u *unrepeated) pass(line []byte) error {
	if u.any && bytes.Equal(line, u.last) {
		return nil
	}
	u.last = append(u.last[:0], line...)
	u.any = true
	_, err := u.w.Write(line)
	return err
}

// Flush passes on what was written last without a line end.
func (u *unrepeated) Flush() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.held) > 0 {
		u.pass(u.held)
		u.held = u.held[:0]
	}
}
