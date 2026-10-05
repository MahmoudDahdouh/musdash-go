// Package cron parses five-field cron expressions and works out when they
// next fire. Times are UTC.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed cron expression. Each field is a bit set of the
// values it matches.
type Schedule struct {
	minute, hour, dom, month, dow uint64
	// domAny and dowAny record a "*" in the day fields. When both days are
	// restricted, cron fires when either matches; otherwise both must.
	domAny, dowAny bool
}

type field struct {
	name     string
	min, max int
	names    []string // optional three-letter names, in order from min
}

var fields = [5]field{
	{name: "minute", min: 0, max: 59},
	{name: "hour", min: 0, max: 23},
	{name: "day of the month", min: 1, max: 31},
	{name: "month", min: 1, max: 12, names: []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}},
	// 7 is Sunday too.
	{name: "day of the week", min: 0, max: 7, names: []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}},
}

var shortcuts = map[string]string{
	"@hourly":   "0 * * * *",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@weekly":   "0 0 * * 0",
	"@monthly":  "0 0 1 * *",
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
}

// Parse reads a cron expression: five fields (minute, hour, day of the
// month, month, day of the week), each "*", a number, a range "a-b", a list
// "a,b,c" or a step "*/n" or "a-b/n"; or one of @hourly, @daily, @weekly,
// @monthly, @yearly.
func Parse(expr string) (Schedule, error) {
	expr = strings.TrimSpace(expr)
	if full, ok := shortcuts[strings.ToLower(expr)]; ok {
		expr = full
	}
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return Schedule{}, fmt.Errorf("a schedule has five fields (minute, hour, day of the month, month, day of the week), such as \"0 3 * * *\"; this one has %d", len(parts))
	}
	var s Schedule
	sets := [5]*uint64{&s.minute, &s.hour, &s.dom, &s.month, &s.dow}
	for i, part := range parts {
		set, err := parseField(part, fields[i])
		if err != nil {
			return Schedule{}, err
		}
		*sets[i] = set
	}
	// Sunday may be written 0 or 7.
	if s.dow&(1<<7) != 0 {
		s.dow = s.dow&^(1<<7) | 1
	}
	s.domAny = strings.HasPrefix(parts[2], "*")
	s.dowAny = strings.HasPrefix(parts[4], "*")
	return s, nil
}

func parseField(text string, f field) (uint64, error) {
	var set uint64
	for _, item := range strings.Split(text, ",") {
		spec, stepText, hasStep := strings.Cut(item, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepText)
			if err != nil || n < 1 || n > f.max {
				return 0, fmt.Errorf("%s: %q is not a step; write a number after the slash, as in */5", f.name, stepText)
			}
			step = n
		}
		lo, hi := f.min, f.max
		switch {
		case spec == "*":
		case strings.Contains(spec, "-"):
			a, b, _ := strings.Cut(spec, "-")
			var err error
			if lo, err = parseValue(a, f); err != nil {
				return 0, err
			}
			if hi, err = parseValue(b, f); err != nil {
				return 0, err
			}
			if lo > hi {
				return 0, fmt.Errorf("%s: the range %s runs backwards", f.name, spec)
			}
		default:
			v, err := parseValue(spec, f)
			if err != nil {
				return 0, err
			}
			lo = v
			// "5/15" means from 5 to the end in steps of 15.
			if !hasStep {
				hi = v
			}
		}
		for v := lo; v <= hi; v += step {
			set |= 1 << uint(v)
		}
	}
	return set, nil
}

func parseValue(text string, f field) (int, error) {
	for i, name := range f.names {
		if strings.EqualFold(text, name) {
			return f.min + i, nil
		}
	}
	n, err := strconv.Atoi(text)
	if err != nil || text == "" || text[0] == '+' || text[0] == '-' {
		return 0, fmt.Errorf("%s: %q is not a number", f.name, text)
	}
	if n < f.min || n > f.max {
		return 0, fmt.Errorf("%s: %d is outside %d to %d", f.name, n, f.min, f.max)
	}
	return n, nil
}

func (s Schedule) dayMatches(t time.Time) bool {
	dom := s.dom&(1<<uint(t.Day())) != 0
	dow := s.dow&(1<<uint(t.Weekday())) != 0
	if s.domAny || s.dowAny {
		return dom && dow
	}
	return dom || dow
}

// Next returns the first minute after t at which the schedule fires, in
// UTC. It returns the zero time for a schedule that never fires, such as
// the 30th of February.
func (s Schedule) Next(after time.Time) time.Time {
	t := after.UTC().Truncate(time.Minute).Add(time.Minute)
	// Far enough to find a 29th of February from anywhere.
	limit := t.AddDate(8, 0, 1)
	for t.Before(limit) {
		switch {
		case s.month&(1<<uint(t.Month())) == 0:
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		case !s.dayMatches(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
		case s.hour&(1<<uint(t.Hour())) == 0:
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, time.UTC)
		case s.minute&(1<<uint(t.Minute())) == 0:
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}
