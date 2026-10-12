package ops

import (
	"context"
	"strconv"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/update"
)

// settingUpdateTried is when the release was last asked for on its own,
// whether or not there was an answer.
const settingUpdateTried = "update_tried"

// UpdateState is what is known about releases.
type UpdateState struct {
	// Current is the version that runs; Latest the newest release that
	// was seen, "" before the first answer.
	Current, Latest string
	// Checked is when Latest was last answered, 0 for never.
	Checked int64
	// Auto says whether the release is asked for once a day.
	Auto bool
}

// Available reports whether a newer release than the running one was seen.
func (u UpdateState) Available() bool { return update.Newer(u.Latest, u.Current) }

// Update reads what is known about releases. It asks nobody.
func (o *Ops) Update(ctx context.Context) (UpdateState, error) {
	u := UpdateState{Current: o.Version}
	latest, err := o.DB.Setting(ctx, db.SettingUpdateLatest)
	if err != nil {
		return u, err
	}
	// Only a tag is taken from the table, as only one is put there.
	if update.Release(latest) {
		u.Latest = latest
	}
	checked, err := o.DB.Setting(ctx, db.SettingUpdateChecked)
	if err != nil {
		return u, err
	}
	u.Checked, _ = strconv.ParseInt(checked, 10, 64)
	auto, err := o.DB.Setting(ctx, db.SettingUpdateAuto)
	if err != nil {
		return u, err
	}
	u.Auto = auto != "off"
	return u, nil
}

// CheckUpdate asks for the latest release and stores the answer. A failure
// leaves what was stored as it is.
func (o *Ops) CheckUpdate(ctx context.Context, now time.Time) (string, error) {
	ask := o.Latest
	if ask == nil {
		ask = update.Latest
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	latest, err := ask(ctx)
	if err != nil {
		return "", err
	}
	if err := o.DB.SetSetting(ctx, db.SettingUpdateLatest, latest); err != nil {
		return "", err
	}
	if err := o.DB.SetSetting(ctx, db.SettingUpdateChecked, strconv.FormatInt(now.Unix(), 10)); err != nil {
		return "", err
	}
	return latest, nil
}

// SetUpdateAuto switches the daily question on or off.
func (o *Ops) SetUpdateAuto(ctx context.Context, on bool) error {
	v := "off"
	if on {
		v = ""
	}
	return o.DB.SetSetting(ctx, db.SettingUpdateAuto, v)
}

// updateIfDue asks for the latest release once in 24 hours, counted from
// the install's own last try: installs do not all ask at the same minute.
// A build that is no release asks nothing by itself.
func (o *Ops) updateIfDue(ctx context.Context, now time.Time) {
	if !update.Release(o.Version) {
		return
	}
	tried, err := o.DB.Setting(ctx, settingUpdateTried)
	if err != nil {
		return
	}
	if last, _ := strconv.ParseInt(tried, 10, 64); last > 0 && now.Unix()-last < 24*60*60 {
		return
	}
	if auto, err := o.DB.Setting(ctx, db.SettingUpdateAuto); err != nil || auto == "off" {
		return
	}
	// Stored first: a server that cannot reach GitHub does not try again
	// every minute.
	if err := o.DB.SetSetting(ctx, settingUpdateTried, strconv.FormatInt(now.Unix(), 10)); err != nil {
		o.Log.Error("record update check", "err", err)
		return
	}
	if _, err := o.CheckUpdate(ctx, now); err != nil && ctx.Err() == nil {
		o.Log.Warn("check for updates", "err", err)
	}
}
