package ui

import (
	"strconv"
	"time"
)

// Ago writes a past moment relative to now: "just now", "5 minutes ago",
// "3 days ago". Zero means the moment never happened.
func Ago(unix int64) string {
	if unix == 0 {
		return "never"
	}
	d := time.Since(time.Unix(unix, 0))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return count(int(d.Minutes()), "minute") + " ago"
	case d < 24*time.Hour:
		return count(int(d.Hours()), "hour") + " ago"
	case d < 30*24*time.Hour:
		return count(int(d.Hours()/24), "day") + " ago"
	}
	return time.Unix(unix, 0).Format("2 January 2006")
}

// Took writes how long something ran: "48s", "2m 5s". It returns "" while
// the end is not known yet.
func Took(start, end int64) string {
	if start == 0 || end == 0 || end < start {
		return ""
	}
	s := end - start
	if s < 60 {
		return strconv.FormatInt(s, 10) + "s"
	}
	return strconv.FormatInt(s/60, 10) + "m " + strconv.FormatInt(s%60, 10) + "s"
}

func count(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

// Until writes a coming moment relative to now: "in 5 hours", "in 3
// days", and a date for one that is further off. A moment that has passed
// is "now".
func Until(unix int64) string {
	d := time.Until(time.Unix(unix, 0))
	switch {
	case d <= 0:
		return "now"
	case d < time.Hour:
		return "in " + count(int(d.Minutes())+1, "minute")
	case d < 24*time.Hour:
		return "in " + count(int(d.Hours())+1, "hour")
	case d < 30*24*time.Hour:
		return "in " + count(int(d.Hours()/24)+1, "day")
	}
	return "on " + time.Unix(unix, 0).Format("2 January 2006")
}
