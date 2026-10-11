package deploy

import (
	"bytes"
	"io"
	"sync"
	"time"
)

// unrepeated passes on what is written to it a line at a time, and leaves
// out what a person reading a deployment's log has no use for twice.
// Without a terminal, Compose reports the progress of a pull as lines:
// the same one many times while a layer waits, a new one for every
// megabyte of a layer that comes down ("821d9dafb26d Downloading 1.049MB"),
// and each thing it creates twice.
//
//   - A line that says what the one before it said is left out.
//   - Of the progress of one layer (Downloading, Extracting) a line is
//     passed on every few seconds: enough to see that it moves.
//
// It is given to a command as both its outputs, so it is safe to write to
// from two goroutines. Flush hands on a last line that had no end.
type unrepeated struct {
	w io.Writer
	// now is the clock; time.Now unless a test sets it.
	now func() time.Time

	mu   sync.Mutex
	held []byte // a line that has not ended yet
	last []byte // the line passed on before
	any  bool   // whether a line was passed on at all
	// said is when the progress of each layer was last passed on, by the
	// layer and what is being done with it.
	said map[string]time.Time
}

// progressEvery is how often the progress of one layer is passed on.
const progressEvery = 5 * time.Second

// progressOf is the layer and the step a line reports the progress of, or
// "" for a line that is not such a report.
func progressOf(line []byte) string {
	fields := bytes.Fields(line)
	if len(fields) < 3 {
		return ""
	}
	if step := string(fields[1]); step != "Downloading" && step != "Extracting" {
		return ""
	}
	return string(fields[0]) + " " + string(fields[1])
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

// pass hands a line on unless it is the one before it again, or one more
// report of a layer's progress so soon after the last.
func (u *unrepeated) pass(line []byte) error {
	if u.any && bytes.Equal(line, u.last) {
		return nil
	}
	if layer := progressOf(line); layer != "" {
		now := time.Now()
		if u.now != nil {
			now = u.now()
		}
		if at, ok := u.said[layer]; ok && now.Sub(at) < progressEvery {
			return nil
		}
		// A pull of many images has many layers; none is remembered for
		// longer than the pull, and not without bound during it.
		if u.said == nil || len(u.said) > 512 {
			u.said = map[string]time.Time{}
		}
		u.said[layer] = now
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
