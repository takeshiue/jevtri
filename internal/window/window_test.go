package window

import (
	"testing"
	"time"
)

var tokyo = time.FixedZone("JST", 9*3600)

func at(text string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", text, tokyo)
	if err != nil {
		panic(err)
	}
	return t
}

// TW-01, TW-02 and TW-03: the window around the reference time.
func TestWindows(t *testing.T) {
	now := at("2026-09-28 10:00:00")
	cases := []struct {
		name, argument string
		minutes        int
		start, end     string
	}{
		{"TW-01 no -t", "", 5, "2026-09-28 09:55:00", "2026-09-28 10:00:00"},
		{"TW-02 date and time", "2026-09-27 15:47", 5, "2026-09-27 15:42:00", "2026-09-27 15:52:00"},
		{"TW-03 time only in the future means yesterday", "15:47", 5, "2026-09-27 15:42:00", "2026-09-27 15:52:00"},
		{"time only in the past means today", "09:30", 10, "2026-09-28 09:20:00", "2026-09-28 09:40:00"},
		{"seconds and slash date", "2026/09/28 09:30:15", 1, "2026-09-28 09:29:15", "2026-09-28 09:31:15"},
	}
	for _, c := range cases {
		w, err := Parse(c.argument, c.minutes, now)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !w.Start.Equal(at(c.start)) || !w.End.Equal(at(c.end)) {
			t.Errorf("%s: got %s .. %s, want %s .. %s", c.name, w.Start, w.End, c.start, c.end)
		}
	}
}

func TestErrors(t *testing.T) {
	now := at("2026-09-28 10:00:00")
	for _, c := range []struct {
		argument string
		minutes  int
	}{{"", 0}, {"", -5}, {"yesterday", 5}, {"2026-09-29 10:00", 5}, {"25:00", 5}} {
		if _, err := Parse(c.argument, c.minutes, now); err == nil {
			t.Errorf("-t %q -m %d: expected error", c.argument, c.minutes)
		}
	}
}

func TestContainsIsInclusive(t *testing.T) {
	w, _ := Parse("2026-09-28 09:30", 5, at("2026-09-28 10:00:00"))
	for _, c := range []struct {
		t    string
		want bool
	}{{"2026-09-28 09:25:00", true}, {"2026-09-28 09:35:00", true}, {"2026-09-28 09:24:59", false}, {"2026-09-28 09:35:01", false}} {
		if got := w.Contains(at(c.t)); got != c.want {
			t.Errorf("Contains(%s) = %v, want %v", c.t, got, c.want)
		}
	}
}

// SEC-004: a huge -m overflows the Duration and would silently read whole
// files as one window.
func TestMinutesUpperBound(t *testing.T) {
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	if _, err := Parse("", MaxMinutes, now); err != nil {
		t.Errorf("%d minutes was refused: %v", MaxMinutes, err)
	}
	for _, minutes := range []int{MaxMinutes + 1, 200000000, 1 << 40} {
		w, err := Parse("", minutes, now)
		if err == nil {
			t.Errorf("-m %d was accepted: %v .. %v", minutes, w.Start, w.End)
		}
	}
}
