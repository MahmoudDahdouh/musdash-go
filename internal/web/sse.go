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

// sseWriter turns a byte stream into Server-Sent Events: a "line" event
// for the lines of each piece it is handed. The text is HTML-escaped: the
// browser inserts each event as markup, and log output is not trusted.
//
// An event holds every line of its piece, not one line each. The page puts
// an event into the log and lays the log out again to stay at its end, and
// a deployment's log has thousands of lines: sent one a line, a log that
// is read from its file kept the page busy for as long as it took to do
// that thousands of times. A piece is at most what a follower reads at
// once, and a live log still arrives as it is written.
type sseWriter struct {
	w       http.ResponseWriter
	rc      *http.ResponseController
	partial []byte
	// event is the event being put together, kept between calls so that a
	// long log is not a new buffer a piece.
	event bytes.Buffer
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

// Write emits one "line" event for the complete lines of p and keeps the
// rest for the next call.
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
		s.line(data[:i])
		data = data[i+1:]
	}
	switch {
	case len(data) > maxLogLine:
		// A line with no end in sight: emit what there is.
		s.line(data)
	case len(data) > 0:
		s.partial = append([]byte(nil), data...)
	}
	if err := s.send(); err != nil {
		return 0, err
	}
	// A dead connection must surface as an error so the producer stops.
	if err := s.rc.Flush(); err != nil {
		return 0, err
	}
	return len(p), nil
}

// line adds one line of output to the event being put together.
func (s *sseWriter) line(line []byte) {
	line = bytes.TrimRight(line, "\r")
	if len(line) > maxLogLine {
		line = append(line[:maxLogLine:maxLogLine], " …"...)
	}
	if s.event.Len() == 0 {
		s.event.WriteString("event: line\n")
	}
	// A carriage return also ends a line in the event-stream format. Left
	// in, log output could start a line of its own and forge fields such as
	// "event: done". Progress bars use it to redraw; a space keeps them
	// readable.
	s.event.WriteString("data: ")
	s.event.WriteString(strings.ReplaceAll(html.EscapeString(string(line)), "\r", " "))
	s.event.WriteByte('\n')
}

// send writes the event that was put together, if there is one. The data
// lines of an event are joined by line breaks, and an empty one after the
// last gives that line its break too: each is a line of its own inside
// the <pre>.
func (s *sseWriter) send() error {
	if s.event.Len() == 0 {
		return nil
	}
	s.event.WriteString("data: \n\n")
	_, err := s.w.Write(s.event.Bytes())
	s.event.Reset()
	return err
}

// finish flushes a trailing partial line and sends the "done" event that
// tells the browser to stop reconnecting.
func (s *sseWriter) finish() {
	s.rc.SetWriteDeadline(time.Now().Add(writeTimeout))
	if len(s.partial) > 0 {
		s.line(s.partial)
		s.partial = nil
		s.send()
	}
	s.w.Write([]byte("event: done\ndata: \n\n"))
	s.rc.Flush()
}
