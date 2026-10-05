package ops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/notify"
)

// How long one channel gets to take an event.
const sendTimeout = 20 * time.Second

// How long after a "stopped unexpectedly" the same one is not repeated.
const containerQuiet = 15 * time.Minute

type outgoing struct {
	teamID string
	event  notify.Event
}

func decode(raw []byte, v any) error { return json.Unmarshal(raw, v) }

// Notify hands an event to the team's channels. It returns at once: the
// sending happens elsewhere, and a failure there is logged, never returned.
// When more events wait than the queue holds, the newest is dropped rather
// than holding up the work that reported it.
func (o *Ops) Notify(teamID string, e notify.Event) {
	if teamID == "" {
		return
	}
	select {
	case o.events <- outgoing{teamID: teamID, event: e}:
	default:
		o.Log.Warn("notification dropped: too many are waiting", "event", e.Kind, "title", e.Title)
	}
}

// NotifyEnvironment is Notify for code that knows a resource's environment
// rather than its team.
func (o *Ops) NotifyEnvironment(environmentID string, e notify.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	teamID, err := o.DB.TeamOfEnvironment(ctx, environmentID)
	if err != nil {
		return
	}
	o.Notify(teamID, e)
}

// How many events are being sent at once. One team's slow channels then
// delay only a share of the sending, not everyone's.
const deliverers = 4

func (o *Ops) deliverLoop(ctx context.Context) {
	slots := make(chan struct{}, deliverers)
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-o.events:
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			go func() {
				defer func() { <-slots }()
				o.deliver(ctx, m.teamID, m.event)
			}()
		}
	}
}

// deliver sends one event to every channel of the team that asked for its
// kind.
func (o *Ops) deliver(ctx context.Context, teamID string, e notify.Event) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	if e.Kind == notify.EventContainer {
		key := teamID + "\x00" + e.Title
		o.recentMu.Lock()
		last, seen := o.recent[key]
		quiet := seen && e.At.Sub(last) < containerQuiet
		if !quiet {
			if len(o.recent) > 256 {
				clear(o.recent)
			}
			o.recent[key] = e.At
		}
		o.recentMu.Unlock()
		if quiet {
			return
		}
	}
	channels, err := o.DB.ListChannels(ctx, teamID)
	if err != nil {
		o.Log.Error("list notification channels", "err", err)
		return
	}
	e.URL = o.absolute(ctx, e.URL)
	// Each channel on its own, so a slow one does not hold up the others.
	var sending sync.WaitGroup
	for _, ch := range channels {
		if !ch.Enabled || !ch.Wants(e.Kind) {
			continue
		}
		sending.Add(1)
		go func() {
			defer sending.Done()
			if err := o.send(ctx, ch, e); err != nil {
				o.Log.Warn("notification not delivered", "channel", ch.Name, "kind", ch.Kind, "err", err)
			}
		}()
	}
	sending.Wait()
}

// absolute turns a dashboard path into an address a person can open. Until
// the dashboard has a domain there is none to give.
func (o *Ops) absolute(ctx context.Context, link string) string {
	if !strings.HasPrefix(link, "/") {
		return link
	}
	domain, err := o.DB.Setting(ctx, db.SettingInstanceDomain)
	if err != nil || domain == "" {
		return ""
	}
	return "https://" + domain + link
}

func (o *Ops) send(ctx context.Context, ch db.Channel, e notify.Event) error {
	cfg, err := o.ChannelConfig(ch)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	return o.Sender.Send(ctx, ch.Kind, cfg, e)
}

// ChannelConfig opens a channel's stored settings.
func (o *Ops) ChannelConfig(ch db.Channel) (map[string]string, error) {
	plain, err := o.Box.Open(ch.Config)
	if err != nil {
		return nil, errors.New("the channel's settings cannot be decrypted: was the master key changed?")
	}
	cfg := map[string]string{}
	if err := json.Unmarshal(plain, &cfg); err != nil {
		return nil, errors.New("the channel's stored settings are damaged")
	}
	return cfg, nil
}

// SealChannelConfig seals a channel's settings for storage.
func (o *Ops) SealChannelConfig(cfg map[string]string) (string, error) {
	plain, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return o.Box.Seal(plain)
}

// TestChannel sends a test event to one channel and reports how it went.
func (o *Ops) TestChannel(ctx context.Context, ch db.Channel) error {
	return o.send(ctx, ch, notify.Event{
		Kind: notify.EventTest, OK: true, At: time.Now(),
		Title: "Test notification from musdash",
		Body:  "If you can read this, the channel \"" + ch.Name + "\" works.",
		URL:   o.absolute(ctx, "/settings/notifications"),
	})
}
