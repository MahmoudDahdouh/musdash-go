package ui

import (
	"context"
	"strings"
	"testing"
)

// TestQueuedPillIsInfo pins the pill of something waiting in the queue: the
// soft blue of the info tone, not the amber of work in progress, and an
// icon that does not turn.
func TestQueuedPillIsInfo(t *testing.T) {
	render := func(state string) string {
		t.Helper()
		var b strings.Builder
		if err := StatusPill(state).Render(context.Background(), &b); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	queued := render("queued")
	if !strings.Contains(queued, `class="pill pill-info"`) || !strings.Contains(queued, "Queued") {
		t.Errorf("the queued pill is not in the info tone: %s", queued)
	}
	if strings.Contains(queued, "animate-spin") {
		t.Errorf("the queued pill's icon turns: %s", queued)
	}
	// What is under way keeps its own tone.
	if busy := render("deploying"); !strings.Contains(busy, `class="pill pill-busy"`) || !strings.Contains(busy, "animate-spin") {
		t.Errorf("the deploying pill is not in the busy tone: %s", busy)
	}
}
