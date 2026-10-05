package cron

import (
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNext(t *testing.T) {
	cases := []struct{ expr, after, want string }{
		{"* * * * *", "2026-03-10 12:00", "2026-03-10 12:01"},
		{"0 3 * * *", "2026-03-10 02:59", "2026-03-10 03:00"},
		// Strictly after: a schedule that fires now fires next tomorrow.
		{"0 3 * * *", "2026-03-10 03:00", "2026-03-11 03:00"},
		{"*/15 * * * *", "2026-03-10 12:14", "2026-03-10 12:15"},
		{"*/15 * * * *", "2026-03-10 12:45", "2026-03-10 13:00"},
		{"5/20 * * * *", "2026-03-10 12:05", "2026-03-10 12:25"},
		{"0-10/5 9-17 * * *", "2026-03-10 17:10", "2026-03-11 09:00"},
		{"30 2 1 * *", "2026-01-31 00:00", "2026-02-01 02:30"},
		{"0 0 31 * *", "2026-01-31 00:00", "2026-03-31 00:00"}, // February and April have no 31st
		{"0 0 29 2 *", "2026-03-01 00:00", "2028-02-29 00:00"}, // the next leap year
		{"0 0 * * 0", "2026-03-10 00:00", "2026-03-15 00:00"},  // a Tuesday; the next Sunday
		{"0 0 * * 7", "2026-03-10 00:00", "2026-03-15 00:00"},  // 7 is Sunday as well
		{"0 0 * * mon-fri", "2026-03-13 00:00", "2026-03-16 00:00"},
		{"0 0 * jan,JUL *", "2026-03-10 00:00", "2026-07-01 00:00"},
		{"0 12 * 12 *", "2026-12-31 12:00", "2027-12-01 12:00"},
		// Both days restricted: either one is enough, as in cron.
		{"0 0 13 * 5", "2026-03-10 00:00", "2026-03-13 00:00"}, // the 13th, which is also a Friday
		{"0 0 1 * 1", "2026-03-10 00:00", "2026-03-16 00:00"},  // a Monday comes before the 1st
		{"0 0 20 * 1", "2026-03-17 00:00", "2026-03-20 00:00"}, // the 20th comes before a Monday
		// A step on the star still counts as "any day".
		{"0 0 */2 * 1", "2026-03-10 00:00", "2026-03-23 00:00"}, // an odd day that is a Monday
		{"@hourly", "2026-03-10 12:30", "2026-03-10 13:00"},
		{"@daily", "2026-03-10 12:30", "2026-03-11 00:00"},
		{"@weekly", "2026-03-10 12:30", "2026-03-15 00:00"},
		{"@monthly", "2026-03-10 12:30", "2026-04-01 00:00"},
		{"@yearly", "2026-03-10 12:30", "2027-01-01 00:00"},
		{"  15   4  *  *  *  ", "2026-03-10 12:30", "2026-03-11 04:15"},
		{"59 23 31 12 *", "2026-12-31 23:59", "2027-12-31 23:59"},
	}
	for _, c := range cases {
		s, err := Parse(c.expr)
		if err != nil {
			t.Errorf("%q: %v", c.expr, err)
			continue
		}
		if got := s.Next(at(c.after)); !got.Equal(at(c.want)) {
			t.Errorf("%q after %s: %s, want %s", c.expr, c.after, got.Format("2006-01-02 15:04 Mon"), c.want)
		}
	}
}

func TestNextIsInUTCAndIgnoresSeconds(t *testing.T) {
	s, _ := Parse("0 3 * * *")
	cairo := time.FixedZone("UTC+2", 2*3600)
	// 04:30 in UTC+2 is 02:30 UTC: the next 03:00 UTC is half an hour away.
	got := s.Next(time.Date(2026, 3, 10, 4, 30, 45, 123, cairo))
	if !got.Equal(at("2026-03-10 03:00")) || got.Location() != time.UTC {
		t.Fatalf("%s", got)
	}
	every, _ := Parse("* * * * *")
	if got := every.Next(time.Date(2026, 3, 10, 12, 0, 59, 999, time.UTC)); !got.Equal(at("2026-03-10 12:01")) {
		t.Fatalf("%s", got)
	}
}

func TestAScheduleThatNeverFires(t *testing.T) {
	for _, expr := range []string{"0 0 30 2 *", "0 0 31 4,6,9,11 *"} {
		s, err := Parse(expr)
		if err != nil {
			t.Fatal(err)
		}
		if got := s.Next(at("2026-01-01 00:00")); !got.IsZero() {
			t.Errorf("%q fires at %s", expr, got)
		}
	}
}

func TestParseRefuses(t *testing.T) {
	for _, expr := range []string{
		"", "* * * *", "* * * * * *", "@reboot", "@every 5m",
		"60 * * * *", "* 24 * * *", "* * 0 * *", "* * 32 * *", "* * * 0 *", "* * * 13 *", "* * * * 8",
		"-1 * * * *", "+5 * * * *", "1.5 * * * *", "a * * * *", "* * * foo *", "* * * * funday",
		"*/0 * * * *", "*/x * * * *", "*/ * * * *", "*/-5 * * * *", "5-1 * * * *", "1- * * * *", "-5 * * * *",
		"1,,2 * * * *", ", * * * *", "1-2-3 * * * *", "*/5/5 * * * *", "* * * * mon-",
	} {
		if s, err := Parse(expr); err == nil {
			t.Errorf("%q was accepted: %+v", expr, s)
		}
	}
}
