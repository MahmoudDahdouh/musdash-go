package web

import (
	"bytes"
	"html"
	"net/http"
	"strings"
	"time"
)

// maxStreams bounds how many live log streams are open at once. Each one
// holds a connection and, for container logs, a `docker logs` process.
const maxStreams = 16

// maxLogLine cuts a single over-long log line.
const maxLogLine = 8 << 10

// sseWriter turns a byte stream into Server-Sent Events, one "line" event
// per line of text. The text is HTML-escaped: the browser inserts each event
// as markup, and log output is not trusted.
type sseWriter struct {
	w       http.ResponseWriter
	rc      *http.ResponseController
	partial []byte
}

// startSSE prepares the response for an event stream.
func startSSE(w http.ResponseWriter) *sseWriter {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	s := &sseWriter{w: w, rc: http.NewResponseController(w)}
	s.rc.Flush()
	return s
}

// writeTimeout is how long one batch of events may take to reach the
// browser before the stream is given up on.
const writeTimeout = 30 * time.Second

// Write emits a "line" event for each complete line and keeps the rest for
// the next call.
func (s *sseWriter) Write(p []byte) (int, error) {
	// Armed before writing: a deadline left over from an earlier call would
	// have expired during a quiet spell and fail the first write after it.
	s.rc.SetWriteDeadline(time.Now().Add(writeTimeout))
	data := p
	if len(s.partial) > 0 {
		data = append(s.partial, p...)
		s.partial = nil
	}
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		if err := s.line(data[:i]); err != nil {
			return 0, err
		}
		data = data[i+1:]
	}
	switch {
	case len(data) > maxLogLine:
		// A line with no end in sight: emit what there is.
		if err := s.line(data); err != nil {
			return 0, err
		}
	case len(data) > 0:
		s.partial = append([]byte(nil), data...)
	}
	// A dead connection must surface as an error so the producer stops.
	if err := s.rc.Flush(); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *sseWriter) line(line []byte) error {
	line = bytes.TrimRight(line, "\r")
	if len(line) > maxLogLine {
		line = append(line[:maxLogLine:maxLogLine], " …"...)
	}
	// A carriage return also ends a line in the event-stream format. Left
	// in, log output could start a line of its own and forge fields such as
	// "event: done". Progress bars use it to redraw; a space keeps them
	// readable.
	text := strings.ReplaceAll(html.EscapeString(string(line)), "\r", " ")
	// Two data lines join into "<text>\n", which gives each event its own
	// line inside the <pre>.
	_, err := s.w.Write([]byte("event: line\ndata: " + text + "\ndata: \n\n"))
	return err
}

// finish flushes a trailing partial line and sends the "done" event that
// tells the browser to stop reconnecting.
func (s *sseWriter) finish() {
	s.rc.SetWriteDeadline(time.Now().Add(writeTimeout))
	if len(s.partial) > 0 {
		s.line(s.partial)
		s.partial = nil
	}
	s.w.Write([]byte("event: done\ndata: \n\n"))
	s.rc.Flush()
}
