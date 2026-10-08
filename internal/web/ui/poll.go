package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"

	"github.com/a-h/templ"
)

type pollKey struct{}

// WatchPoll returns a context in which the first Poll that is rendered
// leaves its mark, for the handler that answers the poll to compare with
// what the request says the page holds.
func WatchPoll(ctx context.Context) (context.Context, *string) {
	mark := new(string)
	return context.WithValue(ctx, pollKey{}, mark), mark
}

// Poll is the attributes of an element that asks for itself again while
// what it shows may change on its own: where it asks, how often (trigger,
// such as "every 2s"; with "" the element does not ask), and that the
// answer replaces it.
//
// The address says what the element holds: a mark of body, which is
// everything the element shows, and of the trigger. The handler answers
// 204 to a request whose mark is the one a fresh rendering has
// (Server.renderPolled), and htmx puts nothing into the page for a 204.
// Without that the element was replaced on every poll with a copy of
// itself: a spinner started over every two seconds, and a button under the
// pointer was taken away and put back.
//
// The mark is a hash that cannot be turned back, because a body holds the
// form token of the session and an address is written to logs.
func Poll(ctx context.Context, url, trigger string, body templ.Component) templ.Attributes {
	if trigger == "" {
		return nil
	}
	attrs := templ.Attributes{"hx-get": url, "hx-trigger": trigger, "hx-swap": "outerHTML"}
	h := sha256.New()
	io.WriteString(h, trigger+"\n")
	// A body that cannot be rendered has no mark: its element is replaced
	// by whatever the poll answers, as it would be without one.
	if err := body.Render(ctx, h); err != nil {
		return attrs
	}
	mark := hex.EncodeToString(h.Sum(nil)[:8])
	if seen, ok := ctx.Value(pollKey{}).(*string); ok && *seen == "" {
		*seen = mark
	}
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	attrs["hx-get"] = url + sep + "seen=" + mark
	return attrs
}
