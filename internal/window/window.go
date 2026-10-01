// Package window decides the time range to examine from -t and -m.
package window

import (
	"fmt"
	"strings"
	"time"
)

// Window is the range of log time to examine, both ends inclusive.
type Window struct {
	Reference time.Time
	Start     time.Time
	End       time.Time
	Minutes   int
	// Specified reports whether the reference time came from -t.
	Specified bool
}

var dateTimeLayouts = []string{
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006/01/02 15:04:05",
	"2006/01/02 15:04",
}

var timeOnlyLayouts = []string{"15:04:05", "15:04"}

// MaxMinutes is 366 days: long enough for any incident window, and far below
// the overflow of a Duration in nanoseconds.
const MaxMinutes = 366 * 24 * 60

// Parse builds the window. Without -t the window ends at now and spans the
// previous minutes. With -t it is centred on the given time. A time without
// a date means today, or yesterday when that would be in the future.
func Parse(timeArgument string, minutes int, now time.Time) (Window, error) {
	if minutes <= 0 {
		return Window{}, fmt.Errorf("-m must be a positive number of minutes, got %d", minutes)
	}
	// A larger span overflows the Duration and reads whole files as one window.
	if minutes > MaxMinutes {
		return Window{}, fmt.Errorf("-m must be at most %d minutes (%d days), got %d", MaxMinutes, MaxMinutes/(60*24), minutes)
	}
	span := time.Duration(minutes) * time.Minute
	if strings.TrimSpace(timeArgument) == "" {
		return Window{Reference: now, Start: now.Add(-span), End: now, Minutes: minutes}, nil
	}
	reference, err := parseReference(strings.TrimSpace(timeArgument), now)
	if err != nil {
		return Window{}, err
	}
	return Window{
		Reference: reference,
		Start:     reference.Add(-span),
		End:       reference.Add(span),
		Minutes:   minutes,
		Specified: true,
	}, nil
}

func parseReference(text string, now time.Time) (time.Time, error) {
	location := now.Location()
	for _, layout := range dateTimeLayouts {
		if t, err := time.ParseInLocation(layout, text, location); err == nil {
			if t.After(now) {
				return time.Time{}, fmt.Errorf("-t %q is in the future", text)
			}
			return t, nil
		}
	}
	for _, layout := range timeOnlyLayouts {
		clock, err := time.ParseInLocation(layout, text, location)
		if err != nil {
			continue
		}
		t := time.Date(now.Year(), now.Month(), now.Day(), clock.Hour(), clock.Minute(), clock.Second(), 0, location)
		if t.After(now) {
			t = t.AddDate(0, 0, -1)
		}
		return t, nil
	}
	return time.Time{}, fmt.Errorf("-t %q is not a time; use HH:MM, HH:MM:SS or YYYY-MM-DD HH:MM[:SS]", text)
}

// Contains reports whether t falls inside the window.
func (w Window) Contains(t time.Time) bool {
	return !t.Before(w.Start) && !t.After(w.End)
}
